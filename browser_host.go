package main

// 浏览器宿主（chromedp，CDP 纯 Go 驱动系统自带的 Edge/Chrome）：
//   * headed：有头独立窗口（旧版行为）——用户在真实窗口里操作，AI 共用；
//   * headless：网页形态下充当「内嵌」目标的后台无头实例，仅供 AI 的
//     browser_* 工具使用（桌面形态的内嵌目标是右栏原生 WebView2 面板，
//     见 browser_panel_windows.go；旧「无头 Edge + MJPEG 截图流镜像」方案
//     已整体移除——图片镜像的交互体验与原生控件不可弥合）。
// 两形态可并存、各自独立进程与配置目录；browserTarget 操作面（browser_ops.go）
// 由本宿主与原生面板共同实现，工具层（tool_browser.go）形态无关。
// 安全：服务仅本机监听（既定边界）；AI 侧工具照旧经 capBrowser 能力门控；
// 页面登录态落在 data/browser-profile*，与用户日常浏览器隔离；助手进程退出
// 由顶层 kill-on-close Job Object 清场（processguard_windows.go）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

const (
	browserTimeout = 60 * time.Second // 单次 CDP 操作上限（与原 tool_browser 一致）
	browserDefW    = 1280
	browserDefH    = 900
	browserMinSide = 320  // 视口边长下限（对齐 ZCode 的 320–3840 × 320–2160）
	browserMaxW    = 3840
	browserMaxH    = 2160
)

// BrowserHost 是一个 chromedp 浏览器宿主实例，两种形态（可并存、各自独立进程与配置目录）：
//   * headless=true  后台无头：网页形态的内嵌目标，AI 工具专用（无画面镜像）；
//   * headless=false 有头独立窗口：弹一个用户可见的 Edge 窗口（旧版行为），
//     人直接在真实窗口里操作，AI 经同一批工具操作。
// 生命周期：browser_open / open-view 触发启动；browser_close 与 Close() 显式关闭。
type BrowserHost struct {
	app      *App
	headless bool

	mu          sync.Mutex // 串行化生命周期与元数据读写
	allocCancel context.CancelFunc
	tabCtx      context.Context
	tabCancel   context.CancelFunc

	vw, vh int    // 当前生效视口（CSS px）
	url    string // 最近一次导航落定的地址/标题（state 端点与工具回显用）
	title  string

	lastX, lastY float64 // 最近鼠标位置（滚轮等不带坐标的输入挂在这里）
}

func newBrowserHost(app *App, headless bool) *BrowserHost {
	return &BrowserHost{app: app, headless: headless}
}

