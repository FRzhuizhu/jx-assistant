package main

// 停止原因（用户手动停止 vs 服务重启异常中断）的落库判定与回报文案测试。
//
// 覆盖四件事：
//  1. 用户点停止 → 紧接着服务重启：回报必须是「已由用户手动停止」，不能是
//     「因服务重启被中断」（这正是原先把用户主动叫停误报成系统异常的那条路）；
//  2. 只有服务重启（没有用户停止）：回报仍是「因服务重启被中断，未完成」，
//     并带上中断时间 / 触发者 / 进度；
//  3. 停止已收口、之后又跑了一轮才崩：不能拿旧记录解释新的一轮（标志必须在
//     收口时作废），要报「服务重启」；
//  4. 旧数据没有停止记录（缺字段）：一律按「未记录」处理，恢复流程照常工作。
//
// 运行方式（仓库约定根目录不放 _test.go，跑完把文件放回 .回收站）：
//   Copy-Item .回收站\stopreason_test.go .\ -Force
//   go test -run TestStopReason -v .
//   Remove-Item .\stopreason_test.go

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newStopTestApp 建一个独立 data 目录上的 App（同一目录可重复调用 = 模拟进程重启）。
func newStopTestApp(t *testing.T, dir string) *App {
	t.Helper()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	a := &App{Store: store, Cfg: &Config{Workspace: "ws"}}
	a.initRuntime()
	return a
}

// seedStopFixture 造出「数字助理主对话 + 它派发的任务会话」并让任务跑过 1 轮 1 步。
func seedStopFixture(t *testing.T, a *App) {
	t.Helper()
	if err := a.Store.Create(&SessionMeta{ID: "pa", Title: "数字助理主对话", Mode: "assistant", CreatedAt: now(), UpdatedAt: now()}); err != nil {
		t.Fatalf("建父会话失败: %v", err)
	}
	if err := a.Store.Create(&SessionMeta{ID: "tk", Title: "被派发的任务", Mode: "task", DispatchedBy: "pa", CreatedAt: now(), UpdatedAt: now()}); err != nil {
		t.Fatalf("建任务会话失败: %v", err)
	}
	_ = a.Store.Append("tk", TextMessage("user", "干活"))
	_ = a.Store.AppendRow("tk", &StoredMessage{
		Message: Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "c1", Type: "function", Function: ToolCallFunc{Name: "read"}},
		}},
		Kind: kindChat,
	})
}

// lastUserText 取会话里最后一条用户侧消息正文（回报都是这个形态注入的）。
func lastUserText(t *testing.T, a *App, sid string) string {
	t.Helper()
	msgs, err := a.Store.Messages(sid)
	if err != nil {
		t.Fatalf("读消息失败: %v", err)
	}
	last := ""
	for _, m := range msgs {
		if m.Role == "user" {
			last = m.Text()
		}
	}
	return last
}

// TestStopReasonUserStopBeatsRestart 场景①：用户点停止 → 进程没来得及收口就被重启
// （服务重启），回报必须是「用户手动停止」。
func TestStopReasonUserStopBeatsRestart(t *testing.T) {
	dir := t.TempDir()
	a := newStopTestApp(t, dir)
	seedStopFixture(t, a)

	if _, ok := a.beginRun("tk"); !ok {
		t.Fatal("beginRun 应成功")
	}
	if !a.stopRun("tk") {
		t.Fatal("stopRun 应成功（会话正在执行）")
	}
	// 连点停止：计数不该翻倍
	if !a.stopRun("tk") {
		t.Fatal("重复 stopRun 仍应返回 true")
	}
	// 落库检查（重启前）
	rec := a.Store.GetStopRecord("tk")
	if rec.Kind != stopByUser || rec.Actor != "user" || !rec.Manual || rec.At == "" || rec.AtMs == 0 {
		t.Fatalf("用户停止记录落库不对: %+v", rec)
	}
	if rec.UserStops != 1 {
		t.Fatalf("连点停止应只记 1 次，得到 %d 次", rec.UserStops)
	}

	// 进程被重启：内存状态全丢（不调 finishRun），换一个 App 读同一 data 目录
	a.Store.Close()
	b := newStopTestApp(t, dir)
	b.recoverInterrupted()

	taskMsg := lastUserText(t, b, "tk")
	if !strings.Contains(taskMsg, "已由用户手动停止") {
		t.Fatalf("任务会话的收口交代应按用户停止：%q", taskMsg)
	}
	if strings.Contains(taskMsg, "本会话当时被中断") {
		t.Fatalf("任务会话被误报成服务重启中断：%q", taskMsg)
	}
	parentMsg := lastUserText(t, b, "pa")
	if !strings.Contains(parentMsg, "【派发回报】") || !strings.Contains(parentMsg, "已由用户手动停止") {
		t.Fatalf("派发回报应按用户停止：%q", parentMsg)
	}
	if strings.Contains(parentMsg, "因服务重启被中断") {
		t.Fatalf("派发回报把用户主动叫停误报成服务重启：%q", parentMsg)
	}
	if !strings.Contains(parentMsg, "进度：已完成 1 轮 / 1 步") {
		t.Fatalf("派发回报应带进度（1 轮 / 1 步）：%q", parentMsg)
	}
	if !strings.Contains(parentMsg, "不要再自动重新派发") {
		t.Fatalf("派发回报应明确要求不要重新派发：%q", parentMsg)
	}
	if !strings.Contains(parentMsg, "停止时间 "+rec.At) {
		t.Fatalf("派发回报应带停止时间 %s：%q", rec.At, parentMsg)
	}
	// 用户主动停止不算异常：侧栏不留「⚠ 已中断」，会话置空闲
	if got := b.Store.LastStops()["tk"]; got != "" {
		t.Fatalf("用户停止不该留异常中断标记，得到 %q", got)
	}
	if m, _ := b.Store.Get("tk"); m.Status != "idle" {
		t.Fatalf("收口后应为 idle，得到 %q", m.Status)
	}
	// 落库记录跨重启仍在，且待收口标志已作废、累计计数保留
	rec2 := b.Store.GetStopRecord("tk")
	if rec2.Kind != stopByUser || rec2.UserStops != 1 || rec2.LastUserAt == "" {
		t.Fatalf("重启后读回的停止记录不对: %+v", rec2)
	}
	if rec2.Manual {
		t.Fatalf("收口后待收口标志应作废: %+v", rec2)
	}
}

