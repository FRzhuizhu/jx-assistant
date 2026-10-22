package main

// 「关闭某个会话的浏览器」收口（closeSessionBrowser）的针对性测试：右栏 ✕
// 与 AI 的 browser_close 走同一个函数，两条路只在「谁发起」的标记与注记上
// 不同。用假 target 顶替原生面板/有头实例（不碰真浏览器、不开窗口），覆盖：
//  1. 用户 ✕：打「用户关闭」标记、清面板可见性记账、投一条 report 注记、
//     广播 browser_nav 复位；
//  2. 本来就没开：幂等成功（closed=false），不打标记、不投注记；
//  3. AI 关闭：不打用户标记、不投注记（发起者就是模型自己），另一形态仍在
//     运行时给出回退目标；
//  4. 两条路关的目标不同：用户 ✕ 只关内嵌形态（有头窗口不碰），AI 关当前
//     作用的实例（含回退）；
//  5. 两个入口（HTTP 端点 / 工具）都不绕过收口——「谁发起」的差别只出现在
//     收口函数里，不许再有第二份实现。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeBrowserTarget 只实现收口用到的三个方法，其余按接口占位——收口不碰
// 页面操作面。
type fakeBrowserTarget struct {
	started bool
	closed  int
	where   string
	reason  string // CloseWithReason 收到的发起方（面板形态据此写关闭日志）
}

func (f *fakeBrowserTarget) Navigate(string) (string, string, error) { return "", "", nil }
func (f *fakeBrowserTarget) NavAction(string) error                  { return nil }
func (f *fakeBrowserTarget) Content() (string, string, string, error) {
	return "", "", "", nil
}
func (f *fakeBrowserTarget) Click(string) error                  { return nil }
func (f *fakeBrowserTarget) TypeInto(string, string, bool) error { return nil }
func (f *fakeBrowserTarget) Scroll(string, int) error            { return nil }
func (f *fakeBrowserTarget) SetViewport(int, int) error          { return nil }
func (f *fakeBrowserTarget) Evaluate(string) (any, error)        { return nil, nil }
func (f *fakeBrowserTarget) Screenshot() ([]byte, error)         { return nil, nil }
func (f *fakeBrowserTarget) State() BrowserState                 { return BrowserState{} }
func (f *fakeBrowserTarget) Started() bool                       { return f.started }
func (f *fakeBrowserTarget) Where() string                       { return f.where }
func (f *fakeBrowserTarget) BroadcastPointer(string)             {}
func (f *fakeBrowserTarget) Close()                              { f.closed++; f.started = false }
func (f *fakeBrowserTarget) CloseWithReason(reason string)       { f.reason = reason; f.Close() }

// testAppWithFake 造一个「内嵌面板=假 target」的 App（面板集合直接塞假对象，
// 不走真实 WebView2 创建；panelDispatch 非 nil 才能让 embeddedTarget 走面板路）。
func testAppWithFake(t *testing.T) (*App, string, *fakeBrowserTarget) {
	t.Helper()
	a, main := newDispatchTestApp(t)
	fake := &fakeBrowserTarget{started: true, where: "右栏「浏览器」面板（假）"}
	key := browserProfileKey(main.ID)
	a.panelMu.Lock()
	a.panels = map[string]browserTarget{key: fake}
	a.panelDispatch = func(f func()) { f() } // 只用于让 embeddedTarget 走面板分支
	a.panelVisible = key
	a.panelMu.Unlock()
	return a, main.ID, fake
}

// subscribeEvents 订阅广播（收口要复位 browser_nav，测试要看它到底发没发）。
func subscribeEvents(a *App) chan Event {
	ch := make(chan Event, 16)
	a.evtMu.Lock()
	if a.subscribers == nil {
		a.subscribers = map[chan Event]bool{}
	}
	a.subscribers[ch] = true
	a.evtMu.Unlock()
	return ch
}

// lastBrowserNav 取最近一条 browser_nav（没有则空值）。
func lastBrowserNav(ch chan Event) (Event, bool) {
	var ev Event
	found := false
	for {
		select {
		case e := <-ch:
			if e.Type == "browser_nav" {
				ev, found = e, true
			}
		default:
			return ev, found
		}
	}
}

// sessionTexts 该会话落库的全部正文（注记断言用）。
func sessionTexts(t *testing.T, a *App, sid string) []string {
	t.Helper()
	rows, err := a.Store.RawMessagesAll(sid)
	if err != nil {
		t.Fatalf("读会话消息失败: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%v", r.Message.Content))
	}
	return out
}

