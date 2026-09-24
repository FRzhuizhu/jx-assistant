package main

import (
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
