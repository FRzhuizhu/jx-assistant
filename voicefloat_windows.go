//go:build windows

package main

// 全局语音输入法的胶囊浮窗：屏幕底部（任务栏上方、水平居中）的顶层小窗口，
// 内嵌第三个 WebView2 控件（独立环境与数据目录 data/webview-float，与主界面
// data/webview、右栏浏览器 data/webview-browser 互不干扰）渲染 web/voice-float.html。
//
// 窗口纪律：
//   - WS_POPUP 无边框 + WS_EX_TOPMOST 置顶 + WS_EX_NOACTIVATE 不抢键盘焦点
//     （听写期间用户继续在目标程序里打字/光标不动）+ WS_EX_TOOLWINDOW 不进
//     任务栏与 Alt+Tab；Win11 上用 DWM 圆角偏好包出胶囊感（Win10 退化为方角）。
//   - 所有 COM / 窗口调用一律发生在浮窗自己的 UI 线程（与 browser_panel 同一
//     纪律）：外部 goroutine 经 voiceFloatDispatch 投递，线程消息循环里被
//     WM_APP+1 唤醒后批量执行；Eval（ExecuteScript）也一样，绝不跨线程直调。
//   - 惰性创建：首次触发全局听写才建窗与 WebView2（大多会话用不到它）；
//     建好后常驻隐藏，反复开关零成本。
//
// 麦克风：环境创建前把 --use-fake-ui-for-media-stream 并进
// WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS（newDesktopFrontend 同款套路），再叠加
// SetPermission(Microphone, Allow) 双保险——浮窗里 getUserMedia 一次都不弹授权。

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	edge "github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

const (
	vfWSPOPUP        = 0x80000000
	vfWSExTopmost    = 0x00000008
	vfWSExToolWindow = 0x00000080
	vfWSExNoActivate = 0x08000000
	vfSWHide         = 0
	vfSWShowNoActivate = 4
	vfWMAppDispatch  = 0x8000 + 1
	vfSPIGetWorkArea = 48
	vfDwmWACorner    = 33 // DWMWA_WINDOW_CORNER_PREFERENCE
	vfDwmCornerRound = 2  // DWMWCP_ROUND
)

var (
	procVfSystemParametersInfo = windows.NewLazySystemDLL("user32.dll").NewProc("SystemParametersInfoW")
	procVfDispatchMessage      = windows.NewLazySystemDLL("user32.dll").NewProc("DispatchMessageW")
	procVfTranslateMessage     = windows.NewLazySystemDLL("user32.dll").NewProc("TranslateMessageW")
	procVfIsWindow             = windows.NewLazySystemDLL("user32.dll").NewProc("IsWindow")
	procVfDwmSetWindowAttr     = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	// 剪贴板写（读在 clipboard_windows.go；这三个只在写路径用）
	procVfEmptyClipboard    = windows.NewLazySystemDLL("user32.dll").NewProc("EmptyClipboard")
	procVfSetClipboardData  = windows.NewLazySystemDLL("user32.dll").NewProc("SetClipboardData")
	procVfGlobalAlloc       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalAlloc")
)

type voiceFloat struct {
	mu       sync.Mutex
	created  bool
	err      error
	hwnd     uintptr
	chrome   *edge.Chromium
	ctl      *edge.ICoreWebView2Controller
	uiCh     chan func()
	tid      uint32
	quitTID  uint32 // 消息循环线程（WM_QUIT 卸载用）
	doneCh   chan struct{}
}

var (
	vfOnce   sync.Once
	vfFloat  = &voiceFloat{uiCh: make(chan func(), 64)}
	vfBaseURL string // runDesktop 注入（voiceGlobalApply 传入），浮窗页面导航用
)

// voiceFloatBaseURL 记录主服务地址（惰性建窗时用；设置页热更配置不改变它）。
func voiceFloatBaseURL(u string) { vfBaseURL = u }

// voiceFloatEnsure 惰性创建浮窗（幂等）。返回创建错误（仅第一次失败有意义，
// 失败会记住，避免反复撞墙；重启程序才能重试）。
func voiceFloatEnsure() error {
	vfOnce.Do(func() {
		done := make(chan error, 1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			done <- vfCreateOnUI()
			// 创建完成后进入常驻消息循环（WM_QUIT 退出）
			vfMessageLoop()
			close(vfFloat.doneCh)
		}()
		err := <-done
		vfFloat.mu.Lock()
		vfFloat.err = err
		vfFloat.created = err == nil
		vfFloat.mu.Unlock()
		if err != nil {
			log.Printf("[语音浮窗] 创建失败: %v", err)
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

// vfCreateOnUI 在浮窗 UI 线程建顶层窗口与 WebView2 控件（保持隐藏，等待首个会话）。
func vfCreateOnUI() error {
	// 尺寸与落位：宽 580、高 48（物理像素），工作区底部居中、任务栏上方 14px
	const wfW, wfH = 580, 48
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
	// Win11 圆角偏好（胶囊观感）；旧系统调用失败直接忽略
	_, _, _ = procVfDwmSetWindowAttr.Call(hwnd, uintptr(vfDwmWACorner),
		uintptr(unsafe.Pointer(&[]int32{vfDwmCornerRound}[0])), 4)

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

	tid, _, _ := procGetCurrentThreadID.Call()
	vfFloat.mu.Lock()
	vfFloat.hwnd, vfFloat.chrome, vfFloat.ctl = hwnd, ch, ctl
	vfFloat.quitTID = uint32(tid)
	vfFloat.mu.Unlock()

	base := vfBaseURL
	if base == "" {
		base = "http://127.0.0.1"
	}
	ch.Navigate(base + "/voice-float.html")
	return nil
}

// vfMessageLoop 浮窗 UI 线程的常驻消息循环：WM_APP+1 唤醒后批量执行投递来的
// 函数（COM/窗口操作全部在这条线程上跑），WM_QUIT 退出。其余消息照常派发
// （WebView2 内部子窗口依赖正常泵送）。
func vfMessageLoop() {
	drain := func() {
		for {
			select {
			case f := <-vfFloat.uiCh:
				safeRun(f)
			default:
				return
			}
		}
	}
	var msg recMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) {
			break
		}
		if msg.Message == vfWMAppDispatch {
			drain()
			continue
		}
		_, _, _ = procVfTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procVfDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func safeRun(f func()) {
	defer func() { _ = recover() }()
	f()
}

// voiceFloatDispatch 把函数投递到浮窗 UI 线程执行（未创建/已退出时静默丢弃）。
func voiceFloatDispatch(f func()) {
	vfFloat.mu.Lock()
	created := vfFloat.created && vfFloat.hwnd != 0
	tid := vfFloat.quitTID
	vfFloat.mu.Unlock()
	if !created || tid == 0 {
		return
	}
	select {
	case vfFloat.uiCh <- f:
	default:
		return // 队列满：丢弃（显示类操作，丢一帧无碍）
	}
	procPostThreadMessageW.Call(uintptr(tid), vfWMAppDispatch, 0, 0)
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
