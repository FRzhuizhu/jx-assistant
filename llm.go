package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Role    string `json:"role"`
	Content any    `json:"content"`
	// 思考模式历史回传：DeepSeek V4/flash 等要求带 tools 的请求把此前
	// assistant 回合的思考原文原样传回（否则 400「reasoning_content in
	// the thinking mode must be passed back」）。仅 DeepSeek/GLM 系端点读取，
	// 其余 OpenAI 兼容端点忽略消息上的未知字段。
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
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
	// 流尾统计：OpenAI 兼容端（DeepSeek/GLM 等）要求显式开启才在最后一个
	// chunk 附带 usage（缓存命中率的数据来源）
	StreamOptions *StreamOptionsCfg `json:"stream_options,omitempty"`
	// 思考控制：DeepSeek/智谱等用 thinking{type}，OpenAI o/gpt-5 系用 reasoning_effort
	Thinking        *ThinkingCfg `json:"thinking,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
}

type StreamOptionsCfg struct {
	IncludeUsage bool `json:"include_usage"`
}

// ContentPart 是多模态消息的内容块（OpenAI 兼容格式）：
// 视觉输入用 {type:"text"} + {type:"image_url"} 组合放在 user 消息里。
type ContentPart struct {
	Type     string    `json:"type"` // text | image_url
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

// ThinkingCfg 是 DeepSeek/GLM 风格的思考开关参数。
type ThinkingCfg struct {
	Type string `json:"type"` // enabled | disabled
}

// ApplyThinking 按档位设置请求的思考参数；空串=不干预（用服务端默认）。
// enabled/disabled → thinking{type}；其余非空档位（low/medium/high/max 等，
// 以服务端 supported_levels 为准）→ reasoning_effort 原样透传。
func (r *ChatRequest) ApplyThinking(level string) {
	switch level {
	case "enabled", "disabled":
		r.Thinking = &ThinkingCfg{Type: level}
		r.ReasoningEffort = ""
	case "":
		// 不干预
	default:
		r.Thinking = nil
		r.ReasoningEffort = level
	}
}

// StreamCallbacks 把流式输出回调给调用方：思考增量、正文增量、完整的工具调用
// 与流尾的用量统计（DeepSeek/GLM 等兼容网关在最后一个 chunk 附带 usage，
// 含 prompt_cache_hit_tokens 前缀缓存命中量）。
type StreamCallbacks struct {
	OnText      func(string)
	OnReasoning func(string)
	OnToolCall  func(ToolCall)
	OnUsage     func(Usage)
	// OnRequest 在请求真正发出前回调一次：url 是最终请求地址，body 是与线上
	// 完全一致的请求体字节（含 model/stream/stream_options 等最终字段）。
	// 「导出原始数据」靠它捕获最近一次发给模型的完整内容。
	OnRequest func(url string, body []byte)
}

// Usage 是流尾统计块（OpenAI 兼容 + DeepSeek 缓存命中扩展）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	PromptCacheHit   int `json:"prompt_cache_hit_tokens"`
	PromptCacheMiss  int `json:"prompt_cache_miss_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChat 向解析后的 LLM 目标发起一轮流式对话。
// target.BaseURL 配为 "mock" 时走内置演示模型，便于离线验证整条链路。
func StreamChat(ctx context.Context, target LLMTarget, req ChatRequest, cb StreamCallbacks) error {
	if target.BaseURL == "mock" {
		return mockStream(req, cb)
	}
	req.Model = target.Model
	req.Stream = true
	req.StreamOptions = &StreamOptionsCfg{IncludeUsage: true}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := strings.TrimRight(target.BaseURL, "/") + "/chat/completions"
	if cb.OnRequest != nil {
		cb.OnRequest(url, body)
	}
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
		if resp != nil {
			_ = resp.Body.Close()
		}
		return fmt.Errorf("无法连接模型服务：%s", connErrText(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.New(upstreamErrText(resp.StatusCode, snippet))
	}
	return parseSSE(resp.Body, cb)
}

// upstreamErrText 把网关返回的错误整理成用户看得懂的一行：优先取 JSON 里的
// error.message / message（余额不足、限流、鉴权失败这类都在这），否则回退原文摘要。
func upstreamErrText(status int, body []byte) string {
	type apiErr struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	var wrap struct {
		Error   *apiErr `json:"error"`
		Msg     string  `json:"msg"`
		Message string  `json:"message"`
		Detail  string  `json:"detail"`
	}
	var text string
	if json.Unmarshal(body, &wrap) == nil {
		switch {
		case wrap.Error != nil && strings.TrimSpace(wrap.Error.Message) != "":
			text = wrap.Error.Message
		case strings.TrimSpace(wrap.Message) != "":
			text = wrap.Message
		case strings.TrimSpace(wrap.Msg) != "":
			text = wrap.Msg
		case strings.TrimSpace(wrap.Detail) != "":
			text = wrap.Detail
		}
	}
	if text == "" {
		text = strings.TrimSpace(string(body))
	}
	// 折叠成单行并限长：报错条是一行提示，不是日志
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		text = http.StatusText(status)
	}
	return fmt.Sprintf("模型服务返回 %d：%s", status, truncateRunes(text, 300))
}

