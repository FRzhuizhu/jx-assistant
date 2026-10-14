package main

// call_api 的验收测试：
//  1. 目录按权限档过滤（只读只见只读接口；⚠ 标记危险接口）；
//  2. 直调真的打到 handler（GET /api/modes 返回与前端同一份模式清单）；
//  3. 未开放接口与低档位调用被拒（目录闸 + 权限闸）；
//  4. 危险接口走审批卡：用户拒绝则取消、批准才执行（真的删掉会话）；
//  5. 工具注册与模式集（assistant/task 有、aux 无），环境快照注入目录区块。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// mkCallAPISession 建一个指定权限档的 task 会话并返回其 toolContext。
func mkCallAPISession(t *testing.T, a *App, id, perm string) *toolContext {
	t.Helper()
	m := &SessionMeta{ID: id, Title: "callapi 测试", Mode: "task", Permission: perm,
		Workspace: a.Cfg.Workspace, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(m); err != nil {
		t.Fatalf("建会话 %s 失败: %v", id, err)
	}
	return &toolContext{app: a, sess: m, emit: func(Event) {}, ctx: context.Background()}
}

// ① 目录按档位过滤；危险接口带 ⚠。
func TestCallAPICatalogByPerm(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	ro := a.apiCatalogText(permReadOnly)
	ws := a.apiCatalogText(permWorkspace)
	full := a.apiCatalogText(permFull)

	if !strings.Contains(ro, "GET /api/status") || !strings.Contains(ro, "GET /api/sessions") {
		t.Fatalf("只读目录应含只读接口：\n%s", ro)
	}
	if strings.Contains(ro, "POST /api/sessions ") || strings.Contains(ro, "DELETE /api/sessions/{id}") {
		t.Fatalf("只读目录不该含写接口：\n%s", ro)
	}
	if !strings.Contains(ws, "POST /api/sessions ") {
		t.Fatalf("读写目录应含 POST /api/sessions：\n%s", ws)
	}
	if strings.Contains(ws, "POST /api/config") {
		t.Fatalf("读写目录不该含完全访问接口（POST /api/config）：\n%s", ws)
	}
	if !strings.Contains(full, "POST /api/config") {
		t.Fatalf("完全访问目录应含 POST /api/config：\n%s", full)
	}
	// 危险标记：DELETE 会话在 ws 目录里可见但要带 ⚠（调用前审批）
	line := ""
	for _, l := range strings.Split(ws, "\n") {
		if strings.Contains(l, "DELETE /api/sessions/{id}") {
			line = l
		}
	}
	if line == "" || !strings.HasPrefix(strings.TrimSpace(line), "⚠") {
		t.Fatalf("危险接口应带 ⚠ 前缀，got：%q", line)
	}
}

// ② 直调真的执行 handler：GET /api/modes 返回前端模式下拉同一份清单。
func TestCallAPIDirectInvokeModes(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	ctx := mkCallAPISession(t, a, "t-ws", permWorkspace)
	out, err := runCallAPI(ctx, mustArgs(t, map[string]any{"path": "/api/modes"}))
	if err != nil {
		t.Fatalf("GET /api/modes 失败: %v", err)
	}
	if !strings.Contains(out, "HTTP 200") {
		t.Fatalf("应返回 HTTP 200：\n%s", out)
	}
	for _, m := range chatModes {
		if !strings.Contains(out, m.Value) || !strings.Contains(out, m.Label) {
			t.Fatalf("响应应含模式清单（%s=%s）：\n%s", m.Value, m.Label, out)
		}
	}
}

// ②b 带路径参数与请求体的直调：POST /api/sessions 真的建出会话（与前端同一
// handler、同一校验），再 PATCH 改标题。
func TestCallAPIDirectCreateAndPatch(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	ctx := mkCallAPISession(t, a, "t-ws", permWorkspace)
	out, err := runCallAPI(ctx, mustArgs(t, map[string]any{
		"path": "/api/sessions", "method": "POST",
		"params": map[string]any{"title": "callapi 建的会话", "mode": "office"},
	}))
	if err != nil {
		t.Fatalf("POST /api/sessions 失败: %v\n%s", err, out)
	}
	if !strings.Contains(out, "HTTP 200") || !strings.Contains(out, "callapi 建的会话") {
		t.Fatalf("建会话应成功并回显标题：\n%s", out)
	}
	sessions, err := a.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, m := range sessions {
		if m.Title == "callapi 建的会话" && m.Mode == "office" {
			id = m.ID
		}
	}
	if id == "" {
		t.Fatal("会话没有真的落库")
	}
	out, err = runCallAPI(ctx, mustArgs(t, map[string]any{
		"path": "/api/sessions/" + id, "method": "PATCH",
		"params": map[string]any{"title": "改过的标题"},
	}))
	if err != nil || !strings.Contains(out, "HTTP 200") || !strings.Contains(out, "改过的标题") {
		t.Fatalf("PATCH 改标题应成功：%v\n%s", err, out)
	}
}

// ③ 目录闸与权限闸：未开放接口拒绝；只读会话调写接口拒绝。
func TestCallAPIRejectsUnknownAndLowPerm(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	ws := mkCallAPISession(t, a, "t-ws2", permWorkspace)
	// 未开放（对 AI）：远程外壳通道
	_, err := runCallAPI(ws, mustArgs(t, map[string]any{"path": "/api/p2p-shell"}))
	if err == nil || !strings.Contains(err.Error(), "不在可调目录") {
		t.Fatalf("未开放接口应拒绝并指路，got %v", err)
	}
	// 不存在
	_, err = runCallAPI(ws, mustArgs(t, map[string]any{"path": "/api/no-such-thing"}))
	if err == nil || !strings.Contains(err.Error(), "不在可调目录") {
		t.Fatalf("不存在接口应拒绝，got %v", err)
	}
	// 只读会话调写接口：权限闸
	ro := mkCallAPISession(t, a, "t-ro", permReadOnly)
	_, err = runCallAPI(ro, mustArgs(t, map[string]any{"path": "/api/sessions", "method": "POST",
		"params": map[string]any{"title": "不该建出来"}}))
	if err == nil || !strings.Contains(err.Error(), "工作区内读写") {
		t.Fatalf("只读档调写接口应被权限闸拒绝（说明需要什么档），got %v", err)
	}
	if sessions, _ := a.Store.List(); len(sessions) != 3 { // main + t-ws2 + t-ro，被拒调用不添会话
		t.Fatalf("被拒的调用不该建出会话（现有 %d 个）", len(sessions))
	}
}

// ④ 危险接口审批：拒绝则取消；批准则真的执行（会话被删）。
func TestCallAPIDangerApproval(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	ws := mkCallAPISession(t, a, "t-ws3", permWorkspace)
	victim := mkSession(t, a, "victim", "待删的会话", "task", "")
	_ = victim // 会话 id 固定为 victim，断言用字符串 id 即可；这里保住变量供阅读

	// 用户拒绝
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := runCallAPI(ws, mustArgs(t, map[string]any{"path": "/api/sessions/victim", "method": "DELETE"}))
		ch <- result{out, err}
	}()
	resolveFirstApproval(t, a, false)
	r := <-ch
	if r.err == nil || !strings.Contains(r.err.Error(), "未批准") {
		t.Fatalf("拒绝审批后应取消，got %v / %s", r.err, r.out)
	}
	if _, err := a.Store.Get("victim"); err != nil {
		t.Fatal("被拒绝的删除不该执行")
	}

	// 用户批准
	go func() {
		out, err := runCallAPI(ws, mustArgs(t, map[string]any{"path": "/api/sessions/victim", "method": "DELETE"}))
		ch <- result{out, err}
	}()
	resolveFirstApproval(t, a, true)
	r = <-ch
	if r.err != nil || !strings.Contains(r.out, "HTTP 2") {
		t.Fatalf("批准后应执行删除，got %v / %s", r.err, r.out)
	}
	if _, err := a.Store.Get("victim"); err == nil {
		t.Fatal("批准后会话应真的被删除（与前端 DELETE 同一 handler）")
	}
}

