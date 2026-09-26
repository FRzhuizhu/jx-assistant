package main

// 内嵌浏览器宿主的端到端测试：真实拉起无头 Edge，验证人机共操的输入通道
// （面板的坐标输入与工具的选择器点击走同一批 CDP 调用）与视口设定。
// 依赖系统自带 Edge/Chrome；环境没有时跳过。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// browserFixture 起一个带计数按钮与标题镜像的测试页 + 一个接好宿主的 App。
func browserFixture(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>t</title></head><body>
<button id="inc">+1</button><input id="field">
<script>
let n = 0, last = 'none';
document.getElementById('inc').addEventListener('click', () => { n++; });
document.addEventListener('mousedown', (e) => { last = 'down@' + e.clientX + ',' + e.clientY; }, true);
document.getElementById('field').addEventListener('input', (e) => { last = 'input:' + e.target.value; });
setInterval(() => {
  const r = document.getElementById('inc').getBoundingClientRect();
  document.title = 'n=' + n + ' inc=' + Math.round(r.x + r.width/2) + ',' + Math.round(r.y + r.height/2) + ' ev=' + last;
}, 200);
</script></body></html>`)
	}))
	// 不用 t.TempDir：chromedp 关闭是异步的，Edge 的 Crashpad 子进程可能仍占用
	// 配置目录，TempDir 的一次性 RemoveAll 会竞态失败；这里手动带重试清理
	dir, err := os.MkdirTemp("", "jx-browser-test")
	if err != nil {
		t.Fatalf("建临时目录失败: %v", err)
	}
	app := &App{Cfg: &Config{DataDir: dir}}
	t.Cleanup(func() {
		app.Browser().Close()
		app.headedBrowser().Close()
		srv.Close()
		for i := 0; i < 25; i++ { // 等 Edge 退干净（删不掉就留给系统临时目录，不影响测试结论）
			if os.RemoveAll(dir) == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	return app, srv
}

// 有头独立窗口路径（旧版行为）：能起有头 Edge、导航、读标题；关闭后 active 回退。
func TestBrowserHostHeadedNavigate(t *testing.T) {
	app, srv := browserFixture(t)
	h := app.headedBrowser()
	url, title, err := h.Navigate(srv.URL + "?headed")
	if err != nil {
		t.Skipf("无可用浏览器，跳过: %v", err)
	}
	if !strings.Contains(url, "?headed") {
		t.Fatalf("有头导航落定地址异常: %s", url)
	}
	// 有头实例不发面板事件（导航后标题经 State 可读，但广播被门控——不在此断言广播）
	if !strings.Contains(title, "inc=") && title != "t" {
		t.Logf("有头页面标题（可能未到镜像周期）: %q", title)
	}
	h.Close()
	if got := app.activeBrowser(); got != nil {
		t.Fatalf("唯一实例关闭后 active 应为 nil，得到 %p", got)
	}
}

func parseCounter(t *testing.T, title string) (n int, x, y float64, ev string) {
	t.Helper()
	m := regexp.MustCompile(`n=(\d+) inc=([\d.]+),([\d.]+) ev=(\S*)`).FindStringSubmatch(title)
	if m == nil {
		t.Fatalf("标题不匹配: %q", title)
	}
	n, _ = strconv.Atoi(m[1])
	fmt.Sscan(m[2], &x)
	fmt.Sscan(m[3], &y)
	return n, x, y, m[4]
}

// waitTitle 轮询「实时」标题：绕开 State 的导航时快照，直接经 CDP 读
// （页面自身的 title 变化不会触发任何 CDP 事件，宿主靠轮询同步，见 navLoop）。
func waitTitle(t *testing.T, h *BrowserHost, substr string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		_ = h.do(func(ctx context.Context) error {
			return chromedp.Title(&last).Do(ctx)
		})
		if strings.Contains(last, substr) {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("等待标题含 %q 超时，最后: %q", substr, last)
	return ""
}

func TestBrowserHostHumanInputClick(t *testing.T) {
	app, srv := browserFixture(t)
	h := app.Browser()
	if _, _, err := h.Navigate(srv.URL); err != nil {
		t.Skipf("无可用浏览器，跳过: %v", err)
	}
	title := waitTitle(t, h, "n=0")
	_, x, y, _ := parseCounter(t, title)

	// 人的路径：坐标 down/up（面板转发的就是这两个）
	if err := h.Input(BrowserInputMsg{Type: "move", X: x, Y: y}); err != nil {
		t.Fatal(err)
	}
	if err := h.Input(BrowserInputMsg{Type: "down", X: x, Y: y}); err != nil {
		t.Fatal(err)
	}
	if err := h.Input(BrowserInputMsg{Type: "up", X: x, Y: y}); err != nil {
		t.Fatal(err)
	}
	title = waitTitle(t, h, "n=1")
	if _, _, _, ev := parseCounter(t, title); ev == "none" {
		t.Logf("注意：计数已生效但 mousedown 捕获记录为 none（事件送达但捕获监听未记录）: %s", title)
	}
}

func TestBrowserHostToolClickAndType(t *testing.T) {
	app, srv := browserFixture(t)
	h := app.Browser()
	if _, _, err := h.Navigate(srv.URL + "?tool"); err != nil {
		t.Skipf("无可用浏览器，跳过: %v", err)
	}
	title := waitTitle(t, h, "n=0")
	parseCounter(t, title)

	// 工具路径：选择器点击（与 browser_click 同一条链）
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Click("#inc", chromedp.ByQueryAll))
	}); err != nil {
		t.Fatal(err)
	}
	waitTitle(t, h, "n=1")

	// 文本注入路径：点输入框 + InsertText（人的中文输入与 AI 打字共用）
	if err := h.Input(BrowserInputMsg{Type: "down", X: 20, Y: 200}); err != nil {
		t.Log("盲点输入框未命中（坐标不精确），仅验证 InsertText 通道:", err)
	}
	_ = h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Click("#field", chromedp.ByQueryAll))
	})
	if err := h.Input(BrowserInputMsg{Type: "text", Text: "你好世界"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.State().Title, "你好世界") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("insertText 未到达输入框: %s", h.State().Title)
}

func TestBrowserHostViewport(t *testing.T) {
	app, srv := browserFixture(t)
	h := app.Browser()
	if _, _, err := h.Navigate(srv.URL + "?vp"); err != nil {
		t.Skipf("无可用浏览器，跳过: %v", err)
	}
	if err := h.SetViewport(393, 852); err != nil {
		t.Fatal(err)
	}
	st := h.State()
	if st.VW != 393 || st.VH != 852 {
		t.Fatalf("视口未生效: %+v", st)
	}
}

// open-view 端点的边界用例：url 校验、非交付清单路径拒绝、无 session 的纯 url 打开。
func TestBrowserOpenViewBoundaries(t *testing.T) {
	dir, err := os.MkdirTemp("", "jx-openview-test")
	if err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); os.RemoveAll(dir) })
	m := &SessionMeta{ID: "s1", Mode: "task", Title: "会话", Workspace: dir, CreatedAt: now(), UpdatedAt: now()}
	if err := st.Create(m); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: &Config{DataDir: dir}, Store: st}
	t.Cleanup(func() { app.Browser().Close(); app.headedBrowser().Close() })
	post := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/browser/open-view", strings.NewReader(body))
		rec := httptest.NewRecorder()
		app.handleBrowserOpenView(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, _ := post(`{"url":"ftp://x"}`); code != 400 {
		t.Fatalf("非 http(s) 应 400，得到 %d", code)
	}
	if code, _ := post(`{}`); code != 400 {
		t.Fatalf("空参数应 400，得到 %d", code)
	}
	if code, _ := post(`{"path":"a.html"}`); code != 400 {
		t.Fatalf("path 无 session 应 400，得到 %d", code)
	}
	// 不在交付清单里的路径：404（文件确实不存在也走这条）
	if code, _ := post(`{"session":"` + m.ID + `","path":"../秘密.html"}`); code != 404 {
		t.Fatalf("非交付清单路径应 404，得到 %d", code)
	}
	if code, _ := post(`{"session":"no-such","path":"a.html"}`); code != 404 {
		t.Fatalf("不存在会话应 404，得到 %d", code)
	}
	// 正路径：工作区里的已登记交付 html 文件 → file:// 打开 + 注入 webview 回报
	html := filepath.Join(dir, "report.html")
	if err := os.WriteFile(html, []byte("<html><body><h1>交付报告</h1></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDelivery(&Delivery{SessionID: m.ID, Path: "report.html", Note: "测试交付", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	code, body := post(`{"session":"` + m.ID + `","path":"report.html"}`)
	if code == 502 && strings.Contains(body, "启动浏览器失败") {
		t.Skipf("无可用浏览器，跳过打开用例: %s", body)
	}
	if code != 200 || !strings.Contains(body, "file:///") {
		t.Fatalf("交付 html 打开应 200 且为 file:// 地址，得到 %d: %s", code, body)
	}
	msgs2, _ := st.MessagesWithSeq(m.ID)
	found := false
	webviewBefore := 0
	for _, x := range msgs2 {
		if x.Origin == "webview" {
			webviewBefore++
			if strings.Contains(fmt.Sprint(x.Content), "report.html") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("打开交付文件应注入【网页查看】回报")
	}

	// 纯 url（无 session）：能打开且不新增回报（之前 file:// 用例已注入过一条）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>ok</body></html>")
	}))
	defer srv.Close()
	code, body = post(`{"url":"` + srv.URL + `"}`)
	if code == 502 && strings.Contains(body, "启动浏览器失败") {
		t.Skipf("无可用浏览器，跳过打开用例: %s", body)
	}
	if code != 200 {
		t.Fatalf("纯 url 打开应 200，得到 %d: %s", code, body)
	}
	msgs3, _ := st.MessagesWithSeq(m.ID)
	webviewAfter := 0
	for _, x := range msgs3 {
		if x.Origin == "webview" {
			webviewAfter++
		}
	}
	if webviewAfter != webviewBefore {
		t.Fatal("无 session 的纯 url 打开不应新增 webview 回报")
	}
}
