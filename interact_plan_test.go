package main

// 计划审批的两条关键语义（interact.go / store.go）：
//   · 计划审批不设时限，而且提交即落库——关掉软件、重启进程之后再打开，
//     它仍以「待审批」挂在会话里（普通提问与「进入计划模式」确认照旧 5 分钟时限）；
//   · 重启后的批准 / 修改 / 忽略由 resolvePersistedPlan 收口：投一条通知进会话、
//     按需拉起新一轮。测试里用 runners 塞一个空 run 把会话标成「正在执行」，
//     投递就走排队（不真跑模型），只验证语义与落库结果。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func testPlanStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newPlanTestApp(t *testing.T, st *Store) *App {
	t.Helper()
	a := &App{Store: st}
	a.sessLocks = map[string]*sync.Mutex{}
	a.runners = map[string]*sessionRun{}
	a.subscribers = map[chan Event]bool{}
	a.grants = map[string]*Grant{}
	a.approvals = map[string]*Approval{}
	a.questions = map[string]*UserQuestion{}
	a.pendingSteer = map[string][]*PendingMsg{}
	a.pendingQueue = map[string][]*PendingMsg{}
	a.queuePaused = map[string]bool{}
	a.runErrors = map[string]string{}
	return a
}

func createPlanSession(t *testing.T, st *Store, id, title string, planMode bool) *SessionMeta {
	t.Helper()
	m := &SessionMeta{ID: id, Title: title, Mode: "task", PlanMode: planMode}
	if err := st.Create(m); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	return m
}

// 计划提交即落库：重启（新 App、内存全空）之后它仍在待办清单里
func TestPendingPlanOutlivesRestart(t *testing.T) {
	st := testPlanStore(t)
	sess := createPlanSession(t, st, "s1", "会话一", true)
	if err := st.SetPendingPlan(PendingPlan{
		ID: "q-1", SessionID: "s1", Plan: "# 计划\n先改 A，再改 B", CreatedAt: now(),
	}); err != nil {
		t.Fatalf("落库失败: %v", err)
	}

	after := newPlanTestApp(t, st) // 重启：内存等待者没了，只剩库里那份
	qs := after.pendingQuestions()
	if len(qs) != 1 {
		t.Fatalf("重启后应有 1 条待审批计划，实际 %d 条", len(qs))
	}
	q := qs[0]
	if q.ID != "q-1" || q.Kind != askKindPlan || q.SessionID != "s1" {
		t.Fatalf("待审批计划的字段不对: %+v", q)
	}
	if q.SessionTitle != "会话一" {
		t.Fatalf("会话标题应随请求下发，实际 %q", q.SessionTitle)
	}
	if !strings.Contains(q.Plan, "先改 A") {
		t.Fatalf("计划正文应能读回，实际 %q", q.Plan)
	}

	// 会话退出计划模式（用户在别处手动关了）：这份计划算过期，不再挂着
	sess.PlanMode = false
	if err := st.SaveMeta(sess); err != nil {
		t.Fatalf("存 meta 失败: %v", err)
	}
	if qs := after.pendingQuestions(); len(qs) != 0 {
		t.Fatalf("已退出计划模式后不该再挂待审批，实际 %d 条", len(qs))
	}
	if _, ok := st.PendingPlanOf("s1"); ok {
		t.Fatal("过期的待审批计划应被顺手清掉")
	}
}

// 重启后的批准：撤掉待审批记录、退出计划模式，并把「计划已批准」的通知投给会话
func TestResolvePersistedPlanApprove(t *testing.T) {
	st := testPlanStore(t)
	createPlanSession(t, st, "s1", "会话一", true)
	if err := st.SetPendingPlan(PendingPlan{ID: "q-2", SessionID: "s1", Plan: "# 计划", CreatedAt: now()}); err != nil {
		t.Fatalf("落库失败: %v", err)
	}
	a := newPlanTestApp(t, st)
	a.runners["s1"] = &sessionRun{} // 会话正在跑：投递走排队，不在这里真起执行体

	if err := a.resolveQuestion("q-2", "__approve__"); err != nil {
		t.Fatalf("批准应成功: %v", err)
	}
	if _, ok := st.PendingPlanOf("s1"); ok {
		t.Fatal("批准后待审批记录应被撤掉")
	}
	m, err := st.Get("s1")
	if err != nil {
		t.Fatalf("读会话失败: %v", err)
	}
	if m.PlanMode {
		t.Fatal("批准后会话应退出计划模式（并落库）")
	}
	q := a.pendingOf("s1")
	if len(q.Queue) != 1 {
		t.Fatalf("应投递 1 条通知进队列，实际 %d 条", len(q.Queue))
	}
	pm := q.Queue[0]
	if pm.Kind != kindReport || !strings.Contains(pm.Content, "计划已批准") {
		t.Fatalf("批准的投递内容不对: kind=%q content=%q", pm.Kind, pm.Content)
	}
}