// ① 用户 ✕：标记 + 可见性清理 + 注记 + 复位广播。
func TestCloseSessionBrowserByUser(t *testing.T) {
	a, sid, fake := testAppWithFake(t)
	evs := subscribeEvents(a)
	// 该会话确实在用浏览器（注记只投给用过的会话）
	a.browserMu.Lock()
	a.browserActive = map[string]browserTarget{browserProfileKey(sid): fake}
	a.browserMu.Unlock()

	res := a.closeSessionBrowser(sid, closeByUser)
	if !res.Closed || fake.closed != 1 {
		t.Fatalf("用户关闭未收口：closed=%v 实例关闭次数=%d", res.Closed, fake.closed)
	}
	if !strings.Contains(fake.reason, "用户") {
		t.Fatalf("关闭没把「谁发起」带给面板（关闭阶段日志要靠它排查）：%q", fake.reason)
	}
	if !a.browserUserClosed(sid) {
		t.Fatal("用户关闭没打「用户关闭」标记（后续工具会把用户操作报成崩溃）")
	}
	if a.panelVisibleKey() != "" {
		t.Fatal("显示中的面板被关掉了，可见性记账没清")
	}
	ev, ok := lastBrowserNav(evs)
	if !ok || ev.SessionID != sid || ev.URL != "" || ev.Title != "" {
		t.Fatalf("没有广播 browser_nav 复位（或带错了会话）：%+v", ev)
	}
	var note string
	for _, s := range sessionTexts(t, a, sid) {
		if strings.Contains(s, "【用户关闭浏览器】") {
			note = s
		}
	}
	if note == "" {
		t.Fatal("用户关闭没有给会话投【用户关闭浏览器】注记")
	}
	if !strings.Contains(note, "非崩溃") {
		t.Fatalf("注记没写明「用户主动关闭，非崩溃」，模型分不清用户操作与自身崩盘：%s", note)
	}
}

// ② 本来就没开：幂等成功，不留标记、不投注记。
func TestCloseSessionBrowserNothingRunning(t *testing.T) {
	a, sid, fake := testAppWithFake(t)
	fake.started = false // 面板键在、实例没跑（等价于从未打开）
	before := len(sessionTexts(t, a, sid))

	res := a.closeSessionBrowser(sid, closeByUser)
	if res.Closed || fake.closed != 0 {
		t.Fatalf("没在运行却报告关闭了：closed=%v 关闭次数=%d", res.Closed, fake.closed)
	}
	if a.browserUserClosed(sid) {
		t.Fatal("没开过浏览器却打上了「用户关闭」标记")
	}
	if after := len(sessionTexts(t, a, sid)); after != before {
		t.Fatalf("没开过浏览器却投了注记（消息数 %d → %d）", before, after)
	}
}

// ③ AI 关闭：不打用户标记、不投注记；关干净后不误报「另一实例仍在运行」。
// （「另一形态还活着 → 给回退目标」需要真的运行中的有头实例，单测里跑不起来，
// 由人工验收覆盖：AI 先 browser_open headed 再 browser_close。）
func TestCloseSessionBrowserByAI(t *testing.T) {
	a, sid, fake := testAppWithFake(t)
	a.browserMu.Lock()
	a.browserActive = map[string]browserTarget{browserProfileKey(sid): fake}
	a.browserMu.Unlock()
	before := len(sessionTexts(t, a, sid))

	res := a.closeSessionBrowser(sid, closeByAI)
	if !res.Closed || fake.closed != 1 {
		t.Fatalf("AI 关闭未收口：closed=%v 关闭次数=%d", res.Closed, fake.closed)
	}
	if !strings.Contains(fake.reason, "AI") {
		t.Fatalf("AI 关闭没把发起方带给面板（日志分不清谁关的）：%q", fake.reason)
	}
	if res.Fallback != nil {
		t.Fatalf("实例都关完了却报「另一实例仍在运行」：%v", res.Fallback.Where())
	}
	if a.browserUserClosed(sid) {
		t.Fatal("AI 关闭不该打「用户关闭」标记（会把 AI 自己的动作误导成用户操作）")
	}
	if after := len(sessionTexts(t, a, sid)); after != before {
		t.Fatalf("AI 关闭不该投注记（发起者就是模型自己，工具返回值即回执）：消息数 %d → %d", before, after)
	}
}

