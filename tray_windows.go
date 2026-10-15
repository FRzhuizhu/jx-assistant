//go:build windows

package main

// 系统托盘（Windows）：桌面形态的常驻入口。用户关闭主窗口（✕ / Alt+F4 /
// 任务栏右键关闭）不再退出进程——后端（HTTP 服务、会话、运行中任务）随托盘
// 驻留；单击/双击图标恢复主窗口，右键菜单提供「打开主界面 / 刷新界面 / 退出」。
//
// 实现要点：
// - 托盘回调窗口是隐藏的普通顶级窗口（不能是 message-only：TaskbarCreated
//   广播只有普通窗口收得到），收到即重挂图标，explorer 重启不丢托盘；
// - 与主窗口同一 UI 线程：托盘 wndproc 依附主消息循环分发，不另起线程；
// - 图标取 exe 资源 RT_GROUP_ICON id=1（与主窗口标题栏同源，见 打包/go-winres）；
// - 「退出」置 frontendQuitReq 后向主窗口投递程序化 WM_CLOSE（不走
//   WM_SYSCOMMAND，因此不会被「关闭进托盘」拦截），主窗口不在时直接
//   PostQuitMessage 结束消息循环——两条路都汇到 runDesktop 窗口循环的
//   退出分支；
// - 各失败路径（注册类/建窗/挂图标）只落日志不中断：托盘缺位时行为退回
//   改造前（窗口关闭即退出进程），主流程不受影响。

import (
	"log"
	"runtime/debug"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nimAdd    = 0x0000
	nimModify = 0x0001
	nimDelete = 0x0002

	nifMessage = 0x0001
	nifIcon    = 0x0002
	nifTip     = 0x0004
	nifInfo    = 0x0010
	niifInfo   = 0x0001

	wmNull          = 0x0000
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205

	trayCallbackMsg  = 0x8000 // WM_APP：托盘图标事件回调消息
	trayWndClassName = "JXAssistantTrayWnd"
	wsPopup          = 0x80000000

	mfSeparator    = 0x0800
	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020
	tpmReturnCmd   = 0x0100

	trayCmdOpen   = 100
	trayCmdReload = 101
	trayCmdExit   = 102
)

var (
	trayShell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW   = trayShell32.NewProc("Shell_NotifyIconW")
	trayUser32             = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW   = trayUser32.NewProc("RegisterClassExW")
	procRegisterWindowMsgW = trayUser32.NewProc("RegisterWindowMessageW")
	procIsIconic           = trayUser32.NewProc("IsIconic")
	procCreatePopupMenu    = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenuW        = trayUser32.NewProc("AppendMenuW")
	procTrackPopupMenu     = trayUser32.NewProc("TrackPopupMenu")
	procDestroyMenu        = trayUser32.NewProc("DestroyMenu")
	procGetCursorPos       = trayUser32.NewProc("GetCursorPos")
	procLoadImageW         = trayUser32.NewProc("LoadImageW")
	procPostQuitMessage    = trayUser32.NewProc("PostQuitMessage")
)

// notifyIconDataW 镜像 NOTIFYICONDATAW（x64 布局：按声明顺序自然对齐后 976
// 字节，与官方一致；字段顺序不可改动）。
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

// wndClassExW 镜像 WNDCLASSEXW（x64 80 字节）。
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type pointStruct struct{ x, y int32 }

var (
	trayApp            *App
	trayHwnd           uintptr
	trayIcon           uintptr
	trayTaskbarCreated uint32 // RegisterWindowMessage("TaskbarCreated")
	trayHiddenOnce     int32  // 首次进托盘的气泡只发一次
	// frontendQuitReq 1 = 请求整体退出。托盘「退出」菜单与自动化冒烟
	// （JX_DESKTOP_AUTOCLOSE）置位；runDesktop 窗口循环读到即收尾退出。
	frontendQuitReq      int32
	trayWndProcCb        = windows.NewCallback(trayWndProcEntry)
	taskbarCreatedLit, _ = windows.UTF16PtrFromString("TaskbarCreated")
)

