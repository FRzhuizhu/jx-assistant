package main

// dispatch 的 remote_workspace（二号工作区）测试：
//   - resolveDispatchRemote 命中/未命中/返回副本（改副本不污染登记库）
//   - dispatch_sub / dispatch_task 带 remote_workspace：子会话挂上登记副本；
//     与 workspace 互斥；缺省时远程父会话继承不回归
//   - envPrompt 的「可派发远程工作区」公告三态（有登记/空库/aux 无行）

import (
	"strings"
	"testing"
)

// seedRegisteredRemote 往登记库放一条可用的远程工作区，返回其 display key。
func seedRegisteredRemote(t *testing.T, a *App, host, dir string) string {
	t.Helper()
	a.registerRemote(&RemoteWorkspace{
		Host: host, Port: 22, User: "tester", Auth: "password",
		Password: "pw", Dir: dir,
	})
	for _, r := range a.loadRegisteredRemotes() {
		if r.Host == host {
			return r.Display()
		}
	}
	t.Fatalf("登记后读不回 %s", host)
	return ""
}

func TestResolveDispatchRemote(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	key := seedRegisteredRemote(t, a, "disp-hit.example", "/srv/proj")

	got, err := a.resolveDispatchRemote(key)
	if err != nil || got == nil {
		t.Fatalf("按 display 命中失败: %v", err)
	}
	if got.Host != "disp-hit.example" || got.Dir != "/srv/proj" || got.Password != "pw" {
		t.Fatalf("副本内容异常: %+v", got)
	}
	// 副本隔离：改副本不影响登记库
	got.Password = "changed"
	if r, _ := a.resolveDispatchRemote(key); r.Password != "pw" {
		t.Fatalf("副本被改污染了登记库: %q", r.Password)
	}
	// 未命中给可行动报错；空 key 返回 nil（缺省路径）
	if _, err := a.resolveDispatchRemote("user@nope:1:/x"); err == nil || !strings.Contains(err.Error(), "不在已登记清单") {
		t.Fatalf("未命中报错误导: %v", err)
	}
	if r, err := a.resolveDispatchRemote("  "); err != nil || r != nil {
		t.Fatalf("空 key 应返回 nil,nil: %v %v", r, err)
	}
}

func TestDispatchSubRemoteWorkspace(t *testing.T) {
	a, main := newDispatchTestApp(t)
	key := seedRegisteredRemote(t, a, "sub-rw.example", "/srv/data")
	ctx := dispatchCtx(a, main)

	// 互斥：workspace 与 remote_workspace 同时给
	_, err := runDispatchSub(ctx, mustArgs(t, map[string]any{
		"task": "互斥测试", "workspace": `D:\tmp`, "remote_workspace": key,
	}))
	if err == nil || !strings.Contains(err.Error(), "互斥") {
		t.Fatalf("应报互斥: %v", err)
	}
	// 未登记的远程：可行动报错
	if _, err = runDispatchSub(ctx, mustArgs(t, map[string]any{
		"task": "坏key", "remote_workspace": "user@nope:/x",
	})); err == nil || !strings.Contains(err.Error(), "不在已登记清单") {
		t.Fatalf("应报未登记: %v", err)
	}
	// 正常派发：本地主对话 + 远程子对话（二号工作区）
	if _, err = runDispatchSub(ctx, mustArgs(t, map[string]any{
		"task": "去远程整理数据", "remote_workspace": key,
	})); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	subs := otherSessions(t, a)
	if len(subs) != 1 || subs[0].Remote == nil {
		t.Fatalf("应建出一个远程子会话: %+v", subs)
	}
	if subs[0].Remote.Host != "sub-rw.example" || subs[0].Remote.Dir != "/srv/data" {
		t.Fatalf("子会话远程不对: %+v", subs[0].Remote)
	}
}

