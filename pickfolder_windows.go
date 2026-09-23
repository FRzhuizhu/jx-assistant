//go:build windows

package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// 系统原生“选择文件夹”对话框（IFileOpenDialog + FOS_PICKFOLDERS，
// 与浏览器目录选择器同款）。浏览器出于安全不回传绝对路径，
// 而本服务运行在用户本机，可直接由后端弹框并拿到真实路径。

var (
	ole32                           = syscall.NewLazyDLL("ole32.dll")
	shell32                         = syscall.NewLazyDLL("shell32.dll")
	kernel32                        = syscall.NewLazyDLL("kernel32.dll")
	user32                          = syscall.NewLazyDLL("user32.dll")
	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	procCoUninitialize              = ole32.NewProc("CoUninitialize")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procLstrlenW                    = kernel32.NewProc("lstrlenW")
	procGetCurrentProcessID         = kernel32.NewProc("GetCurrentProcessId")
	procKeybdEvent                  = user32.NewProc("keybd_event")
	procFindWindowW                 = user32.NewProc("FindWindowW")
	procSetForegroundWindow         = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop            = user32.NewProc("BringWindowToTop")
	procGetWindowThreadProcessId    = user32.NewProc("GetWindowThreadProcessId")
)

const (
	coinitApartmentThreaded = 2 // COINIT_APARTMENTTHREADED（STA，对话框要求）
	clsctxInprocServer      = 1
	fosPickFolders          = 0x20 // 只选文件夹
	fosForceFileSystem      = 0x40 // 限定真实文件系统路径
	sigdnFilesysPath        = 0x80058000
	hresultCancelled        = 0x800704C7 // ERROR_CANCELLED(1223)：用户点了取消/关闭
)

// IFileDialog 的 vtable 槽位（0-2 为 IUnknown）
const (
	vtShow = 3 + iota
	vtSetFileTypes
	vtSetFileTypeIndex
	vtGetFileTypeIndex
	vtAdvise
	vtUnadvise
	vtSetOptions
	vtGetOptions
	vtSetDefaultFolder
	vtSetFolder
	vtGetFolder
	vtGetCurrentSelection
	vtSetFileName
	vtGetFileName
	vtSetTitle
	vtSetOkButtonLabel
	vtSetFileNameLabel
	vtGetResult
	vtAddPlace
	vtSetDefaultExtension
	vtClose
	vtSetClientGuid
	vtClearClientData
	vtSetFilter
)

// pickFolderNative 弹出选择框并阻塞到用户关闭；返回所选目录，
// 用户取消返回空串。COM 的 STA 约定要求调用固定在一个 OS 线程上。
func pickFolderNative(startDir, title string) (string, error) {
	type pickResult struct {
		path string
		err  error
	}
	ch := make(chan pickResult, 1)
	go func() {
		runtime.LockOSThread()
		path, err := pickFolderSTA(startDir, title)
		runtime.UnlockOSThread()
		ch <- pickResult{path, err}
	}()
	r := <-ch
	return r.path, r.err
}

func pickFolderSTA(startDir, title string) (string, error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr != 0 && hr != 1 { // S_FALSE=1 表示本线程已初始化，同样可用
		return "", fmt.Errorf("COM 初始化失败 (0x%x)", hr)
	}
	defer procCoUninitialize.Call()

	clsid, err := parseGUID("{DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}") // CLSID_FileOpenDialog
	if err != nil {
		return "", err
	}
	iid, err := parseGUID("{D57C7288-D4AD-4768-BE02-9D969532D960}") // IID_IFileOpenDialog
	if err != nil {
		return "", err
	}
	var dlg unsafe.Pointer
	if hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&dlg))); hr != 0 {
		return "", fmt.Errorf("创建文件对话框失败 (0x%x)", hr)
	}
	vt := *(**[29]uintptr)(unsafe.Pointer(dlg))
	defer syscall.SyscallN(vt[2], uintptr(dlg)) // IUnknown::Release

	var opts uintptr
	if hr, _, _ = syscall.SyscallN(vt[vtGetOptions], uintptr(dlg), uintptr(unsafe.Pointer(&opts))); hr != 0 {
		return "", fmt.Errorf("GetOptions 失败 (0x%x)", hr)
	}
	if hr, _, _ = syscall.SyscallN(vt[vtSetOptions], uintptr(dlg), opts|fosPickFolders|fosForceFileSystem); hr != 0 {
		return "", fmt.Errorf("SetOptions 失败 (0x%x)", hr)
	}
	if t, e := syscall.UTF16PtrFromString(title); e == nil {
		syscall.SyscallN(vt[vtSetTitle], uintptr(dlg), uintptr(unsafe.Pointer(t)))
	}
	if si := shellItemFromPath(startDir); si != nil {
		syscall.SyscallN(vt[vtSetFolder], uintptr(dlg), uintptr(si)) // 起始定位目录
		sivt := *(**[8]uintptr)(unsafe.Pointer(si))
		syscall.SyscallN(sivt[2], uintptr(si))
	}

	raiseDialogToForeground(title) // Show 期间把对话框提到前台（见函数注释）

	if hr, _, _ = syscall.SyscallN(vt[vtShow], uintptr(dlg), 0); hr != 0 {
		if hr == hresultCancelled {
			return "", nil
		}
		return "", fmt.Errorf("对话框显示失败 (0x%x)", hr)
	}

	var item unsafe.Pointer
	if hr, _, _ = syscall.SyscallN(vt[vtGetResult], uintptr(dlg), uintptr(unsafe.Pointer(&item))); hr != 0 {
		return "", fmt.Errorf("GetResult 失败 (0x%x)", hr)
	}
	ivt := *(**[8]uintptr)(unsafe.Pointer(item))
	defer syscall.SyscallN(ivt[2], uintptr(item))

	var pstr *uint16
	if hr, _, _ = syscall.SyscallN(ivt[5], uintptr(item), sigdnFilesysPath, uintptr(unsafe.Pointer(&pstr))); hr != 0 || pstr == nil {
		return "", fmt.Errorf("GetDisplayName 失败 (0x%x)", hr)
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pstr)))
	n, _, _ := procLstrlenW.Call(uintptr(unsafe.Pointer(pstr)))
	return syscall.UTF16ToString(unsafe.Slice(pstr, n)), nil
}

