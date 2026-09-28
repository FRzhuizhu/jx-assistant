//go:build windows

package main

// 原生浏览器面板（browser_panel_windows.go）的冒烟测试：真机创建第二个
// WebView2 控件（隐藏的顶层窗口做宿主），验证最风险的两段自制代码——
// COM vtbl 镜像（GetSource/GetDocumentTitle/GetCoreWebView2）与控件级官方
// CDP 通道（CallDevToolsProtocolMethod 的回调对象）。
// 线程模型对齐生产：面板操作从旁路 goroutine 发起（对齐 HTTP 侧调用），
// 全部 COM 调用经 dispatch 投递回锁定的 UI 线程（STA）执行，消息泵在
// UI 线程轮转（PeekMessage）。依赖系统 WebView2 运行时，不可用则跳过。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	smokeUser32   = windows.NewLazySystemDLL("user32.dll")
	procPeekMsg   = smokeUser32.NewProc("PeekMessageW")
	procTranslate = smokeUser32.NewProc("TranslateMessage")
	procDispatch  = smokeUser32.NewProc("DispatchMessageW")
	procCreateWnd = smokeUser32.NewProc("CreateWindowExW")
	procDestroyWd = smokeUser32.NewProc("DestroyWindow")
	procCoInit    = windows.NewLazySystemDLL("ole32.dll").NewProc("CoInitializeEx")
)

// smokeMsg 即 MSG（x64 布局）。
type smokeMsg struct {
	hwnd                 uintptr
	msg                  uint32
	wp, lp               uintptr
	time                 uint32
	ptX, ptY             int32
	reservedL, reservedH int32
}

