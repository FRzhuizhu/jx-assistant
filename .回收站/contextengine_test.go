package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 引擎契约 1：组装是确定性的——同样的会话状态重复组装，请求材料逐字节一致
// （前缀缓存命中的根基；环境快照首行是「用户最近发消息时间」，取自存储而非挂钟，
// 同一状态重复组装逐字节一致——见 agent.go 的 envPrompt 与 Store.LastUserMessageAt）。
func TestContextEngineDeterministic(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "s1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	_ = a.Store.Append("s1", TextMessage("user", "第一轮需求"))
	_ = a.Store.Append("s1", TextMessage("assistant", "已完成"))

	asm1 := a.ctxEngine().Assemble(sess, nil)
	asm2 := a.ctxEngine().Assemble(sess, nil)
	if asm1.Sys != asm2.Sys || asm1.Env != asm2.Env || !reflect.DeepEqual(asm1.Tools, asm2.Tools) || !reflect.DeepEqual(asm1.Msgs, asm2.Msgs) {
		t.Fatal("同一状态的两次组装结果不一致（破坏前缀缓存的前提）")
	}
	// 测量口径随包产出且同样确定（token 估算/前缀指纹是调用日志与请求缓存的共用口径）
	if asm1.EstTokens <= 0 || asm1.SysHash == "" || asm1.EnvHash == "" {
		t.Fatalf("组装应带测量口径: est=%d sysHash=%q envHash=%q", asm1.EstTokens, asm1.SysHash, asm1.EnvHash)
	}
	if asm1.EstTokens != asm2.EstTokens || asm1.SysHash != asm2.SysHash || asm1.EnvHash != asm2.EnvHash {
		t.Fatal("测量口径也应确定性一致")
	}
	if asm1.Sys == "" || asm1.Env == "" || len(asm1.Tools) == 0 || len(asm1.Msgs) < 3 {
		t.Fatalf("组装材料不完整：sys=%d字 env=%d字 tools=%d msgs=%d", len(asm1.Sys), len(asm1.Env), len(asm1.Tools), len(asm1.Msgs))
	}
	// 消息数组末位应是环境快照（尾部 user 消息），工具目录与请求体同源
	if last := asm1.Msgs[len(asm1.Msgs)-1]; last.Role != "user" || !strings.Contains(fmt.Sprint(last.Content), "运行环境快照") {
		t.Fatalf("消息数组末位应为环境快照，实际 role=%q", last.Role)
	}
}

// 引擎契约 2：Notify 是上下文事件总线的入口，落 kind=audit、type=ctx 行。
func TestContextEngineNotifyJournal(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "s1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	a.ctxEngine().Notify(sess, ctxModel, "模型：a → b")
	raw, _ := a.Store.RawMessagesAll("s1")
	found := false
	for _, r := range raw {
		if r.Kind != "audit" {
			continue
		}
		var ar auditRow
		if json.Unmarshal([]byte(r.Text()), &ar) == nil && ar.Type == "ctx" && ar.Kind == ctxModel && ar.Detail == "模型：a → b" {
			found = true
		}
	}
	if !found {
		t.Fatal("Notify 应落 kind=audit、type=ctx 的事件行")
	}
}

// 引擎契约 3：会话配置经 PATCH 变更时经总线留痕（权限与模式开关各一条，
// 且带前后值；未变化的字段不产生噪声行）。
func TestPatchSessionNotifiesCtxEvents(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "s1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	h := a.Routes()
	patch := func(body string) {
		t.Helper()
		req := httptest.NewRequest("PATCH", "/api/sessions/s1", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("PATCH %s 失败：%d %s", body, rec.Code, rec.Body.String())
		}
	}
	patch(`{"title":"改名不带上下文变化"}`) // 标题不是上下文材料：不应产生事件
	patch(`{"permission":"full"}`)
	patch(`{"planMode":true}`)

	raw, _ := a.Store.RawMessagesAll("s1")
	var kinds []string
	for _, r := range raw {
		if r.Kind != "audit" {
			continue
		}
		var ar auditRow
		if json.Unmarshal([]byte(r.Text()), &ar) == nil && ar.Type == "ctx" {
			kinds = append(kinds, ar.Kind)
			if ar.Kind == ctxPerm && !strings.Contains(ar.Detail, "→") {
				t.Fatalf("权限事件应带前后值：%s", ar.Detail)
			}
		}
	}
	if len(kinds) != 2 || kinds[0] != ctxPerm || kinds[1] != ctxMode {
		t.Fatalf("应恰好产生权限与模式两条上下文事件，实际 %v", kinds)
	}
}

