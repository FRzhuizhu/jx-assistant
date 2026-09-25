package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 上下文引擎：会话上下文的唯一组装者 + 上下文事件总线。
//
// 设计契约（对齐 pi 的显式装配管线与 Reasonix 的 cache-first loop）：
//  1. 一切影响「发给模型什么」的材料——系统提示词（可被 data/prompts/<mode>.md
//     引用文件覆盖）、环境快照、工具目录、历史/摘要视图、视觉内联——都经
//     Assemble 组装成请求材料；RunAgent 只做编排，不再亲手拼装。
//  2. 组装是确定性的：同样的会话状态与技能/提示词文件，重复组装得到逐字节
//     相同的请求材料（这是前缀缓存命中的根基；破坏确定性等于破坏缓存，
//     有测试看护，见 contextengine_test.go）。
//  3. 一切会改变下一次组装结果的会话级事实（权限/模型/思考档位/计划目标/
//     工作区/远程挂载变更）经 Notify 进总线，统一落上下文事件日志
//     （audit type=ctx 行，导出时间线按发生位置呈现）；技能是全局资源，
//     其增减不走 Notify——由环境快照 diff 在受影响会话的尾部留痕
//     （maybeAppendEnvSnapshot 的技能变更行），同一事实不重复记账。
//  4. 引擎不发模型调用、不执行工具、不做编排——那些仍是 RunAgent 的事；
//     组装产出的快照留痕（env 行）与请求材料同源，日志天然对得上线上。
//  5. 引擎也是记录者与导出门面：组装/调用/请求/事件统一经它落日志
//     （本文件后段的 Journal/RecordCall/RecordRequest），发给模型的请求
//     环形缓存最近 keepRequests 轮；前端两类下载（运行日志 / 原始数据）
//     统一经 ExportTimeline / ExportRawRequests 向引擎申请。
type ContextEngine struct{ app *App }

// ctxEngine 取上下文引擎：NewApp 已装配；测试等直接构造 App 的路径惰性
// 补一个（引擎无状态，重复装配无害）。
func (a *App) ctxEngine() *ContextEngine {
	if a.Ctx == nil {
		a.Ctx = &ContextEngine{app: a}
	}
	return a.Ctx
}

// Assembly 是一次组装的产物：一次模型调用请求的全部上下文材料。
type Assembly struct {
	Sys   string    // 系统提示词（含版本与提示词来源标识）
	Env   string    // 环境快照（发给模型时作为尾部 user 消息）
	Tools []ToolDef // 工具目录（请求体里平级发送的 JSON 数组）
	Msgs  []Message // 消息数组（压缩视图 + 环境快照尾消息 + 视觉内联）
}

// Assemble 为一步模型调用组装全部请求材料，并完成环境快照留痕（内容有变化
// 才落新快照行，见 maybeAppendEnvSnapshot）。RunAgent 每步调用一次。
func (e *ContextEngine) Assemble(sess *SessionMeta, emit func(Event)) *Assembly {
	sys := e.app.systemPrompt(sess)
	env := e.app.envPrompt(sess)
	env = e.app.maybeAppendEnvSnapshot(sess, env, emit)
	tools := e.app.toolsFor(sess.Mode)
	msgs := e.app.buildContext(sess, sys, env)
	msgs = inlineImagesForRequest(e.app, msgs, e.app.resolveTarget(sess))
	defs := make([]ToolDef, len(tools))
	for i, tl := range tools {
		defs[i] = tl.Spec
	}
	return &Assembly{Sys: sys, Env: env, Tools: defs, Msgs: msgs}
}

// 上下文事件类型（Notify 的 kind）：每类都必然改变下一次组装的结果。
const (
	ctxPerm      = "perm_changed"      // 权限/能力开关变更
	ctxModel     = "model_changed"     // 提供方/模型变更
	ctxThinking  = "thinking_changed"  // 思考档位变更
	ctxMode      = "mode_flag_changed" // 计划/目标模式开关
	ctxWorkspace = "workspace_changed" // 工作目录变更
	ctxRemote    = "remote_changed"    // 远程工作区挂载/解除
)

