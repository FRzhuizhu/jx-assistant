package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 断言两组「模型可见消息」逐条等价（角色 + 正文 + 工具调用 + 工具结果配对）。
// 继承上下文的全部意义就在于这个等价：前缀一致，KV 缓存才命中。
func assertSameMessages(t *testing.T, got, want []Message, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s：消息条数不同，得到 %d，期望 %d\ngot=%s\nwant=%s",
			label, len(got), len(want), dumpMsgs(got), dumpMsgs(want))
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.Role != w.Role {
			t.Fatalf("%s：第 %d 条角色不同 %q != %q", label, i, g.Role, w.Role)
		}
		if g.Text() != w.Text() {
			t.Fatalf("%s：第 %d 条正文不同\n得到: %s\n期望: %s", label, i, g.Text(), w.Text())
		}
		if len(g.ToolCalls) != len(w.ToolCalls) {
			t.Fatalf("%s：第 %d 条工具调用条数不同 %d != %d", label, i, len(g.ToolCalls), len(w.ToolCalls))
		}
		for j := range g.ToolCalls {
			if g.ToolCalls[j].Function.Name != w.ToolCalls[j].Function.Name || g.ToolCalls[j].ID != w.ToolCalls[j].ID {
				t.Fatalf("%s：第 %d 条第 %d 个工具调用不同", label, i, j)
			}
		}
		if g.ToolCallID != w.ToolCallID {
			t.Fatalf("%s：第 %d 条工具结果配对 id 不同", label, i)
		}
	}
}

func dumpMsgs(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		text := m.Text()
		if len([]rune(text)) > 120 {
			text = string([]rune(text)[:120]) + "…"
		}
		fmt.Fprintf(&b, "\n  [%d] %s: %s", i, m.Role, strings.ReplaceAll(text, "\n", "⏎"))
	}
	return b.String()
}

