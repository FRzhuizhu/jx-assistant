//go:build windows

package main

// 全局语音输入法的胶囊浮窗：屏幕底部（任务栏上方、水平居中）的顶层小窗口，
// 内嵌第三个 WebView2 控件（独立环境与数据目录 data/webview-float，与主界面
// data/webview、右栏浏览器 data/webview-browser 互不干扰）渲染 web/voice-float.html。
//
// 窗口纪律：
//   - WS_POPUP 无边框 + WS_EX_TOPMOST 置顶 + WS_EX_NOACTIVATE 不抢键盘焦点
//     （听写期间用户继续在目标程序里打字/光标不动）+ WS_EX_TOOLWINDOW 不进
//     任务栏与 Alt+Tab；SetWindowRgn 把窗口裁成真胶囊（圆角半径=高/2，
//     CreateRoundRectRgn），页面铺满主题色即得胶囊观感，Win10/Win11 一致。
//   - 所有 COM / 窗口调用一律发生在桌面壳的**主 UI 线程**（与 browser_panel
//     同一纪律，经 mainWndDispatch 投递）：WebView2 的第二个环境必须在主线程
//     创建——2026-10-13 实测在自建线程上创建时 go-webview2 的环境回调会拿到
//     nil env 直接 panic（EnvironmentCompleted 崩溃，整个桌面进程陪葬），
//     首版浮窗自建线程的写法已回退。
//   - 惰性创建：首次触发全局听写才建窗与 WebView2（大多会话用不到它）；
//     建好后常驻隐藏，反复开关零成本。创建全程带 recover 兜底：库里再出
//     任何崩溃也只降级为「本进程无浮窗」，绝不带走进程。
//
// 麦克风：环境创建前把 --use-fake-ui-for-media-stream 并进
// WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS（newDesktopFrontend 同款套路），再叠加
// SetPermission(Microphone, Allow) 双保险——浮窗里 getUserMedia 一次都不弹授权。

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	edge "github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

const (
	vfWSPOPUP          = 0x80000000
	vfWSExTopmost      = 0x00000008
	vfWSExToolWindow   = 0x00000080
	vfWSExNoActivate   = 0x08000000
	vfSWHide           = 0
	vfSWShowNoActivate = 4
	vfSPIGetWorkArea   = 48
)

var (
	procVfSystemParametersInfo = windows.NewLazySystemDLL("user32.dll").NewProc("SystemParametersInfoW")
	procVfIsWindow             = windows.NewLazySystemDLL("user32.dll").NewProc("IsWindow")
	procVfSetWindowRgn         = windows.NewLazySystemDLL("user32.dll").NewProc("SetWindowRgn")
	procVfCreateRoundRectRgn   = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateRoundRectRgn")
	// 剪贴板写（读在 clipboard_windows.go；这三个只在写路径用）
	procVfEmptyClipboard    = windows.NewLazySystemDLL("user32.dll").NewProc("EmptyClipboard")
	procVfSetClipboardData  = windows.NewLazySystemDLL("user32.dll").NewProc("SetClipboardData")
	procVfGlobalAlloc       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalAlloc")
)

type voiceFloat struct {
	mu      sync.Mutex
	created bool
	err     error
	hwnd    uintptr
	chrome  *edge.Chromium
	ctl     *edge.ICoreWebView2Controller
}

var (
	vfOnce    sync.Once
	vfFloat   = &voiceFloat{}
	vfBaseURL string // runDesktop 注入（voiceGlobalApply 传入），浮窗页面导航用
)

// voiceFloatBaseURL 记录主服务地址（惰性建窗时用；设置页热更配置不改变它）。
func voiceFloatBaseURL(u string) { vfBaseURL = u }

// voiceFloatEnsure 惰性创建浮窗（幂等）。返回创建错误（仅第一次失败有意义，
// 失败会记住，避免反复撞墙；重启程序才能重试）。创建投递到桌面壳主 UI
// 线程执行（mainWndDispatch，与浏览器面板同一通道），这里同步等结果。
func voiceFloatEnsure() error {
	vfOnce.Do(func() {
		var err error
		if mainWndDispatch == nil {
			err = fmt.Errorf("桌面主 UI 线程未就绪（非桌面形态）")
		} else {
			done := make(chan struct{})
			mainWndDispatch(func() {
				err = vfCreateOnUI()
				close(done)
			})
			<-done
		}
		vfFloat.mu.Lock()
		vfFloat.err = err
		vfFloat.created = err == nil
		vfFloat.mu.Unlock()
		if err != nil {
			log.Printf("[语音浮窗] 创建失败: %v", err)
		} else {
			log.Printf("[语音浮窗] 已创建（主 UI 线程，常驻隐藏）")
		}
	})
	vfFloat.mu.Lock()
	defer vfFloat.mu.Unlock()
	return vfFloat.err
}

