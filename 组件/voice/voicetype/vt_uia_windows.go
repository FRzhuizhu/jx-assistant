//go:build windows

package main

// UI Automation 探测：读「当前键盘焦点元素」的控件类型/只读/值模式等。
//
// 为什么非要有这一层：Chromium 系窗口（浏览器、Electron、WebView2）在
// GetGUIThreadInfo 里既不给 caret 也给不出子控件 HWND——网页里到底有没有
// 输入框聚焦，Win32 老接口完全看不出来。UIA 的焦点元素能看出来：
//   · 网页正文聚焦 → ControlType=Document(50030)，ValuePattern 只读=true
//   · 网页输入框/文本框聚焦 → ControlType=Edit(50004)，ValuePattern 只读=false
//   · WebView2 应用的输入框（如数字助理的 composer-input）同样是 Edit 只读=false
// 实测记录（Edge 页面正文 vs 数字助理输入框）见开发笔记。
//
// 纪律：
//   · COM 是 STA 线程亲和——这里每次调用都 LockOSThread + CoInitializeEx，
//     用完立刻 Release/CoUninitialize，绝不把接口指针带出本函数（Go 的 goroutine
//     会换线程，跨线程用 STA 接口会间歇崩）。
//   · 只取「是/不是」这类布尔与控件类型，绝不读输入框内容（隐私）。
//   · 任何一步失败都只返回 OK=false，由上层退化成「只复制」，绝不猜。

import (
	"runtime"
	"syscall"
	"unsafe"
)

var (
	ole32dll    = syscall.NewLazyDLL("ole32.dll")
	oleaut32dll = syscall.NewLazyDLL("oleaut32.dll")

	procCoInitializeEx   = ole32dll.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32dll.NewProc("CoUninitialize")
	procCoCreateInstance = ole32dll.NewProc("CoCreateInstance")
	procSysStringLen     = oleaut32dll.NewProc("SysStringLen")
	procSysFreeString    = oleaut32dll.NewProc("SysFreeString")
)

// COM 接口的 GUID（字面量形式，避免手算字节序）
var (
	clsidUIAutomation = "FF48DBA4-60EF-4201-AA87-54103EEF594E" // CLSID_CUIAutomation
	iidIUIAutomation  = "30CBE57D-D9D0-452A-AB13-7AC5AC4825EE" // IID_IUIAutomation
)

// vtGuid COM GUID 的内存布局（Data1/2/3 小端，Data4 按字节序）。
type vtGuid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// vtParseGuid 把 "XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX" 解成内存结构。
func vtParseGuid(s string) vtGuid {
	var g vtGuid
	// 手工解析：不做错误处理（常量串，写错就是开发期立刻暴露）
	hex := func(c byte) uint32 {
		switch {
		case c >= '0' && c <= '9':
			return uint32(c - '0')
		case c >= 'a' && c <= 'f':
			return uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			return uint32(c-'A') + 10
		}
		return 0
	}
	clean := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			clean = append(clean, s[i])
		}
	}
	if len(clean) != 32 {
		return g
	}
	val := func(from, n int) uint32 {
		var v uint32
		for i := 0; i < n; i++ {
			v = v<<4 | hex(clean[from+i])
		}
		return v
	}
	g.Data1 = val(0, 8)
	g.Data2 = uint16(val(8, 4))
	g.Data3 = uint16(val(12, 4))
	for i := 0; i < 8; i++ {
		g.Data4[i] = byte(val(16+i*2, 2))
	}
	return g
}

// vtComCall 按 vtable 下标调用 COM 方法（index 从 QueryInterface=0 起算）。
func vtComCall(p uintptr, index int, args ...uintptr) uintptr {
	vt := *(*uintptr)(unsafe.Pointer(p))
	fn := *(*uintptr)(unsafe.Pointer(vt + uintptr(index)*unsafe.Sizeof(uintptr(0))))
	full := append([]uintptr{p}, args...)
	r, _, _ := syscall.SyscallN(fn, full...)
	return r
}

const (
	vtComIUnknownRelease = 2

	// IUIAutomation 的槽位（IUnknown 3 个之后）
	vtUiaGetFocusedElement = 8

	// IUIAutomationElement 的槽位
	vtElGetCurrentPattern       = 16
	vtElGetCurrentProcessId     = 20
	vtElGetCurrentControlType   = 21
	vtElGetCurrentLocalizedType = 22
	vtElGetCurrentKeyboardFocus = 27
	vtElGetCurrentIsEnabled     = 28
	vtElGetCurrentClassName     = 30
	vtElGetCurrentIsPassword    = 35

	// IUIAutomationValuePattern 的槽位
	vtValueGetIsReadOnly = 5

	uiaValuePatternId = 10002
	uiaTextPatternId  = 10014

	// 控件类型 ID（UIAutomationClient.h）
	uiaCtButton      = 50000
	uiaCtComboBox    = 50003
	uiaCtEdit        = 50004
	uiaCtImage       = 50006
	uiaCtMenu        = 50009
	uiaCtMenuBar     = 50010
	uiaCtMenuItem    = 50011
	uiaCtPane        = 50033
	uiaCtCustom      = 50025
	uiaCtGroup       = 50026
	uiaCtDocument    = 50030
	uiaCtHyperlink   = 50005
	uiaCtProgressBar = 50012
	uiaCtScrollBar   = 50014
	uiaCtStatusBar   = 50017
	uiaCtTab         = 50018
	uiaCtTabItem     = 50019
	uiaCtToolBar     = 50021
	uiaCtTitleBar    = 50037
	uiaCtSeparator   = 50038
	uiaCtHeader      = 50034
	uiaCtHeaderItem  = 50035
)

