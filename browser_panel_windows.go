//go:build windows

package main

// 桌面右栏原生浏览器面板：在桌面壳窗口（desktop_windows.go 的主 WebView2）
// 上叠一个 WS_CHILD 子窗口，内嵌第二个 WebView2 控件——用户得到的是真正的
// Edge 渲染与交互（原生滚动、输入法、右键菜单、下载），替换旧「无头 Edge +
// MJPEG 截图流镜像」方案（browser_host.go 原实现，人机交互体验与原生差距
// 无法靠补丁弥合）。AI 的 browser_* 工具驱动同一控件：人机共操天然成立。
//
// AI 驱动通道：控件级官方 CDP 接口 ICoreWebView2.CallDevToolsProtocolMethod
// （evaluate/输入注入/截图全部可表达）。不走 --remote-debugging-port——新版
// WebView2（Chromium 136 起）已封禁该参数，chromedp 直连不可靠。
//
// 布局同步：右栏地址栏工具栏由主界面渲染（HTML），网页区域是占位 div；前端
// 把占位矩形（CSS px × devicePixelRatio = 物理像素）POST /api/browser/panel-rect，
// 宿主在 UI 线程移动子窗口并 PutBounds。HTML 元素无法浮于原生子窗口之上，
// 弹层需自行避让该矩形。
//
// 线程模型：所有 COM 调用必须发生在创建控件的 UI 线程（STA）——HTTP 侧一律
// 经桌面壳 WebView.Dispatch 投递，结果经 channel 回到调用方 goroutine。
// 生命周期用 createMu 串行化（创建/关闭），数据字段用 stateMu 短临界区——
// UI 回调（导航完成等）只碰 stateMu，绝不反向拿 createMu，防泵内死锁。
//
// 绑定库边界：jchv/go-webview2 的 pkg/edge 只导出了少量包装（Navigate/
// Show/Hide/GetController…），webview 对象与 CDP/事件面未导出。COM 对象布局
// 是稳定 ABI（首字段即 vtbl 指针），这里用同构 vtbl 镜像结构直接调用，
// 并自制事件回调对象（windows.NewCallback，仿库内 comproc 模式）。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	edge "github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

const (
	wsChild       = 0x40000000
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
	swHide        = 0
	swShow        = 5
)

var (
	panelUser32              = windows.NewLazySystemDLL("user32.dll")
	procPanelCreateWindow    = panelUser32.NewProc("CreateWindowExW")
	procPanelDestroyWindow   = panelUser32.NewProc("DestroyWindow")
	procPanelSetWindowPos    = panelUser32.NewProc("SetWindowPos")
	procPanelShowWindow      = panelUser32.NewProc("ShowWindow")
	panelKernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procPanelGetModuleHandle = panelKernel32.NewProc("GetModuleHandleW")
)

// ---------- COM vtbl 镜像（字段顺序 = Microsoft COM ABI，不可改动顺序） ----------

// wvRawVtbl 镜像 edge.iCoreWebView2Vtbl（ICoreWebView2）。
type wvRawVtbl struct {
	queryInterface, addRef, release                                                        edge.ComProc
	getSettings, getSource, navigate, navigateToString                                     edge.ComProc
	addNavigationStarting, removeNavigationStarting                                        edge.ComProc
	addContentLoading, removeContentLoading                                                edge.ComProc
	addSourceChanged, removeSourceChanged                                                  edge.ComProc
	addHistoryChanged, removeHistoryChanged                                                edge.ComProc
	addNavigationCompleted, removeNavigationCompleted                                      edge.ComProc
	addFrameNavigationStarting, removeFrameNavigationStarting                              edge.ComProc
	addFrameNavigationCompleted, removeFrameNavigationCompleted                            edge.ComProc
	addScriptDialogOpening, removeScriptDialogOpening                                      edge.ComProc
	addPermissionRequested, removePermissionRequested                                      edge.ComProc
	addProcessFailed, removeProcessFailed                                                  edge.ComProc
	addScriptToExecuteOnDocumentCreated, removeScriptToExecuteOnDocumentCreated            edge.ComProc
	executeScript, capturePreview, reload                                                  edge.ComProc
	postWebMessageAsJSON, postWebMessageAsString                                           edge.ComProc
	addWebMessageReceived, removeWebMessageReceived                                        edge.ComProc
	callDevToolsProtocolMethod, getBrowserProcessID, getCanGoBack, getCanGoForward         edge.ComProc
	goBack, goForward, getDevToolsProtocolEventReceiver, stop                              edge.ComProc
	addNewWindowRequested, removeNewWindowRequested                                        edge.ComProc
	addDocumentTitleChanged, removeDocumentTitleChanged, getDocumentTitle                   edge.ComProc
	addHostObjectToScript, removeHostObjectFromScript, openDevToolsWindow                  edge.ComProc
	addContainsFullScreenElementChanged, removeContainsFullScreenElementChanged             edge.ComProc
	getContainsFullScreenElement                                                           edge.ComProc
	addWebResourceRequested, removeWebResourceRequested                                    edge.ComProc
	addWebResourceRequestedFilter, removeWebResourceRequestedFilter                        edge.ComProc
	addWindowCloseRequested, removeWindowCloseRequested                                    edge.ComProc
}

