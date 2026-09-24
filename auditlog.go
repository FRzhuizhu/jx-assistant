package main

// 会话内审计日志：把「不发给模型、但复盘对话演进必需」的事件以 kind=audit
// 行落库（role=system：前端对话流、上下文组装、历史工具都跳过这类行）。
// 记录内容——每次模型调用的请求规模/返回/用量（缓存命中）、运行中报错、
// 用户停止、上下文超限重试、规则压缩生效；导出日志按时间线原样呈现。
// 配套的 kind=request 行存最近一次真正发给模型的完整请求体（替换式写入，
// 每会话只留最新一条），供「导出原始数据」逐字节还原。

import (
	"encoding/json"
)

// auditWrite 落一条审计事件行；payload 至少带 type（call|error|stop|compact|overflow）。
func (a *App) auditWrite(sess *SessionMeta, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{
		Message: TextMessage("system", string(b)), Kind: kindAudit, CreatedAt: now(),
	})
}

// auditUsage 是一次模型调用的流尾用量（审计行里的精简形态）。
type auditUsage struct {
	Prompt     int `json:"prompt"`
	Hit        int `json:"hit"`
	Miss       int `json:"miss"`
	Completion int `json:"completion"`
}

// auditRow 是 kind=audit 行正文的解析形态；各 type 只用自己那几个字段。
type auditRow struct {
	Type  string      `json:"type"`
	Step  int         `json:"step"`  // call：本轮第几步（从 1 起）
	Msgs  int         `json:"msgs"`  // call：发送的消息条数
	Est   int         `json:"est"`   // call：发送前的 token 估算
	Ms    int64       `json:"ms"`    // call：耗时
	Text  int         `json:"text"`  // call：返回正文字数
	Tools []string    `json:"tools"` // call：返回的工具调用名
	Usage *auditUsage `json:"usage"`
	Error string      `json:"error"` // call/error/overflow：错误原文
	Rounds int        `json:"rounds"` // compact：折叠的轮数
	From  int64       `json:"from"`   // compact：折叠区间起始 seq
	To    int64       `json:"to"`     // compact：折叠区间结束 seq
}

// auditCall 在一次模型调用返回后（无论成败）落审计行：调用前后的关键事实一次记全。
func (a *App) auditCall(sess *SessionMeta, step, msgs, est int, ms int64, usage *Usage, textRunes int, toolNames []string, callErr error) {
	p := map[string]any{
		"type": "call", "step": step, "msgs": msgs, "est": est, "ms": ms, "text": textRunes,
	}
	if len(toolNames) > 0 {
		p["tools"] = toolNames
	}
	if usage != nil {
		p["usage"] = auditUsage{Prompt: usage.PromptTokens, Hit: usage.PromptCacheHit,
			Miss: usage.PromptCacheMiss, Completion: usage.CompletionTokens}
	}
	if callErr != nil {
		p["error"] = callErr.Error()
	}
	a.auditWrite(sess, p)
}

// toolNamesOf 取一组工具调用的名字（审计行的返回摘要用）。
func toolNamesOf(calls []ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, tc := range calls {
		names = append(names, tc.Function.Name)
	}
	return names
}

// lastRequestRec 是 kind=request 行的正文：最近一次发给模型的请求全量记录。
// Body 与线上逐字节同源（发送前捕获）；URL/Model 只记元信息，绝不含 API Key。
type lastRequestRec struct {
	URL       string          `json:"url"`
	Model     string          `json:"model"`
	Step      int             `json:"step"`
	MsgCount  int             `json:"msgs"`
	EstTokens int             `json:"estTokens"`
	Body      json.RawMessage `json:"body"`
}

// recordLastRequest 在请求发出前捕获并替换式落库（每会话只保留最近一条，
// 避免旧版「每步一行全量快照」把库无界撑大的问题）。
func (a *App) recordLastRequest(sess *SessionMeta, url, model string, step, msgs, est int, body []byte) {
	rec := lastRequestRec{URL: url, Model: model, Step: step, MsgCount: msgs, EstTokens: est,
		Body: append(json.RawMessage(nil), body...)}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = a.Store.ReplaceRequestRow(sess.ID, &StoredMessage{
		Message: TextMessage("system", string(b)), Kind: kindRequest, CreatedAt: now(),
	})
}
