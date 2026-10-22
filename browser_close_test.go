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
)

// fakeBrowserTarget 只实现收口用到的三个方法，其余按接口占位——收口不碰
// 页面操作面。
type fakeBrowserTarget struct {
	started bool
	closed  int
	where   string
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
