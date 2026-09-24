package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 引擎契约 1：组装是确定性的——同样的会话状态重复组装，请求材料逐字节一致
// （前缀缓存命中的根基；这里的环境快照含分钟级时间戳，同一分钟内可比）。
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