// ctlRawVtbl 镜像 edge._ICoreWebView2ControllerVtbl。
type ctlRawVtbl struct {
	queryInterface, addRef, release                     edge.ComProc
	getIsVisible, putIsVisible, getBounds, putBounds    edge.ComProc
	getZoomFactor, putZoomFactor                        edge.ComProc
	addZoomFactorChanged, removeZoomFactorChanged       edge.ComProc
	setBoundsAndZoomFactor, moveFocus                   edge.ComProc
	addMoveFocusRequested, removeMoveFocusRequested     edge.ComProc
	addGotFocus, removeGotFocus                         edge.ComProc
	addLostFocus, removeLostFocus                       edge.ComProc
	addAcceleratorKeyPressed, removeAcceleratorKeyPressed edge.ComProc
	getParentWindow, putParentWindow                    edge.ComProc
	notifyParentWindowPositionChanged, close            edge.ComProc
	getCoreWebView2                                     edge.ComProc
}

// nwrArgsVtbl 镜像 ICoreWebView2NewWindowRequestedEventArgs（未随绑定库导出）。
type nwrArgsVtbl struct {
	queryInterface, addRef, release edge.ComProc
	getUri, getNewWindow            edge.ComProc
	putHandled, getHandled          edge.ComProc
	getDeferral, getWindowFeatures  edge.ComProc
}

// panelRect 即 COREWEBVIEW2_PHYSICAL_BOUNDS（tagRECT 布局）。
type panelRect struct{ Left, Top, Right, Bottom int32 }

func wvVtblOf(wv *edge.ICoreWebView2) *wvRawVtbl {
	return *(**wvRawVtbl)(unsafe.Pointer(wv))
}

func ctlVtblOf(ctl *edge.ICoreWebView2Controller) *ctlRawVtbl {
	return *(**ctlRawVtbl)(unsafe.Pointer(ctl))
}

// ---------- 自制 COM 回调对象（仿库内 comproc 模式） ----------

var (
	comQIProc  = windows.NewCallback(func(this, iid, obj uintptr) uintptr { return 0 })
	comRefProc = windows.NewCallback(func(this uintptr) uintptr { return 1 })
)

// evtHandler 通用双参事件回调（sender/args 均为 COM 接口指针）。
type evtHandler struct {
	vtbl *evtHandlerVtbl
	fn   func(sender, args unsafe.Pointer)
}

type evtHandlerVtbl struct{ queryInterface, addRef, release, invoke uintptr }

var evtHandlerVtblInst = evtHandlerVtbl{
	comQIProc, comRefProc, comRefProc,
	windows.NewCallback(func(this *evtHandler, sender, args unsafe.Pointer) uintptr {
		if this != nil && this.fn != nil {
			this.fn(sender, args)
		}
		return 0
	}),
}

func newEvtHandler(fn func(sender, args unsafe.Pointer)) *evtHandler {
	return &evtHandler{vtbl: &evtHandlerVtblInst, fn: fn}
}

type cdpResult struct {
	hr   int32
	json string
}

// cdpCompletedHandler 是 ICoreWebView2CallDevToolsProtocolMethodCompletedHandler
// 的 Go 实现（绑定库未导出该回调接口）。returnObjectAsJson 字符串仅在回调
// 期间有效，复制后立即回传。
type cdpCompletedHandler struct {
	vtbl *cdpCompletedVtbl
	ch   chan cdpResult
}

type cdpCompletedVtbl struct{ queryInterface, addRef, release, invoke uintptr }