// Notify 是上下文事件总线的唯一入口：kind 用上面的 ctxEvent 常量，detail
// 一句人话（含前后值），原样进导出时间线。在配置落库成功后调用。
func (e *ContextEngine) Notify(sess *SessionMeta, kind, detail string) {
	if sess == nil {
		return
	}
	e.app.auditWrite(sess, map[string]any{"type": "ctx", "kind": kind, "detail": detail})
}

// promptOverride 读 data/prompts/<mode>.md 引用文件（存在即覆盖该模式的基础
// 提示词）；不存在返回空串。文件缺失/读失败按无覆盖处理，不阻断组装。
func (a *App) promptOverride(mode string) string {
	if a.Cfg == nil || a.Cfg.DataDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(a.Cfg.DataDir, "prompts", mode+".md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---------- 导出门面：前端两类下载统一向引擎申请 ----------

// ExportTimeline 生成会话的运行日志 Markdown（排版实现在 export.go）。
func (e *ContextEngine) ExportTimeline(m *SessionMeta) string {
	return e.app.exportTimeline(m)
}

// rawRequestExport 是「导出原始数据」的文档形态：引擎缓存的最近三轮请求
// （旧→新，Body 与线上发送逐字节同源）+ 相邻两两对比。
type rawRequestExport struct {
	Session  rawSessionInfo   `json:"session"`
	Count    int              `json:"count"`
	Requests []lastRequestRec `json:"requests"` // 旧→新
	Diffs    []rawRequestDiff `json:"diffs"`    // 相邻两两（#i → #i+1）
	Note     string           `json:"note"`
}

type rawSessionInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Mode  string `json:"mode"`
}

// rawRequestDiff 是相邻两次请求的对比：直接服务于「上下文变了什么、缓存
// 会不会断」——前缀在 FirstDiffMessage 之前的消息逐字相同，理论上仍命中。
type rawRequestDiff struct {
	From             int  `json:"from"` // 第几条请求（1 起）
	To               int  `json:"to"`
	MsgsDelta        int  `json:"msgsDelta"`        // 消息条数变化（新-旧）
	EstDelta         int  `json:"estDelta"`         // 估算 token 变化（新-旧）
	ToolsChanged     bool `json:"toolsChanged"`     // 工具目录是否变化（变化=前缀必断）
	SysHashChanged   bool `json:"sysHashChanged"`   // 系统提示词指纹是否变化
	EnvHashChanged   bool `json:"envHashChanged"`   // 环境快照指纹是否变化
	FirstDiffMessage int  `json:"firstDiffMessage"` // 首条变化消息的下标（0 起；-1=消息数组未变）
}

// ExportRawRequests 生成「导出原始数据」文档：最近 keepRequests 轮发给模型的
// 完整请求体 + 两两对比。没有请求记录时返回错误（与旧版口径一致）。
func (e *ContextEngine) ExportRawRequests(m *SessionMeta) ([]byte, error) {
	recs, err := e.RecentRequests(m, keepRequests)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("该会话还没有发给模型的请求记录（发送过一条消息后可用；离线演示模式不产生请求）")
	}
	doc := rawRequestExport{
		Session:  rawSessionInfo{ID: m.ID, Title: m.Title, Mode: m.Mode},
		Count:    len(recs),
		Requests: recs,
		Note: "requests 为引擎缓存的最近各轮请求（旧→新，body 与线上发送逐字节同源）；" +
			"diffs 为相邻两两对比：firstDiffMessage 之前的前缀逐字相同（理论上仍命中缓存），" +
			"toolsChanged/sysHashChanged 为真则整段前缀必不命中。",
	}
	for i := 1; i < len(recs); i++ {
		doc.Diffs = append(doc.Diffs, diffRequests(i, recs[i-1], recs[i]))
	}
	return json.MarshalIndent(doc, "", "  ")
}