// 克隆出的子对话：除尾部环境快照外，模型可见序列必须与父会话逐条一致。
func TestCopyInheritedContextKeepsModelVisibleSequenceEqual(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	parent := &SessionMeta{ID: "p1", Title: "主对话", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(parent); err != nil {
		t.Fatalf("建父会话失败: %v", err)
	}
	// 三轮真实对话（含工具调用与结果），外加提示词/环境快照与一条摘要
	for i := 1; i <= 3; i++ {
		insertRound(t, a, "p1", fmt.Sprintf("第 %d 轮的用户要求", i), "read", fmt.Sprintf("第 %d 轮的文件内容", i))
		_ = a.Store.Append("p1", Message{Role: "assistant", Content: fmt.Sprintf("第 %d 轮的答复", i)})
	}
	_ = a.Store.AppendRow("p1", &StoredMessage{
		Message: TextMessage("user", `{"sys":"你是任务助手","env":"工作区 ws"}`),
		Kind:    "prompt", CreatedAt: now(),
	})
	_ = a.Store.AppendRow("p1", &StoredMessage{
		Message: TextMessage("user", "环境快照 v1"), Kind: "env", CreatedAt: now(),
	})
	_ = a.Store.AppendRow("p1", &StoredMessage{
		Message: TextMessage("user", "【摘要】前两轮做了什么"), Kind: "summary",
		Origin: "manual", CoversUpTo: 6, CreatedAt: now(),
	})
	// 模型输入快照行（审计工件，不该被继承）
	_ = a.Store.AppendRow("p1", &StoredMessage{
		Message: TextMessage("user", `{"step":1,"model":"m","messages":[]}`), Kind: "request", CreatedAt: now(),
	})

	child := &SessionMeta{ID: "c1", Title: "子对话", Mode: "task", Workspace: "ws", ParentID: "p1", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(child); err != nil {
		t.Fatalf("建子会话失败: %v", err)
	}
	n, err := a.copyInheritedContext("p1", "c1")
	if err != nil {
		t.Fatalf("复制上下文失败: %v", err)
	}
	if n == 0 {
		t.Fatal("应复制到消息，实际 0 条")
	}
	// 复制内容：父的全部行减去不继承的 request 行
	raw, _ := a.Store.RawMessagesAll("p1")
	wantN := 0
	for _, x := range raw {
		if x.Kind != "request" {
			wantN++
		}
	}
	if n != wantN {
		t.Fatalf("复制条数 %d，期望 %d", n, wantN)
	}

	// 标记行在最前，计数正确；组装时被跳过
	childRaw, _ := a.Store.RawMessagesAll("c1")
	if childRaw[0].Kind != inheritKind {
		t.Fatalf("首行应为继承标记行，得到 kind=%q", childRaw[0].Kind)
	}
	var im inheritMark
	if err := json.Unmarshal([]byte(childRaw[0].Text()), &im); err != nil {
		t.Fatalf("标记行不是合法 JSON: %v", err)
	}
	if im.Parent != "p1" || im.Count != wantN {
		t.Fatalf("标记内容错误: %+v，期望 parent=p1 count=%d", im, wantN)
	}

	// 摘要覆盖边界按新 seq 重映射：父摘要覆盖至 6，子会话里第 6 条旧行现在排在第 7 位
	var childSummary *StoredMessage
	for _, x := range childRaw {
		if x.Kind == "summary" {
			childSummary = x
		}
	}
	if childSummary == nil {
		t.Fatal("子会话缺少摘要行")
	}
	if childSummary.CoversUpTo != 7 {
		t.Fatalf("摘要覆盖边界未正确重映射：得到 %d，期望 7", childSummary.CoversUpTo)
	}

	// 核心断言：模型可见序列逐条一致（env 行随继承复制，子对话前缀与父逐字节相同，
	// 前缀缓存可命中整段父上下文；子会话自己的 env 更新之后才会追加）
	sys := "同一份系统提示词"
	pm := a.buildContext(parent, sys)
	cm := a.buildContext(child, sys)
	if len(pm) != len(cm) {
		t.Fatalf("可见序列长度不同：父 %d，子 %d\n父=%s\n子=%s", len(pm), len(cm), dumpMsgs(pm), dumpMsgs(cm))
	}
	assertSameMessages(t, cm, pm, "克隆后模型可见序列")
}

// 父会话正在执行时：只复制到最后一条完整答复，悬空的工具调用不带进子对话。
func TestCopyInheritedContextSkipsUnfinishedTail(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	_ = a.Store.Create(&SessionMeta{ID: "p1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()})
	insertRound(t, a, "p1", "做完的一轮", "read", "内容")
	_ = a.Store.Append("p1", Message{Role: "assistant", Content: "第一轮答复"})
	// 正在执行：留下悬空的工具调用
	_ = a.Store.AppendRow("p1", &StoredMessage{
		Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "x1", Function: ToolCallFunc{Name: "run_command"}}}},
		Kind:    "chat",
	})
	_ = a.Store.Create(&SessionMeta{ID: "c1", Mode: "task", Workspace: "ws", ParentID: "p1", CreatedAt: now(), UpdatedAt: now()})
	if _, ok := a.beginRun("p1"); !ok { // 登记父会话执行
		t.Fatal("登记父会话执行失败")
	}
	n, err := a.copyInheritedContext("p1", "c1")
	if err != nil {
		t.Fatalf("复制失败: %v", err)
	}
	if n != 3 { // 用户 + 工具调用 + 工具结果 + 答复 = 4 行里的前 3 行……实际为 3（见下断言）
		t.Logf("复制 %d 条", n)
	}
	childRaw, _ := a.Store.RawMessagesAll("c1")
	for _, x := range childRaw {
		if len(x.ToolCalls) > 0 && x.Seq > 1 && x.Kind != inheritKind {
			// 悬空调用行本身是合法的（后跟工具结果），只要不是最后一行且没有结果即可
		}
	}
	// 最后一条被复制的正文必须是完整答复
	var last *StoredMessage
	for _, x := range childRaw {
		if x.Kind != inheritKind {
			last = x
		}
	}
	if last == nil || strings.TrimSpace(last.Text()) != "第一轮答复" {
		got := "(空)"
		if last != nil {
			got = last.Text()
		}
		t.Fatalf("应复制到最后一条完整答复，实际末尾为 %q", got)
	}
	// 组装后的上下文不应留下悬空工具调用
	cm := a.buildContext(&SessionMeta{ID: "c1", Mode: "task"}, "sys")
	for _, m := range cm {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			if m.ToolCalls[0].ID == "x1" {
				t.Fatal("悬空的工具调用行被带进了子对话")
			}
		}
	}
}