var cdpCompletedVtblInst = cdpCompletedVtbl{
	comQIProc, comRefProc, comRefProc,
	windows.NewCallback(func(this *cdpCompletedHandler, errCode uintptr, resultJSON *uint16) uintptr {
		if this == nil || this.ch == nil {
			return 0
		}
		r := cdpResult{hr: int32(uint32(errCode))}
		if resultJSON != nil {
			r.json = copyWVString(resultJSON)
		}
		select {
		case this.ch <- r:
		default:
		}
		return 0
	}),
}

// ---------- 字符串工具 ----------

// copyWVString 复制 WebView2 给出的 UTF-16 字符串（不释放，调用方内存）。
func copyWVString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), 2*n)) != 0 {
		n++
		if n > 1<<20 { // 保险丝：异常指针不至于死循环
			break
		}
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}

// readWVString 复制并释放调用 get_* 方法取回的出参字符串（CoTaskMem 分配）。
func readWVString(p *uint16) string {
	s := copyWVString(p)
	if p != nil {
		windows.CoTaskMemFree(unsafe.Pointer(p))
	}
	return s
}

func wvGetSource(wv *edge.ICoreWebView2) string {
	var p *uint16
	_, _, _ = wvVtblOf(wv).getSource.Call(uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(&p)))
	return readWVString(p)
}

func wvGetDocumentTitle(wv *edge.ICoreWebView2) string {
	var p *uint16
	_, _, _ = wvVtblOf(wv).getDocumentTitle.Call(uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(&p)))
	return readWVString(p)
}

// ctlWebView 经控制器 vtbl 取 CoreWebView2（绑定库未导出该包装）。
func ctlWebView(ctl *edge.ICoreWebView2Controller) *edge.ICoreWebView2 {
	var wv *edge.ICoreWebView2
	_, _, _ = ctlVtblOf(ctl).getCoreWebView2.Call(uintptr(unsafe.Pointer(ctl)), uintptr(unsafe.Pointer(&wv)))
	return wv
}

// ---------- 面板宿主 ----------

type browserPanel struct {
	app      *App
	dispatch func(f func()) // 桌面壳 WebView.Dispatch：投递到 UI 线程
	mainWnd  uintptr        // 桌面壳主窗口 HWND

	createMu  sync.Mutex // 生命周期串行化（创建/关闭），不跨 UI 等待持锁于 UI 线程
	createErr error

	stateMu sync.Mutex // 数据锁：短临界区，UI 回调也仅持此锁
	started bool
	chrome  *edge.Chromium
	ctl     *edge.ICoreWebView2Controller
	wv      *edge.ICoreWebView2
	hwnd    uintptr
	visible bool
	rectX, rectY, rectW, rectH int // 物理像素（前端 CSS px × dpr）
	dpr     float64
	cssW, cssH int                 // 面板视口的 CSS px 估算（物理 ÷ dpr）
	url, title string
	navCh   chan struct{} // 最近一次导航的完成信号（NavigationCompleted 关闭）

	newWinHandler *evtHandler // 引用保活：COM 侧只存裸指针，Go 侧必须持有
	titleHandler  *evtHandler
}

func newBrowserPanel(app *App, dispatch func(f func()), mainWnd uintptr) browserTarget {
	return &browserPanel{app: app, dispatch: dispatch, mainWnd: mainWnd}
}

// ensureStarted 惰性创建面板控件（首个矩形同步或首次导航触发）。创建在
// UI 线程执行（Embed 内部靠消息泵等环境就绪），HTTP 侧等 channel。
func (p *browserPanel) ensureStarted() error {
	p.createMu.Lock()
	defer p.createMu.Unlock()
	p.stateMu.Lock()
	started, cerr := p.started, p.createErr
	p.stateMu.Unlock()
	if started {
		return nil
	}
	if cerr != nil {
		return cerr
	}
	done := make(chan error, 1)
	p.dispatch(func() { done <- p.createOnUI() })
	select {
	case err := <-done:
		if err != nil {
			p.stateMu.Lock()
			p.createErr = err
			p.stateMu.Unlock()
			return fmt.Errorf("浏览器面板创建失败: %w", err)
		}
		p.stateMu.Lock()
		p.started = true
		p.stateMu.Unlock()
		return nil
	case <-time.After(30 * time.Second):
		err := fmt.Errorf("浏览器面板创建超时")
		p.stateMu.Lock()
		p.createErr = err
		p.stateMu.Unlock()
		return err
	}
}

