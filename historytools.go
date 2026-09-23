package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 历史查询工具：让模型能找到并读取任何会话的原生历史（未压缩原文，
// append-only 永久保留）。对齐 DSH 的 session_search / session_event_read 思路。

func searchSessionsTool() Tool {
	return Tool{
		Spec: toolSpec("search_sessions", "跨所有会话（含任务会话、子任务、辅助对话）全文搜索消息内容，返回命中的会话标题、id 与片段。想找「之前哪个对话说过/做过什么」用它。",
			objSchema(map[string]any{
				"query": strProp("搜索关键词"),
				"limit": intProp("最多返回的会话数，默认 20"),
			}, "query")),
		Run: runSearchSessions,
	}
}

func runSearchSessions(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	hits, err := t.app.Store.SearchMessages(strings.TrimSpace(p.Query), p.Limit)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "（没有命中）", nil
	}
	lines := []string{}
	for _, h := range hits {
		title := h.Title
		if title == "" {
			title = "(未命名)"
		}
		lines = append(lines, fmt.Sprintf("- %s｜id=%s｜模式=%s｜第 %d 条 %s：%s",
			title, h.SessionID, h.Mode, h.Seq, h.Role, h.Snippet))
	}
	return strings.Join(lines, "\n") + "\n（需要细看用 read_session_messages 按 id 翻页读取原文）", nil
}

func readSessionMessagesTool() Tool {
	return Tool{
		Spec: toolSpec("read_session_messages", "分页读取任意会话的原生历史（未压缩的原始记录，含工具调用与完整结果）。默认从最新往前读。",
			objSchema(map[string]any{
				"session_id": strProp("目标会话 id，来自 list_sessions 或 search_sessions"),
				"limit":      intProp("读取条数，默认 30，上限 200"),
				"offset":     intProp("从最新往前偏移的条数，默认 0"),
				"role":       strProp("可选：只看某角色（user/assistant/tool）"),
			}, "session_id")),
		Run: runReadSessionMessages,
	}
}

func runReadSessionMessages(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		Role      string `json:"role"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	sid := strings.TrimSpace(p.SessionID)
	if sid == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	if sid == t.sess.ID {
		sid = t.sess.ID // 允许读自己：历史查询是本会话的正当能力
	}
	if _, err := t.app.Store.Get(sid); err != nil {
		return "", fmt.Errorf("会话不存在：%s", sid)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	all, err := t.app.Store.RawMessagesAll(sid)
	if err != nil {
		return "", err
	}
	// 只保留对话流（跳过摘要行），按角色过滤
	rows := []*StoredMessage{}
	for _, m := range all {
		if m.Kind == "summary" {
			continue
		}
		if p.Role != "" && m.Role != p.Role {
			continue
		}
		rows = append(rows, m)
	}
	total := len(rows)
	offset := p.Offset
	if offset < 0 {
		offset = 0
	}
	// 从最新往前取一页
	hi := total - offset
	if hi > total {
		hi = total
	}
	lo := hi - limit
	if lo < 0 {
		lo = 0
	}
	if hi <= 0 {
		return fmt.Sprintf("（会话 %s 共 %d 条记录，offset=%d 已越过最旧记录）", sid, total, offset), nil
	}
	page := rows[lo:hi]
	lines := []string{fmt.Sprintf("（会话 %s 共 %d 条，当前显示第 %d–%d 条，从旧到新）",
		sid, total, lo+1, hi)}
	for _, m := range page {
		text := m.Text()
		for _, tc := range m.ToolCalls {
			text += fmt.Sprintf("\n[调用 %s %s]", tc.Function.Name, clipText(tc.Function.Arguments, argKeepLimit))
		}
		if len([]rune(text)) > 3000 {
			text = string([]rune(text)[:3000]) + "…（本条过长已截断）"
		}
		lines = append(lines, fmt.Sprintf("[%d] %s：%s", m.Seq, m.Role, strings.TrimSpace(text)))
	}
	return strings.Join(lines, "\n"), nil
}
