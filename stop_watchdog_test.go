package main

// 停止看门狗（stopRunBy → watchStop → forceStopRun）的验收测试：
//  1. 卡死不响应取消的执行体：协作取消后看门狗到点强制终结——摘注册表
//     （IsRunning=false）、关闭 done、状态回 idle、新一轮可立即开始；迟到的
//     旧执行体醒来收口（finishRunOf）不得误摘新一轮；
//  2. 协作取消正常生效（收口早于看门狗到点）：看门狗静默退出，不标 killed；
//  3. 硬终结只动目标会话：同一时刻在跑的另一个会话纹丝不动；
//  4. 模型流强断注册表（registerStreamKill / killSessionStreams）：登记、覆盖、
//     注销、触发四种行为各就各位；
//  5. StreamChat 的 OnBody 契约：响应体建立时注册一次（非 nil）、流结束时
//     注销一次（nil）。
//
// 「卡死」不靠真卡一个进程：执行体就是一条阻塞在不看 ctx 的通道上的
// goroutine——这正是硬终结要兜的形态（取消信号发了，执行体不响应）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ① 卡死的执行体被看门狗强制终结，且不误伤后续新一轮。
func TestStopWatchdogForceTerminatesStuckRun(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	a.stopWatchdog = 300 * time.Millisecond // 测试里缩短等待
	sub := mkSession(t, a, "t-stuck", "卡死的任务", "task", "")
	run, ok := a.beginRun(sub.ID)
	if !ok {
		t.Fatal("登记执行失败")
	}

	// 模拟卡死的执行体：阻塞在永不到达的通道上，不看 ctx（取消对它无效）
	release := make(chan struct{})
	woke := make(chan struct{})
	go func() {
		defer close(woke)
		<-release                   // 卡死点
		a.finishRunOf(sub.ID, run)  // 迟到的收口：应幂等空转
	}()

	if !a.stopRun(sub.ID) {
		t.Fatal("在跑的会话应停止成功")
	}
	// 看门狗到点强制终结：注册表被摘、done 关闭
	deadline := time.Now().Add(5 * time.Second)
	for a.IsRunning(sub.ID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.IsRunning(sub.ID) {
		t.Fatal("卡死的执行体应被看门狗强制终结")
	}
	select {
	case <-run.done:
	default:
		t.Fatal("强制终结后 done 应已关闭")
	}
	// 状态回空闲：会话不再停在「停止中」，可立即再交互
	m, err := a.Store.Get(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "idle" {
		t.Fatalf("硬终结后状态应回 idle, got %q", m.Status)
	}
	if !run.wasKilled() {
		t.Fatal("硬终结过的 run 应带 killed 标志（迟到醒来据此跳过终态交代）")
	}
	// 停止记录按人为叫停落库（协作阶段写下的那份，口径不变）；收口完成后
	// Manual 待收口标志由 finishRun 的 ClearStopPending 清除（与协作停止一致）。
	// 摘表与 ClearStopPending 之间有几步间隔，轮询等它落地，不跟调度赛跑。
	deadlineClear := time.Now().Add(5 * time.Second)
	for {
		rec := a.Store.GetStopRecord(sub.ID)
		if rec.Kind != stopByUser || rec.Actor != stopActorUser {
			t.Fatalf("停止记录口径应与协作停止一致, got %+v", rec)
		}
		if !rec.Manual {
			break
		}
		if time.Now().After(deadlineClear) {
			t.Fatalf("硬终结收口后 Manual 待收口标志应已清除（ClearStopPending）: %+v", rec)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 新一轮可立即占住执行权
	if _, ok := a.beginRun(sub.ID); !ok {
		t.Fatal("硬终结后应能立即开始新一轮")
	}
	// 释放卡死的执行体：它的迟到收口不得把新一轮摘掉
	close(release)
	<-woke
	if !a.IsRunning(sub.ID) {
		t.Fatal("旧执行体的迟到收口误摘了新一轮（finishRunOf 身份比对失效）")
	}
	a.finishRun(sub.ID) // 清场
}

// ② 协作取消正常生效（早于看门狗到点收口）：看门狗静默退出。
func TestStopWatchdogQuietWhenCooperativeStopWorks(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	a.stopWatchdog = 300 * time.Millisecond
	sub := mkSession(t, a, "t-coop", "能正常停的任务", "task", "")
	run, ok := a.beginRun(sub.ID)
	if !ok {
		t.Fatal("登记执行失败")
	}
	if !a.stopRun(sub.ID) {
		t.Fatal("停止应成功")
	}
	a.finishRunOf(sub.ID, run) // 模拟执行体响应取消、自行收口
	time.Sleep(600 * time.Millisecond)
	if run.wasKilled() {
		t.Fatal("协作收口已生效时不该触发硬终结")
	}
	if a.IsRunning(sub.ID) {
		t.Fatal("会话应已收口")
	}
}

// ③ 硬终结只动目标会话：同时在跑的另一个会话纹丝不动。
func TestStopWatchdogSparesOtherSessions(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	a.stopWatchdog = 300 * time.Millisecond
	stuck := mkSession(t, a, "t-stuck2", "卡死的任务", "task", "")
	bystander := mkSession(t, a, "t-other", "无关会话", "task", "")
	if _, ok := a.beginRun(stuck.ID); !ok {
		t.Fatal("登记卡死会话失败")
	}
	otherRun, ok := a.beginRun(bystander.ID)
	if !ok {
		t.Fatal("登记无关会话失败")
	}
	defer a.finishRun(bystander.ID)

	release := make(chan struct{})
	go func() { <-release }() // 卡死不收口（测试结束随进程回收）
	if !a.stopRun(stuck.ID) {
		t.Fatal("停止应成功")
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.IsRunning(stuck.ID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.IsRunning(stuck.ID) {
		t.Fatal("卡死的会话应被强制终结")
	}
	if !a.IsRunning(bystander.ID) {
		t.Fatal("硬终结误伤了无关会话")
	}
	if otherRun.wasKilled() {
		t.Fatal("无关会话的执行体不该被标记 killed")
	}
	close(release)
	a.finishRunOf(stuck.ID, a.runOf(stuck.ID)) // nil 安全：摘卡死会话的残留登记（已被看门狗摘掉则空转）
}

// ④ 模型流强断注册表：登记 → kill 触发一次并清空；注销后 kill 不触发；覆盖后只认最后登记的。
func TestStreamKillRegistry(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	fired := make(chan int, 4)
	a.registerStreamKill("s1", func() { fired <- 1 })
	a.registerStreamKill("s1", func() { fired <- 2 }) // 覆盖
	a.killSessionStreams("s1")
	select {
	case v := <-fired:
		if v != 2 {
			t.Fatalf("应只触发出最后登记的断开器, got %d", v)
		}
	default:
		t.Fatal("kill 应触发登记的断开器")
	}
	select { // 只触发一次，不重复
	case v := <-fired:
		t.Fatalf("断开器不该被重复触发, got %d", v)
	default:
	}
	a.killSessionStreams("s1") // 已清空：安全空转
	a.registerStreamKill("s1", func() { fired <- 3 })
	a.clearStreamKill("s1") // 注销
	a.killSessionStreams("s1")
	select {
	case <-fired:
		t.Fatal("已注销的断开器不该被触发")
	default:
	}
}

// ⑤ StreamChat 的 OnBody 契约：响应体建立时注册（非 nil）、流结束时注销（nil）。
func TestStreamChatOnBodyLifecycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	var calls []func()
	gotNil := 0
	cb := StreamCallbacks{
		OnText: func(string) {},
		OnBody: func(abort func()) {
			if abort == nil {
				gotNil++
				return
			}
			calls = append(calls, abort)
		},
	}
	err := StreamChat(t.Context(), LLMTarget{BaseURL: srv.URL, Model: "m"}, ChatRequest{Messages: []Message{TextMessage("user", "hi")}}, cb)
	if err != nil {
		t.Fatalf("StreamChat 应成功: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("应恰好注册一次断开器, got %d", len(calls))
	}
	if gotNil != 1 {
		t.Fatalf("流结束应注销一次（OnBody(nil)）, got %d", gotNil)
	}
	calls[0]() // 断开器可安全重调（关已关的 body 幂等）
	if !strings.Contains(srv.URL, "127.0.0.1") {
		t.Fatal("httptest 地址异常")
	}
}
