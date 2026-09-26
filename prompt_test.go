package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 计划模式守卫：目录不变、调用时拦截；白名单外的只读工具不受影响。
func TestSuspendedInPlan(t *testing.T) {
	cases := []struct {
		mode, name string
		want       bool
	}{
		{"task", "write", true},
		{"task", "edit", true},
		{"task", "run_command", true},
		{"task", "job_output", true},
		{"task", "browser_click", true},
		{"task", "computer_screenshot", true},
		{"task", "read", false},
		{"task", "grep", false},
		{"task", "web_fetch", false},
		{"task", "ask_user_question", false},
		{"task", "exit_plan_mode", false},
		{"task", "update_goal", false},
		{"task", "progress_write", false},
		{"assistant", "dispatch_task", true},
		{"assistant", "send_to_session", true},
		{"assistant", "list_sessions", false},
		{"aux", "read", false},
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
	a.maybeAppendEnvSnapshot(sess, "ENV-A", nil)
	a.maybeAppendEnvSnapshot(sess, "ENV-A", nil) // 相同：不追加
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
	a.maybeAppendEnvSnapshot(sess, "ENV-B", nil)
	if n := count(); n != 2 {
		t.Fatalf("变化后应追加，现有 %d 条", n)
	}
}

// prompt/env 行不进模型上下文；prompt 行记录 {sys,env}。
func TestBuildContextEnvRowsAppendOnly(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "sp4", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "你好"), Kind: "chat", CreatedAt: now()})
	// 首份快照：整份呈现
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user",
		"# 运行环境快照（最新，取代更早的快照）\n- 当前时间：22:00\n- 权限模式：只读"), Kind: "env", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", `{"sys":"S","env":"E","tools":[]}`), Kind: "prompt", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("assistant", "好的"), Kind: "chat", CreatedAt: now()})
	// 第二份快照：时间变、新增一行、其余不变 → 渲染为短「环境更新」
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user",
		"# 运行环境快照（最新，取代更早的快照）\n- 当前时间：22:05\n- 权限模式：只读\n- 能力：已开启「操作浏览器」"), Kind: "env", CreatedAt: now()})

	msgs := a.buildContext(sess, "SYS")
	// prompt 行不进上下文；env 行按落位进：[SYS, 你好, 快照整份, 好的, 环境更新]
	if len(msgs) != 5 || msgs[0].Content != "SYS" || msgs[1].Content != "你好" || msgs[3].Content != "好的" {
		t.Fatalf("组装序列应为 [SYS, 你好, 快照, 好的, 更新]，得到 %v", dumpContents(msgs))
	}
	if full := msgs[2].Text(); !strings.Contains(full, "权限模式：只读") || !strings.Contains(full, "22:00") {
		t.Fatalf("首份快照应整份呈现: %q", full)
	}
	upd := msgs[4].Text()
	if !strings.Contains(upd, "环境更新") || !strings.Contains(upd, "22:05") || !strings.Contains(upd, "操作浏览器") {
		t.Fatalf("第二份快照应渲染为含变化行的更新: %q", upd)
	}
	if strings.Contains(upd, "22:00") || strings.Contains(upd, "权限模式：只读") {
		t.Fatalf("更新消息不应重复未变化的行: %q", upd)
	}
}

// 摘要折叠掉首份快照后，边界后的首条快照重新整份呈现（完整基线不丢）。
func TestBuildContextEnvBaseResetsAfterSummary(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "sp5", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "第一轮"), Kind: "chat", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "- 快照一：完整内容"), Kind: "env", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "第一轮答复"), Kind: "chat", CreatedAt: now()})
	// 摘要覆盖前 3 条（含首份快照）
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "【摘要】已覆盖"), Kind: "summary", Origin: "manual", CoversUpTo: 3, CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "第二轮"), Kind: "chat", CreatedAt: now()})
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", "- 快照一：完整内容\n- 当前时间：22:10"), Kind: "env", CreatedAt: now()})

	msgs := a.buildContext(sess, "SYS")
	// [SYS, 摘要, 第二轮, 快照二整份（作为新的完整基线）]
	if len(msgs) != 4 {
		t.Fatalf("组装序列应为 4 条，得到 %v", dumpContents(msgs))
	}
	if last := msgs[3].Text(); !strings.Contains(last, "快照一：完整内容") || !strings.Contains(last, "22:10") {
		t.Fatalf("摘要边界后的首条快照应整份呈现而非差分: %q", last)
	}
}

func dumpContents(msgs []Message) []string {
	out := []string{}
	for _, m := range msgs {
		out = append(out, fmt.Sprintf("%s:%.40s", m.Role, m.Text()))
	}
	return out
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

// answerAsync 模拟用户在确认卡上作答（等提问登记进来后回传答案）。
func answerAsync(a *App, ans string) {
	go func() {
		for i := 0; i < 500; i++ {
			if qs := a.pendingQuestions(); len(qs) > 0 {
				_ = a.resolveQuestion(qs[0].ID, ans)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

// enter_plan_mode 状态机：reason 空 / 已在计划模式 / 目标模式互斥均报错；
// 用户同意才落库开启，拒绝维持原模式。
func TestEnterPlanModeTool(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	sess := &SessionMeta{ID: "ep1", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	tc := &toolContext{ctx: context.Background(), app: a, sess: sess}
	if _, err := runEnterPlanMode(tc, []byte(`{"reason":"  "}`)); err == nil {
		t.Fatal("reason 为空应报错")
	}
	sess.PlanMode = true
	if _, err := runEnterPlanMode(tc, []byte(`{"reason":"再计划一次"}`)); err == nil {
		t.Fatal("已在计划模式应报错")
	}
	sess.PlanMode = false
	sess.GoalMode = true
	sess.GoalText = "持续推进整理"
	if _, err := runEnterPlanMode(tc, []byte(`{"reason":"想先计划"}`)); err == nil {
		t.Fatal("目标模式运行中应报互斥错误")
	}
	sess.GoalMode = false

	answerAsync(a, "__approve__")
	out, err := runEnterPlanMode(tc, []byte(`{"reason":"跨三个模块的重构，想先调研再动手"}`))
	if err != nil || !strings.Contains(out, "同意进入计划模式") {
		t.Fatalf("用户同意应成功切换：%v %s", err, out)
	}
	got, _ := a.Store.Get(sess.ID)
	if !got.PlanMode {
		t.Fatal("PlanMode 应落库为 true")
	}

	sess.PlanMode = false
	_ = a.Store.SaveMeta(sess)
	answerAsync(a, "不用，直接做")
	out, err = runEnterPlanMode(tc, []byte(`{"reason":"又想计划"}`))
	if err != nil || !strings.Contains(out, "未同意") || !strings.Contains(out, "直接做") {
		t.Fatalf("用户拒绝应带回反馈且维持原模式：%v %s", err, out)
	}
	if sess.PlanMode {
		t.Fatal("拒绝后不应开启计划模式")
	}
}
