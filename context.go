package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// 上下文管理：组装管线（系统提示词 + 活跃摘要 + 规则压缩视图 + 近期原文）
// 与两种压缩机制——规则压缩（零模型调用）与模型摘要（自动阈值 + 手动按钮）。
// 设计对齐 DSH 的哲学：原始历史 append-only 永久保留，折叠只发生在
// 「模型可见表面」上；模型随时可用历史查询工具读到未压缩原文。

const compactOmit = "omit" // 规则压缩的特殊标记：组装时整行跳过

const (
	argKeepLimit    = 500 // 工具调用参数保留上限（防 write_file 带整文件内容）
	resultKeepLimit = 200 // 工具结果保留的开头长度
)

// round 是一轮对话：一条用户消息（chat/report）到下一条用户消息之前的全部。
type round struct {
	startSeq, endSeq int64
}

// splitRounds 按用户消息分轮；开头若无用户消息（不应发生），并入第一轮。
func splitRounds(msgs []*StoredMessage) []round {
	rounds := []round{}
	cur := round{}
	started := false
	for _, m := range msgs {
		if m.Kind == "chat" && m.Role == "user" || m.Kind == "report" {
			if started {
				cur.endSeq = m.Seq - 1
				if cur.endSeq < cur.startSeq {
					cur.endSeq = cur.startSeq
				}
				rounds = append(rounds, cur)
			}
			cur = round{startSeq: m.Seq}
			started = true
			continue
		}
		if !started {
			cur = round{startSeq: m.Seq}
			started = true
		}
	}
	if started {
		if len(msgs) > 0 {
			cur.endSeq = msgs[len(msgs)-1].Seq
		}
		rounds = append(rounds, cur)
	}
	return rounds
}

// activeSummaries 取未过期的摘要：手动摘要全部保留（叠加、永不重压），
// 自动摘要只取最新一条（DSH 式：新自动摘要合并旧自动摘要）。
func activeSummaries(msgs []*StoredMessage) []*StoredMessage {
	manual := []*StoredMessage{}
	var auto *StoredMessage
	for _, m := range msgs {
		if m.Kind != "summary" || m.Superseded {
			continue
		}
		if m.Origin == "manual" {
			manual = append(manual, m)
		} else if auto == nil || m.Seq > auto.Seq {
			auto = m
		}
	}
	out := manual
	if auto != nil {
		out = append(out, auto)
	}
	// 按覆盖边界升序，保证注入顺序与时间线一致
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CoversUpTo < out[j-1].CoversUpTo; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// buildContext 是组装管线：系统提示词 + 活跃摘要 + 边界后消息
// （规则压缩视图生效，最近轮保持原文），并修剪结尾悬空的工具调用。
// 环境快照（env）不进历史序列——历史里的 kind=env/prompt 行全部跳过，
// 最新环境快照作为一条 user 消息固定放在序列末尾：模型永远读到最新环境，
// 旧快照不重复占用表面上下文，系统提示词前缀保持稳定命中缓存。
func (a *App) buildContext(sess *SessionMeta, sys, env string) []Message {
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return []Message{TextMessage("system", sys), TextMessage("user", env)}
	}
	summaries := activeSummaries(raw)
	boundary := int64(0)
	for _, s := range summaries {
		if s.CoversUpTo > boundary {
			boundary = s.CoversUpTo
		}
	}
	ruleMode := a.Cfg.CompactMode == "rule"

	out := make([]Message, 0, len(raw)+2)
	out = append(out, TextMessage("system", sys))
	for _, s := range summaries {
		out = append(out, TextMessage("user", s.Text()))
	}
	for _, m := range raw {
		if m.Seq <= boundary || m.Kind == "summary" || m.Kind == "env" || m.Kind == "prompt" {
			continue
		}
		if ruleMode && m.Compact != "" {
			if m.Compact == compactOmit {
				continue // 整行跳过（工具结果行）
			}
			// 压缩表示：纯文本消息，不带 tool_calls（协议要求配对完整）
			out = append(out, Message{Role: m.Role, Content: m.Compact})
			continue
		}
		out = append(out, m.Message)
	}
	// 修剪尾部悬空的工具调用（客户端中断可能留下未配对的调用）
	for len(out) > 0 {
		last := out[len(out)-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	// 环境快照固定在序列末尾（易变内容永远在尾部，前缀缓存不受影响）
	out = append(out, TextMessage("user", env))
	return out
}

// estimateTokens 粗估一组消息的 token 量（中文为主的启发式）。
func estimateTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			total += len([]rune(s)) * 3 / 4
		}
		for _, tc := range m.ToolCalls {
			total += len([]rune(tc.Function.Arguments)) * 3 / 4
		}
		total += 8
	}
	return total
}