// resolveFirstApproval 等第一张审批卡出现并作出决定（测试里替代用户点击）。
func resolveFirstApproval(t *testing.T, a *App, approved bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if aps := a.pendingApprovals(); len(aps) > 0 {
			if err := a.resolveApproval(aps[0].ID, approved, false); err != nil {
				t.Fatalf("审批决定失败: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("审批卡没有出现（危险接口调用应触发 requestApproval）")
}

// ⑤ 工具注册与模式集；环境快照注入目录。
func TestCallAPIToolRegistrationAndEnv(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	if !toolNames(t, a, "assistant")["call_api"] {
		t.Fatal("数字助理应有 call_api")
	}
	if !toolNames(t, a, "task")["call_api"] {
		t.Fatal("任务会话应有 call_api")
	}
	if toolNames(t, a, "aux")["call_api"] {
		t.Fatal("辅助对话（只读答疑）不该有 call_api")
	}
	// 环境快照：执行类模式注入「可调接口」区块，aux 不注入
	env := a.envPrompt(&SessionMeta{Mode: "task", Permission: permWorkspace, Workspace: a.Cfg.Workspace})
	if !strings.Contains(env, "可调接口") || !strings.Contains(env, "GET /api/status") {
		t.Fatalf("任务会话环境快照应含可调接口目录：\n%s", env)
	}
	envAux := a.envPrompt(&SessionMeta{Mode: "aux", Permission: permReadOnly, Workspace: a.Cfg.Workspace})
	if strings.Contains(envAux, "可调接口") {
		t.Fatal("aux 会话不该注入可调接口目录（没有 call_api 工具）")
	}
	// 只读会话的目录不含写接口
	envRO := a.envPrompt(&SessionMeta{Mode: "task", Permission: permReadOnly, Workspace: a.Cfg.Workspace})
	if strings.Contains(envRO, "POST /api/sessions ") {
		t.Fatal("只读会话的目录不该含写接口")
	}
}