// 重启后的修改：留在计划模式，把反馈投给模型让它修订后重新提交
func TestResolvePersistedPlanReject(t *testing.T) {
	st := testPlanStore(t)
	createPlanSession(t, st, "s1", "会话一", true)
	if err := st.SetPendingPlan(PendingPlan{ID: "q-3", SessionID: "s1", Plan: "# 计划", CreatedAt: now()}); err != nil {
		t.Fatalf("落库失败: %v", err)
	}
	a := newPlanTestApp(t, st)
	a.runners["s1"] = &sessionRun{}

	if err := a.resolveQuestion("q-3", "第二步换个做法"); err != nil {
		t.Fatalf("修改应成功: %v", err)
	}
	if _, ok := st.PendingPlanOf("s1"); ok {
		t.Fatal("修改后这份待审批记录应作废（模型会重新提交一份新的）")
	}
	m, _ := st.Get("s1")
	if !m.PlanMode {
		t.Fatal("修改后会话应留在计划模式")
	}
	q := a.pendingOf("s1")
	if len(q.Queue) != 1 || !strings.Contains(q.Queue[0].Content, "第二步换个做法") {
		t.Fatalf("修改应把用户反馈投给会话，实际 %+v", q.Queue)
	}
}

// 忽略（搁置）：计划不撤销——它仍在待审批，只是这一轮停下来
func TestResolvePersistedPlanIgnore(t *testing.T) {
	st := testPlanStore(t)
	createPlanSession(t, st, "s1", "会话一", true)
	if err := st.SetPendingPlan(PendingPlan{ID: "q-4", SessionID: "s1", Plan: "# 计划", CreatedAt: now()}); err != nil {
		t.Fatalf("落库失败: %v", err)
	}
	a := newPlanTestApp(t, st)

	if err := a.resolveQuestion("q-4", "__ignore__"); err != nil {
		t.Fatalf("搁置应成功: %v", err)
	}
	pp, ok := st.PendingPlanOf("s1")
	if !ok || pp.ID != "q-4" {
		t.Fatal("搁置后计划必须还在（仍等用户处置）")
	}
	m, _ := st.Get("s1")
	if !m.PlanMode {
		t.Fatal("搁置不该改变计划模式状态")
	}
	if q := a.pendingOf("s1"); len(q.Queue) != 0 {
		t.Fatalf("搁置不该往会话里投任何消息，实际 %+v", q.Queue)
	}
	if qs := a.pendingQuestions(); len(qs) != 1 || qs[0].ID != "q-4" {
		t.Fatalf("搁置后它仍应挂在待办清单里，实际 %+v", qs)
	}
}