// ---------- 上下文占用统计（前端「上下文」面板的数据源） ----------

type ctxCategory struct {
	Name   string  `json:"name"`
	Tokens int     `json:"tokens"`
	Share  float64 `json:"share"` // 占总上下文的百分比（0-100）
}

type ctxStats struct {
	Window     int           `json:"window"` // 模型上下文窗口（token）
	Total      int           `json:"total"`  // 下一次请求的估算总量
	Categories []ctxCategory `json:"categories"`
	Cache      struct {
		PromptTokens int64   `json:"promptTokens"` // 历次调用 prompt_tokens 总和
		CacheHit     int64   `json:"cacheHit"`     // 历次调用缓存命中总和
		Rate         float64 `json:"rate"`         // 平均缓存命中率（0-1；无数据为 0）
		Calls        int64   `json:"calls"`        // 模型调用次数
	} `json:"cache"`
}

// contextStats 按与 buildContext 完全相同的组装规则，把下一次请求的上下文
// 拆成分类 token 估算（同一启发式）。注意：buildContext 的组装规则改动时需同步此处。
func (a *App) contextStats(sess *SessionMeta) ctxStats {
	cats := map[string]int{}
	add := func(name string, msgs ...Message) {
		for _, m := range msgs {
			cats[name] += estimateTokens([]Message{m})
		}
	}

	sys := a.systemPrompt(sess)
	env := a.envPrompt(sess)
	add("系统提示词", TextMessage("system", sys))
	add("环境快照", TextMessage("user", env))

	// 工具定义按序列化后的 JSON 体积估算（与消息同一 rune*3/4 启发式）
	if tools := a.toolsFor(sess.Mode); len(tools) > 0 {
		defs := make([]ToolDef, len(tools))
		for i, tl := range tools {
			defs[i] = tl.Spec
		}
		if raw, err := json.Marshal(defs); err == nil {
			cats["系统工具"] += len([]rune(string(raw))) * 3 / 4
		}
	}

	if raw, err := a.Store.RawMessagesAll(sess.ID); err == nil {
		summaries := activeSummaries(raw)
		boundary := int64(0)
		for _, s := range summaries {
			if s.CoversUpTo > boundary {
				boundary = s.CoversUpTo
			}
		}
		ruleMode := a.Cfg.CompactMode == "rule"
		for _, s := range summaries {
			add("摘要", TextMessage("user", s.Text()))
		}
		for _, m := range raw {
			if m.Seq <= boundary || m.Kind == "summary" || m.Kind == "env" || m.Kind == "prompt" {
				continue
			}
			if ruleMode && m.Compact != "" {
				if m.Compact == compactOmit {
					continue
				}
				add("消息", Message{Role: m.Role, Content: m.Compact})
				continue
			}
			add("消息", m.Message)
		}
	}

	order := []string{"消息", "系统工具", "系统提示词", "摘要", "环境快照", "其他"}
	total := 0
	for _, v := range cats {
		total += v
	}
	var out ctxStats
	out.Window = a.resolveTarget(sess).ContextWindow
	out.Total = total
	for _, name := range order {
		tok := cats[name]
		share := 0.0
		if total > 0 {
			share = float64(tok) * 100 / float64(total)
		}
		out.Categories = append(out.Categories, ctxCategory{Name: name, Tokens: tok, Share: math.Round(share*10) / 10})
	}
	out.Cache.PromptTokens = sess.UsagePrompt
	out.Cache.CacheHit = sess.UsageCacheHit
	out.Cache.Calls = sess.UsageCalls
	if sess.UsagePrompt > 0 {
		out.Cache.Rate = float64(sess.UsageCacheHit) / float64(sess.UsagePrompt)
	}
	return out
}

// ---------- 规则压缩（零模型调用） ----------