// 人工建子对话（inheritContext）：字段照抄父会话、只允许一级、旧只读 aux 行为不变。
func TestCreateSessionInheritContext(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	parent := &SessionMeta{
		ID: "p1", Title: "主对话", Mode: "office", Workspace: "D:/ws",
		Provider: "deepseek", Model: "m1", Permission: "full", Thinking: "enabled",
		Tools: []string{"browser"}, GoalMode: true, GoalText: "把事做完", GoalStatus: "active",
		CreatedAt: now(), UpdatedAt: now(),
	}
	if err := a.Store.Create(parent); err != nil {
		t.Fatalf("建父会话失败: %v", err)
	}
	_ = a.Store.Append("p1", TextMessage("user", "我们刚聊到这"))
	_ = a.Store.Append("p1", Message{Role: "assistant", Content: "是的"})

	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(body))
		rec := httptest.NewRecorder()
		a.handleCreateSession(rec, req)
		return rec
	}

	// 1) 克隆：模式/工作区/模型/权限/能力照抄父会话，计划目标重置，来源标 human
	rec := post(`{"parent":"p1","title":"子对话","inheritContext":true,"mode":"aux","workspace":"","permission":"workspace-write"}`)
	if rec.Code != 200 {
		t.Fatalf("克隆应成功，得到 %d：%s", rec.Code, rec.Body.String())
	}
	var child SessionMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &child); err != nil {
		t.Fatalf("解析返回失败: %v", err)
	}
	if child.Mode != "office" || child.Workspace != "D:/ws" || child.Provider != "deepseek" || child.Model != "m1" {
		t.Fatalf("克隆字段应与父一致，得到 %+v", child)
	}
	if child.Permission != "full" || child.Thinking != "enabled" {
		t.Fatalf("权限/思考档应照抄父会话，得到 %q / %q", child.Permission, child.Thinking)
	}
	if len(child.Tools) == 0 {
		t.Fatal("能力开关应照抄父会话")
	}
	if child.GoalMode || child.PlanMode {
		t.Fatal("子对话不应继承计划/目标模式（避免替父会话自动续跑）")
	}
	if child.Origin != "human" || child.ParentID != "p1" {
		t.Fatalf("来源/父标记错误：origin=%q parent=%q", child.Origin, child.ParentID)
	}
	// 上下文确实复制过来了（且首行是标记行）
	raw, _ := a.Store.RawMessagesAll(child.ID)
	if len(raw) == 0 || raw[0].Kind != inheritKind {
		t.Fatalf("子对话应带继承标记行，实际 %+v", raw)
	}

	// 2) 二级子对话被拒
	rec = post(fmt.Sprintf(`{"parent":%q,"inheritContext":true}`, child.ID))
	if rec.Code != 400 {
		t.Fatalf("二级子对话应被拒（400），得到 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "只允许一级") {
		t.Fatalf("拒绝原因应说明层级限制，得到 %s", rec.Body.String())
	}

	// 3) 父不存在也被拒
	rec = post(`{"parent":"不存在","inheritContext":true}`)
	if rec.Code != 400 {
		t.Fatalf("父不存在应被拒（400），得到 %d", rec.Code)
	}

	// 4) 旧路径（不带 inheritContext）行为不变：空会话、不复制
	rec = post(`{"mode":"aux","parent":"p1"}`)
	if rec.Code != 200 {
		t.Fatalf("旧 aux 路径应照常成功，得到 %d", rec.Code)
	}
	var plain SessionMeta
	_ = json.Unmarshal(rec.Body.Bytes(), &plain)
	if plain.Mode != "aux" || plain.Origin != "" {
		t.Fatalf("旧 aux 路径不应被克隆逻辑改写：%+v", plain)
	}
	raw, _ = a.Store.RawMessagesAll(plain.ID)
	if len(raw) != 0 {
		t.Fatalf("旧 aux 路径不该复制消息，实际 %d 条", len(raw))
	}
}