// createOnUI 在 UI 线程创建子窗口与第二个 WebView2 控件（独立环境与
// profile：data/webview-browser，与主界面 data/webview 隔离，同浏览器模型
// 于旧方案的 data/browser-profile）。
func (p *browserPanel) createOnUI() error {
	p.stateMu.Lock()
	x, y, w, h := p.rectX, p.rectY, p.rectW, p.rectH
	p.stateMu.Unlock()
	if w < 64 {
		w, x = 64, 0
	}
	if h < 64 {
		h, y = 64, 0
	}
	cls, _ := windows.UTF16PtrFromString("Static") // 系统控件类做中性容器
	name, _ := windows.UTF16PtrFromString("JXBrowserPanel")
	hInst, _, _ := procPanelGetModuleHandle.Call(0)
	hwnd, _, _ := procPanelCreateWindow.Call(
		0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)), wsChild,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		p.mainWnd, 0, hInst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW 失败")
	}
	ch := edge.NewChromium()
	ch.DataPath = filepath.Join(p.app.Cfg.DataDir, "webview-browser")
	ch.NavigationCompletedCallback = func(*edge.ICoreWebView2, *edge.ICoreWebView2NavigationCompletedEventArgs) {
		p.onNavCompleted()
	}
	if !ch.Embed(hwnd) { // 内部消息泵等环境/控制器就绪
		_, _, _ = procPanelDestroyWindow.Call(hwnd)
		return fmt.Errorf("WebView2 控件初始化失败")
	}
	ctl := ch.GetController()
	if ctl == nil {
		return fmt.Errorf("WebView2 控件未就绪")
	}
	wv := ctlWebView(ctl)
	if wv == nil {
		return fmt.Errorf("CoreWebView2 不可用")
	}
	var token int64
	nwh := newEvtHandler(func(sender, args unsafe.Pointer) { p.onNewWindow(args) })
	_, _, _ = wvVtblOf(wv).addNewWindowRequested.Call(
		uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(nwh)), uintptr(unsafe.Pointer(&token)))
	th := newEvtHandler(func(sender, args unsafe.Pointer) { p.onTitleChanged() })
	_, _, _ = wvVtblOf(wv).addDocumentTitleChanged.Call(
		uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(th)), uintptr(unsafe.Pointer(&token)))
	p.stateMu.Lock()
	p.chrome, p.ctl, p.wv, p.hwnd = ch, ctl, wv, hwnd
	p.newWinHandler, p.titleHandler = nwh, th
	p.stateMu.Unlock()
	p.applyRectOnUI()
	return nil
}