// traySetup 挂托盘图标（UI 线程调用一次；失败只落日志，主流程不受影响）。
func traySetup(app *App) {
	trayApp = app
	hInst, _, _ := procPanelGetModuleHandle.Call(0)
	cls, _ := windows.UTF16PtrFromString(trayWndClassName)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   trayWndProcCb,
		hInstance:     hInst,
		lpszClassName: cls,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))) // 已注册（重复调用）会失败，无碍
	name, _ := windows.UTF16PtrFromString("JXAssistantTray")
	hwnd, _, cerr := procPanelCreateWindow.Call(0, uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(name)), wsPopup, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		log.Printf("托盘窗口创建失败（托盘不可用，不影响主流程）: %v", cerr)
		return
	}
	trayHwnd = hwnd
	if r, _, _ := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(taskbarCreatedLit))); r != 0 {
		trayTaskbarCreated = uint32(r)
	}
	// 图标：exe 资源 RT_GROUP_ICON id=1（与主窗口标题栏同源）；加载失败退回
	// 系统默认应用图标，保证托盘至少可见。
	icon, _, _ := procLoadImageW.Call(hInst, 1 /*MAKEINTRESOURCE(1)*/, 1, /*IMAGE_ICON*/
		0, 0, 0x8040 /*LR_DEFAULTSIZE|LR_SHARED*/)
	if icon == 0 {
		icon, _, _ = procLoadImageW.Call(0, 32512 /*IDI_APPLICATION*/, 1, 0, 0, 0x8040)
	}
	trayIcon = icon
	if !trayNotify(nimAdd) {
		log.Printf("托盘图标挂载失败（窗口关闭将退化为直接退出）")
		return
	}
	log.Printf("托盘图标已挂载：关闭主窗口将驻留后台，双击图标恢复界面")
}

// trayTeardown 摘托盘图标并拆回调窗口（进程整体退出前调用；幂等）。
func trayTeardown() {
	if trayHwnd == 0 {
		return
	}
	nid := trayNID()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	procPanelDestroyWindow.Call(trayHwnd)
	trayHwnd = 0
}

// trayNID 组装当前托盘图标的基础 NOTIFYICONDATAW（回调消息/图标/悬停提示）。
// uFlags 必须三样全带：漏了 NIF_MESSAGE 系统不回投鼠标事件（图标挂上了但
// 双击/右键全无反应），漏了 NIF_ICON / NIF_TIP 则 hIcon / szTip 被忽略。
func trayNID() notifyIconDataW {
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = trayHwnd
	nid.uID = 1
	nid.uCallbackMessage = trayCallbackMsg
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.hIcon = trayIcon
	tip, _ := windows.UTF16FromString(desktopAppTitle)
	copy(nid.szTip[:], tip)
	return nid
}

// trayNotify 执行一次 Shell_NotifyIconW（op = nimAdd / nimModify / nimDelete）。
func trayNotify(op uintptr) bool {
	if trayHwnd == 0 {
		return false
	}
	nid := trayNID()
	r, _, _ := procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid)))
	return r != 0
}

// trayBalloon 弹托盘气泡通知（NIF_INFO，经 NIM_MODIFY）。
func trayBalloon(title, text string) {
	if trayHwnd == 0 {
		return
	}
	nid := trayNID()
	nid.uFlags = nifInfo
	nid.dwInfoFlags = niifInfo
	info, _ := windows.UTF16FromString(text)
	titleLit, _ := windows.UTF16FromString(title)
	copy(nid.szInfo[:], info)
	copy(nid.szInfoTitle[:], titleLit)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// hideMainWindowToTray 用户关闭主窗口（✕ / Alt+F4）：隐藏进托盘而非退出进程。
// 首次给一次气泡引导（之后安静隐藏，不打扰）。
func hideMainWindowToTray(hwnd uintptr) {
	log.Printf("主窗口关闭 → 隐藏进托盘（后端与会话/任务继续；双击托盘图标恢复界面）")
	procDesktopShowWindow.Call(hwnd, 0) // SW_HIDE
	if atomic.CompareAndSwapInt32(&trayHiddenOnce, 0, 1) {
		trayBalloon(desktopAppTitle,
			"程序已驻留后台：会话与运行中的任务继续执行。\n双击托盘图标随时回到界面；右键图标可刷新界面或退出。")
	}
}

// trayShowMainWindow 从托盘恢复主窗口（隐藏/最小化都能拉起来）。只在最小化时
// 用 SW_RESTORE（它会把最大化窗口降级成普通大小）；隐藏窗口用 SW_SHOW 直接
// 显示，最大化状态原样保留。前端正在重建的间隙里没有主窗口——本次唤起忽略，
// 新窗口建好后自然可见。
func trayShowMainWindow() {
	hwnd := mainWndHwnd.Load()
	if hwnd == 0 {
		log.Printf("托盘：主窗口当前不在（重建间隙），本次唤起忽略")
		return
	}
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procDesktopShowWindow.Call(hwnd, 9) // SW_RESTORE
	} else {
		procDesktopShowWindow.Call(hwnd, 5) // SW_SHOW
	}
	procSetForegroundWindow.Call(hwnd)
}