// vfWorkArea 主屏工作区（物理像素；进程已 DPI 感知）。
func vfWorkArea() (l, t, r, b int32) {
	type rect struct{ Left, Top, Right, Bottom int32 }
	var wa rect
	_, _, _ = procVfSystemParametersInfo.Call(vfSPIGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
	return wa.Left, wa.Top, wa.Right, wa.Bottom
}

// vfCreateOnUI 在桌面壳主 UI 线程建顶层窗口与 WebView2 控件（保持隐藏，等待
// 首个会话）。全程 recover：go-webview2 的环境回调历史上崩过（nil env 空指针），
// 这里把它降级为创建错误，绝不让库崩溃带走进程。
func vfCreateOnUI() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("WebView2 初始化崩溃（go-webview2 回调异常）: %v", r)
		}
	}()
	// 尺寸与落位：宽 400、高 56（物理像素，微信浮窗的比例），工作区底部居中、
	// 任务栏上方 14px
	const wfW, wfH = 400, 56
	l, _, r, b := vfWorkArea()
	if r <= l || b <= 0 { // 拿不到工作区（异常环境）：退到主屏右下角估算
		l, r, b = 0, 1920, 1040
	}
	x := (r-l-int32(wfW))/2 + l
	y := b - int32(wfH) - 14
	cls, _ := windows.UTF16PtrFromString("Static") // 系统控件类做中性容器（browser_panel 同款）
	name, _ := windows.UTF16PtrFromString("JXVoiceFloat")
	hInst, _, _ := procPanelGetModuleHandle.Call(0)
	// CreateWindowExW 首参是 dwExStyle（16:42 实机报「参数不正确」的根因：
	// 漏传 exStyle，类名指针错位顶到它头上）。置顶 + 不抢焦点 + 不进任务栏。
	hwnd, _, err := procPanelCreateWindow.Call(
		uintptr(vfWSExTopmost|vfWSExToolWindow|vfWSExNoActivate),
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)),
		uintptr(vfWSPOPUP),
		uintptr(int(x)), uintptr(int(y)), uintptr(wfW), uintptr(wfH),
		0, 0, hInst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW 失败: %v", err)
	}
	// 真胶囊：窗口区域裁成两端半圆的胶囊。CreateRoundRectRgn 后两参是**角落
	// 椭圆的宽高**（半径=其一半）——传整窗高（56×56）时半径=高/2=28，上下两
	// 个四分之一圆弧在侧边正中拼成一整个半圆，左右两条竖直边完全消失（2026-
	// 10-14：此前误传 28×28，实际半径只有 14px，侧边还留着 28px 直线、看着
	// 仍是圆角矩形）。此前靠页面自画胶囊 + DWM 小圆角会在矩形四角露出"圆角
	// 矩形包裹"的底色边；窗口本体裁形后页面只需铺满主题色。
	rgn, _, _ := procVfCreateRoundRectRgn.Call(0, 0, uintptr(wfW), uintptr(wfH),
		uintptr(wfH), uintptr(wfH))
	if rgn != 0 {
		procVfSetWindowRgn.Call(hwnd, rgn, 1)
	}

	// 麦克风免授权：环境创建参数（创建后立即还原，不影响别的环境）
	const autoGrantMic = "--use-fake-ui-for-media-stream"
	prevArgs, hadArgs := os.LookupEnv(webView2ArgsEnv)
	if !strings.Contains(prevArgs, autoGrantMic) {
		_ = os.Setenv(webView2ArgsEnv, strings.TrimSpace(prevArgs+" "+autoGrantMic))
	}

	ch := edge.NewChromium()
	ch.DataPath = filepath.Join("data", "webview-float")
	ch.SetPermission(edge.CoreWebView2PermissionKindMicrophone, edge.CoreWebView2PermissionStateAllow)
	ok := ch.Embed(hwnd)
	if hadArgs {
		_ = os.Setenv(webView2ArgsEnv, prevArgs)
	} else {
		_ = os.Unsetenv(webView2ArgsEnv)
	}
	if !ok {
		_, _, _ = procPanelDestroyWindow.Call(hwnd)
		return fmt.Errorf("WebView2 控件初始化失败")
	}
	ctl := ch.GetController()
	if ctl == nil {
		_, _, _ = procPanelDestroyWindow.Call(hwnd)
		return fmt.Errorf("WebView2 控件未就绪")
	}
	wv := ctlWebView(ctl)
	if wv == nil {
		_, _, _ = procPanelDestroyWindow.Call(hwnd)
		return fmt.Errorf("CoreWebView2 不可用")
	}
	// 控件 bounds = 整个客户区
	rect := panelRect{0, 0, int32(wfW), int32(wfH)}
	_, _, _ = ctlVtblOf(ctl).putBounds.Call(uintptr(unsafe.Pointer(ctl)), uintptr(unsafe.Pointer(&rect)))
	_ = ctl.PutIsVisible(true)

	vfFloat.mu.Lock()
	vfFloat.hwnd, vfFloat.chrome, vfFloat.ctl = hwnd, ch, ctl
	vfFloat.mu.Unlock()

	base := vfBaseURL
	if base == "" {
		base = "http://127.0.0.1"
	}
	ch.Navigate(base + "/voice-float.html")
	return nil
}