// applyRectOnUI 把缓存的矩形应用到子窗口与控件（仅 UI 线程调用）。
// 收起≠停渲染：visible=false 时把控件挪到屏外并保持 IsVisible=true——
// 实测 IsVisible=false 会让合成器停帧，Page.captureScreenshot 挂起；屏外
// +可见仍出帧，用户收起右栏后 AI 的 browser_screenshot 照常可用，且视口
// 保持最后一次矩形，截图视野一致。
func (p *browserPanel) applyRectOnUI() {
	p.stateMu.Lock()
	x, y, w, h, visible := p.rectX, p.rectY, p.rectW, p.rectH, p.visible
	ctl, hwnd := p.ctl, p.hwnd
	p.stateMu.Unlock()
	if ctl == nil || hwnd == 0 {
		return
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if w < 8 || h < 8 {
		// 尚无有效矩形（创建后首个矩形未到）：保持真隐藏
		_, _, _ = procPanelShowWindow.Call(hwnd, swHide)
		_ = ctl.PutIsVisible(false)
	} else if visible {
		_, _, _ = procPanelSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
			swpNoZOrder|swpNoActivate)
		_, _, _ = procPanelShowWindow.Call(hwnd, swShow)
		r := panelRect{0, 0, int32(w), int32(h)}
		_, _, _ = ctlVtblOf(ctl).putBounds.Call(uintptr(unsafe.Pointer(ctl)), uintptr(unsafe.Pointer(&r)))
		_ = ctl.PutIsVisible(true)
	} else {
		_, _, _ = procPanelSetWindowPos.Call(hwnd, 0, uintptr(^uintptr(31999)), uintptr(^uintptr(31999)),
			uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
		_, _, _ = procPanelShowWindow.Call(hwnd, swShow)
		r := panelRect{0, 0, int32(w), int32(h)}
		_, _, _ = ctlVtblOf(ctl).putBounds.Call(uintptr(unsafe.Pointer(ctl)), uintptr(unsafe.Pointer(&r)))
		_ = ctl.PutIsVisible(true)
	}
	_ = ctl.NotifyParentWindowPositionChanged()
}

// SetRect 前端矩形同步入口（物理像素）；visible=true 时确保面板已创建。
func (p *browserPanel) SetRect(x, y, w, h int, dpr float64, visible bool) error {
	if dpr <= 0 {
		dpr = 1
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if visible && (w < 8 || h < 8) {
		visible = false // 布局未就绪/极小：按隐藏处理，别用脏矩形
	}
	if visible {
		if err := p.ensureStarted(); err != nil {
			return err
		}
	}
	p.stateMu.Lock()
	p.rectX, p.rectY, p.rectW, p.rectH = x, y, w, h
	p.dpr, p.visible = dpr, visible
	if visible {
		p.cssW = clampInt(int(float64(w)/dpr+0.5), 1, 65535)
		p.cssH = clampInt(int(float64(h)/dpr+0.5), 1, 65535)
	}
	created := p.ctl != nil
	p.stateMu.Unlock()
	if !created {
		return nil
	}
	p.dispatch(p.applyRectOnUI)
	return nil
}

// ---------- 导航 ----------

func (p *browserPanel) Navigate(raw string) (string, string, error) {
	return p.navigate(raw, false)
}

func (p *browserPanel) NavigatePrivileged(raw string) (string, string, error) {
	return p.navigate(raw, true)
}

func (p *browserPanel) navigate(raw string, allowFile bool) (string, string, error) {
	u := strings.TrimSpace(raw)
	ok := strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
	if !ok && allowFile && strings.HasPrefix(u, "file:///") {
		ok = true
	}
	if !ok {
		return "", "", fmt.Errorf("只支持 http/https 地址，收到: %s", u)
	}
	if err := p.ensureStarted(); err != nil {
		return "", "", err
	}
	p.stateMu.Lock()
	if p.navCh != nil {
		close(p.navCh) // 旧等待者放行（读到中间态）
	}
	p.navCh = make(chan struct{})
	ch := p.navCh
	wv := p.wv
	p.stateMu.Unlock()
	p.dispatch(func() {
		if wv == nil {
			return
		}
		u16, _ := windows.UTF16PtrFromString(u)
		_, _, _ = wvVtblOf(wv).navigate.Call(uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(u16)))
	})
	select {
	case <-ch:
	case <-time.After(browserTimeout):
		return "", "", fmt.Errorf("导航超时（60 秒）: %s", u)
	}
	p.stateMu.Lock()
	url, title := p.url, p.title
	p.stateMu.Unlock()
	return url, title, nil
}

func (p *browserPanel) NavAction(action string) error {
	if err := p.ensureStarted(); err != nil {
		return err
	}
	p.stateMu.Lock()
	wv := p.wv
	if p.navCh != nil {
		close(p.navCh)
	}
	p.navCh = make(chan struct{})
	ch := p.navCh
	p.stateMu.Unlock()
	if wv == nil {
		return fmt.Errorf("浏览器面板尚未就绪")
	}
	p.dispatch(func() {
		v := wvVtblOf(wv)
		switch action {
		case "back":
			_, _, _ = v.goBack.Call(uintptr(unsafe.Pointer(wv)))
		case "forward":
			_, _, _ = v.goForward.Call(uintptr(unsafe.Pointer(wv)))
		case "reload":
			_, _, _ = v.reload.Call(uintptr(unsafe.Pointer(wv)))
		}
	})
	// 无历史可跳时不会触发 NavigationCompleted：短等即读当前态，不报错
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
	}
	url, title := p.uiReadNav()
	p.stateMu.Lock()
	p.url, p.title = url, title
	p.stateMu.Unlock()
	return nil
}

// onNavCompleted NavigationCompleted（UI 线程）：读回地址标题、广播、放行等待者。
func (p *browserPanel) onNavCompleted() {
	p.stateMu.Lock()
	wv, ch := p.wv, p.navCh
	p.navCh = nil
	p.stateMu.Unlock()
	if wv == nil {
		return
	}
	u := wvGetSource(wv)
	t := wvGetDocumentTitle(wv)
	p.stateMu.Lock()
	p.url, p.title = u, t
	p.stateMu.Unlock()
	p.app.broadcast(Event{Type: "browser_nav", URL: u, Title: t})
	if ch != nil {
		close(ch)
	}
	// 标题常在完成后一拍才就绪（SPA 渐进渲染）：稍后补读一次
	time.AfterFunc(600*time.Millisecond, func() { p.dispatch(p.onTitleChanged) })
}

