package main

// 上下文引擎的记录半边（contextengine.go 的配套实现）：引擎收到什么、发出
// 什么都从这里落库，前端两类导出（运行日志 / 原始数据）统一经引擎申请。
//
// kind=audit 行（role=system，前端对话流、上下文组装、历史工具都跳过）记录
// 「不发给模型、但复盘对话演进必需」的事件——每次模型调用的请求规模/前缀
// 指纹/返回/用量（缓存命中）、运行中报错（含网关错误响应摘要）、用户停止、
// 上下文超限重试、规则压缩生效、上下文事件（Notify）；导出日志按时间线呈现。
//
// kind=request 行缓存最近 keepRequests=3 次真正发给模型的完整请求体
// （环形裁剪，Body 与线上逐字节同源），供「导出原始数据」观察相邻请求间
// 的变化与缓存影响。

import (
	"encoding/json"
)

// Journal 落一条引擎日志事件行；payload 至少带 type
// （call|error|stop|compact|overflow|ctx）。
func (e *ContextEngine) Journal(sess *SessionMeta, payload map[string]any) {
	if sess == nil {
		return
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = e.app.Store.AppendRow(sess.ID, &StoredMessage{
		Message: TextMessage("system", string(b)), Kind: kindAudit, CreatedAt: now(),
	})
}

// auditWrite 兼容入口：存量调用点（主循环报错/压缩/停止等）统一汇入引擎日志。
func (a *App) auditWrite(sess *SessionMeta, payload map[string]any) {
	a.ctxEngine().Journal(sess, payload)
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
	Type   string      `json:"type"`
	Step   int         `json:"step"`  // call：本轮第几步（从 1 起）
	Msgs   int         `json:"msgs"`  // call：发送的消息条数
	Est    int         `json:"est"`   // call：发送前的 token 估算
	Ms     int64       `json:"ms"`    // call：耗时
	Text   int         `json:"text"`  // call：返回正文字数
	Tools  []string    `json:"tools"` // call：返回的工具调用名
	Usage  *auditUsage `json:"usage"`
	Error  string      `json:"error"` // call/error/overflow：错误原文（网关错误含响应摘要）
	SysHash string     `json:"sysHash,omitempty"` // call：本次系统提示词指纹（变化=前缀缓存必断）
	EnvHash string     `json:"envHash,omitempty"` // call：本次环境快照指纹
	Rounds int         `json:"rounds"` // compact：折叠的轮数
	From   int64       `json:"from"`   // compact：折叠区间起始 seq
	To     int64       `json:"to"`     // compact：折叠区间结束 seq
	Kind   string      `json:"kind"`   // ctx（上下文事件总线）：事件类别，见 contextengine.go
	Detail string      `json:"detail"` // ctx：一句人话说明（含前后值）
}

// RecordCall 在一次模型调用返回后（无论成败）落引擎日志行：调用前后的关键
// 事实一次记全（含当次组装的前缀指纹——指纹变化即能解释缓存命中变化）。
func (e *ContextEngine) RecordCall(sess *SessionMeta, step, msgs, est int, sysHash, envHash string, ms int64, usage *Usage, textRunes int, toolNames []string, callErr error) {
	p := map[string]any{
		"type": "call", "step": step, "msgs": msgs, "est": est, "ms": ms, "text": textRunes,
	}
	if sysHash != "" {
		p["sysHash"] = sysHash
	}
	if envHash != "" {
		p["envHash"] = envHash
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
	e.Journal(sess, p)
}

// toolNamesOf 取一组工具调用的名字（审计行的返回摘要用）。
func toolNamesOf(calls []ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, tc := range calls {
		names = append(names, tc.Function.Name)
	}
	return names
}

// lastRequestRec 是 kind=request 行的正文：一次发给模型的请求全量记录。
// Body 与线上逐字节同源（发送前捕获）；URL/Model 只记元信息，绝不含 API Key。
// Sys/Env 指纹与 audit 行同源，导出对比用。
type lastRequestRec struct {
	URL       string          `json:"url"`
	Model     string          `json:"model"`
	Step      int             `json:"step"`
	MsgCount  int             `json:"msgs"`
	EstTokens int             `json:"estTokens"`
	SysHash   string          `json:"sysHash,omitempty"`
	EnvHash   string          `json:"envHash,omitempty"`
	Body      json.RawMessage `json:"body"`
}

// RecordRequest 在请求发出前捕获并落库（环形保留最近 keepRequests 条）。
func (e *ContextEngine) RecordRequest(sess *SessionMeta, url, model string, step, msgs, est int, sysHash, envHash string, body []byte) {
	rec := lastRequestRec{URL: url, Model: model, Step: step, MsgCount: msgs, EstTokens: est,
		SysHash: sysHash, EnvHash: envHash, Body: append(json.RawMessage(nil), body...)}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = e.app.Store.AppendRequestRow(sess.ID, &StoredMessage{
		Message: TextMessage("system", string(b)), Kind: kindRequest, CreatedAt: now(),
	})
}

// RecentRequests 取最近 n 次发给模型的请求记录（旧→新）；供原始数据导出。
func (e *ContextEngine) RecentRequests(m *SessionMeta, n int) ([]lastRequestRec, error) {
	rows, err := e.app.Store.RecentRequestRows(m.ID, n)
	if err != nil {
		return nil, err
	}
	out := make([]lastRequestRec, 0, len(rows))
	for _, r := range rows {
		var rec lastRequestRec
		if err := json.Unmarshal([]byte(r.Text()), &rec); err == nil && len(rec.Body) > 0 {
			out = append(out, rec)
		}
	}
	return out, nil
}