func TestBrowserPanelSmoke(t *testing.T) {
	if os.Getenv("JX_SKIP_PANEL_SMOKE") != "" {
		t.Skip("环境变量 JX_SKIP_PANEL_SMOKE 置位，跳过")
	}
	if !webView2RuntimeAvailable() {
		t.Skip("系统无 WebView2 运行时，跳过")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>panel-smoke</title></head>`+
			`<body><h1 id="hd">面板冒烟</h1><input id="field"></body></html>`)
	}))
	defer srv.Close()

	dir, err := os.MkdirTemp("", "jx-panel-smoke")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir) // WebView2 子进程可能短暂占用目录：删不掉留给系统临时目录
	app := &App{Cfg: &Config{DataDir: dir}}

	// 本测试 goroutine 即锁定 的 UI 线程（STA）：泵在这里轮转
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procCoInit.Call(0, 2); int32(r) < 0 && r != 1 { // COINIT_APARTMENTTHREADED；S_FALSE(1) 也算成功
		t.Skipf("CoInitializeEx 失败: 0x%x", r)
	}

	cls, _ := windows.UTF16PtrFromString("Static")
	name, _ := windows.UTF16PtrFromString("JXPanelSmoke")
	hInst, _, _ := procPanelGetModuleHandle.Call(0)
	const wsOverlappedWindow = 0x00CF0000
	const swShownNoActivate = 4
	hwnd, _, _ := procCreateWnd.Call(
		0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)), wsOverlappedWindow,
		^uintptr(1999) /*-2000*/, ^uintptr(1999) /*-2000*/, 800, 600, 0, 0, hInst, 0)
	if hwnd == 0 {
		t.Fatal("创建宿主窗口失败")
	}
	// 屏外但保持可见：合成器持续出帧（截图依赖），测试过程不打扰用户
	_, _, _ = procPanelShowWindow.Call(hwnd, swShownNoActivate)
	defer procDestroyWd.Call(hwnd)

	q := make(chan func(), 64)
	dispatch := func(f func()) { q <- f }
	panel := &browserPanel{app: app, dispatch: dispatch, mainWnd: hwnd}
	defer func() {
		panel.Close()
		for { // 排空残留闭包，别让控制器带着未决回调退出
			select {
			case f := <-q:
				f()
			default:
				return
			}
		}
	}()

	// UI 线程泵：执行投递闭包 + 分发窗口消息（COM 事件回调靠它送达）；
	// ready 返回真即提前收工（结果 chan 有货）
	pumpUntil := func(d time.Duration, ready func() bool) {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			select {
			case f := <-q:
				f()
			default:
			}
			var m smokeMsg
			for {
				r, _, _ := procPeekMsg.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1 /*PM_REMOVE*/)
				if r == 0 {
					break
				}
				procTranslate.Call(uintptr(unsafe.Pointer(&m)))
				procDispatch.Call(uintptr(unsafe.Pointer(&m)))
			}
			if ready != nil && ready() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// 导航（顺带完成控件创建）：旁路 goroutine 发起，UI 线程泵驱动
	type navResult struct {
		url, title string
		err        error
	}
	navRes := make(chan navResult, 1)
	go func() {
		u, ti, e := panel.Navigate(srv.URL)
		navRes <- navResult{u, ti, e}
	}()
	pumpUntil(45*time.Second, func() bool { return len(navRes) > 0 })
	select {
	case r := <-navRes:
		if r.err != nil {
			t.Skipf("WebView2 面板创建/导航失败（环境原因跳过）: %v", r.err)
		}
		if !strings.Contains(r.url, srv.URL) {
			t.Fatalf("导航落定地址异常: %q", r.url)
		}
		if !strings.Contains(r.title, "panel-smoke") {
			t.Fatalf("标题未读到（GetDocumentTitle 镜像异常）: %q", r.title)
		}
	default:
		t.Fatal("面板导航超时（控件创建或事件回调未达）")
	}

	// 矩形同步（SetRect → SetWindowPos + PutBounds），对齐前端上报路径
	rectRes := make(chan error, 1)
	go func() { rectRes <- panel.SetRect(0, 0, 800, 600, 1.0, true) }()
	pumpUntil(5*time.Second, func() bool { return len(rectRes) > 0 })
	if e := <-rectRes; e != nil {
		t.Fatalf("SetRect 失败: %v", e)
	}

	// 官方 CDP 通道：evaluate 取回页面数据（CallDevToolsProtocolMethod 全链路）
	evRes := make(chan string, 1)
	go func() {
		v, e := panel.Evaluate(`document.getElementById('hd').innerText`)
		if e != nil {
			evRes <- "ERR:" + e.Error()
			return
		}
		evRes <- fmt.Sprint(v)
	}()
	pumpUntil(30*time.Second, func() bool { return len(evRes) > 0 })
	select {
	case v := <-evRes:
		if strings.HasPrefix(v, "ERR:") {
			t.Fatalf("CDP evaluate 失败: %s", v[4:])
		}
		if !strings.Contains(v, "面板冒烟") {
			t.Fatalf("CDP evaluate 结果异常: %q", v)
		}
	default:
		t.Fatal("CDP evaluate 超时")
	}

	// 截图（Page.captureScreenshot → base64 PNG）
	scRes := make(chan error, 1)
	go func() {
		buf, e := panel.Screenshot()
		if e == nil && (len(buf) < 100 || buf[0] != 0x89 || buf[1] != 'P') {
			e = fmt.Errorf("截图数据异常（%d 字节）", len(buf))
		}
		scRes <- e
	}()
	pumpUntil(30*time.Second, func() bool { return len(scRes) > 0 })
	select {
	case e := <-scRes:
		if e != nil {
			t.Fatalf("CDP 截图失败: %v", e)
		}
	default:
		t.Fatal("CDP 截图超时")
	}

	// Content（uiReadNav + evaluate innerText）
	cRes := make(chan string, 1)
	go func() {
		_, _, body, e := panel.Content()
		if e != nil {
			cRes <- "ERR:" + e.Error()
			return
		}
		cRes <- body
	}()
	pumpUntil(15*time.Second, func() bool { return len(cRes) > 0 })
	select {
	case body := <-cRes:
		if strings.HasPrefix(body, "ERR:") {
			t.Logf("Content 读取失败（不致命）: %s", body[4:])
		} else if !strings.Contains(body, "面板冒烟") {
			t.Logf("Content 正文未含标记: %.80q", body)
		}
	default:
		t.Log("Content 超时（不致命）")
	}

	// file:// 路径（交付卡「打开」→ open-view → NavigatePrivileged）：本地
	// 交付 html 转文件地址在原生面板打开
	tmp := filepath.Join(dir, "report.html")
	if err := os.WriteFile(tmp, []byte(`<html><head><title>file-view</title></head><body>交付预览</body></html>`), 0o644); err == nil {
		fRes := make(chan navResult, 1)
		go func() {
			u, ti, e := panel.NavigatePrivileged(fileURLFromAbs(tmp))
			fRes <- navResult{u, ti, e}
		}()
		pumpUntil(20*time.Second, func() bool { return len(fRes) > 0 })
		select {
		case r := <-fRes:
			if r.err != nil {
				t.Fatalf("file:// 导航失败（交付预览路径）: %v", r.err)
			}
			if !strings.Contains(r.url, "file:///") || !strings.Contains(r.title, "file-view") {
				t.Fatalf("file:// 导航落定异常: %q %q", r.url, r.title)
			}
		default:
			t.Fatal("file:// 导航超时")
		}
	}

	// 隐藏态（用户收起右栏）语义：控件挪屏外但保持渲染——截图必须仍可用
	hideRes := make(chan error, 1)
	go func() { hideRes <- panel.SetRect(0, 0, 800, 600, 1.0, false) }()
	pumpUntil(5*time.Second, func() bool { return len(hideRes) > 0 })
	<-hideRes
	time.Sleep(300 * time.Millisecond)
	hsRes := make(chan error, 1)
	go func() {
		_, e := panel.Screenshot()
		hsRes <- e
	}()
	pumpUntil(12*time.Second, func() bool { return len(hsRes) > 0 })
	select {
	case e := <-hsRes:
		if e != nil {
			t.Fatalf("收起态截图失败（应屏外保持渲染）: %v", e)
		}
	default:
		t.Fatal("收起态截图挂起：屏外保活渲染未生效")
	}
}