// ruleCompactPass 在每轮对话结束后调用：新增轮数达到间隔阈值时，
// 把「保留窗口之外、尚未压缩」的轮次折叠为轻量表示。
// 用户消息与模型正文永远保留原文；工具调用保留名字+参数（超长截断）；
// 工具结果只留开头片段。以轮为单位整体折叠，绝不拆散调用/结果配对。
func (a *App) ruleCompactPass(sess *SessionMeta) {
	if a.Cfg.CompactMode != "rule" {
		return
	}
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return
	}
	rounds := splitRounds(raw)
	if len(rounds) <= a.Cfg.KeepRounds {
		return
	}
	// 已压缩边界：最后一条带 compact 标记的消息 seq
	ruleSeq := int64(0)
	for _, m := range raw {
		if m.Compact != "" && m.Seq > ruleSeq && m.Kind == "chat" {
			ruleSeq = m.Seq
		}
	}
	// 统计边界后的新增轮数，达到间隔才做一次
	newRounds := 0
	for _, r := range rounds {
		if r.startSeq > ruleSeq {
			newRounds++
		}
	}
	if newRounds < a.Cfg.RuleInterval {
		return
	}
	// 压缩 [已压缩边界之后, 保留窗口之前] 的所有轮
	keepFrom := rounds[len(rounds)-a.Cfg.KeepRounds].startSeq
	for _, r := range rounds {
		if r.startSeq <= ruleSeq || r.startSeq >= keepFrom {
			continue
		}
		a.compactRound(sess.ID, r, raw)
	}
}

// compactRound 把一轮里的工具调用/结果折叠为轻量表示并落库。
func (a *App) compactRound(sid string, r round, raw []*StoredMessage) {
	// 该轮内 assistant 的工具调用行：正文保留 + 调用清单（参数限长、结果开头）
	for _, m := range raw {
		if m.Seq < r.startSeq || m.Seq > r.endSeq {
			continue
		}
		switch {
		case len(m.ToolCalls) > 0:
			var b strings.Builder
			if s := m.Text(); strings.TrimSpace(s) != "" {
				b.WriteString(s + "\n")
			}
			results := toolResultsOf(raw, r, m)
			for i, tc := range m.ToolCalls {
				b.WriteString(fmt.Sprintf("[调用 %s", tc.Function.Name))
				if args := clipText(tc.Function.Arguments, argKeepLimit); args != "" {
					b.WriteString(" 参数=" + args)
				}
				if i < len(results) {
					b.WriteString(" 结果=" + clipText(results[i], resultKeepLimit))
				}
				b.WriteString("]\n")
			}
			_ = a.Store.SetCompact(sid, m.Seq, strings.TrimRight(b.String(), "\n"))
		case m.Role == "tool":
			_ = a.Store.SetCompact(sid, m.Seq, compactOmit)
		}
	}
}

// toolResultsOf 取与调用行配对的结果文本（按 ToolCallID 对齐）。
func toolResultsOf(raw []*StoredMessage, r round, callRow *StoredMessage) []string {
	byID := map[string]string{}
	for _, m := range raw {
		if m.Seq >= r.startSeq && m.Seq <= r.endSeq && m.Role == "tool" && m.ToolCallID != "" {
			byID[m.ToolCallID] = m.Text()
		}
	}
	out := []string{}
	for _, tc := range callRow.ToolCalls {
		out = append(out, byID[tc.ID])
	}
	return out
}

// ---------- 模型摘要压缩 ----------

const summarizeInstruction = `请把下面这段对话历史压缩成一份结构化中文摘要，供同一对话的后续轮次作为上下文使用。
要求：
1. 输出以下小节（没有内容的写「无」）：主要请求与意图 / 关键概念与决定 / 涉及的文件与改动 / 错误与修复 / 未完成事项 / 当前进行中的工作 / 下一步。
2. 保留精确的路径、命令、错误信息、数字与名称，不得改写。
3. 只输出摘要本身，不要评论、不要提及压缩这件事。`