// 计划在等待中被停止/中断（ctx 取消）：计划仍留在库里继续等审批，
// 而且**不能广播 question_resolved**——前端收到它就会把卡收了，计划框就没了。
func TestPlanWaitCancelledKeepsPlan(t *testing.T) {
	st := testPlanStore(t)
	a := newPlanTestApp(t, st)
	sess := createPlanSession(t, st, "s1", "会话一", true)

	ch, unsub := a.subscribe()
	defer unsub()
	var mu sync.Mutex
	var events []Event
	go func() {
		for ev := range ch {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	q := &UserQuestion{Kind: askKindPlan, Question: "请审批以下执行计划", Plan: "# 计划", AllowFree: true}
	done := make(chan bool, 1)
	go func() { _, ok := a.requestUserAnswer(ctx, sess, q); done <- ok }()
	for i := 0; i < 500 && q.ID == ""; i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if q.ID == "" {
		t.Fatal("计划提问没登记上")
	}

	cancel() // 用户点了停止：本轮取消
	select {
	case ok := <-done:
		if ok {
			t.Fatal("取消等待应返回 ok=false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后等待没有结束")
	}
	if pp, ok := st.PendingPlanOf("s1"); !ok || pp.ID != q.ID {
		t.Fatal("等待被停止/中断后，计划必须仍在库里等审批")
	}
	time.Sleep(50 * time.Millisecond) // 放行广播
	mu.Lock()
	for _, ev := range events {
		if ev.Type == "question_resolved" {
			mu.Unlock()
			t.Fatal("计划仍在等审批，不该广播 question_resolved（前端会据此收掉计划框）")
		}
	}
	mu.Unlock()

	// 停止之后用户仍能批准：内存里已没有等待者，走遗留计划那条路
	a.runners["s1"] = &sessionRun{}
	if err := a.resolveQuestion(q.ID, "__approve__"); err != nil {
		t.Fatalf("停止之后仍应能批准这份计划: %v", err)
	}
	if _, ok := st.PendingPlanOf("s1"); ok {
		t.Fatal("批准后待审批记录应撤掉")
	}
}

// 等待时长：plan 没有时限（expiresAt 为零值）、提交即落库；ask 仍是 5 分钟
func TestPlanAskHasNoTTL(t *testing.T) {
	st := testPlanStore(t)
	a := newPlanTestApp(t, st)
	sess := createPlanSession(t, st, "s1", "会话一", true)

	planQ := &UserQuestion{Kind: askKindPlan, Question: "请审批以下执行计划", Plan: "# 计划", AllowFree: true}
	askQ := &UserQuestion{Kind: askKindAsk, Question: "选哪个？", Options: []string{"甲", "乙"}, AllowFree: true}
	planAns := make(chan string, 1)
	askAns := make(chan string, 1)
	go func() { s, _ := a.requestUserAnswer(context.Background(), sess, planQ); planAns <- s }()
	go func() { s, _ := a.requestUserAnswer(context.Background(), sess, askQ); askAns <- s }()

	waitAsk := func(q *UserQuestion) {
		for i := 0; i < 500; i++ {
			if q.ID != "" {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatal("等待提问登记超时")
	}
	waitAsk(planQ)
	waitAsk(askQ)

	if !planQ.expiresAt.IsZero() {
		t.Fatalf("计划审批不该有时限，实际 expiresAt=%v", planQ.expiresAt)
	}
	if askQ.expiresAt.IsZero() {
		t.Fatal("普通提问应仍有时限")
	}
	pp, ok := st.PendingPlanOf("s1")
	if !ok || pp.ID != planQ.ID || pp.Plan != "# 计划" {
		t.Fatalf("计划应已落库，实际 %+v ok=%v", pp, ok)
	}
	// 内存等待者与库里的那份是同一个 id：清单只出现一次（不重复弹卡）
	qs := a.pendingQuestions()
	n := 0
	for _, q := range qs {
		if q.Kind == askKindPlan {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("计划待审批项应只出现一次，实际 %d 次（清单 %+v）", n, qs)
	}

	if err := a.resolveQuestion(planQ.ID, "__approve__"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	select {
	case ans := <-planAns:
		if ans != "__approve__" {
			t.Fatalf("等待中的计划工具应拿到批准答案，实际 %q", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("批准后等待中的计划工具没被唤醒")
	}
	if err := a.resolveQuestion(askQ.ID, "甲"); err != nil {
		t.Fatalf("回答失败: %v", err)
	}
	select {
	case ans := <-askAns:
		if ans != "甲" {
			t.Fatalf("等待中的提问应拿到答案，实际 %q", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("回答后等待中的提问没被唤醒")
	}
}

// 计划获批要把主循环的步数预算从头计：exit_plan_mode 获批时应置位
// toolContext.planApproved（主循环据此重置 step）；修改不置位（仍在计划模式，没开始执行）。
func TestPlanApprovalSignalsStepReset(t *testing.T) {
	run := func(answer string) bool {
		st := testPlanStore(t)
		a := newPlanTestApp(t, st)
		sess := createPlanSession(t, st, "s1", "会话一", true)
		var reset bool
		tc := &toolContext{ctx: context.Background(), app: a, sess: sess, planApproved: &reset}
		done := make(chan error, 1)
		go func() {
			_, err := runExitPlanMode(tc, json.RawMessage(`{"plan":"# 计划\n第一步 A，第二步 B"}`))
			done <- err
		}()
		qid := ""
		for i := 0; i < 500 && qid == ""; i++ {
			a.permMu.Lock()
			for id, q := range a.questions {
				if q.Kind == askKindPlan && !q.decided {
					qid = id
				}
			}
			a.permMu.Unlock()
			time.Sleep(2 * time.Millisecond)
		}
		if qid == "" {
			t.Fatal("计划审批提问没挂出来")
		}
		if err := a.resolveQuestion(qid, answer); err != nil {
			t.Fatalf("回传用户决定失败: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("exit_plan_mode 执行失败: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("用户决定后 exit_plan_mode 没有返回")
		}
		return reset
	}

	if !run("__approve__") {
		t.Fatal("计划获批应置位步数重算信号（主循环要重新计步）")
	}
	if run("第二步改一下") {
		t.Fatal("计划被修改不该置位步数重算信号（仍留在计划模式修订，没有开始执行）")
	}
}

// ask_user_question 的 timeout_sec：自定义时长落到 deadline（DeadlineMs）、
// 钳制 5-3600、到点未答返回超时（视为放行）；停表（holdQuestion，对应界面
// 悬停/触碰/停止按钮）后越过原时限仍在等，且随后作答照常返回。
func TestAskTimeoutSecAndHold(t *testing.T) {
	st := testPlanStore(t)
	a := newPlanTestApp(t, st)
	sess := createPlanSession(t, st, "s1", "会话一", false)

	waitAsk := func(q *UserQuestion) {
		for i := 0; i < 500; i++ {
			if q.ID != "" {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatal("等待提问登记超时")
	}

	// 自定义时长生效 + 钳制边界
	q7 := &UserQuestion{Kind: askKindAsk, Question: "七秒的问题", TimeoutSec: 7}
	go func() { _, _ = a.requestUserAnswer(context.Background(), sess, q7) }()
	waitAsk(q7)
	if d := time.Until(time.UnixMilli(q7.DeadlineMs)); d <= 5*time.Second || d > 7*time.Second {
		t.Fatalf("timeout_sec=7 未生效：剩余 %v", d)
	}
	if clampAskTimeoutSec(1) != 30 || clampAskTimeoutSec(99999) != 600 || clampAskTimeoutSec(45) != 45 {
		t.Fatal("timeout_sec 钳制（30-600）不正确")
	}
	a.resolveQuestion(q7.ID, "答了")

	// 显式不限时（timeout_sec=0 → NoTimeout）：无截止、无计时器，一直等作答
	qN := &UserQuestion{Kind: askKindAsk, Question: "必须确认的问题", NoTimeout: true}
	go func() { _, _ = a.requestUserAnswer(context.Background(), sess, qN) }()
	waitAsk(qN)
	if !qN.expiresAt.IsZero() || qN.DeadlineMs != 0 {
		t.Fatal("不限时提问不应有截止时刻")
	}
	if qN.timer != nil {
		t.Fatal("不限时提问不应建计时器")
	}
	a.resolveQuestion(qN.ID, "定了")

	// 到点未作答：ok=false（上层文案告知模型视为放行）
	qT := &UserQuestion{Kind: askKindAsk, Question: "会超时的问题", TimeoutSec: 5}
	ans := make(chan bool, 1)
	go func() { _, ok := a.requestUserAnswer(context.Background(), sess, qT); ans <- ok }()
	waitAsk(qT)
	if ok := <-ans; ok {
		t.Fatal("到点未作答应超时返回 ok=false")
	}

	// 停表：越过原时限仍在等，随后作答照常返回
	qH := &UserQuestion{Kind: askKindAsk, Question: "停表的问题", TimeoutSec: 2}
	held := make(chan string, 1)
	go func() {
		s, ok := a.requestUserAnswer(context.Background(), sess, qH)
		if ok {
			held <- s
		}
	}()
	waitAsk(qH)
	a.holdQuestion(qH.ID)
	a.permMu.Lock()
	heldFlag := qH.TimerHeld
	a.permMu.Unlock()
	if !heldFlag {
		t.Fatal("停表标记未落")
	}
	time.Sleep(2500 * time.Millisecond) // 越过原 2 秒时限
	a.permMu.Lock()
	_, waiting := a.questions[qH.ID]
	a.permMu.Unlock()
	if !waiting {
		t.Fatal("停表后越过原时限不应超时退出")
	}
	a.resolveQuestion(qH.ID, "慢慢选好了")
	if s := <-held; s != "慢慢选好了" {
		t.Fatalf("停表后作答应照常返回，实际 %q", s)
	}
}