// trayReloadUI 刷新前端界面：页面卡死/渲染异常时用户可手动恢复（等价浏览器
// 的 F5）。shellEval 即主界面 WebView2 的 ExecuteScript（runDesktop 注入）。
func trayReloadUI() {
	if trayApp == nil || trayApp.shellEval == nil {
		log.Printf("托盘：界面刷新通道未就绪")
		return
	}
	log.Printf("托盘：刷新前端界面（location.reload）")
	trayApp.shellEval("location.reload()")
}

// trayRequestExit 托盘菜单「退出」：置退出请求，再按主窗口在与否选择唤起
// 退出路径——窗口在则投程序化 WM_CLOSE（正常销毁，热重启同款路径），不在则
// 直接 PostQuitMessage 让 UI 消息循环收口；两条路都汇到窗口循环的退出分支。
func trayRequestExit() {
	log.Printf("托盘：请求退出程序")
	atomic.StoreInt32(&frontendQuitReq, 1)
	if hwnd := mainWndHwnd.Load(); hwnd != 0 {
		procPostMessageW.Call(hwnd, 0x0010, 0, 0) // WM_CLOSE（程序化，不被关闭拦截）
	} else {
		procPostQuitMessage.Call(0)
	}
}

// trayWndProcEntry 托盘回调窗口过程：图标事件 + TaskbarCreated 重挂。
func trayWndProcEntry(hwnd, msg, wp, lp uintptr) uintptr {
	// 原生消息栈上 panic 会无声带走整个进程：就地兜住落日志（同
	// mainWndSubclassProc 的纪律）。
	defer func() {
		if r := recover(); r != nil {
			log.Printf("托盘窗口过程 panic（已兜住）: %v\n%s", r, debug.Stack())
		}
	}()
	if trayTaskbarCreated != 0 && msg == uintptr(trayTaskbarCreated) {
		// explorer 重启：图标随旧任务栏消失，重新挂上
		trayNotify(nimAdd)
		return 0
	}
	if msg == trayCallbackMsg {
		switch lp {
		case wmLButtonUp, wmLButtonDblClk:
			trayShowMainWindow()
		case wmRButtonUp:
			trayShowMenu(hwnd)
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// trayShowMenu 托盘右键菜单（打开主界面 / 刷新界面 / 退出）。TrackPopupMenu
// 前后配合 SetForegroundWindow + WM_NULL：否则菜单在桌面别处点击时不收起
// （Win32 通知图标的经典坑）。
func trayShowMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	mOpen, _ := windows.UTF16PtrFromString("打开主界面")
	mReload, _ := windows.UTF16PtrFromString("刷新界面")
	mExit, _ := windows.UTF16PtrFromString("退出")
	procAppendMenuW.Call(menu, 0 /*MF_STRING*/, trayCmdOpen, uintptr(unsafe.Pointer(mOpen)))
	procAppendMenuW.Call(menu, 0, trayCmdReload, uintptr(unsafe.Pointer(mReload)))
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	procAppendMenuW.Call(menu, 0, trayCmdExit, uintptr(unsafe.Pointer(mExit)))
	var pt pointStruct
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(hwnd)
	// x64 下 TrackPopupMenu 的 x、y 是各自独立的参数位，不能像 32 位那样
	// 打包成 x|y<<16 传一个槽（菜单会弹去屏幕外）；负坐标（多显示器左侧）
	// 经 int32→uintptr 二进制补码直传。
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmBottomAlign|tpmReturnCmd,
		uintptr(int32(pt.x)), uintptr(int32(pt.y)), 0, hwnd, 0)
	procPostMessageW.Call(hwnd, wmNull, 0, 0)
	switch cmd {
	case trayCmdOpen:
		trayShowMainWindow()
	case trayCmdReload:
		trayReloadUI()
	case trayCmdExit:
		trayRequestExit()
	}
}