func TestDispatchSubRemoteInheritanceNotRegressed(t *testing.T) {
	a, main := newDispatchTestApp(t)
	parentRemote := &RemoteWorkspace{Host: "parent.example", Port: 22, User: "u", Auth: "password", Password: "p", Dir: "/srv/parent"}
	main.Remote = parentRemote
	ctx := dispatchCtx(a, main)

	// 缺省：远程父会话继承同一远程
	if _, err := runDispatchSub(ctx, mustArgs(t, map[string]any{"task": "继承"})); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	subs := otherSessions(t, a)
	if len(subs) != 1 || subs[0].Remote == nil || subs[0].Remote.Host != "parent.example" {
		t.Fatalf("缺省应继承父远程: %+v", subs)
	}
	// 显式 remote_workspace 优先于继承（换目标）
	key := seedRegisteredRemote(t, a, "sub-other.example", "/srv/other")
	if _, err := runDispatchSub(ctx, mustArgs(t, map[string]any{
		"task": "换目标", "remote_workspace": key,
	})); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	// Store.List 顺序不保证：按远程主机定位断言，不按下标
	var inheritChild, overrideChild *SessionMeta
	for _, s := range otherSessions(t, a) {
		if s.Remote == nil {
			continue
		}
		switch s.Remote.Host {
		case "parent.example":
			inheritChild = s
		case "sub-other.example":
			overrideChild = s
		}
	}
	if inheritChild == nil {
		t.Fatal("缺省派发应继承父远程（parent.example）")
	}
	if overrideChild == nil {
		t.Fatal("显式远程应覆盖继承（sub-other.example）")
	}
}

func TestDispatchTaskRemoteWorkspace(t *testing.T) {
	a, main := newDispatchTestApp(t)
	key := seedRegisteredRemote(t, a, "task-rw.example", "/srv/build")
	ctx := dispatchCtx(a, main)

	// 互斥
	_, err := runDispatchTask(ctx, mustArgs(t, map[string]any{
		"task": "互斥", "workspace": `D:\tmp`, "remote_workspace": key,
	}))
	if err == nil || !strings.Contains(err.Error(), "互斥") {
		t.Fatalf("应报互斥: %v", err)
	}
	// 正常派发到远程 + 附件被拒
	if _, err = runDispatchTask(ctx, mustArgs(t, map[string]any{
		"task": "去远程构建", "remote_workspace": key,
		"attachments": []string{`D:\不存在的附件.docx`},
	})); err == nil || !strings.Contains(err.Error(), "附件不能带给远程") {
		t.Fatalf("远程派发应拒附件: %v", err)
	}
	if _, err = runDispatchTask(ctx, mustArgs(t, map[string]any{
		"task": "去远程构建", "remote_workspace": key,
	})); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	subs := otherSessions(t, a)
	if len(subs) != 1 || subs[0].Remote == nil || subs[0].Remote.Host != "task-rw.example" {
		t.Fatalf("应建出远程任务会话: %+v", subs)
	}
	// 回报注记提到远程
	// （deliverSub 的 note 在返回串里，这里只验证会话侧；串的验证交给 envPrompt 之外的手测）
}

func TestEnvPromptDispatchRemoteLine(t *testing.T) {
	a, main := newDispatchTestApp(t)
	// 空登记库：无公告行
	if s := a.envPrompt(main); strings.Contains(s, "可派发远程工作区") {
		t.Fatalf("空库不应有公告行:\n%s", s)
	}
	key := seedRegisteredRemote(t, a, "envline.example", "/srv/x")
	s := a.envPrompt(main)
	if !strings.Contains(s, "可派发远程工作区") || !strings.Contains(s, key) {
		t.Fatalf("登记库非空应公告（含完整地址）:\n%s", s)
	}
	// aux 模式：无派发工具，不给公告
	aux := &SessionMeta{ID: "aux1", Mode: "aux", Workspace: a.Cfg.Workspace}
	if s := a.envPrompt(aux); strings.Contains(s, "可派发远程工作区") {
		t.Fatalf("aux 不应有公告行:\n%s", s)
	}
}

func TestDispatchToolsSchemaHasRemoteWorkspace(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	taskProps := paramTypes(t, dispatchTaskTool())
	if _, ok := taskProps["remote_workspace"]; !ok {
		t.Fatal("dispatch_task 缺 remote_workspace 参数")
	}
	subProps := paramTypes(t, a.dispatchSubTool())
	if _, ok := subProps["remote_workspace"]; !ok {
		t.Fatal("dispatch_sub 缺 remote_workspace 参数")
	}
}