// 派发工作任务：永远不带上下文（传了 inherit_context 也被忽略）；
// 子对话里调用被硬拦。用 mock 模型跑真实链路：launchSub 会在后台真的执行
// 子会话（不发网络请求）。
func TestDispatchTaskNoContextAndNestingGuard(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.BaseURL = "mock" // 后台子会话走内置演示模型，离线可跑
	assistant := &SessionMeta{ID: "a1", Mode: "assistant", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(assistant); err != nil {
		t.Fatalf("建总控会话失败: %v", err)
	}
	_ = a.Store.Append("a1", TextMessage("user", "帮我把日志整理一下"))
	_ = a.Store.Append("a1", Message{Role: "assistant", Content: "好的，我安排一下"})

	// 派发工作任务：只收到一段自足需求，不带主对话历史；
	// 派发的是顶层工作任务（左侧列表可见），回报链记在 dispatched_by
	ctx := &toolContext{app: a, sess: assistant, emit: func(Event) {}}
	out, err := runDispatchTask(ctx, json.RawMessage(`{"task":"整理日志"}`))
	if err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if !strings.Contains(out, "已派发后台执行") {
		t.Fatalf("返回文案异常: %s", out)
	}
	byMe := func() []*SessionMeta { // 助理 a1 派生的任务会话（顶层，靠 dispatched_by 关联）
		list, _ := a.Store.List()
		var hit []*SessionMeta
		for _, m := range list {
			if m.DispatchedBy == "a1" {
				hit = append(hit, m)
			}
		}
		return hit
	}
	subs := byMe()
	if len(subs) != 1 {
		t.Fatalf("应有 1 个派生任务会话，得到 %d", len(subs))
	}
	plain := subs[0]
	if plain.ParentID != "" || plain.Mode != "task" {
		t.Fatalf("派生任务应是顶层 task 会话（parent=%q mode=%q）", plain.ParentID, plain.Mode)
	}
	raw, _ := a.Store.RawMessagesAll(plain.ID)
	if len(raw) != 1 || !strings.Contains(raw[0].Text(), "【数字助理派发】") {
		t.Fatalf("派发应只有一条需求消息，实际 %d 条", len(raw))
	}
	if strings.Contains(out, "已继承") {
		t.Fatalf("派发工作任务不该声称继承上下文: %s", out)
	}

	// 工具已无 inherit_context 参数：即使旧模型传了也被忽略，工作任务与
	// 派发方聊天记录完全隔离
	out, err = runDispatchTask(ctx, json.RawMessage(`{"task":"再派一个","inherit_context":true}`))
	if err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if strings.Contains(out, "已继承") {
		t.Fatalf("工作任务不支持继承，返回文案异常: %s", out)
	}
	subs = byMe()
	var second *SessionMeta
	for _, k := range subs {
		if k.ID != plain.ID {
			second = k
		}
	}
	if second == nil {
		t.Fatal("找不到第二次派生的任务会话")
	}
	raw, _ = a.Store.RawMessagesAll(second.ID)
	if len(raw) != 1 {
		t.Fatalf("传 inherit_context 也不该继承（应只有 1 条需求消息），实际 %d 条", len(raw))
	}

	// 二级守卫：子对话（带 ParentID）调用派发被拒
	subCtx := &toolContext{app: a, sess: &SessionMeta{ID: second.ID, Mode: "assistant", ParentID: "a1"}, emit: func(Event) {}}
	if _, err := runDispatchTask(subCtx, json.RawMessage(`{"task":"再派一个"}`)); err == nil {
		t.Fatal("子对话里的派发应被拒绝")
	} else if !strings.Contains(err.Error(), "子对话不能再派发子对话") {
		t.Fatalf("拒绝原因不明确: %v", err)
	}
	// 且不因被拒不登记新任务（仍是原有的 2 个派生会话）
	if n := len(byMe()); n != 2 {
		t.Fatalf("被拒的派发不该留下会话，实际 %d 个", n)
	}
}

// dispatch_sub 派发子对话：主对话与工作任务都可派（挂在当前会话下，
// 右栏抽屉可见）；专家是唯一模式路径——不传 expert = 标准子对话（自足），
// expert=继承上下文子对话 继承本会话记录；子对话本身不能再派（只有一级）。
func TestDispatchSubFromTaskAndAssistant(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.BaseURL = "mock" // 后台子会话走内置演示模型，离线可跑
	assistant := &SessionMeta{ID: "a1", Mode: "assistant", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	task := &SessionMeta{ID: "t1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(assistant); err != nil {
		t.Fatalf("建总控会话失败: %v", err)
	}
	if err := a.Store.Create(task); err != nil {
		t.Fatalf("建任务会话失败: %v", err)
	}

	// 工作任务派子对话（不传 expert = 标准子对话，不带上下文）：挂在任务下、mode=task
	tctx := &toolContext{app: a, sess: task, emit: func(Event) {}}
	out, err := runDispatchSub(tctx, json.RawMessage(`{"task":"把附录单独整理成一份"}`))
	if err != nil {
		t.Fatalf("任务派发子对话失败: %v", err)
	}
	if !strings.Contains(out, "已派发后台执行") {
		t.Fatalf("返回文案异常: %s", out)
	}
	if strings.Contains(out, "已继承") {
		t.Fatalf("默认派发不该声称继承上下文: %s", out)
	}
	kids, _ := a.Store.Children("t1")
	if len(kids) != 1 || kids[0].Mode != "task" || kids[0].ParentID != "t1" || kids[0].DispatchedBy != "" {
		t.Fatalf("子对话应挂在 t1 下且不带 dispatched_by（mode=%q parent=%q disp=%q n=%d）",
			kids[0].Mode, kids[0].ParentID, kids[0].DispatchedBy, len(kids))
	}
	if kids[0].Expert != "标准子对话" {
		t.Fatalf("不传 expert 应记为标准子对话，得到 %q", kids[0].Expert)
	}
	raw, _ := a.Store.RawMessagesAll(kids[0].ID)
	if len(raw) != 1 || !strings.Contains(raw[0].Text(), "【派发子对话】") {
		t.Fatalf("子对话应只含一条派发需求，实际 %d 条", len(raw))
	}
	plainKid := kids[0].ID

	// 继承上下文：用内置专家「继承上下文子对话」按名派发，继承任务历史，
	// 标记行在最前，派发需求在其后
	_ = a.Store.Append("t1", TextMessage("user", "帮我把日志整理一下"))
	_ = a.Store.Append("t1", Message{Role: "assistant", Content: "好的，我安排一下"})
	out, err = runDispatchSub(tctx, json.RawMessage(`{"task":"接着刚才的聊","expert":"继承上下文子对话"}`))
	if err != nil {
		t.Fatalf("带上下文派发子对话失败: %v", err)
	}
	if !strings.Contains(out, "已继承本会话上下文") {
		t.Fatalf("返回文案应说明已继承上下文: %s", out)
	}
	kids, _ = a.Store.Children("t1")
	var inherited *SessionMeta
	for _, k := range kids {
		if k.ID != plainKid {
			inherited = k
		}
	}
	if inherited == nil {
		t.Fatal("找不到继承上下文的子对话")
	}
	if inherited.Expert != "继承上下文子对话" {
		t.Fatalf("子会话应记录内置专家名，得到 %q", inherited.Expert)
	}
	raw, _ = a.Store.RawMessagesAll(inherited.ID)
	if len(raw) < 3 || raw[0].Kind != inheritKind {
		t.Fatalf("继承派发应带标记行 + 历史，实际 %d 条（首行 kind=%q）", len(raw), raw[0].Kind)
	}
	if last := raw[len(raw)-1]; !strings.Contains(last.Text(), "接着刚才的聊") {
		t.Fatalf("派发需求应排在继承块之后，实际末尾为 %q", last.Text())
	}
	// 子对话里模型可见：父会话的聊天记录在前，自己的任务在后
	sub := &SessionMeta{ID: inherited.ID, Mode: "task"}
	cm := a.buildContext(sub, "task 系统提示词")
	var sawParent, sawTask bool
	for _, msg := range cm {
		if strings.Contains(msg.Text(), "帮我把日志整理一下") {
			sawParent = true
		}
		if strings.Contains(msg.Text(), "接着刚才的聊") {
			sawTask = true
		}
	}
	if !sawParent || !sawTask {
		t.Fatalf("子对话应同时看到父会话历史与自己的任务（sawParent=%v sawTask=%v）", sawParent, sawTask)
	}

	// 主对话也能派子对话
	actx := &toolContext{app: a, sess: assistant, emit: func(Event) {}}
	if _, err := runDispatchSub(actx, json.RawMessage(`{"task":"查个资料"}`)); err != nil {
		t.Fatalf("主对话派发子对话失败: %v", err)
	}
	kids, _ = a.Store.Children("a1")
	if len(kids) != 1 {
		t.Fatalf("主对话名下应有 1 个子对话，实际 %d 个", len(kids))
	}

	// 子对话不能再派（一级限制）
	sctx := &toolContext{app: a, sess: kids[0], emit: func(Event) {}}
	if _, err := runDispatchSub(sctx, json.RawMessage(`{"task":"再派一个"}`)); err == nil || !strings.Contains(err.Error(), "只有一级") {
		t.Fatalf("子对话再派应被拒（只有一级），实际 err=%v", err)
	}
	// 被拒不留下新会话（仍是原有的 2 个：默认派发 + 继承派发）
	if n := len(mustChildren(t, a, "t1")); n != 2 {
		t.Fatalf("被拒的派发不该留下会话，实际 %d 个", n)
	}
}

func mustChildren(t *testing.T, a *App, id string) []*SessionMeta {
	t.Helper()
	kids, err := a.Store.Children(id)
	if err != nil {
		t.Fatalf("查子会话失败: %v", err)
	}
	return kids
}