// TestStopReasonRestartOnly 场景②：没有用户停止，服务重启导致中断 —— 回报仍是
// 系统异常中断（服务重启），并带中断时间/触发者/进度。
func TestStopReasonRestartOnly(t *testing.T) {
	dir := t.TempDir()
	a := newStopTestApp(t, dir)
	seedStopFixture(t, a)

	if _, ok := a.beginRun("tk"); !ok {
		t.Fatal("beginRun 应成功")
	}
	// 进程崩了：不 stopRun、不 finishRun
	a.Store.Close()
	b := newStopTestApp(t, dir)
	if rec := b.Store.GetStopRecord("tk"); rec.Kind != "" {
		t.Fatalf("没停过就不该有停止记录: %+v", rec)
	}
	b.recoverInterrupted()

	parentMsg := lastUserText(t, b, "pa")
	if !strings.Contains(parentMsg, "因服务重启被中断，未完成") {
		t.Fatalf("重启回报文案不符：%q", parentMsg)
	}
	if !strings.Contains(parentMsg, "触发者：系统（服务重启）") || !strings.Contains(parentMsg, "进度：已完成 1 轮 / 1 步") {
		t.Fatalf("重启回报应带触发者与进度：%q", parentMsg)
	}
	if strings.Contains(parentMsg, "已由用户手动停止") {
		t.Fatalf("没有用户停止却报成用户停止：%q", parentMsg)
	}
	if got := b.Store.LastStops()["tk"]; got != stopRestart {
		t.Fatalf("重启中断应留 restart 标记，得到 %q", got)
	}
	if rec := b.Store.GetStopRecord("tk"); rec.Kind != stopBySystem || rec.Actor != "system" || rec.Manual {
		t.Fatalf("系统中断记录落库不对: %+v", rec)
	}
}

// TestStopReasonStaleMarkerNotReused 场景③：用户停止已收口，之后又跑了一轮才崩
// ——不能拿旧记录解释新的一轮，仍报「服务重启中断」，但累计的「用户停过」保留。
func TestStopReasonStaleMarkerNotReused(t *testing.T) {
	dir := t.TempDir()
	a := newStopTestApp(t, dir)
	seedStopFixture(t, a)

	run, ok := a.beginRun("tk")
	if !ok {
		t.Fatal("beginRun 应成功")
	}
	_ = run
	if !a.stopRun("tk") {
		t.Fatal("stopRun 应成功")
	}
	// 这一轮真的收口了（executeRun/runSubAgent 收尾都会走 finishRun）
	a.finishRun("tk")
	if rec := a.Store.GetStopRecord("tk"); rec.Manual {
		t.Fatalf("收口后待收口标志应已作废: %+v", rec)
	}
	// 用户又发了一轮，这一轮崩在服务重启上
	if _, ok := a.beginRun("tk"); !ok {
		t.Fatal("第二次 beginRun 应成功")
	}
	a.Store.Close()
	b := newStopTestApp(t, dir)
	b.recoverInterrupted()

	parentMsg := lastUserText(t, b, "pa")
	if !strings.Contains(parentMsg, "因服务重启被中断，未完成") {
		t.Fatalf("新的一轮崩了应报服务重启：%q", parentMsg)
	}
	if strings.Contains(parentMsg, "已由用户手动停止") {
		t.Fatalf("旧记录不该解释新的一轮：%q", parentMsg)
	}
	rec := b.Store.GetStopRecord("tk")
	if rec.Kind != stopBySystem || rec.UserStops != 1 || rec.LastUserAt == "" {
		t.Fatalf("系统中断记录应覆盖最新原因、但保留累计的用户停止次数: %+v", rec)
	}
}