// findBrowserExe 优先找系统自带的 Edge（Win10/11 必装，符合自包含
// 原则——不依赖用户另装浏览器）；Edge 异常缺失时兜底找 Chrome；
// 都找不到交给 chromedp 自行探测。
func findBrowserExe() string {
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		os.Getenv("LOCALAPPDATA") + `\Microsoft\Edge\Application\msedge.exe`,
		os.Getenv("LOCALAPPDATA") + `\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Browser 返回（惰性创建）无头宿主：网页形态的内嵌目标（桌面形态是原生面板）。
func (a *App) Browser() *BrowserHost {
	a.browserMu.Lock()
	defer a.browserMu.Unlock()
	if a.browserEmbedded == nil {
		a.browserEmbedded = newBrowserHost(a, true)
	}
	return a.browserEmbedded
}

// headedBrowser 返回（惰性创建）有头独立窗口宿主（旧版行为）。
func (a *App) headedBrowser() *BrowserHost {
	a.browserMu.Lock()
	defer a.browserMu.Unlock()
	if a.browserHeaded == nil {
		a.browserHeaded = newBrowserHost(a, false)
	}
	return a.browserHeaded
}

// embeddedTarget 内嵌形态目标：桌面=右栏原生 WebView2 面板；网页=无头 chromedp。
func (a *App) embeddedTarget() browserTarget {
	if a.nativePanel != nil {
		return a.nativePanel
	}
	return a.Browser()
}

// browserFor 按 browser_open 的 mode 取目标（headless 缺省=内嵌形态），并记为
// 工具作用的当前实例（active）——其余工具（click/type/…）都作用于 active。
func (a *App) browserFor(mode string) browserTarget {
	var t browserTarget
	if mode == "headed" {
		t = a.headedBrowser()
	} else {
		t = a.embeddedTarget()
	}
	a.browserMu.Lock()
	a.browserActive = t
	a.browserMu.Unlock()
	return t
}

// activeBrowser 工具作用目标：最近一次 browser_open 选定的实例；没有 active
// 时兜底到正在运行的那个；都没运行返回 nil（调用方报「先 browser_open」）。
func (a *App) activeBrowser() browserTarget {
	a.browserMu.Lock()
	t := a.browserActive
	a.browserMu.Unlock()
	if t != nil && t.Started() {
		return t
	}
	// active 已关：回退到仍在运行的实例，并更新 active
	for _, cand := range []browserTarget{a.embeddedTarget(), a.headedBrowser()} {
		if cand != nil && cand.Started() {
			a.browserMu.Lock()
			a.browserActive = cand
			a.browserMu.Unlock()
			return cand
		}
	}
	return nil
}

// ---------- 生命周期 ----------

func (h *BrowserHost) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ensureStarted()
}

func (h *BrowserHost) Started() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tabCtx != nil
}

// ensureStarted 拉起浏览器（调用方需持有 h.mu）。按形态取启动参数：
// 内嵌无头走镜像帧流那套；有头按旧版行为弹独立可见窗口（不静音、不设窗口尺寸）。
// profile 必须转绝对路径：Chromium 对相对 user-data-dir 会静默秒退（stderr
// 无输出），chromedp 只能报出空的 "chrome failed to start"——实测踩坑。
func (h *BrowserHost) ensureStarted() error {
	if h.tabCtx != nil {
		return nil
	}
	profile := filepath.Join(h.app.Cfg.DataDir, "browser-profile")
	if !h.headless {
		profile = filepath.Join(h.app.Cfg.DataDir, "browser-profile-headed")
	}
	if abs, err := filepath.Abs(profile); err == nil {
		profile = abs
	}
	if err := os.MkdirAll(profile, 0o755); err != nil {
		return fmt.Errorf("创建浏览器配置目录失败: %w", err)
	}
	var opts []chromedp.ExecAllocatorOption
	if h.headless {
		opts = append(chromedp.DefaultExecAllocatorOptions[:], // 默认即 headless + disable-gpu + no-first-run
			chromedp.Flag("user-data-dir", profile),
			chromedp.Flag("window-size", fmt.Sprintf("%d,%d", browserDefW, browserDefH)),
			chromedp.Flag("mute-audio", true),
			// 无头页面无人观察时会被挂起（定时器停摆、SPA 不刷新）：AI 可能在
			// 面板未打开时操作页面，必须保持常活（自动化栈的标准三旗）
			chromedp.Flag("disable-background-timer-throttling", true),
			chromedp.Flag("disable-backgrounding-occluded-windows", true),
			chromedp.Flag("disable-renderer-backgrounding", true),
		)
	} else {
		// 有头独立窗口（旧版行为）：可见、有声、真窗口；用户直接操作，AI 共用
		opts = append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", false), // 有头模式：用户可见，操作可信
			chromedp.Flag("disable-gpu", false),
			chromedp.Flag("user-data-dir", profile),
		)
	}
	if exe := findBrowserExe(); exe != "" {
		opts = append(opts, chromedp.ExecPath(exe))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	// 预热：真正拉起浏览器进程，把启动失败暴露在第一次调用上。必须带超时——
	// 启动卡住（如残留实例占用 profile）时没有超时会无限占住 h.mu，之后所有
	// 浏览器端点（state/输入/导航）全部被堵死；快速失败让前端能走降级路径。
	// 注意不能拿 WithTimeout 派生 ctx 去 Run：会话会绑定到该 ctx，成功后取消
	// 派生 ctx 等于掐断会话（实测后续 navigate 全部 context canceled）。本机
	// 还实测过服务进程内启动的 Edge 会「进程起来、ws 已连、首个标签页事件
	// 永不到达」式挂起（同参数探针进程正常），浏览器宿主方案待更换，此处只
	// 保证不拖死进程：看门狗 20 秒未完成即拆掉这次尝试。
	runDone := make(chan error, 1)
	go func() { runDone <- chromedp.Run(ctx) }()
	select {
	case err := <-runDone:
		if err != nil {
			cancel()
			allocCancel()
			return fmt.Errorf("启动浏览器失败（需要系统自带的 Edge，或已安装的 Chrome）: %w", err)
		}
	case <-time.After(20 * time.Second):
		cancel()
		allocCancel()
		return fmt.Errorf("启动浏览器失败（需要系统自带的 Edge，或已安装的 Chrome）: 20 秒内未完成启动（可能被残留浏览器实例占用配置目录）")
	}
	h.allocCancel, h.tabCancel = allocCancel, cancel
	h.tabCtx = ctx
	h.vw, h.vh = browserDefW, browserDefH
	return nil
}

func (h *BrowserHost) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tabCancel != nil {
		h.tabCancel()
	}
	if h.allocCancel != nil {
		h.allocCancel()
	}
	h.tabCtx, h.allocCancel, h.tabCancel = nil, nil, nil
	h.url, h.title = "", ""
}

// ---------- 导航 ----------

// Navigate 打开/跳转地址，返回落定后的地址与标题，并广播 browser_nav。
// Navigate 打开/跳转地址（http/https）。file:// 仅经 open-view 的用户点击路径
// 进入（navigate 内部通道），模型工具 browser_open 仍只许 http/https。
func (h *BrowserHost) Navigate(raw string) (string, string, error) {
	return h.navigate(raw, false)
}

func (h *BrowserHost) navigate(raw string, allowFile bool) (string, string, error) {
	u := strings.TrimSpace(raw)
	if allowFile && strings.HasPrefix(u, "file:///") {
		// 本地交付文件（用户点击触发）
	} else if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", "", fmt.Errorf("只支持 http/https 地址，收到: %s", u)
	}
	if err := h.Start(); err != nil {
		return "", "", err
	}
	var url, title string
	if err := h.do(func(ctx context.Context) error {
		if err := chromedp.Run(ctx, chromedp.Navigate(u)); err != nil {
			return err
		}
		if err := chromedp.Run(ctx, chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
			return err
		}
		return chromedp.Run(ctx, chromedp.Location(&url), chromedp.Title(&title))
	}); err != nil {
		return "", "", err
	}
	h.setNav(url, title)
	return url, title, nil
}

// NavAction 后退/前进/刷新（面板与工具共用）。
func (h *BrowserHost) NavAction(action string) error {
	if err := h.Start(); err != nil {
		return err
	}
	var act chromedp.Action
	switch action {
	case "back":
		act = chromedp.NavigateBack()
	case "forward":
		act = chromedp.NavigateForward()
	case "reload":
		act = chromedp.Reload()
	default:
		return fmt.Errorf("未知导航动作: %s（支持 back/forward/reload）", action)
	}
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, act)
	}); err != nil {
		return err
	}
	h.readNav()
	return nil
}

func (h *BrowserHost) setNav(url, title string) {
	h.mu.Lock()
	h.url, h.title = url, title
	h.mu.Unlock()
}

func (h *BrowserHost) readNav() {
	var url, title string
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Location(&url), chromedp.Title(&title))
	}); err != nil {
		return
	}
	h.setNav(url, title)
}

// ---------- 视口 ----------

// SetViewport 设定 CSS 视口（Emulation.SetDeviceMetricsOverride）：网页形态下
// 供 AI 做响应式测试（无面板跟随，画面不展示）。桌面形态的内嵌目标是原生
// 面板（browser_panel_windows.go），视口=面板实际尺寸，不走本方法。
func (h *BrowserHost) SetViewport(w, hi int) error {
	if w == 0 || hi == 0 {
		w, hi = browserDefW, browserDefH
	}
	w = clampInt(w, browserMinSide, browserMaxW)
	hi = clampInt(hi, browserMinSide, browserMaxH)
	if err := h.Start(); err != nil {
		return err
	}
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(int64(w), int64(hi), 1.0, false))
	}); err != nil {
		return err
	}
	h.mu.Lock()
	h.vw, h.vh = w, hi
	h.mu.Unlock()
	return nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------- 输入（人机共操通道） ----------

// BrowserInputMsg 面板转发的人的输入事件；AI 工具的点击/输入最终也汇入同一批 CDP 调用。
type BrowserInputMsg struct {
	Type string `json:"type"` // down|up|move|scroll|key|text
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	DX   float64 `json:"dx"`
	DY   float64 `json:"dy"`
	Button string `json:"button"` // left|middle|right（缺省 left）
	Count int    `json:"count"`   // down/up 的连击数（1=单击 2=双击）
	Key   string `json:"key"`     // key：键名（Enter/Backspace/ArrowDown/…）
	Text  string `json:"text"`    // text：整段文本（InsertText，中文正确）
	Ctrl  bool   `json:"ctrl"`
	Alt   bool   `json:"alt"`
	Shift bool   `json:"shift"`
	Meta  bool   `json:"meta"`
}

var browserKeyMap = map[string]struct {
	code    int64
	keyName string
	codeStr string
}{
	"enter":      {13, "Enter", "Enter"},
	"backspace":  {8, "Backspace", "Backspace"},
	"tab":        {9, "Tab", "Tab"},
	"escape":     {27, "Escape", "Escape"},
	"delete":     {46, "Delete", "Delete"},
	"arrowleft":  {37, "ArrowLeft", "ArrowLeft"},
	"arrowup":    {38, "ArrowUp", "ArrowUp"},
	"arrowright": {39, "ArrowRight", "ArrowRight"},
	"arrowdown":  {40, "ArrowDown", "ArrowDown"},
	"home":       {36, "Home", "Home"},
	"end":        {35, "End", "End"},
	"pageup":     {33, "PageUp", "PageUp"},
	"pagedown":   {34, "PageDown", "PageDown"},
}

// Input 把面板输入翻译为 CDP 输入事件。坐标一律是视口 CSS px
// （前端按 显示尺寸÷视口尺寸 换算好再发）。人的明确操作允许直接拉起宿主。
func (h *BrowserHost) Input(m BrowserInputMsg) error {
	if err := h.Start(); err != nil {
		return err
	}
	if m.Type == "move" || m.Type == "down" || m.Type == "up" || m.Type == "scroll" {
		if m.X != 0 || m.Y != 0 {
			h.mu.Lock()
			h.lastX, h.lastY = m.X, m.Y
			h.mu.Unlock()
		}
	}
	return h.do(func(ctx context.Context) error {
		switch m.Type {
		case "down", "up":
			t := input.MousePressed
			if m.Type == "up" {
				t = input.MouseReleased
			}
			count := int64(m.Count)
			if count < 1 {
				count = 1
			}
			return chromedp.Run(ctx, input.DispatchMouseEvent(t, m.X, m.Y).
				WithButton(mouseButtonOf(m.Button)).WithClickCount(count))
		case "move":
			return chromedp.Run(ctx, input.DispatchMouseEvent(input.MouseMoved, m.X, m.Y).
				WithButton(input.None))
		case "scroll":
			x, y := m.X, m.Y
			if x == 0 && y == 0 {
				h.mu.Lock()
				x, y = h.lastX, h.lastY
				h.mu.Unlock()
			}
			return chromedp.Run(ctx, input.DispatchMouseEvent(input.MouseWheel, x, y).
				WithDeltaX(m.DX).WithDeltaY(m.DY))
		case "key":
			return dispatchKey(ctx, m)
		case "text":
			if m.Text == "" {
				return nil
			}
			return chromedp.Run(ctx, input.InsertText(m.Text))
		default:
			return fmt.Errorf("未知输入类型: %s", m.Type)
		}
	})
}

func mouseButtonOf(b string) input.MouseButton {
	switch b {
	case "middle":
		return input.Middle
	case "right":
		return input.Right
	default:
		return input.Left
	}
}

func dispatchKey(ctx context.Context, m BrowserInputMsg) error {
	name := strings.ToLower(strings.TrimSpace(m.Key))
	var mods input.Modifier
	if m.Alt {
		mods |= input.ModifierAlt
	}
	if m.Ctrl {
		mods |= input.ModifierCtrl
	}
	if m.Meta {
		mods |= input.ModifierMeta
	}
	if m.Shift {
		mods |= input.ModifierShift
	}
	if k, ok := browserKeyMap[name]; ok {
		p := input.DispatchKeyEvent(input.KeyRawDown).
			WithKey(k.keyName).WithCode(k.codeStr).
			WithWindowsVirtualKeyCode(k.code).WithModifiers(mods)
		if err := chromedp.Run(ctx, p); err != nil {
			return err
		}
		return chromedp.Run(ctx, input.DispatchKeyEvent(input.KeyUp).
			WithKey(k.keyName).WithCode(k.codeStr).
			WithWindowsVirtualKeyCode(k.code).WithModifiers(mods))
	}
	// 未收录的键名：单字符走 InsertText，其余拒绝（前端只送已知键名）
	if len([]rune(m.Key)) == 1 && mods == 0 {
		return chromedp.Run(ctx, input.InsertText(m.Key))
	}
	return fmt.Errorf("不支持的键: %s", m.Key)
}

// ---------- 截图 / 状态 ----------

func (h *BrowserHost) Screenshot() ([]byte, error) {
	var buf []byte
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf))
	}); err != nil {
		return nil, err
	}
	return buf, nil
}

type BrowserState struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	VW      int    `json:"viewportW"`
	VH      int    `json:"viewportH"`
}

func (h *BrowserHost) State() BrowserState {
	h.mu.Lock()
	running, vw, vh := h.tabCtx != nil, h.vw, h.vh
	url, title := h.url, h.title
	h.mu.Unlock()
	if !running {
		return BrowserState{Running: false, URL: url, Title: title, VW: vw, VH: vh}
	}
	// 运行中实时读（SPA 的 title 与内部路由变化不触发导航事件，缓存会过期）
	var u, t string
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Location(&u), chromedp.Title(&t))
	}); err == nil && u != "" {
		h.setNav(u, t)
		url, title = u, t
	}
	return BrowserState{Running: true, URL: url, Title: title, VW: vw, VH: vh}
}

// SubscribeFrames 已随 MJPEG 镜像方案移除（桌面=原生面板，网页=无面板）。

// do 在浏览器上下文里执行一段操作（统一超时；不持锁执行，锁外并发安全由 CDP 串行化）。
func (h *BrowserHost) do(action func(ctx context.Context) error) error {
	h.mu.Lock()
	ctx := h.tabCtx
	h.mu.Unlock()
	if ctx == nil {
		return fmt.Errorf("浏览器尚未打开：先调用 browser_open")
	}
	runCtx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()
	return chromedp.Run(runCtx, chromedp.ActionFunc(action))
}

// BroadcastPointer 对 chromedp 宿主是空操作：AI 位置标记只对原生面板有意义
// （面板在目标页面内注入标记元素；有头窗口里用户直接看见真实点击）。
func (h *BrowserHost) BroadcastPointer(selector string) {}

// ---------- browserTarget 操作面适配（工具层形态无关，见 browser_ops.go） ----------

func (h *BrowserHost) NavigatePrivileged(raw string) (string, string, error) {
	return h.navigate(raw, true)
}

func (h *BrowserHost) Content() (string, string, string, error) {
	var url, title, body string
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.Location(&url),
			chromedp.Title(&title),
			chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &body),
		)
	}); err != nil {
		return "", "", "", err
	}
	h.setNav(url, title)
	return url, title, body, nil
}

func (h *BrowserHost) Click(selector string) error {
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.WaitVisible(selector, chromedp.ByQueryAll),
			chromedp.Click(selector, chromedp.ByQueryAll),
			chromedp.Sleep(800*time.Millisecond),
		)
	}); err != nil {
		return fmt.Errorf("点击失败（元素不存在或不可见）: %w", err)
	}
	return nil
}

func (h *BrowserHost) TypeInto(selector, text string, clear bool) error {
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQueryAll))
	}); err != nil {
		return fmt.Errorf("输入失败（元素不存在或不可见）: %w", err)
	}
	if clear {
		// Clear 对少数控件（contenteditable 等）会失败，尽力而为
		_ = h.do(func(ctx context.Context) error {
			return chromedp.Run(ctx, chromedp.Clear(selector, chromedp.ByQueryAll))
		})
	}
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.SendKeys(selector, text, chromedp.ByQueryAll))
	}); err != nil {
		return fmt.Errorf("输入失败: %w", err)
	}
	return nil
}

func (h *BrowserHost) Scroll(direction string, amount int) error {
	if amount <= 0 {
		amount = 5
	}
	dy := float64(amount) * 100
	if strings.EqualFold(strings.TrimSpace(direction), "up") {
		dy = -dy
	}
	return h.Input(BrowserInputMsg{Type: "scroll", DY: dy})
}

func (h *BrowserHost) Evaluate(js string) (any, error) {
	var res any
	if err := h.do(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Evaluate(js, &res))
	}); err != nil {
		return nil, err
	}
	return res, nil
}

func (h *BrowserHost) Where() string {
	if h.headless {
		return "内嵌浏览器（后台无头）"
	}
	return "独立浏览器窗口"
}

// ---------- HTTP handlers ----------

func (a *App) handleBrowserNavigate(w http.ResponseWriter, r *http.Request) {
	var p struct {
		URL    string `json:"url"`
		Action string `json:"action"` // back|forward|reload
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	h := a.browserFor("") // 面板地址栏也是用户发起：内嵌形态随之成为工具作用目标
	if p.Action != "" {
		if err := h.NavAction(p.Action); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		st := h.State()
		writeJSON(w, http.StatusOK, st)
		return
	}
	url, title, err := h.Navigate(p.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, BrowserState{Running: true, URL: url, Title: title})
}

func (a *App) handleBrowserViewport(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Width  int  `json:"width"`
		Height int  `json:"height"`
		Reset  bool `json:"reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	if p.Reset {
		p.Width, p.Height = 0, 0
	}
	h := a.embeddedTarget()
	if err := h.SetViewport(p.Width, p.Height); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.State())
}

