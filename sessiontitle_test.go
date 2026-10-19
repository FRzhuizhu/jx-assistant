package main

// 「AI 自动会话标题」组件的验收测试：
//  1. PATCH 钉住语义：手动改 title 即打钉 → autoTitle 改动被 409 拒 →
//     autoTitleUnpin 解钉 → autoTitle 恢复可改且不打钉（对齐老 DSH「user
//     来源标题钉住」决策；GET 详情回带 titlePinned）；
//  2. GET /api/sessions/{id}/messages 轻量文本列表的 role=user 过滤；
//  3. POST /api/llm/ask 独立单轮调用（httptest 假 LLM 的 SSE 应答）——
//     一次请求一次输出，且不往会话消息表落任何行；
//  4. 全链路集成（需共享便携 Python，缺则跳过）：真实组件 worker 订阅
//     run.end → 反向读用户消息 → 调 llm/ask（假 LLM）→ 清洗 → PATCH autoTitle
//     改标题；再手动命名打钉后，同样的 run.end 不再改标题。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeLLMServer 起一个 OpenAI 兼容的 SSE 假模型：无 tools 的请求固定回复 reply。
func fakeLLMServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", enc)
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTitleTestApp 建一个接假 LLM 的最小 App（Store/DataDir 隔离在临时目录）。
func newTitleTestApp(t *testing.T, llmURL string) *App {
	t.Helper()
	a := newCompactTestApp(t)
	a.initRuntime()
	a.Cfg.Workspace = t.TempDir()
	a.Cfg.Providers = nil
	a.Cfg.BaseURL = llmURL
	a.Cfg.Model = "fake-model"
	return a
}