// ④ 关的目标不同：用户 ✕ 只关内嵌形态——AI 正在用的有头实例不受影响。
func TestCloseSessionBrowserUserKeepsHeaded(t *testing.T) {
	a, sid, fake := testAppWithFake(t)
	headed := newBrowserHost(a, false, "") // 未启动的宿主：只用来占 active 记账
	a.browserMu.Lock()
	a.browserActive = map[string]browserTarget{browserProfileKey(sid): headed}
	a.browserMu.Unlock()

	a.closeSessionBrowser(sid, closeByUser)
	if fake.closed != 1 {
		t.Fatal("用户 ✕ 没关内嵌面板")
	}
	a.browserMu.Lock()
	active := a.browserActive[browserProfileKey(sid)]
	a.browserMu.Unlock()
	if active != headed {
		t.Fatal("用户 ✕ 不该动到 AI 正在作用的有头实例")
	}
}

// ⑤ 两个「入口」都不许绕过收口（用户手工点 ✕ 与 AI 调工具必须是同一套动作）：
// 直接打 HTTP 端点、直接跑工具，各自的副作用都要与收口函数一致——「谁发起」
// 的差别（标记/注记/回执）只允许出现在收口函数里。
func TestBrowserCloseEntrypointsShareCollector(t *testing.T) {
	// 入口一：右栏 ✕ 的 HTTP 端点
	a, sid, fake := testAppWithFake(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/browser/close",
		strings.NewReader(`{"session":"`+sid+`"}`))
	a.handleBrowserClose(w, req)
	if fake.closed != 1 {
		t.Fatalf("HTTP 入口没走收口（实例关闭次数=%d）", fake.closed)
	}
	if !a.browserUserClosed(sid) {
		t.Fatal("HTTP 入口没走收口（缺「用户关闭」标记）")
	}
	if !strings.Contains(w.Body.String(), `"closed":true`) {
		t.Fatalf("HTTP 入口回执不对：%s", w.Body.String())
	}

	// 入口二：AI 的 browser_close 工具
	a2, sid2, fake2 := testAppWithFake(t)
	m, err := a2.Store.Get(sid2)
	if err != nil || m == nil {
		t.Fatalf("取会话失败: %v", err)
	}
	m.Tools = []string{capBrowser} // 能力门控放行（工具本身不是被测对象）
	out, err := browserCloseTool().Run(&toolContext{app: a2, sess: m, toolName: "browser_close"},
		json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("工具返回错误: %v", err)
	}
	if fake2.closed != 1 {
		t.Fatalf("工具入口没走收口（实例关闭次数=%d）", fake2.closed)
	}
	if a2.browserUserClosed(sid2) {
		t.Fatal("工具入口被当成了用户发起（打了「用户关闭」标记）")
	}
	if out != "浏览器已关闭" {
		t.Fatalf("工具回执文案变了（模型口径会跟着变）：%q", out)
	}
}

// ---------- 关闭阶段二：等进程真退出 → 超时强杀 → 拆窗口（不碰真 WebView2） ----------
//
// 用户的硬要求：关闭必须真关（进程真退出、窗口真拆）、不许延迟回收、不许软
// 关闭、超时也必须真关。下面用假进程（fakePanelProc）与假 UI 投递覆盖四条
// 分支：正常退出、超时强杀、拆窗口只投一次且只在 UI 线程、无进程可等。

// fakePanelProc 假浏览器进程：Wait 结果按 results 逐次返回（用完后重复最后
// 一项），并把调用顺序记进 events（断言「先等退出、后拆窗口」）。
type fakePanelProc struct {
	pid     uint32
	results []bool
	waits   int
	kills   int
	release int
	events  *[]string
}

func (f *fakePanelProc) PID() uint32 { return f.pid }

func (f *fakePanelProc) Wait(time.Duration) bool {
	i := f.waits
	f.waits++
	if f.events != nil {
		*f.events = append(*f.events, "wait")
	}
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	if i < 0 {
		return true
	}
	return f.results[i]
}

func (f *fakePanelProc) Kill() error {
	f.kills++
	if f.events != nil {
		*f.events = append(*f.events, "kill")
	}
	return nil
}

func (f *fakePanelProc) Release() { f.release++ }

// testPanelCloseTiming 测试用的等待上限（真实值 10s/5s，测试里只验证分支）。
var testPanelCloseTiming = panelCloseTiming{exit: 50 * time.Millisecond, kill: 50 * time.Millisecond}