// shellItemFromParsingName 把路径包成 IShellItem（失败返回 nil，调用方忽略即可）。
func shellItemFromPath(dir string) unsafe.Pointer {
	if dir == "" {
		return nil
	}
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return nil
	}
	iid, err := parseGUID("{43826D1E-E718-42EE-BC55-A1E261C37BFE}") // IID_IShellItem
	if err != nil {
		return nil
	}
	var si unsafe.Pointer
	if hr, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&si))); hr != 0 {
		return nil
	}
	return si
}

var pickFolderMu sync.Mutex // 同时最多弹一个选择框，避免叠窗

// parseGUID 解析 "{XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX}" 形式的 GUID。
func parseGUID(s string) (syscall.GUID, error) {
	var g syscall.GUID
	h := strings.ReplaceAll(strings.Trim(s, "{}"), "-", "")
	if len(h) != 32 {
		return g, errors.New("GUID 格式错误: " + s)
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return g, err
	}
	g.Data1 = binary.BigEndian.Uint32(b[0:4])
	g.Data2 = binary.BigEndian.Uint16(b[4:6])
	g.Data3 = binary.BigEndian.Uint16(b[6:8])
	copy(g.Data4[:], b[8:16])
	return g, nil
}

// raiseDialogToForeground 解决后台进程弹窗不置顶的问题。
// 本服务不是前台进程，Windows 默认禁止它抢占前台——对话框会被创建但压在
// 其它窗口后面，用户看来就是“点了没反应”。两步组合：
//  1. 先模拟一次 ALT 键（keybd_event），让系统认为本进程刚收到用户输入，
//     解除 SetForegroundWindow 的前台锁；
//  2. Show 阻塞期间由独立 goroutine 轮询找到对话框窗口并反复尝试提前台
//     （对话框创建有时机差，第一次尝试可能早于窗口诞生）。
// 必须在调用 Show 的同一线程执行（KeybdEvent 部分影响的是本线程输入状态）。
func raiseDialogToForeground(title string) {
	const (
		vkMenu          = 0x12
		keyeventfKeyUp  = 0x0002
	)
	procKeybdEvent.Call(vkMenu, 0, 0, 0)
	procKeybdEvent.Call(vkMenu, 0, keyeventfKeyUp, 0)

	go func() {
		cls, _ := syscall.UTF16PtrFromString("#32770") // 标准对话框窗口类
		ttl, err := syscall.UTF16PtrFromString(title)
		if err != nil {
			return
		}
		pid, _, _ := procGetCurrentProcessID.Call()
		for i := 0; i < 40; i++ { // 最多 4 秒
			if h, _, _ := procFindWindowW.Call(
				uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(ttl))); h != 0 {
				var wpid uint32
				procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&wpid)))
				if uint32(wpid) == uint32(pid) {
					procSetForegroundWindow.Call(h)
					procBringWindowToTop.Call(h)
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

// pickFolderSerialized 串行化包装，供 HTTP 处理器调用。
func pickFolderSerialized(startDir, title string) (string, error) {
	pickFolderMu.Lock()
	defer pickFolderMu.Unlock()
	return pickFolderNative(startDir, title)
}
