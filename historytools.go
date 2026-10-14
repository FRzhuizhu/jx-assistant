package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 历史查询工具：让模型能找到并读取任何会话的原生历史（未压缩原文，
// append-only 永久保留）。对齐 DSH 的 session_search / session_event_read 思路，
// 体验像翻一份超长对话日志：search 定位（命中给 seq 与上下文片段）→
// read_session_messages 从命中位置往后按字数预算分页读取，绝不整段返回全文。

const (
	histEntryClip = 3000  // 历史查询里单条消息的截断长度
	histCharsDef  = 6000  // 默认单次返回的总字数预算
	histCharsMax  = 20000 // 单次返回总字数上限
)

func searchSessionsTool() Tool {
	return Tool{
		Spec: toolSpec("search_sessions", "跨所有会话（含任务会话、子任务、辅助对话）搜索消息原文：用户输入、助手正文、工具调用参数、思考过程、工具结果都能搜到，返回命中的会话标题、id、位置（seq）与片段。想找「之前哪个对话说过/做过什么」用它；命中后用 read_session_messages（from_seq=命中 seq）从该位置往后读原文。",
			objSchema(map[string]any{
				"query":   strProp("搜索关键词"),
				"limit":   intProp("最多返回的会话数，默认 20"),
				"context": intProp("每个命中的上下文字数（命中点前后各一半，如 2000=前1000字+后1000字），默认 0 只给短片段；上限 4000"),
			}, "query")),
		Run: runSearchSessions,
	}
}

func runSearchSessions(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		Context int    `json:"context"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	radius := p.Context / 2
	if radius > 2000 {
		radius = 2000 // 上下文窗口合计上限 4000 字
	}
	hits, err := t.app.Store.SearchMessages(strings.TrimSpace(p.Query), p.Limit, radius)
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
	return strings.Join(lines, "\n") + "\n（要细看用 read_session_messages（from_seq=命中 seq）从该位置往后读原文）", nil
}

func readSessionMessagesTool() Tool {
	return Tool{
		Spec: toolSpec("read_session_messages", "分页读取任意会话的原生历史（未压缩的原始记录，含思考过程、工具调用与完整结果）。主用法：search_sessions 命中后传 from_seq 从该条开始往后读（从旧到新），字数预算用尽自动停止、提示下个起点；不带 from_seq 时从最新往前翻页。",
			objSchema(map[string]any{
				"session_id": strProp("目标会话 id，来自 list_sessions 或 search_sessions"),
				"from_seq":   intProp("从第几条开始往后读（含）。配合搜索定位：看命中点之后的内容"),
				"limit":      intProp("最多读取条数，默认 30，上限 200"),
				"chars":      intProp("本次返回的总字数预算，默认 6000，上限 20000；预算用尽自动停止"),
				"offset":     intProp("不带 from_seq 时的旧用法：从最新往前偏移的条数，默认 0"),
				"role":       strProp("可选：只看某角色（user/assistant/tool）"),
			}, "session_id")),
		Run: runReadSessionMessages,
	}
}

func runReadSessionMessages(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		FromSeq   int64  `json:"from_seq"`
		Limit     int    `json:"limit"`
		Chars     int    `json:"chars"`
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
	chars := p.Chars
	if chars <= 0 {
		chars = histCharsDef
	}
	if chars > histCharsMax {
		chars = histCharsMax
	}
	all, err := t.app.Store.RawMessagesAll(sid)
	if err != nil {
		return "", err
	}
	// 只保留对话流：跳过摘要行、继承标记行、模型输入快照、审计行，以及
	// 上下文材料（环境快照/拼装快照）——读的是对话日志，不是上下文组装材料
	rows := []*StoredMessage{}
	for _, m := range all {
		if m.Kind == "summary" || m.Kind == "request" || m.Kind == inheritKind || m.Kind == "audit" || m.Kind == "env" || m.Kind == "prompt" {
			continue
		}
		if p.Role != "" && m.Role != p.Role {
			continue
		}
		rows = append(rows, m)
	}
	total := len(rows)

	// 正向窗口：从 from_seq（含）往后读，字数预算用尽即停——搜到位置后看
	// 「这段及之后」的主路径
	if p.FromSeq > 0 {
		start := sort.Search(len(rows), func(i int) bool { return rows[i].Seq >= p.FromSeq })
		if start >= len(rows) {
			return fmt.Sprintf("（会话 %s 共 %d 条记录，from_seq=%d 已越过最新记录）", sid, total, p.FromSeq), nil
		}
		end := start + limit
		if end > len(rows) {
			end = len(rows)
		}
		lines := []string{fmt.Sprintf("（会话 %s 共 %d 条，从第 %d 条开始从旧到新，预算 %d 字）", sid, total, rows[start].Seq, chars)}
		used, next := 0, int64(0)
		for i := start; i < end; i++ {
			entry := renderHistoryEntry(rows[i])
			n := len([]rune(entry))
			if used+n > chars {
				if i == start {
					// 首条就超预算：截断到预算内也给出（保证至少能看到命中点本身）
					lines = append(lines, string([]rune(entry)[:chars])+"…（单条超预算截断）")
					next = rows[i].Seq + 1
				} else {
					next = rows[i].Seq
				}
				break
			}
			used += n
			lines = append(lines, entry)
			if i == end-1 {
				next = rows[i].Seq + 1
			}
		}
		if next > 0 && next <= rows[len(rows)-1].Seq {
			lines = append(lines, fmt.Sprintf("（已达读取边界，继续读用 from_seq=%d）", next))
		}
		return strings.Join(lines, "\n"), nil
	}

	// 倒序翻页（旧用法）：从最新往前取一页
	offset := p.Offset
	if offset < 0 {
		offset = 0
	}
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
	lines := []string{fmt.Sprintf("（会话 %s 共 %d 条，当前显示第 %d–%d 条，从旧到新）", sid, total, lo+1, hi)}
	for _, m := range page {
		lines = append(lines, renderHistoryEntry(m))
	}
	return strings.Join(lines, "\n"), nil
}

// renderHistoryEntry 把一条原始消息渲染为历史查询的文本行：思考过程前置
//（【思考】段），正文与工具调用并入同条预算，单条截断。
func renderHistoryEntry(m *StoredMessage) string {
	var b strings.Builder
	if m.Thinking != "" {
		b.WriteString("【思考】" + clipRunes(m.Thinking, histEntryClip) + "\n")
	}
	if s := m.Text(); strings.TrimSpace(s) != "" {
		b.WriteString(strings.TrimSpace(s))
	}
	for _, tc := range m.ToolCalls {
		b.WriteString("\n[调用 " + tc.Function.Name + " 参数=" + clipText(tc.Function.Arguments, argKeepLimit) + "]")
	}
	entry := strings.TrimSpace(b.String())
	if entry == "" {
		entry = "（无文本内容）"
	}
	return fmt.Sprintf("[%d] %s：%s", m.Seq, m.Role, clipRunes(entry, histEntryClip))
}

// clipRunes 按 rune 数截断（保留换行与原格式），区别于压平空白的 clipText。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…（后面省略）"
}