func (a *App) handleBrowserState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.embeddedTarget().State())
}

// handleBrowserPanelRect 桌面右栏原生面板的矩形同步端点：前端把网页占位区
// 的矩形（CSS px）与 devicePixelRatio 上报，宿主换算物理像素后移动 WebView2
// 子窗口。网页模式没有原生面板，忽略。
func (a *App) handleBrowserPanelRect(w http.ResponseWriter, r *http.Request) {
	var p struct {
		X       float64 `json:"x"`
		Y       float64 `json:"y"`
		W       float64 `json:"w"`
		H       float64 `json:"h"`
		Dpr     float64 `json:"dpr"`
		Visible bool    `json:"visible"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	ps, ok := a.nativePanel.(interface {
		SetRect(x, y, w, h int, dpr float64, visible bool) error
	})
	if !ok || ps == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	dpr := p.Dpr
	if dpr <= 0 {
		dpr = 1
	}
	if err := ps.SetRect(
		int(p.X*dpr+0.5), int(p.Y*dpr+0.5), int(p.W*dpr+0.5), int(p.H*dpr+0.5),
		dpr, p.Visible); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// fileURLFromAbs 把本地绝对路径拼成合法 file:// URL（逐段转义，保留 / 分隔）。
func fileURLFromAbs(abs string) string {
	segs := strings.Split(strings.TrimPrefix(filepath.ToSlash(abs), "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "file:///" + strings.Join(segs, "/")
}

// posixInside 判断 abs 是否落在 root 内（远程工作区是 posix 路径；path 包
// 没有 Rel，按 / 规整后做目录段前缀比较）。
func posixInside(root, abs string) (string, bool) {
	r := path.Clean(strings.ReplaceAll(root, "\\", "/"))
	a := path.Clean(strings.ReplaceAll(abs, "\\", "/"))
	if a == r {
		return ".", true
	}
	if !strings.HasPrefix(a, r+"/") {
		return "", false
	}
	return strings.TrimPrefix(a, r+"/"), true
}

// workspaceFileForView 校验 rel 是 sid 会话工作区内已存在的文件（聊天里
// file-link 的点击预览与交付卡「打开」共用）：用户主动点击触发，不经模型
// 权限门控；边界 = 工作区包含检查 + 存在性检查（交付登记文件必然在工作区
// 内，天然覆盖）。返回会话与解析后的绝对路径（远程会话 = 远程侧绝对路径）。
func (a *App) workspaceFileForView(sid, rel string) (m *SessionMeta, abs string, ok bool) {
	rel = strings.TrimSpace(rel)
	m, err := a.Store.Get(sid)
	if err != nil || m == nil {
		return nil, "", false
	}
	tctx := &toolContext{app: a, sess: m}
	abs, err = tctx.resolve(rel)
	if err != nil {
		return nil, "", false
	}
	if !insideWorkspace(tctx, abs) {
		return nil, "", false
	}
	if isDir, _, serr := tctx.envStat(abs); serr != nil || isDir {
		return nil, "", false
	}
	return m, abs, true
}

// insideWorkspace 判定 abs（已解析的绝对路径）是否在会话工作区内。Windows 卷
// 不区分大小写：小写后做 Rel 包含判定，防 d:\ 与 D:\ 误判越界；远程会话按
// POSIX 口径（posixInside）。open-view 与右键菜单的 open-path/reveal-path 共用。
func insideWorkspace(tctx *toolContext, abs string) bool {
	root, err := tctx.resolve("")
	if err != nil {
		return false
	}
	if tctx.sess.Remote != nil {
		_, inside := posixInside(root, abs)
		return inside
	}
	sub, rerr := filepath.Rel(strings.ToLower(root), strings.ToLower(abs))
	if rerr != nil || sub == ".." || strings.HasPrefix(sub, ".."+string(filepath.Separator)) || filepath.IsAbs(sub) {
		return false
	}
	return true
}

// handleBrowserOpenView 聊天链接点开的统一入口（用户主动操作，不经模型权限门控）：
//   * url：http/https 直接在内嵌实例打开；
//   * path：会话工作区内已存在的文件——本地工作区转 file://（同目录相对
//     资源可用），SSH 远程工作区转 /api/browser/view 流式地址。
// 一律经 browserFor("") 打开：内嵌实例成为工具作用目标（active）。不注入
// 任何对话回报（用户打开什么不经模型；需要看画面时用户会说，模型用
// panel_screenshot 截右侧栏）。
func (a *App) handleBrowserOpenView(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Session string `json:"session"`
		URL     string `json:"url"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	viewedPath := strings.TrimSpace(p.Path)
	var target string
	var m *SessionMeta
	if u := strings.TrimSpace(p.URL); u != "" {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeErr(w, http.StatusBadRequest, "只支持 http/https 地址，收到: "+u)
			return
		}
		target = u
	} else if viewedPath != "" {
		sid := strings.TrimSpace(p.Session)
		if sid == "" {
			writeErr(w, http.StatusBadRequest, "打开工作区文件需要 session")
			return
		}
		var abs string
		var ok bool
		if m, abs, ok = a.workspaceFileForView(sid, viewedPath); !ok {
			writeErr(w, http.StatusNotFound, "文件不存在或不在会话工作区内: "+viewedPath)
			return
		}
		if m.Remote != nil {
			// 远程工作区：本机无此文件，走流式渲染端点（相对资源不加载，已知限制）
			q := url.Values{}
			q.Set("sid", m.ID)
			q.Set("path", viewedPath)
			target = "http://" + r.Host + "/api/browser/view?" + q.Encode()
		} else {
			target = fileURLFromAbs(abs)
		}
	} else {
		writeErr(w, http.StatusBadRequest, "需要 url 或 path")
		return
	}
	h := a.browserFor("") // 用户发起的打开：内嵌形态成为工具作用目标
	if _, _, err := h.NavigatePrivileged(target); err != nil {
		writeErr(w, http.StatusBadGateway, "打开失败: "+err.Error())
		return
	}
	// 不再注入【网页查看】回报：对话里不显示、模型也不看（用户口径：打开
	// 什么是个人的事，需要时用户会自己说）。模型要看画面走 panel_screenshot
	// 工具（对右侧栏截图，浏览器页/文件查看页通用），见 panelshot_windows.go。
	writeJSON(w, http.StatusOK, map[string]string{"url": target})
}

// handleBrowserView 远程（SSH/SFTP）会话工作区文件的流式渲染，仅供内嵌
// 浏览器经 open-view 转址访问；边界与 open-view 一致（会话工作区内已存在文件）。
func (a *App) handleBrowserView(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSpace(r.URL.Query().Get("sid"))
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if sid == "" || rel == "" {
		writeErr(w, http.StatusBadRequest, "需要 sid 与 path")
		return
	}
	m, abs, ok := a.workspaceFileForView(sid, rel)
	if !ok {
		writeErr(w, http.StatusNotFound, "文件不存在或不在会话工作区内: "+rel)
		return
	}
	tctx := &toolContext{app: a, sess: m}
	data, err := tctx.envReadFile(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