// connErrText 把底层连接错误整理成一句人话（超时/拒连/证书各说各的）。
func connErrText(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "请求超时，模型服务没有在预期时间内响应"
	case strings.Contains(msg, "context canceled"):
		return "请求已取消"
	case strings.Contains(msg, "no such host"):
		return "域名解析失败，请检查 API 地址（baseURL）是否正确"
	case strings.Contains(msg, "certificate"), strings.Contains(msg, "x509"):
		return "TLS 证书校验失败，请检查代理或证书配置"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "actively refused"):
		return "连接被拒绝，请确认 API 地址与端口可达"
	}
	return truncateRunes(strings.Join(strings.Fields(msg), " "), 300)
}

// isModelUnavailableErr 判断一次模型调用失败是不是「这个模型/提供方用不了」：
// 余额或额度耗尽、鉴权失败、模型不存在、提供方连不上这类硬性不可用。
// 纯限流（rate limit）与请求被取消不算——它们是瞬时的，换个模型不是正解。
// 专家预设模型的回退兜底（RunAgent）靠它判定。
func isModelUnavailableErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	if strings.Contains(s, "无法连接模型服务") {
		return !strings.Contains(s, "请求已取消") // 提供方连不上：换默认提供方有意义
	}
	quotaWords := []string{"余额", "额度", "欠费", "充值", "balance", "quota", "insufficient", "arrear"}
	hardWords := append(append([]string{}, quotaWords...),
		"unauthorized", "forbidden", "invalid api key", "authentication",
		"not exist", "not found", "no such model", "permission denied")
	for _, sc := range []int{400, 401, 402, 403, 404, 429} {
		if !strings.Contains(s, fmt.Sprintf("返回 %d", sc)) {
			continue
		}
		switch sc {
		case 429: // 限流类 429 只在说余额/额度时才算不可用
			return containsAnyFold(s, quotaWords)
		case 400: // 400 多为请求本身的问题；只有点名「模型不存在」这类才算
			// （错误前缀「模型服务返回…」自带"模型"二字，不能拿它当关键词）
			return containsAnyFold(s, []string{"not exist", "no such model", "unknown model",
				"invalid model", "model not found", "模型不存在", "未知模型", "无可用模型"})
		default: // 401/402/403/404：鉴权失败、欠费、禁止、模型不存在，一律算
			return true
		}
	}
	return containsAnyFold(s, hardWords)
}

// containsAnyFold 大小写不敏感地判断 s 是否含 words 中任一词。
func containsAnyFold(s string, words []string) bool {
	ls := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(ls, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`         // OpenAI 新版/部分网关
			ReasoningContent string `json:"reasoning_content"` // DeepSeek/GLM 风格
			ToolCalls        []struct {
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
	Usage *Usage `json:"usage,omitempty"`
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
		// 流尾用量块（缓存命中率的数据来源）；网关不带 usage 时静默跳过
		if chunk.Usage != nil && cb.OnUsage != nil {
			cb.OnUsage(*chunk.Usage)
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
// 任务会话依次发起 write、run_command 两个工具调用，
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
		emitCall("write", map[string]string{
			"path":    "mock-演示.txt",
			"content": "这是演示模式创建的测试文件，用于验证主循环与工具链路。\n",
		})
	case 1:
		emitCall("run_command", map[string]any{
			"command": "echo shell-ok",
			"shell":   "cmd",
		})
	case 2:
		// 演示交付链路：产出成果文件后用 deliver_result 登记，前端出交付卡片
		emitCall("deliver_result", map[string]any{
			"files": []any{map[string]any{
				"path": "mock-演示.txt",
				"note": "演示模式生成的成果文件",
			}},
			"summary": "本轮演示的交付成果",
		})
	default:
		segs := []string{
			"演示模式：",
			"我已调用 write 创建了「mock-演示.txt」，又用 run_command 执行了 cmd 命令，",
			"并用 deliver_result 登记了交付成果（见交付卡片）；文件快照、交付与回退链路均正常。",
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
