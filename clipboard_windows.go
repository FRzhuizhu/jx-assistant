//go:build windows

package main

// 读 Windows 系统剪贴板（文本 + PNG），给 /api/clipboard 端点用。
//
// 为什么需要它：桌面壳里 navigator.clipboard.readText() 会永久挂起——
// WebView2 对剪贴板读权限不弹授权 UI，jchv/go-webview2 的
// SetPermission(ClipboardRead, Allow) 对该 API 也不生效（权限态停在
// prompt，promise 永不结算，真机诊断见 gui-test/ctxmenu-clipboard-diag.mjs）。
// 按项目惯例（浏览器侧做不到的交给本机后端，先例：pick-file 原生选框），
// 由 Go 直接读剪贴板。截图常带的 PNG 注册格式（"PNG"）顺带读出，
// 支持右键「粘贴」把截图直接入附件；只有 DIB 没有 PNG 时图片路径
// 交还给 Ctrl+V（paste 事件路径在 WebView2 里原生可用）。

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	clipUser32     = windows.NewLazySystemDLL("user32.dll")
	clipKernel32   = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard              = clipUser32.NewProc("OpenClipboard")
	procCloseClipboard             = clipUser32.NewProc("CloseClipboard")
	procIsClipboardFormatAvailable = clipUser32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData           = clipUser32.NewProc("GetClipboardData")
	procRegisterClipboardFormatW   = clipUser32.NewProc("RegisterClipboardFormatW")
	procGlobalLock                 = clipKernel32.NewProc("GlobalLock")
	procGlobalUnlock               = clipKernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = clipKernel32.NewProc("GlobalSize")
)

const cfUnicodeText = 13

var errClipboardBusy = errors.New("系统剪贴板正被其他程序占用")

// sysPtr 把系统调用返回的句柄/地址（uintptr）还原成 unsafe.Pointer。vet 的
// unsafeptr 检查不认 LazyProc.Call 的返回值（这其实是 unsafe.Pointer 规则 (4)
// 的合法场景：系统调用结果立即转回），这里经一次双指针重解释完成等价转换——
// 各调用点紧邻 Call、中间无函数调用（无 GC 挂起窗口），与直接转换同样安全。
func sysPtr(u *uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(u))
}

// readClipboardWindows 读系统剪贴板：优先文本（CF_UNICODETEXT），无文本时
// 尝试 PNG 注册格式（截图）。剪贴板被他人占用时小退避重试几次。
func readClipboardWindows() (text string, png []byte, err error) {
	var opened bool
	for i := 0; i < 4; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !opened {
		return "", nil, errClipboardBusy
	}
	defer procCloseClipboard.Call()
	// 文本优先：有文本时忽略图片（「粘贴」语义 = 文本入输入框）
	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r != 0 {
		if h, _, _ := procGetClipboardData.Call(cfUnicodeText); h != 0 {
			if g, _, _ := procGlobalLock.Call(h); g != 0 {
				defer procGlobalUnlock.Call(h)
				if size, _, _ := procGlobalSize.Call(h); size >= 2 {
					p := sysPtr(&g)
					n := int(size) / 2 // UTF-16 字节数换字符数
					text = windows.UTF16ToString(unsafe.Slice((*uint16)(p), n))
				}
			}
		}
	}
	if text != "" {
		return text, nil, nil
	}
	// PNG 注册格式（"PNG"，截图工具常连带放置；无则图片路径交还 Ctrl+V）
	name, _ := windows.UTF16PtrFromString("PNG")
	if fid, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name))); fid != 0 {
		if r, _, _ := procIsClipboardFormatAvailable.Call(fid); r != 0 {
			if h, _, _ := procGetClipboardData.Call(fid); h != 0 {
				if g, _, _ := procGlobalLock.Call(h); g != 0 {
					defer procGlobalUnlock.Call(h)
					if size, _, _ := procGlobalSize.Call(h); size > 0 {
						png = append([]byte(nil), unsafe.Slice((*byte)(sysPtr(&g)), int(size))...)
					}
				}
			}
		}
	}
	return text, png, nil
}