func (p *browserPanel) onTitleChanged() {
	p.stateMu.Lock()
	wv, old := p.wv, p.title
	p.stateMu.Unlock()
	if wv == nil {
		return
	}
	t := wvGetDocumentTitle(wv)
	if t == old {
		return
	}
	p.stateMu.Lock()
	p.title = t
	u := p.url
	p.stateMu.Unlock()
	p.app.broadcast(Event{Type: "browser_nav", URL: u, Title: t})
}

// onNewWindow 弹窗请求（target=_blank / window.open）：原地导航，不弹新窗。
func (p *browserPanel) onNewWindow(args unsafe.Pointer) {
	v := *(**nwrArgsVtbl)(args)
	h := uintptr(args)
	var uri *uint16
	_, _, _ = v.getUri.Call(h, uintptr(unsafe.Pointer(&uri)))
	u := readWVString(uri)
	if u != "" && u != "about:blank" {
		p.stateMu.Lock()
		wv := p.wv
		p.stateMu.Unlock()
		if wv != nil {
			u16, _ := windows.UTF16PtrFromString(u)
			_, _, _ = wvVtblOf(wv).navigate.Call(uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(u16)))
		}
	}
	_, _, _ = v.putHandled.Call(h, 1)
}

// ---------- CDP 通道 ----------

// callCDP 在面板控件上调用 CDP 方法并等待结果（官方 DevTools 协议入口）。
func (p *browserPanel) callCDP(method string, params map[string]any) (json.RawMessage, error) {
	paramsJSON := "{}"
	if params != nil {
		if b, err := json.Marshal(params); err == nil {
			paramsJSON = string(b)
		}
	}
	p.stateMu.Lock()
	wv := p.wv
	p.stateMu.Unlock()
	if wv == nil {
		return nil, fmt.Errorf("浏览器面板尚未就绪：先 browser_open")
	}
	m16, err1 := windows.UTF16PtrFromString(method)
	pj16, err2 := windows.UTF16PtrFromString(paramsJSON)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("CDP 参数编码失败")
	}
	res := make(chan cdpResult, 1)
	h := &cdpCompletedHandler{vtbl: &cdpCompletedVtblInst, ch: res}
	p.dispatch(func() {
		_, _, _ = wvVtblOf(wv).callDevToolsProtocolMethod.Call(
			uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(m16)), uintptr(unsafe.Pointer(pj16)),
			uintptr(unsafe.Pointer(h)))
	})
	select {
	case r := <-res:
		if r.hr != 0 {
			return nil, fmt.Errorf("CDP %s 调用失败: 0x%08x", method, uint32(r.hr))
		}
		return json.RawMessage(r.json), nil
	case <-time.After(browserTimeout):
		return nil, fmt.Errorf("CDP %s 调用超时", method)
	}
}