func doReq(t *testing.T, a *App, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func mkTitleSession(t *testing.T, a *App, id string) {
	t.Helper()
	if err := a.Store.Create(&SessionMeta{ID: id, Title: "新会话", Mode: "task",
		Workspace: a.Cfg.Workspace, CreatedAt: now(), UpdatedAt: now()}); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
}

// ① PATCH 钉住语义。
func TestTitlePinSemantics(t *testing.T) {
	a := newTitleTestApp(t, "")
	mkTitleSession(t, a, "s-pin")

	// 用户手动命名 → 打钉
	rec, _ := doReq(t, a, "PATCH", "/api/sessions/s-pin", `{"title":"我的手工命名"}`)
	if rec.Code != 200 {
		t.Fatalf("手动命名应 200，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	rec, d := doReq(t, a, "GET", "/api/sessions/s-pin", "")
	if rec.Code != 200 || d["titlePinned"] != true {
		t.Fatalf("手动命名后应 titlePinned=true，得到 code=%d pinned=%v", rec.Code, d["titlePinned"])
	}
	// 组件通道（autoTitle）改钉住会话 → 409 拒绝
	rec, _ = doReq(t, a, "PATCH", "/api/sessions/s-pin", `{"title":"AI 想改","autoTitle":true}`)
	if rec.Code != 409 {
		t.Fatalf("钉住会话的 autoTitle 改名应 409，得到 %d", rec.Code)
	}
	if m, _ := a.Store.Get("s-pin"); m.Title != "我的手工命名" {
		t.Fatalf("钉住标题不应被改写，得到 %q", m.Title)
	}
	// 解钉 → autoTitle 恢复可改，且成功后不打钉
	rec, _ = doReq(t, a, "PATCH", "/api/sessions/s-pin", `{"autoTitleUnpin":true}`)
	if rec.Code != 200 {
		t.Fatalf("解钉应 200，得到 %d", rec.Code)
	}
	rec, _ = doReq(t, a, "PATCH", "/api/sessions/s-pin", `{"title":"AI 修订后的标题","autoTitle":true}`)
	if rec.Code != 200 {
		t.Fatalf("解钉后 autoTitle 应 200，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	rec, d = doReq(t, a, "GET", "/api/sessions/s-pin", "")
	if d["titlePinned"] != false {
		t.Fatalf("autoTitle 成功后不应打钉，得到 %v", d["titlePinned"])
	}
	if m, _ := a.Store.Get("s-pin"); m.Title != "AI 修订后的标题" {
		t.Fatalf("autoTitle 标题未生效: %q", m.Title)
	}
}

// ② 轻量消息列表的 role 过滤。
func TestSessionMessagesRoleFilter(t *testing.T) {
	a := newTitleTestApp(t, "")
	mkTitleSession(t, a, "s-msg")
	for _, m := range []Message{
		TextMessage("user", "帮我做一个标题组件"),
		TextMessage("assistant", "好的，我来实现"),
		TextMessage("user", "顺便加上钉住机制"),
	} {
		if err := a.Store.Append("s-msg", m); err != nil {
			t.Fatalf("落消息失败: %v", err)
		}
	}
	rec, d := doReq(t, a, "GET", "/api/sessions/s-msg/messages?role=user", "")
	if rec.Code != 200 {
		t.Fatalf("messages 应 200，得到 %d", rec.Code)
	}
	msgs, _ := d["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("role=user 应只回 2 条，得到 %d（%v）", len(msgs), d)
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "user" || first["text"] != "帮我做一个标题组件" {
		t.Fatalf("消息内容不符: %v", first)
	}
	// 不带 role：全部有文本的行
	_, d = doReq(t, a, "GET", "/api/sessions/s-msg/messages", "")
	all, _ := d["messages"].([]any)
	if len(all) != 3 {
		t.Fatalf("不带 role 应回 3 条，得到 %d", len(all))
	}
}

// ③ 独立单轮调用：一次请求一次输出，不落会话消息表。
func TestLLMAskSingleTurn(t *testing.T) {
	llm := fakeLLMServer(t, "「组件标题」")
	a := newTitleTestApp(t, llm.URL)
	mkTitleSession(t, a, "s-ask")

	rec, d := doReq(t, a, "POST", "/api/llm/ask",
		`{"session":"s-ask","system":"你是标题撰写员","user":"当前标题：（无）\n用户消息：[1] 做个标题组件"}`)
	if rec.Code != 200 {
		t.Fatalf("llm/ask 应 200，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	if d["text"] != "「组件标题」" {
		t.Fatalf("应答文本不符: %v", d["text"])
	}
	// 独立路径：会话消息表不能多出任何行（不进上下文）
	msgs, err := a.Store.Messages("s-ask")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("独立调用不应落消息表，现有 %d 条", len(msgs))
	}
	// 空 user 拒绝
	rec, _ = doReq(t, a, "POST", "/api/llm/ask", `{"user":"  "}`)
	if rec.Code != 400 {
		t.Fatalf("空 user 应 400，得到 %d", rec.Code)
	}
}

// ④ 全链路：真实组件 worker（组件/session-title）收 run.end → 生成并落标题；
// 手动命名打钉后同样的 run.end 不再改。
func TestSessionTitleWorkerEndToEnd(t *testing.T) {
	testPython(t) // 共享运行时缺失时 skip（同桥钩子集成测试口径）；解释器由 {python} 声明解析
	if _, err := os.Stat(filepath.Join("组件", "session-title", "title_worker.py")); err != nil {
		t.Skip("组件目录未就绪：跳过端到端测试")
	}
	llm := fakeLLMServer(t, "  \"订单导出与透视表\"\n")
	a := newTitleTestApp(t, llm.URL)
	// 临时组件根 + 组件目录 + 绝对解释器路径：真实 worker 脚本原样拷贝
	//（{python} 在 go test 的构建目录下解析不到仓库里的共享运行时）
	py := testPython(t)
	compRoot := t.TempDir()
	compDir := filepath.Join(compRoot, "session-title")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("组件", "session-title", "title_worker.py"))
	if err != nil {
		t.Skipf("组件 worker 脚本不可读: %v", err)
	}
	if err := os.WriteFile(filepath.Join(compDir, "title_worker.py"), src, 0644); err != nil {
		t.Fatal(err)
	}
	man := ComponentManifest{ID: "session-title", Name: "AI 自动会话标题", Version: "1"}
	man.Workers = []ComponentWorkerDef{{Name: "title", Command: []string{py, "title_worker.py"}, Hooks: []string{"run.end"}}}
	data, _ := json.Marshal(man)
	if err := os.WriteFile(filepath.Join(compDir, "manifest.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	a.Components = InitComponents(compRoot, filepath.Join(a.Cfg.DataDir, "components.json"))
	a.Bridge = newBridgeManager(a)
	t.Cleanup(func() { a.Bridge.KillComponent("session-title") })
	mkTitleSession(t, a, "s-e2e")
	if err := a.Store.Append("s-e2e", TextMessage("user", "把生产订单导出并做成透视表")); err != nil {
		t.Fatal(err)
	}

	// 一轮结束 → worker 异步生成标题（清洗掉首尾空白与包裹引号）
	subs := a.Components.HookSubscribers("run.end")
	t.Logf("run.end 订阅者 %d 个", len(subs))
	a.emitComponentHooks("run.end", map[string]any{"sessionID": "s-e2e", "status": "done", "error": ""})
	deadline := time.Now().Add(20 * time.Second)
	for {
		m, _ := a.Store.Get("s-e2e")
		if m != nil && m.Title != "新会话" {
			if m.Title != "订单导出与透视表" {
				t.Fatalf("标题清洗结果不符: %q", m.Title)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("20 秒内未见标题被 worker 改写")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 手动命名 → 打钉 → 新一轮 run.end 不改标题（换一个回复内容排除“恰好相同”）
	llm2 := fakeLLMServer(t, "完全不同的新标题")
	a.Cfg.BaseURL = llm2.URL
	rec, _ := doReq(t, a, "PATCH", "/api/sessions/s-e2e", `{"title":"我手工定的名字"}`)
	if rec.Code != 200 {
		t.Fatalf("手动命名应 200，得到 %d", rec.Code)
	}
	if err := a.Store.Append("s-e2e", TextMessage("user", "再来一轮新话题")); err != nil {
		t.Fatal(err)
	}
	a.emitComponentHooks("run.end", map[string]any{"sessionID": "s-e2e", "status": "done", "error": ""})
	time.Sleep(4 * time.Second) // worker 若违规改写，4 秒足够显形
	m, _ := a.Store.Get("s-e2e")
	if m.Title != "我手工定的名字" {
		t.Fatalf("钉住后标题不应被自动改写，得到 %q", m.Title)
	}
}