// ① 正常退出：等到真实退出信号就拆窗口，不动强杀。
func TestFinishPanelCloseWaitsForRealExit(t *testing.T) {
	var events []string
	ex := &fakePanelProc{pid: 4242, results: []bool{true}, events: &events}
	destroys, posts := 0, 0
	destroy := func() { destroys++; events = append(events, "destroy") }
	post := func(f func()) { posts++; f() } // 模拟 UI 线程投递：立即执行

	killed := finishPanelClose(ex, post, "sid-1", "hwnd=0x1", destroy, testPanelCloseTiming)

	if killed || ex.kills != 0 {
		t.Fatalf("进程正常退出却动了强杀：killed=%v 强杀次数=%d", killed, ex.kills)
	}
	if ex.waits != 1 {
		t.Fatalf("没等退出就往下走（Wait 次数=%d）", ex.waits)
	}
	if posts != 1 || destroys != 1 {
		t.Fatalf("拆窗口没有恰好投递/执行一次：投递=%d 执行=%d", posts, destroys)
	}
	if ex.release != 1 {
		t.Fatalf("进程句柄没释放（Release 次数=%d）", ex.release)
	}
	if len(events) != 2 || events[0] != "wait" || events[1] != "destroy" {
		t.Fatalf("动作顺序不对（必须等进程退出后才拆窗口）：%v", events)
	}
}

// ② 超时强杀：等不到就强制终止我们自己的这个子进程，再等到确实退出才拆窗口
// ——不留「放弃拆、留空壳」的分支。
func TestFinishPanelCloseKillsOnTimeout(t *testing.T) {
	var events []string
	ex := &fakePanelProc{pid: 77, results: []bool{false, true}, events: &events}
	destroys := 0
	destroy := func() { destroys++; events = append(events, "destroy") }

	killed := finishPanelClose(ex, func(f func()) { f() }, "sid-2", "hwnd=0x2", destroy, testPanelCloseTiming)

	if !killed || ex.kills != 1 {
		t.Fatalf("超时没强杀：killed=%v 强杀次数=%d", killed, ex.kills)
	}
	if ex.waits != 2 {
		t.Fatalf("强杀后没有继续等到确实退出（Wait 次数=%d）", ex.waits)
	}
	if destroys != 1 {
		t.Fatalf("强杀后没拆窗口（或拆了多次）：执行=%d", destroys)
	}
	want := []string{"wait", "kill", "wait", "destroy"}
	if len(events) != len(want) {
		t.Fatalf("动作序列不对：%v（期望 %v）", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("动作序列不对：%v（期望 %v）", events, want)
		}
	}
}

// ③ 拆窗口只在 UI 线程（投递进去的闭包里）执行；UI 投递不可用时什么都不乱拆。
func TestFinishPanelCloseDestroysOnUIThread(t *testing.T) {
	inPost := false
	posts, destroys := 0, 0
	post := func(f func()) { posts++; inPost = true; f(); inPost = false }
	destroy := func() {
		destroys++
		if !inPost {
			t.Error("拆窗口在后台 goroutine 里直接跑了：必须投递回 UI 线程执行")
		}
	}
	ex := &fakePanelProc{pid: 9, results: []bool{true}}
	finishPanelClose(ex, post, "sid-3", "hwnd=0x3", destroy, testPanelCloseTiming)
	if posts != 1 || destroys != 1 {
		t.Fatalf("拆窗口投递/执行次数不对：投递=%d 执行=%d", posts, destroys)
	}

	// 消息泵已停（post 为 nil）时：不拆（也无从拆），但不许 panic、句柄照样释放
	ex2 := &fakePanelProc{pid: 10, results: []bool{true}}
	finishPanelClose(ex2, nil, "sid-4", "hwnd=0x4", func() { t.Error("没有 UI 投递却直接拆了窗口") },
		testPanelCloseTiming)
	if ex2.release != 1 {
		t.Fatalf("投递不可用时进程句柄没释放（Release 次数=%d）", ex2.release)
	}
}

// ④ 没有可等的进程句柄（PID 取不到/进程已退）：按已退出处理，照常拆窗口。
func TestFinishPanelCloseWithoutProcess(t *testing.T) {
	destroys := 0
	killed := finishPanelClose(nil, func(f func()) { f() }, "sid-5", "hwnd=0x5",
		func() { destroys++ }, testPanelCloseTiming)
	if killed || destroys != 1 {
		t.Fatalf("没有进程可等时应当直接拆窗口：killed=%v 拆窗口=%d", killed, destroys)
	}
	// 没有窗口可拆：不投递、不 panic、不报强杀
	if killed := finishPanelClose(nil, func(f func()) { t.Error("没有窗口却投递了动作") },
		"sid-6", "hwnd=0x0", nil, testPanelCloseTiming); killed {
		t.Fatal("没有窗口时不该报强杀")
	}
}