func safeRun(f func()) {
	defer func() { _ = recover() }()
	f()
}

// voiceFloatDispatch 把函数投递到桌面壳主 UI 线程执行（未创建/已退出时静默丢弃）。
// 窗口与 WebView2 都活在主线程的消息循环里，与浏览器面板共用 mainWndDispatch。
func voiceFloatDispatch(f func()) {
	vfFloat.mu.Lock()
	created := vfFloat.created && vfFloat.hwnd != 0
	vfFloat.mu.Unlock()
	if !created || mainWndDispatch == nil {
		return
	}
	mainWndDispatch(func() { safeRun(f) })
}

// voiceFloatEval 在浮窗页面执行 JS（COM 调用经 Dispatch 落到 UI 线程）。
func voiceFloatEval(js string) {
	voiceFloatDispatch(func() {
		vfFloat.mu.Lock()
		ch := vfFloat.chrome
		vfFloat.mu.Unlock()
		if ch != nil {
			ch.Eval(js)
		}
	})
}

// voiceFloatShow / voiceFloatHide 显示与隐藏（SW_SHOWNOACTIVATE：绝不抢前台）。
func voiceFloatShow() {
	voiceFloatDispatch(func() {
		_, _, _ = procPanelShowWindow.Call(vfFloat.hwnd, uintptr(vfSWShowNoActivate))
	})
}

func voiceFloatHide() {
	voiceFloatDispatch(func() {
		_, _, _ = procPanelShowWindow.Call(vfFloat.hwnd, uintptr(vfSWHide))
	})
}

// writeClipboardText 写系统剪贴板（CF_UNICODETEXT）。读实现在 clipboard_windows.go，
// 这里补写半边：全局语音结束「已复制」路径与粘贴前的双保险都走它。
func writeClipboardText(text string) error {
	u16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	var opened bool
	for i := 0; i < 4; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !opened {
		return errClipboardBusy
	}
	defer procCloseClipboard.Call()
	_, _, _ = procVfEmptyClipboard.Call()
	const gmemMoveable = 0x0002
	byteLen := uintptr(len(u16)) * 2
	h, _, _ := procVfGlobalAlloc.Call(gmemMoveable, byteLen)
	if h == 0 {
		return fmt.Errorf("GlobalAlloc 失败")
	}
	if p, _, _ := procGlobalLock.Call(h); p != 0 {
		// vet 的 unsafeptr 检查不认 LazyProc.Call 的返回值（clipboard_windows.go
		// 的 sysPtr 注释同款场景），经双指针重解释完成等价转换
		copy(unsafe.Slice((*uint16)(sysPtr(&p)), len(u16)), u16)
		procGlobalUnlock.Call(h)
	} else {
		return fmt.Errorf("GlobalLock 失败")
	}
	if r, _, err := procVfSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return fmt.Errorf("SetClipboardData 失败: %v", err)
	}
	return nil // 成功后系统接管内存句柄，不要 GlobalFree
}