// vtUiaInfo 焦点元素的判定相关属性（只留「能不能打字」用得上的）。
type vtUiaInfo struct {
	OK                bool   // 探测是否成功（失败一律当"没证据"）
	ControlType       int32  // UIA 控件类型 ID
	LocalizedType     string // 本地化类型名（"编辑"/"文档"…，只进日志）
	ClassName         string // 焦点元素的 UIA ClassName（如 composer-input，只进日志）
	KeyboardFocusable bool
	Enabled           bool
	Password          bool
	HasValuePattern   bool
	ValueReadOnly     bool // 仅在 HasValuePattern 时有意义
	HasTextPattern    bool
}

// vtUiaControlTypeName 控件类型中文名（日志/排查用，取不到的给数字）。
func vtUiaControlTypeName(id int32) string {
	switch id {
	case uiaCtButton:
		return "按钮"
	case uiaCtComboBox:
		return "组合框"
	case uiaCtEdit:
		return "编辑"
	case uiaCtDocument:
		return "文档"
	case uiaCtPane:
		return "区域"
	case uiaCtCustom:
		return "自定义"
	case uiaCtGroup:
		return "分组"
	case uiaCtHyperlink:
		return "链接"
	case uiaCtImage:
		return "图像"
	case uiaCtMenu, uiaCtMenuBar, uiaCtMenuItem:
		return "菜单"
	case uiaCtTab, uiaCtTabItem:
		return "标签页"
	case uiaCtToolBar:
		return "工具栏"
	case uiaCtStatusBar:
		return "状态栏"
	case uiaCtTitleBar:
		return "标题栏"
	case uiaCtScrollBar:
		return "滚动条"
	case uiaCtProgressBar:
		return "进度条"
	case uiaCtSeparator:
		return "分隔符"
	case uiaCtHeader, uiaCtHeaderItem:
		return "表头"
	}
	return ""
}

// vtBstr 读 BSTR 并释放（SysStringLen 定界，绝不按 0 结尾扫）。
func vtBstr(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	n, _, _ := procSysStringLen.Call(ptr)
	var s string
	if n > 0 {
		s = syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), int(n)))
	}
	procSysFreeString.Call(ptr)
	return s
}

// vtUiaFocused 探测当前键盘焦点元素。任何一步失败都返回 OK=false。
//
// 必须在同一个 OS 线程上完成「初始化 → 建对象 → 用 → 放」：STA 接口不能跨线程。
func vtUiaFocused() vtUiaInfo {
	var info vtUiaInfo
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, 2 /*COINIT_APARTMENTTHREADED*/)
	// S_OK=0 / S_FALSE=1：都算本线程初始化成功，需配对 CoUninitialize；
	// RPC_E_CHANGED_MODE(0x80010106)：本线程已是别的套间，仍可试，但不反初始化。
	needUninit := hr == 0 || hr == 1
	if needUninit {
		defer procCoUninitialize.Call()
	} else if uint32(hr) == 0x80010106 {
		vtLog("UIA: 线程套间模式冲突（不影响本次探测）")
	}

	clsid := vtParseGuid(clsidUIAutomation)
	iid := vtParseGuid(iidIUIAutomation)
	var uia uintptr
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsid)), 0, 1, /*CLSCTX_INPROC_SERVER*/
		uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&uia)))
	if hr != 0 || uia == 0 {
		return info
	}
	defer vtComCall(uia, vtComIUnknownRelease)

	var el uintptr
	hr = vtComCall(uia, vtUiaGetFocusedElement, uintptr(unsafe.Pointer(&el)))
	if hr != 0 || el == 0 {
		return info
	}
	defer vtComCall(el, vtComIUnknownRelease)

	var ct, kf, en, pw int32
	var lct, cls uintptr
	vtComCall(el, vtElGetCurrentControlType, uintptr(unsafe.Pointer(&ct)))
	vtComCall(el, vtElGetCurrentLocalizedType, uintptr(unsafe.Pointer(&lct)))
	vtComCall(el, vtElGetCurrentClassName, uintptr(unsafe.Pointer(&cls)))
	vtComCall(el, vtElGetCurrentKeyboardFocus, uintptr(unsafe.Pointer(&kf)))
	vtComCall(el, vtElGetCurrentIsEnabled, uintptr(unsafe.Pointer(&en)))
	vtComCall(el, vtElGetCurrentIsPassword, uintptr(unsafe.Pointer(&pw)))

	info.OK = true
	info.ControlType = ct
	info.LocalizedType = vtBstr(lct)
	info.ClassName = vtBstr(cls)
	info.KeyboardFocusable = kf != 0
	info.Enabled = en != 0
	info.Password = pw != 0

	// 模式可用性：GetCurrentPattern 不支持时返回空指针（实测 hr 仍是 0）
	var vp, tp uintptr
	vtComCall(el, vtElGetCurrentPattern, uiaValuePatternId, uintptr(unsafe.Pointer(&vp)))
	vtComCall(el, vtElGetCurrentPattern, uiaTextPatternId, uintptr(unsafe.Pointer(&tp)))
	if vp != 0 {
		info.HasValuePattern = true
		var ro int32
		vtComCall(vp, vtValueGetIsReadOnly, uintptr(unsafe.Pointer(&ro)))
		info.ValueReadOnly = ro != 0
		vtComCall(vp, vtComIUnknownRelease)
	}
	if tp != 0 {
		info.HasTextPattern = true
		vtComCall(tp, vtComIUnknownRelease)
	}
	return info
}