// doSummarize 把 [最后摘要边界, 保留窗口之前] 的区间压成一条摘要。
// origin=auto 时合并旧自动摘要（DSH 式单一检查点）；origin=manual 时独立叠加、永不重压。
// keepTail=0 表示溢出兜底（保留尾为零，最大化压缩）。
func (a *App) doSummarize(sess *SessionMeta, origin string, keepTail int) (string, error) {
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return "", err
	}
	summaries := activeSummaries(raw)
	boundary := int64(0)
	var prevAuto *StoredMessage
	for _, s := range summaries {
		if s.CoversUpTo > boundary {
			boundary = s.CoversUpTo
		}
		if s.Origin == "auto" {
			prevAuto = s
		}
	}
	keep := keepTail
	if keep <= 0 {
		keep = a.Cfg.KeepRounds
	}
	rounds := splitRounds(raw)
	segEnd := int64(0)
	if len(rounds) > keep {
		segEnd = rounds[len(rounds)-keep].startSeq - 1
	}
	if segEnd <= boundary {
		return "", fmt.Errorf("没有可压缩的区间（保留最近 %d 轮）", keep)
	}
	var b strings.Builder
	if origin == "auto" && prevAuto != nil {
		b.WriteString("【先前的自动摘要（请合并进新摘要，保留仍成立的事实、丢弃过时的）】\n" + prevAuto.Text() + "\n\n")
	}
	b.WriteString("【待压缩的对话历史】\n")
	for _, m := range raw {
		if m.Seq <= boundary || m.Seq > segEnd || m.Kind == "summary" || m.Kind == "env" || m.Kind == "prompt" {
			continue
		}
		role := map[string]string{"user": "用户", "assistant": "助手", "tool": "工具结果"}[m.Role]
		if role == "" {
			role = m.Role
		}
		text := m.Text()
		for _, tc := range m.ToolCalls {
			text += "\n[调用 " + tc.Function.Name + " " + clipText(tc.Function.Arguments, argKeepLimit) + "]"
		}
		b.WriteString(fmt.Sprintf("%s：%s\n", role, clipText(strings.TrimSpace(text), 4000)))
	}

	summary, err := a.callSummarizer(sess, summarizeInstruction+"\n\n"+b.String())
	if err != nil {
		return "", err
	}
	if origin == "auto" {
		_ = a.Store.SupersedeAutoSummaries(sess.ID)
	}
	label := "手动"
	if origin == "auto" {
		label = "自动"
	}
	content := fmt.Sprintf("【%s历史摘要（覆盖至第 %d 条，原文可用历史查询工具读取）】\n%s", label, segEnd, summary)
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{
		Message: TextMessage("user", content), Kind: "summary",
		Origin: origin, CoversUpTo: segEnd, CreatedAt: now(),
	})
	return summary, nil
}

// callSummarizer 发起一次摘要调用（复用会话的模型；mock 模式直接给占位摘要）。
func (a *App) callSummarizer(sess *SessionMeta, input string) (string, error) {
	target := a.resolveTarget(sess)
	if target.BaseURL == "mock" {
		return "（离线演示摘要：本段历史已完成创建演示文件并验证，细节见原始记录。）", nil
	}
	req := ChatRequest{Messages: []Message{
		TextMessage("system", "你是严谨的对话历史压缩助手。"),
		TextMessage("user", input),
	}}
	var sb strings.Builder
	err := StreamChat(context.Background(), target, req, StreamCallbacks{
		OnText: func(s string) { sb.WriteString(s) },
	})
	if err != nil {
		return "", fmt.Errorf("摘要调用失败: %w", err)
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return "", fmt.Errorf("摘要调用没有产出内容")
	}
	return out, nil
}

// maybeAutoCompact 在 llm 模式下请求前检查：估算 token 超过阈值就自动压缩。
func (a *App) maybeAutoCompact(sess *SessionMeta, msgs []Message, ctxWindow int) error {
	if a.Cfg.CompactMode != "llm" || ctxWindow <= 0 {
		return nil
	}
	if estimateTokens(msgs) < int(float64(ctxWindow)*a.Cfg.CompactThreshold) {
		return nil
	}
	_, err := a.doSummarize(sess, "auto", a.Cfg.KeepRounds)
	return err
}

// overflowCompact 是上下文超限的兜底：保留尾为零强制压缩一次（DSH 式）。
func (a *App) overflowCompact(sess *SessionMeta) error {
	if a.Cfg.CompactMode == "off" {
		return fmt.Errorf("上下文超限且压缩已关闭，请手动精简会话或开启压缩")
	}
	_, err := a.doSummarize(sess, "manual", 0)
	return err
}

// isOverflowError 判断模型错误是否为上下文超限。
func isOverflowError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context length") || strings.Contains(s, "maximum context") ||
		strings.Contains(s, "too long") || strings.Contains(s, "context window")
}