// diffRequests 对比相邻两次请求，产出变化摘要。
func diffRequests(idx int, prev, cur lastRequestRec) rawRequestDiff {
	d := rawRequestDiff{
		From: idx, To: idx + 1,
		MsgsDelta:        cur.MsgCount - prev.MsgCount,
		EstDelta:         cur.EstTokens - prev.EstTokens,
		SysHashChanged:   cur.SysHash != prev.SysHash,
		EnvHashChanged:   cur.EnvHash != prev.EnvHash,
		FirstDiffMessage: -1,
	}
	var pb, cb struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	_ = json.Unmarshal(prev.Body, &pb)
	_ = json.Unmarshal(cur.Body, &cb)
	d.ToolsChanged = !toolDefsEqual(pb.Tools, cb.Tools)
	n := len(pb.Messages)
	if len(cb.Messages) < n {
		n = len(cb.Messages)
	}
	for i := 0; i <= n; i++ {
		var a, b json.RawMessage
		if i < len(pb.Messages) {
			a = pb.Messages[i]
		}
		if i < len(cb.Messages) {
			b = cb.Messages[i]
		}
		if !bytes.Equal(canonicalJSON(a), canonicalJSON(b)) {
			d.FirstDiffMessage = i
			break
		}
	}
	return d
}

// toolDefsEqual 比较两段工具目录 JSON 是否等价（都为空视为相等）。
func toolDefsEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return bytes.Equal(canonicalJSON(a), canonicalJSON(b))
}

// canonicalJSON 把 JSON 片段规整化（键序无关的稳定序列化），nil 返回 nil。
func canonicalJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// ---------- 引擎日志与请求缓存（上下文引擎的记录半边） ----------
//
// 引擎收到什么、发出什么都从这里落库，前端两类导出（运行日志 / 原始数据）
// 统一经引擎申请。
//
// kind=audit 行（role=system，前端对话流、上下文组装、历史工具都跳过）记录
// 「不发给模型、但复盘对话演进必需」的事件——每次模型调用的请求规模/前缀
// 指纹/返回/用量（缓存命中）、运行中报错（含网关错误响应摘要）、用户停止、
// 上下文超限重试、规则压缩生效、上下文事件（Notify）；导出日志按时间线呈现。
//
// kind=request 行缓存最近 keepRequests=3 次真正发给模型的完整请求体
// （环形裁剪，Body 与线上逐字节同源），供「导出原始数据」观察相邻请求间
// 的变化与缓存影响。

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
	Type    string      `json:"type"`
	Step    int         `json:"step"`  // call：本轮第几步（从 1 起）
	Msgs    int         `json:"msgs"`  // call：发送的消息条数
	Est     int         `json:"est"`   // call：发送前的 token 估算
	Ms      int64       `json:"ms"`    // call：耗时
	Text    int         `json:"text"`  // call：返回正文字数
	Tools   []string    `json:"tools"` // call：返回的工具调用名
	Usage   *auditUsage `json:"usage"`
	Error   string      `json:"error"`             // call/error/overflow：错误原文（网关错误含响应摘要）
	SysHash string      `json:"sysHash,omitempty"` // call：本次系统提示词指纹（变化=前缀缓存必断）
	EnvHash string      `json:"envHash,omitempty"` // call：本次环境快照指纹
	Rounds  int         `json:"rounds"`            // compact：折叠的轮数
	From    int64       `json:"from"`              // compact：折叠区间起始 seq
	To      int64       `json:"to"`                // compact：折叠区间结束 seq
	Kind    string      `json:"kind"`              // ctx（上下文事件总线）：事件类别，见上文常量
	Detail  string      `json:"detail"`            // ctx：一句人话说明（含前后值）
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