func (p *browserPanel) Evaluate(js string) (any, error) {
	raw, err := p.callCDP("Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true})
	if err != nil {
		return nil, err
	}
	// returnObjectAsJson 即 CDP 命令结果本体：Runtime.evaluate →
	// {"result":{"type":"string","value":…},"exceptionDetails":{…}?}
	var out struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text        string `json:"text"`
			Description string `json:"description"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("evaluate 结果解析失败: %w", err)
	}
	if e := out.ExceptionDetails; e != nil {
		msg := e.Description
		if msg == "" {
			msg = e.Text
		}
		return nil, fmt.Errorf("页面脚本异常: %s", msg)
	}
	var v any
	if len(out.Result.Value) > 0 {
		_ = json.Unmarshal(out.Result.Value, &v)
	}
	return v, nil
}

// ---------- browserTarget 操作面 ----------

func (p *browserPanel) Content() (string, string, string, error) {
	v, err := p.Evaluate(`document.body ? document.body.innerText : ""`)
	if err != nil {
		return "", "", "", err
	}
	body, _ := v.(string)
	u, t := p.uiReadNav()
	p.stateMu.Lock()
	p.url, p.title = u, t
	p.stateMu.Unlock()
	return u, t, body, nil
}

// evalPoint 取选择器中心坐标（元素不存在或不可见时报错）。
func (p *browserPanel) evalPoint(selector string) (float64, float64, error) {
	sel, _ := json.Marshal(selector)
	js := `(function(){var el=document.querySelector(` + string(sel) + `);if(!el)return null;` +
		`var r=el.getBoundingClientRect();if(r.width<1||r.height<1)return null;` +
		`return [r.x+r.width/2, r.y+r.height/2];})()`
	v, err := p.Evaluate(js)
	if err != nil {
		return 0, 0, err
	}
	arr, ok := v.([]any)
	if !ok || len(arr) != 2 {
		return 0, 0, fmt.Errorf("元素不存在或不可见: %s", selector)
	}
	x, ok1 := arr[0].(float64)
	y, ok2 := arr[1].(float64)
	if !ok1 || !ok2 {
		return 0, 0, fmt.Errorf("元素位置异常: %s", selector)
	}
	return x, y, nil
}

func (p *browserPanel) waitVisible(selector string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, _, err := p.evalPoint(selector); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	if _, _, err := p.evalPoint(selector); err == nil {
		return nil
	}
	if lastErr != nil && strings.Contains(lastErr.Error(), "元素不存在") {
		return fmt.Errorf("元素不存在或不可见: %s", selector)
	}
	return fmt.Errorf("等待元素超时: %s", selector)
}

func (p *browserPanel) mouseAt(t string, x, y float64, extra map[string]any) error {
	params := map[string]any{"type": t, "x": x, "y": y}
	for k, v := range extra {
		params[k] = v
	}
	_, err := p.callCDP("Input.dispatchMouseEvent", params)
	return err
}

func (p *browserPanel) Click(selector string) error {
	if err := p.waitVisible(selector, 10*time.Second); err != nil {
		return err
	}
	x, y, err := p.evalPoint(selector)
	if err != nil {
		return err
	}
	p.showPointerAt(x, y)
	if err := p.mouseAt("mousePressed", x, y, map[string]any{"button": "left", "clickCount": 1}); err != nil {
		return err
	}
	if err := p.mouseAt("mouseReleased", x, y, map[string]any{"button": "left", "clickCount": 1}); err != nil {
		return err
	}
	time.Sleep(800 * time.Millisecond)
	return nil
}

func (p *browserPanel) TypeInto(selector, text string, clear bool) error {
	if err := p.waitVisible(selector, 10*time.Second); err != nil {
		return err
	}
	x, y, err := p.evalPoint(selector)
	if err != nil {
		return err
	}
	p.showPointerAt(x, y)
	// 点击聚焦：走真实鼠标事件路径，focus/blur 行为与用户操作一致
	if err := p.mouseAt("mousePressed", x, y, map[string]any{"button": "left", "clickCount": 1}); err != nil {
		return err
	}
	if err := p.mouseAt("mouseReleased", x, y, map[string]any{"button": "left", "clickCount": 1}); err != nil {
		return err
	}
	if clear {
		sel, _ := json.Marshal(selector)
		js := `(function(){var el=document.querySelector(` + string(sel) + `);if(!el)return;` +
			`if(el.value!==undefined){el.value='';el.dispatchEvent(new Event('input',{bubbles:true}));}` +
			`else{el.textContent='';}})()`
		if _, err := p.Evaluate(js); err != nil {
			return fmt.Errorf("清空输入框失败: %w", err)
		}
	}
	if text == "" {
		return nil
	}
	// 整段注入（Input.insertText）：中文/输入法的正确路径
	if _, err := p.callCDP("Input.insertText", map[string]any{"text": text}); err != nil {
		return fmt.Errorf("输入失败: %w", err)
	}
	return nil
}

func (p *browserPanel) Scroll(direction string, amount int) error {
	if amount <= 0 {
		amount = 5
	}
	dy := float64(amount) * 100
	if strings.EqualFold(strings.TrimSpace(direction), "up") {
		dy = -dy
	}
	p.stateMu.Lock()
	x := float64(p.cssW) / 2
	y := float64(p.cssH) / 2
	p.stateMu.Unlock()
	_, err := p.callCDP("Input.dispatchMouseEvent",
		map[string]any{"type": "mouseWheel", "x": x, "y": y, "deltaX": 0, "deltaY": dy})
	return err
}

func (p *browserPanel) Screenshot() ([]byte, error) {
	raw, err := p.callCDP("Page.captureScreenshot", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("截图结果解析失败: %w", err)
	}
	buf, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil || len(buf) == 0 {
		return nil, fmt.Errorf("截图数据无效")
	}
	return buf, nil
}

// SetViewport 桌面面板语义：视口=面板实际尺寸。宽度经 browser_resize 事件请
// 前端让位（--content-w 调整），高度跟随右栏；矩形回流后 State 反映实际值。
func (p *browserPanel) SetViewport(w, h int) error {
	if w == 0 || h == 0 {
		w, h = browserDefW, browserDefH
	}
	w = clampInt(w, browserMinSide, browserMaxW)
	h = clampInt(h, browserMinSide, browserMaxH)
	p.app.broadcast(Event{Type: "browser_resize", X: float64(w), Y: float64(h)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.stateMu.Lock()
		cw := p.cssW
		p.stateMu.Unlock()
		if cw >= w-4 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (p *browserPanel) State() BrowserState {
	p.stateMu.Lock()
	st := BrowserState{Running: p.started, URL: p.url, Title: p.title, VW: p.cssW, VH: p.cssH}
	p.stateMu.Unlock()
	if !st.Running {
		return st
	}
	if u, t := p.uiReadNav(); u != "" {
		st.URL, st.Title = u, t
	}
	return st
}

func (p *browserPanel) Started() bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.started
}

func (p *browserPanel) Where() string {
	return "右栏「浏览器」面板（原生 Edge，用户可直接操作）"
}

// Close 关闭面板实例（browser_close）：拆控件与子窗口，下次打开重建。
// 进程退出路径不调它——WebView2 子进程由顶层 kill-on-close Job Object 清场。
func (p *browserPanel) Close() {
	p.createMu.Lock()
	defer p.createMu.Unlock()
	p.stateMu.Lock()
	hwnd, ctl := p.hwnd, p.ctl
	p.hwnd, p.ctl, p.wv, p.chrome = 0, nil, nil, nil
	p.started, p.visible = false, false
	p.url, p.title, p.cssW, p.cssH = "", "", 0, 0
	if p.navCh != nil {
		close(p.navCh)
		p.navCh = nil
	}
	p.stateMu.Unlock()
	p.createErr = nil // 允许下次重建
	if ctl != nil {
		p.dispatch(func() {
			_, _, _ = ctlVtblOf(ctl).close.Call(uintptr(unsafe.Pointer(ctl)))
			if hwnd != 0 {
				_, _, _ = procPanelDestroyWindow.Call(hwnd)
			}
		})
	}
	p.app.broadcast(Event{Type: "browser_nav", URL: "", Title: ""})
}

// BroadcastPointer 点击/输入前亮出 AI 位置：在目标页面内注入临时标记元素
// （原生面板上 HTML 无法覆盖，只能进页面内部画）。
func (p *browserPanel) BroadcastPointer(selector string) {
	x, y, err := p.evalPoint(selector)
	if err != nil {
		return
	}
	p.showPointerAt(x, y)
}

func (p *browserPanel) showPointerAt(x, y float64) {
	js := fmt.Sprintf(`(function(x,y){try{var m=document.getElementById('__jx_ai_marker');`+
		`if(!m){m=document.createElement('div');m.id='__jx_ai_marker';`+
		`m.style.cssText='position:fixed;left:0;top:0;z-index:2147483647;pointer-events:none;`+
		`background:#3b82f6;color:#fff;font:600 11px/1 system-ui;padding:3px 6px;border-radius:4px;`+
		`transform:translate(-50%%,-130%%);box-shadow:0 2px 6px rgba(0,0,0,.35);opacity:0;transition:opacity .2s';`+
		`(document.body||document.documentElement).appendChild(m);}`+
		`m.textContent='AI';m.style.left=x+'px';m.style.top=y+'px';m.style.opacity='1';`+
		`clearTimeout(m.__t);m.__t=setTimeout(function(){m.style.opacity='0';},1500);}catch(e){}})(%g,%g)`, x, y)
	_, _ = p.Evaluate(js)
}

// uiReadNav 同步读当前地址与标题（UI 线程往返，短超时兜底缓存值）。
func (p *browserPanel) uiReadNav() (string, string) {
	res := make(chan [2]string, 1)
	p.dispatch(func() {
		p.stateMu.Lock()
		wv := p.wv
		p.stateMu.Unlock()
		if wv == nil {
			res <- [2]string{"", ""}
			return
		}
		res <- [2]string{wvGetSource(wv), wvGetDocumentTitle(wv)}
	})
	select {
	case r := <-res:
		return r[0], r[1]
	case <-time.After(5 * time.Second):
		p.stateMu.Lock()
		defer p.stateMu.Unlock()
		return p.url, p.title
	}
}
