package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ---------- OpenAI chat-completions 兼容的消息与请求结构 ----------

type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolCallFunc `json:"function"`
}

// Message 同时用于本地持久化与 LLM 请求体。Content 用 any 是因为
// 带工具调用的 assistant 消息需要 content 为 null，而其余角色是字符串。
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func TextMessage(role, text string) Message {
	return Message{Role: role, Content: text}
}

func (m Message) Text() string {
	if s, ok := m.Content.(string); ok {
		return s
	}
	return ""
}

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolDef struct {
	Type     string   `json:"type"` // 固定 "function"
	Function ToolSpec `json:"function"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
	// 思考控制：DeepSeek/智谱等用 thinking{type}，OpenAI o/gpt-5 系用 reasoning_effort
	Thinking        *ThinkingCfg `json:"thinking,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
}

// ThinkingCfg 是 DeepSeek/GLM 风格的思考开关参数。
type ThinkingCfg struct {
	Type string `json:"type"` // enabled | disabled
}

// ApplyThinking 按档位设置请求的思考参数；空串=不干预（用服务端默认）。
// enabled/disabled → thinking{type}；low/medium/high → reasoning_effort。
func (r *ChatRequest) ApplyThinking(level string) {
	switch level {
	case "enabled", "disabled":
		r.Thinking = &ThinkingCfg{Type: level}
		r.ReasoningEffort = ""
	case "low", "medium", "high":
		r.Thinking = nil
		r.ReasoningEffort = level
	}
}

// StreamCallbacks 把流式输出回调给调用方：思考增量、正文增量与完整的工具调用。
type StreamCallbacks struct {
	OnText     func(string)
	OnReasoning func(string)
	OnToolCall func(ToolCall)
}

// StreamChat 向解析后的 LLM 目标发起一轮流式对话。
// target.BaseURL 配为 "mock" 时走内置演示模型，便于离线验证整条链路。
func StreamChat(ctx context.Context, target LLMTarget, req ChatRequest, cb StreamCallbacks) error {
	if target.BaseURL == "mock" {
		return mockStream(req, cb)
	}
	req.Model = target.Model
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := strings.TrimRight(target.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if target.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+target.APIKey)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("连接 LLM 服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("LLM 服务返回 %d: %s", resp.StatusCode, string(snippet))
	}
	return parseSSE(resp.Body, cb)
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`         // OpenAI 新版/部分网关
			ReasoningContent string `json:"reasoning_content"` // DeepSeek/GLM 风格
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason any `json:"finish_reason"`
	} `json:"choices"`
}

// parseSSE 解析 text/event-stream：聚合按 index 分片的工具调用参数增量，
// 流结束后按序回调完整的工具调用。
func parseSSE(r io.Reader, cb StreamCallbacks) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	acc := map[int]*ToolCall{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" && cb.OnText != nil {
				cb.OnText(ch.Delta.Content)
			}
			// 思考增量：两个字段名都收（reasoning_content / reasoning）
			if rn := ch.Delta.ReasoningContent; rn != "" && cb.OnReasoning != nil {
				cb.OnReasoning(rn)
			} else if rn := ch.Delta.Reasoning; rn != "" && cb.OnReasoning != nil {
				cb.OnReasoning(rn)
			}
			for _, tc := range ch.Delta.ToolCalls {
				cur := acc[tc.Index]
				if cur == nil {
					cur = &ToolCall{Type: "function"}
					acc[tc.Index] = cur
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cur.Function.Name += tc.Function.Name
				}
				cur.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	indexes := make([]int, 0, len(acc))
	for i := range acc {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	if cb.OnToolCall != nil {
		for i, idx := range indexes {
			tc := acc[idx]
			if tc.ID == "" {
				tc.ID = fmt.Sprintf("call_%d", i)
			}
			if tc.Function.Name != "" {
				cb.OnToolCall(*tc)
			}
		}
	}
	return nil
}

// mockStream 是内置的离线演示模型：按会话形态演示一轮完整链路。
// 助理会话（带 dispatch_task 工具）演示「派发 → 汇报」；
// 任务会话依次发起 write_file、run_command 两个工具调用，
// 收到足够工具结果后输出总结文本，用于无 API Key 时验证主循环与工具链路。
func mockStream(req ChatRequest, cb StreamCallbacks) error {
	toolResults := 0
	for _, m := range req.Messages {
		if m.Role == "tool" {
			toolResults++
		}
	}
	isAssistant := false
	for _, t := range req.Tools {
		if t.Function.Name == "dispatch_task" {
			isAssistant = true
		}
	}
	emitCall := func(name string, args any) {
		raw, _ := json.Marshal(args)
		if cb.OnToolCall != nil {
			cb.OnToolCall(ToolCall{
				ID:       fmt.Sprintf("call_mock_%d", toolResults+1),
				Type:     "function",
				Function: ToolCallFunc{Name: name, Arguments: string(raw)},
			})
		}
	}
	if isAssistant {
		switch toolResults {
		case 0:
			emitCall("dispatch_task", map[string]string{
				"task":  "在演示工作区创建 mock-演示.txt 并写入一行问候，然后用 shell 确认文件存在",
				"title": "演示子任务",
			})
		default:
			segs := []string{
				"演示模式（数字助理）：",
				"我已把任务派发给子会话执行，它创建并确认了「mock-演示.txt」。",
				"主对话 → 派发 → 子会话执行 → 压缩摘要回传 的完整链路均正常。",
				"在设置中填入 API Key 并把 baseURL 改回真实服务地址，即可使用真实模型。",
			}
			if cb.OnText != nil {
				for _, seg := range segs {
					cb.OnText(seg)
					time.Sleep(80 * time.Millisecond)
				}
			}
		}
		return nil
	}
	switch toolResults {
	case 0:
		emitCall("write_file", map[string]string{
			"path":    "mock-演示.txt",
			"content": "这是演示模式创建的测试文件，用于验证主循环与工具链路。\n",
		})
	case 1:
		emitCall("run_command", map[string]any{
			"command": "echo shell-ok",
			"shell":   "cmd",
		})
	default:
		segs := []string{
			"演示模式：",
			"我已调用 write_file 创建了「mock-演示.txt」，又用 run_command 执行了 cmd 命令，",
			"主循环、工具调用、会话持久化与流式输出链路均正常。",
			"在设置中填入 API Key 并把 baseURL 改回真实服务地址，即可使用真实模型。",
		}
		if cb.OnText != nil {
			for _, seg := range segs {
				cb.OnText(seg)
				time.Sleep(80 * time.Millisecond)
			}
		}
	}
	return nil
}