// TestStopReasonLegacyCompat 场景④：旧数据（没有任何停止记录、也没有 userStops
// 之类新字段）读出来就是零值「未记录」，恢复流程照常按服务重启收口，不会因为
// 缺字段而失败。
func TestStopReasonLegacyCompat(t *testing.T) {
	dir := t.TempDir()
	a := newStopTestApp(t, dir)
	seedStopFixture(t, a)

	// 旧库形态：只有 status=running，没有 meta 里的停止记录
	if err := a.Store.UpdateStatus("tk", "running"); err != nil {
		t.Fatalf("置 running 失败: %v", err)
	}
	if rec := a.Store.GetStopRecord("tk"); rec.Kind != "" || rec.Manual || rec.UserStops != 0 {
		t.Fatalf("缺记录时应为零值「未记录」: %+v", rec)
	}
	// 空闲会话被点停止（没有在执行）：不该落下停止记录（否则会误标下一次崩溃）
	if a.stopRun("tk") {
		t.Fatal("没有执行的会话 stopRun 应返回 false")
	}
	if rec := a.Store.GetStopRecord("tk"); rec.Kind != "" {
		t.Fatalf("空闲时点停止不该写记录: %+v", rec)
	}

	a.Store.Close()
	b := newStopTestApp(t, dir)
	b.recoverInterrupted()
	parentMsg := lastUserText(t, b, "pa")
	if !strings.Contains(parentMsg, "因服务重启被中断，未完成") {
		t.Fatalf("旧数据应照常按服务重启收口：%q", parentMsg)
	}
	if m, _ := b.Store.Get("tk"); m.Status != "idle" {
		t.Fatalf("收口后应为 idle，得到 %q", m.Status)
	}
}

// TestStopReportNotAutoContinued 场景⑤（数字助理侧的自动行为）：用户手动停止的
// 任务回报到父会话后，父会话不会被自动起一轮（那正是「助理自作主张重新派发」的
// 来源）；正常结束的回报仍照旧自动起汇报轮。
//
// 用本地假模型服务（慢速 SSE）把「自动起轮」这件事变成可观测的：正常结束那一
// 条回报会立刻占住父会话的执行权（runOf(pa) != nil），用户停止那条不会。
func TestStopReportNotAutoContinued(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 第一条（被派发任务那一轮）立即回；后续（父会话的汇报轮）压慢一点，
		// 让「这一轮正在跑」有稳定的观察窗口
		if atomic.AddInt32(&hits, 1) > 1 {
			time.Sleep(500 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"好的\"}}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "test-model"
	cfg.DataDir = dir
	cfg.Workspace = dir
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.Close()
	a := &App{Store: store, Cfg: cfg}
	a.initRuntime()
	seedStopFixture(t, a)
	parent, _ := a.Store.Get("pa")
	sub, _ := a.Store.Get("tk")

	// 用户停止的那条：ctx 已取消，runSubAgent 立即收手，父会话不该被自动起轮
	run, ok := a.beginRun("tk")
	if !ok {
		t.Fatal("beginRun 应成功")
	}
	if !a.stopRun("tk") {
		t.Fatal("stopRun 应成功")
	}
	a.runSubAgent(parent, sub, run) // 内部 defer finishRun
	if a.runOf("pa") != nil {
		t.Fatal("用户停止的回报不该自动触发父会话的一轮")
	}
	if got := lastUserText(t, a, "pa"); !strings.Contains(got, "已由用户手动停止") || !strings.Contains(got, "不要再自动重新派发") {
		t.Fatalf("父会话应收到用户停止的回报与「不要重派」指令：%q", got)
	}

	// 对照：正常结束的回报会自动起汇报轮
	_ = a.Store.Create(&SessionMeta{ID: "tk2", Title: "正常跑完的任务", Mode: "task", DispatchedBy: "pa", CreatedAt: now(), UpdatedAt: now()})
	run2, ok := a.beginRun("tk2")
	if !ok {
		t.Fatal("beginRun 应成功")
	}
	sub2, _ := a.Store.Get("tk2")
	a.runSubAgent(parent, sub2, run2) // 正常结束：内部会自动起父会话的汇报轮
	if a.runOf("pa") == nil {
		t.Fatal("正常结束的回报应照旧自动起父会话的一轮")
	}
	if got := lastUserText(t, a, "pa"); !strings.Contains(got, "本轮结束") {
		t.Fatalf("正常结束的回报文案应保持原样：%q", got)
	}
	// 等汇报轮收口，别让它在测试收尾（关库）时还在写
	for i := 0; i < 300 && a.runOf("pa") != nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
}