// 引擎契约 4：系统提示词支持引用文件覆盖（data/prompts/<mode>.md），版本行
// 标注来源与内容 hash；未覆盖时版本行与内置完全一致（存量会话缓存不受影响）。
func TestSystemPromptOverrideFile(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.DataDir = t.TempDir()
	sess := &SessionMeta{ID: "s1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}

	builtin := a.systemPrompt(sess)
	if !strings.HasSuffix(builtin, "驱动）") || strings.Contains(builtin, "引用文件") {
		t.Fatalf("未覆盖时版本行应保持内置原样：%q", builtin[len(builtin)-40:])
	}
	if !strings.Contains(builtin, taskPrompt[:20]) {
		t.Fatal("未覆盖时应使用内置基础提示词")
	}

	dir := filepath.Join(a.Cfg.DataDir, "prompts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("你是定制版任务助手，一切用中文简答。"), 0644); err != nil {
		t.Fatal(err)
	}
	over := a.systemPrompt(sess)
	if !strings.Contains(over, "你是定制版任务助手") || !strings.Contains(over, "引用文件 ") {
		t.Fatalf("覆盖后应使用引用文件并标注来源：%q", over[len(over)-60:])
	}
	if !strings.Contains(over, "文件修改与交付规范") {
		t.Fatal("模式附加段（产品硬规则）在覆盖后仍应追加")
	}
	if over == builtin {
		t.Fatal("覆盖未生效")
	}
}

// 引擎契约 5：OpenRound 开轮落 kind=prompt 快照行，材料与 Assemble 同源
// （主循环不再自拼这条行）；emit 为 nil 不应 panic；每轮各落一条。
func TestContextEngineOpenRound(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "s1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	_ = a.Store.Append("s1", TextMessage("user", "第一轮需求"))

	a.ctxEngine().OpenRound(sess, nil) // emit 为 nil：只落库，不发事件
	a.ctxEngine().OpenRound(sess, nil) // 第二轮：再落一条

	raw, err := a.Store.RawMessagesAll("s1")
	if err != nil {
		t.Fatal(err)
	}
	var pj struct {
		Sys   string    `json:"sys"`
		Env   string    `json:"env"`
		Tools []ToolDef `json:"tools"`
	}
	rows := 0
	for _, m := range raw {
		if m.Kind != kindPrompt {
			continue
		}
		rows++
		if err := json.Unmarshal([]byte(m.Text()), &pj); err != nil {
			t.Fatalf("prompt 行不是合法 JSON: %v", err)
		}
	}
	if rows != 2 {
		t.Fatalf("两次开轮应落两条 prompt 行，实际 %d", rows)
	}
	// 与 Assemble 同源：新会话无技能变更补丁行，env 两边一致；工具目录经
	// JSON 序列化对比（参数 schema 里的数字经 JSON 往返后是 float64，
	// 不能用 reflect.DeepEqual 与原生构造直比）
	asm := a.ctxEngine().Assemble(sess, nil)
	pjTools, _ := json.Marshal(pj.Tools)
	asmTools, _ := json.Marshal(asm.Tools)
	if pj.Sys != asm.Sys || pj.Env != asm.Env || string(pjTools) != string(asmTools) {
		t.Fatal("prompt 快照行应与 Assemble 请求材料同源")
	}
	if len(pj.Tools) == 0 || pj.Env == "" {
		t.Fatalf("快照材料不完整: tools=%d env=%d字", len(pj.Tools), len(pj.Env))
	}
}

// 引擎契约 7：工具目录相对上一轮有增删时，开轮先落一条 origin=tools 的
// 变更公告（追加式上下文的尾部消息）；目录未变不再重复公告。
func TestOpenRoundToolChangeAnnouncement(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "s1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	// 上一轮的 prompt 行带一个不存在的旧工具
	pj, _ := json.Marshal(promptRec{Sys: "S", Env: "E",
		Tools: []ToolDef{{Type: "function", Function: ToolSpec{Name: "ghost_tool", Parameters: map[string]any{}}}}})
	if err := a.Store.AppendRow("s1", &StoredMessage{Message: TextMessage("system", string(pj)), Kind: kindPrompt, CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}

	a.ctxEngine().OpenRound(sess, nil)
	raw, _ := a.Store.RawMessagesAll("s1")
	var note string
	for _, m := range raw {
		if m.Kind == kindReport && m.Origin == "tools" {
			note = m.Text()
		}
	}
	if note == "" {
		t.Fatal("工具目录变化时应落一条 origin=tools 的变更公告")
	}
	if !strings.Contains(note, "ghost_tool") || !strings.Contains(note, "移除工具") {
		t.Fatalf("公告应列出移除的工具: %s", note)
	}

	// 目录未变的开轮：不再追加公告
	a.ctxEngine().OpenRound(sess, nil)
	raw2, _ := a.Store.RawMessagesAll("s1")
	count := 0
	for _, m := range raw2 {
		if m.Kind == kindReport && m.Origin == "tools" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("目录未变不应重复公告，实际 %d 条", count)
	}
}

// 引擎契约 6：摘要模型调用归引擎——拼装（压缩助手系统提示词+输入）、发送、
// 留痕（audit type=call scope=summarize）一体；不进 kind=request 环形缓存
// （那是主对话前缀观察窗）；mock 目标保持占位摘要且不留调用痕。
func TestEngineCallSummarizer(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	var gotBody []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"摘要内容OK\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer stub.Close()
	// 首个 provider 是 mock（离线演示默认），p1 指向桩服务
	a.Cfg.Providers = []ProviderCfg{
		{ID: "mock0", Name: "离线", BaseURL: "mock", Models: []ModelCfg{{ID: "demo"}}},
		{ID: "p1", Name: "桩", BaseURL: stub.URL, Models: []ModelCfg{{ID: "sum-model"}}},
	}
	sess := &SessionMeta{ID: "sz1", Mode: "task", Workspace: t.TempDir(),
		Provider: "p1", Model: "sum-model", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}

	out, err := a.ctxEngine().CallSummarizer(sess, "【待压缩的对话历史】\n用户：你好")
	if err != nil {
		t.Fatalf("摘要调用应成功: %v", err)
	}
	if out != "摘要内容OK" {
		t.Fatalf("摘要产出不符: %q", out)
	}
	// 请求形态：system=引擎的压缩助手提示词，user=输入（与历史行为同形）
	var req struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v", err)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[0].Text() != summarizerPrompt ||
		req.Messages[1].Role != "user" || req.Messages[1].Text() != "【待压缩的对话历史】\n用户：你好" {
		t.Fatalf("摘要请求形态不符: %s", gotBody)
	}

	raw, _ := a.Store.RawMessagesAll("sz1")
	auditFound, ringRows := 0, 0
	for _, m := range raw {
		switch m.Kind {
		case kindAudit:
			var ar auditRow
			if json.Unmarshal([]byte(m.Text()), &ar) == nil && ar.Type == "call" && ar.Scope == "summarize" {
				auditFound++
			}
		case kindRequest:
			ringRows++
		}
	}
	if auditFound != 1 {
		t.Fatalf("应落一条 scope=summarize 的调用审计行，实际 %d", auditFound)
	}
	if ringRows != 0 {
		t.Fatalf("摘要调用不应进请求环形缓存，实际 %d 条", ringRows)
	}

	// mock 目标（无 Provider 回落首个 provider=mock）：占位摘要、不留调用痕
	mockSess := &SessionMeta{ID: "sz2", Mode: "task", Workspace: t.TempDir(), CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(mockSess); err != nil {
		t.Fatal(err)
	}
	out2, err := a.ctxEngine().CallSummarizer(mockSess, "任意输入")
	if err != nil || !strings.Contains(out2, "离线演示摘要") {
		t.Fatalf("mock 摘要应保持占位: %q %v", out2, err)
	}
	raw2, _ := a.Store.RawMessagesAll("sz2")
	for _, m := range raw2 {
		if m.Kind == kindAudit {
			t.Fatal("mock 摘要不应落调用审计行")
		}
	}
}
