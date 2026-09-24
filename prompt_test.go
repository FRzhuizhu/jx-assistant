package main

import (
	"strings"
	"testing"
)

// 计划模式守卫：目录不变、调用时拦截；白名单外的只读工具不受影响。
func TestSuspendedInPlan(t *testing.T) {
	cases := []struct {
		mode, name string
		want       bool
	}{
		{"task", "write_file", true},
		{"task", "edit_file", true},
		{"task", "run_command", true},
		{"task", "job_output", true},
		{"task", "browser_click", true},
		{"task", "computer_screenshot", true},
		{"task", "read_file", false},
		{"task", "grep", false},
		{"task", "web_fetch", false},
		{"task", "ask_user_question", false},
		{"task", "exit_plan_mode", false},
		{"task", "update_goal", false},
		{"task", "todo_write", false},
		{"assistant", "dispatch_task", true},
		{"assistant", "send_to_session", true},
		{"assistant", "list_sessions", false},
		{"aux", "read_file", false},
	}
	for _, c := range cases {
		if got := suspendedInPlan(c.mode, c.name); got != c.want {
			t.Errorf("suspendedInPlan(%s, %s) = %v，应为 %v", c.mode, c.name, got, c.want)
		}
	}
}

// 系统提示词：会话内稳定（不含时间/权限等易变内容），只含基础提示词+版本。
func TestSystemPromptStable(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "sp1", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	s1 := a.systemPrompt(sess)
	s2 := a.systemPrompt(sess)
	if s1 != s2 {
		t.Fatal("同一会话的系统提示词应完全一致")
	}
	if strings.Contains(s1, "当前时间") || strings.Contains(s1, "权限模式") {
		t.Fatal("系统提示词不应包含易变信息（时间/权限应归环境快照）")
	}
	if !strings.Contains(s1, "执行助手") || !strings.Contains(s1, appVersion) {
		t.Fatal("系统提示词应含基础提示词与版本标识")
	}
}

// 环境快照：包含易变信息；计划/目标模式有公告行。
func TestEnvPromptContainsModes(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "sp2", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	env := a.envPrompt(sess)
	if !strings.Contains(env, "当前时间") || !strings.Contains(env, "取代更早的快照") {
		t.Fatal("环境快照应含时间与取代声明")
	}
	sess.PlanMode = true
	if e := a.envPrompt(sess); !strings.Contains(e, "计划模式") || !strings.Contains(e, "exit_plan_mode") {
		t.Fatal("计划模式的环境快照应含公告行")
	}
	sess.PlanMode = false
	sess.GoalMode = true
	sess.GoalText = "整理测试目录"
	if e := a.envPrompt(sess); !strings.Contains(e, "整理测试目录") || !strings.Contains(e, "update_goal") {
		t.Fatal("目标模式的环境快照应含目标文本与 update_goal 指引")
	}
}

// 环境快照 diff：内容不变不追加；变化才落库新的一条。
func TestEnvSnapshotDiff(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "sp3", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	a.maybeAppendEnvSnapshot(sess, "ENV-A")
	a.maybeAppendEnvSnapshot(sess, "ENV-A") // 相同：不追加
	count := func() int {
		raw, _ := a.Store.RawMessagesAll(sess.ID)
		n := 0
		for _, m := range raw {
			if m.Kind == "env" {
				n++
			}
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("相同快照不应追加，现有 %d 条", n)
	}
	a.maybeAppendEnvSnapshot(sess, "ENV-B")
	if n := count(); n != 2 {
		t.Fatalf("变化后应追加，现有 %d 条", n)
	}
}

// prompt/env 行不进模型上下文；prompt 行记录 {sys,env}。
func TestBuildContextSkipsPromptAndEnvRows(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "sp4", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "你好"), Kind: "chat", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "ENV-OLD"), Kind: "env", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", `{"sys":"S","env":"E"}`), Kind: "prompt", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("assistant", "好的"), Kind: "chat", CreatedAt: now()})
	msgs := a.buildContext(sess, "SYS", "ENV-NEW")
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			if s == "ENV-OLD" || strings.Contains(s, `"sys":"S"`) {
				t.Fatalf("env/prompt 行不应进入模型上下文：%s", s)
			}
		}
	}
	if len(msgs) != 4 || msgs[0].Content != "SYS" || msgs[1].Content != "你好" || msgs[2].Content != "好的" || msgs[3].Content != "ENV-NEW" {
		t.Fatalf("组装序列应为 [SYS, 你好, 好的, ENV-NEW]，得到 %v", msgs)
	}
}

// update_goal 状态机：未开目标模式报错；complete/blocked 落库并置状态。
func TestUpdateGoalTool(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "sp5", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	tc := &toolContext{app: a, sess: sess}
	if _, err := runUpdateGoal(tc, []byte(`{"result":"complete","summary":"x"}`)); err == nil {
		t.Fatal("未开目标模式应报错")
	}
	sess.GoalMode = true
	sess.GoalStatus = "active"
	if _, err := runUpdateGoal(tc, []byte(`{"result":"done","summary":"x"}`)); err == nil {
		t.Fatal("result 只能是 complete/blocked")
	}
	out, err := runUpdateGoal(tc, []byte(`{"result":"complete","summary":"三个文件已创建"}`))
	if err != nil || !strings.Contains(out, "已达成") {
		t.Fatalf("complete 应成功：%v %s", err, out)
	}
	got, _ := a.Store.Get(sess.ID)
	if got.GoalStatus != "complete" {
		t.Fatalf("GoalStatus 应落库为 complete，得到 %s", got.GoalStatus)
	}
}
