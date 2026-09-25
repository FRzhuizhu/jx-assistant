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
//     （auditlog.go 的 Journal/RecordCall/RecordRequest），发给模型的请求
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
	Session  rawSessionInfo `json:"session"`
	Count    int            `json:"count"`
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
	From            int  `json:"from"` // 第几条请求（1 起）
	To              int  `json:"to"`
	MsgsDelta       int  `json:"msgsDelta"`       // 消息条数变化（新-旧）
	EstDelta        int  `json:"estDelta"`        // 估算 token 变化（新-旧）
	ToolsChanged    bool `json:"toolsChanged"`    // 工具目录是否变化（变化=前缀必断）
	SysHashChanged  bool `json:"sysHashChanged"`  // 系统提示词指纹是否变化
	EnvHashChanged  bool `json:"envHashChanged"`  // 环境快照指纹是否变化
	FirstDiffMessage int `json:"firstDiffMessage"` // 首条变化消息的下标（0 起；-1=消息数组未变）
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
		Note:     "requests 为引擎缓存的最近各轮请求（旧→新，body 与线上发送逐字节同源）；" +
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
		MsgsDelta: cur.MsgCount - prev.MsgCount,
		EstDelta:  cur.EstTokens - prev.EstTokens,
		SysHashChanged: cur.SysHash != prev.SysHash,
		EnvHashChanged: cur.EnvHash != prev.EnvHash,
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
