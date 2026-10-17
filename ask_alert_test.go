package main

// 久等声光提醒（ask_alert）的两条判定（interact.go）：
//   · askAlertDue：Timer 到点那一刻，只有「还在等 + 用户没作答 + 没停表/没进去
//     处理」才响；
//   · holdQuestion 也必须受理**没有计时器**的请求（计划审批 kind=plan 无时限）——
//     用户从任何入口打开计划页/浮窗就是「已在处理」，此时没有表可停，但仍要置位
//     TimerHeld 把那次提醒作废（前端 notePlanHandled 正是这么调的）。
// 另外确认：对计划审批置位不会动等待本身——它照样等在那儿，之后批准/修改照常。

import (
	"context"
	"sync"
	"testing"
	"time"
)

// waitRegistered 等这条请求真的登记进 a.questions（requestUserAnswer 的登记
// 在其 goroutine 里完成，q.ID 更早就赋了值，不能拿它当登记完成的信号）。
func waitRegistered(t *testing.T, a *App, q *UserQuestion) {
	t.Helper()
	for i := 0; i < 500; i++ {
		a.permMu.Lock()
		_, ok := a.questions[q.ID]
		a.permMu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待提问登记超时（id=%q）", q.ID)
}

// holdQuestion 对无时限的计划审批同样生效：置位 + 广播停表，且不打断等待
func TestHoldQuestionCoversTimelessPlan(t *testing.T) {
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

	q := &UserQuestion{Kind: askKindPlan, Question: "请审批以下执行计划", Plan: "# 计划", AllowFree: true}
	done := make(chan string, 1)
	go func() { s, ok := a.requestUserAnswer(context.Background(), sess, q); if ok { done <- s } }()
	waitRegistered(t, a, q)
	if q.timer != nil {
		t.Fatal("计划审批本就不该有计时器（无时限）")
	}
	if !a.askAlertDue(q.ID) {
		t.Fatal("刚挂出来的计划审批、没人碰过：久等提醒到点应当要响")
	}

	// 用户从右下角/上方浮窗/计划页签打开这份计划：登记「已在处理」
	a.holdQuestion(q.ID)
	if a.askAlertDue(q.ID) {
		t.Fatal("已登记「已在处理」之后，久等提醒到点不该再响")
	}
	a.permMu.Lock()
	held := q.TimerHeld
	_, waiting := a.questions[q.ID]
	a.permMu.Unlock()
	if !held {
		t.Fatal("无时限的计划审批也应置位 TimerHeld（没有表可停，但标记要有）")
	}
	if !waiting {
		t.Fatal("这只是停提醒，等待本身不该结束——计划仍该等用户批准")
	}
	time.Sleep(50 * time.Millisecond) // 放行广播
	mu.Lock()
	sawHeld := false
	for _, ev := range events {
		if ev.Type == "question_deadline" && ev.QuestionID == q.ID && ev.Held {
			sawHeld = true
		}
	}
	mu.Unlock()
	if !sawHeld {
		t.Fatal("登记后应广播 question_deadline（held=true），别的端好同步显示")
	}

	// 停提醒不影响后续处置：批准照常唤醒等待中的工具
	if err := a.resolveQuestion(q.ID, "__approve__"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	select {
	case ans := <-done:
		if ans != "__approve__" {
			t.Fatalf("等待中的计划工具应拿到批准答案，实际 %q", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("登记之后计划仍该能批准（提醒去重不该影响等待）")
	}
}

// askAlertDue：还在等且没人碰过才响；作答（从清单里消失）与未知 id 都不响
func TestAskAlertDueGate(t *testing.T) {
	st := testPlanStore(t)
	a := newPlanTestApp(t, st)
	sess := createPlanSession(t, st, "s1", "会话一", false)

	q := &UserQuestion{Kind: askKindAsk, Question: "选哪个？", Options: []string{"甲", "乙"}, AllowFree: true}
	go func() { _, _ = a.requestUserAnswer(context.Background(), sess, q) }()
	waitRegistered(t, a, q)
	if !a.askAlertDue(q.ID) {
		t.Fatal("挂出来且没人碰过的提问：到点该响")
	}
	if a.askAlertDue("q-不存在") {
		t.Fatal("清单里没有的 id 不该响（提问已结束/已作答）")
	}

	a.holdQuestion(q.ID) // 悬停/触碰卡片：停表
	if a.askAlertDue(q.ID) {
		t.Fatal("停表之后到点不该响")
	}
	if err := a.resolveQuestion(q.ID, "甲"); err != nil {
		t.Fatalf("回答失败: %v", err)
	}
	if a.askAlertDue(q.ID) {
		t.Fatal("已作答（从等待清单里消失）之后不该响")
	}
}
