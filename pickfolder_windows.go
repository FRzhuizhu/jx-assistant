//go:build windows

package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
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
	comdlg32                        = syscall.NewLazyDLL("comdlg32.dll")
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
	procGetOpenFileNameW            = comdlg32.NewProc("GetOpenFileNameW")
	procCommDlgExtendedError        = comdlg32.NewProc("CommDlgExtendedError")
)

const (
	coinitApartmentThreaded = 2 // COINIT_APARTMENTTHREADED（STA，对话框要求）
	clsctxInprocServer      = 1
	fosPickFolders          = 0x20 // 只选文件夹
	fosForceFileSystem      = 0x40 // 限定真实文件系统路径
	fosAllowMultiSelect     = 0x200
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
	vtGetResults // IFileOpenDialog::GetResults：多选结果集（IShellItemArray）
)

// IShellItemArray 的 vtable 槽位（0-2 为 IUnknown；BindToHandler=3、GetAttributesAt=4）
const (
	vtArrayGetCount  = 5
	vtArrayGetItemAt = 6
)

// pickFolderNative 弹出系统原生「选择文件夹」对话框并阻塞到用户关闭；返回所选目录，
// 用户取消返回空串。COM 的 STA 约定要求调用固定在一个 OS 线程上；pickFolder 必须为 true——传 false 会退化成选文件对话框（默认还挂 *.zip 过滤器），用户根本选不到文件夹。
func pickFolderNative(startDir, title string) (string, error) {
	paths, err := pickDialogNative(startDir, title, true, false, false)
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// pickFileNative 同款 IFileOpenDialog，但选择文件而非文件夹。
// allFilter=false 时限定 ZIP 压缩包（技能安装场景，附「所有文件」兜底）；
// allFilter=true 时不设过滤器（任意文件，聊天上传场景）。
// multi=true 的多选走 pickFilesComDlg（见其头注：IFileOpenDialog 的多选提取
// 链 GetResults→GetCount 在真机上原生层空指针写，进程级硬死拦不住）。
func pickFileNative(startDir, title string, allFilter, multi bool) ([]string, error) {
	if multi {
		return pickDialogComDlg(startDir, title, allFilter)
	}
	return pickDialogNative(startDir, title, false, allFilter, false)
}

// pickDialogComDlg 用 comdlg32 的 GetOpenFileNameW 弹「打开」对话框（多选）。
//
// 为什么弃用 IFileOpenDialog 的多选路径（FOS_ALLOWMULTISELECT + GetResults
// 取 IShellItemArray → GetCount/GetItemAt）：确认选择后在 GetCount 调用处
// 发生原生层空指针写（Exception 0xC0000005 write 0x0，stderr 转储定位到
// pickDialogSTA 的 GetCount 行）——syscall 内的硬件异常 defer/recover 拦不住，
// 整个进程直接死掉，桌面壳表现为「选完文件点打开就闪退」。同一对话框的单选
// 提取（GetResult，选目录/装技能 ZIP 在用）长期稳定，问题只出在多选提取链。
// GetOpenFileNameW 是微软自带的完整实现（现代系统上内部同样托管新式对话框），
// 结果经缓冲区回传，没有手工 vtable 调用，多选语义由系统自己解析。
// 取消返回 nil；缓冲区不够时给出可读错误。
func pickDialogComDlg(startDir, title string, allFilter bool) ([]string, error) {
	const (
		ofnHideReadOnly  = 0x4
		ofnNoChangeDir   = 0x8
		ofnAllowMulti    = 0x200
		ofnFileMustExist = 0x1000
		ofnExplorer      = 0x80000
	)
	const bufLen = 1 << 16 // 多选路径串都装进来，64KB ≈ 数百个文件
	buf := make([]uint16, bufLen)
	// 过滤器串：目标段 + 「所有文件」兜底段，每段 名\0模式\0，整体双 NUL 收尾
	name1, spec1 := "所有文件", "*.*"
	if !allFilter {
		name1, spec1 = "ZIP 压缩包", "*.zip"
	}
	var flt []uint16
	for _, s := range []string{name1, spec1, "所有文件", "*.*"} {
		u, _ := syscall.UTF16FromString(s) // 自带结尾 NUL
		flt = append(flt, u...)
	}
	flt = append(flt, 0)
	titleP, _ := syscall.UTF16PtrFromString(title)
	var dirP uintptr
	if startDir != "" {
		if p, err := syscall.UTF16PtrFromString(startDir); err == nil {
			dirP = uintptr(unsafe.Pointer(p))
		}
	}
	ofn := openFileNameW{
		lStructSize:     uint32(unsafe.Sizeof(openFileNameW{})),
		lpstrFilter:     uintptr(unsafe.Pointer(&flt[0])),
		nFilterIndex:    1,
		lpstrFile:       uintptr(unsafe.Pointer(&buf[0])),
		nMaxFile:        uint32(bufLen),
		lpstrInitialDir: dirP,
		lpstrTitle:      uintptr(unsafe.Pointer(titleP)),
		Flags:           ofnExplorer | ofnAllowMulti | ofnFileMustExist | ofnHideReadOnly | ofnNoChangeDir,
	}
	raiseDialogToForeground(title) // 同款后台进程置前（comdlg 对话框也是 #32770）
	r, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		if e, _, _ := procCommDlgExtendedError.Call(); e != 0 {
			// 0x3003=FNERR_BUFFERTOOOSMALL 等
			return nil, fmt.Errorf("文件选择框错误 (0x%x)", e)
		}
		return nil, nil // 用户取消
	}
	// 解析缓冲区：单个字符串 = 完整路径；多选 = 目录\0文件1\0文件2\0\0
	var strs []string
	for cur := buf; len(cur) > 0; {
		n := 0
		for n < len(cur) && cur[n] != 0 {
			n++
		}
		if n == 0 {
			break // 到达双 NUL 终点
		}
		strs = append(strs, syscall.UTF16ToString(cur[:n]))
		if n == len(cur) {
			break
		}
		cur = cur[n+1:]
	}
	if len(strs) == 0 {
		return nil, nil
	}
	if len(strs) == 1 {
		return []string{strs[0]}, nil
	}
	paths := make([]string, 0, len(strs)-1)
	for _, f := range strs[1:] {
		paths = append(paths, strs[0]+"\\"+f)
	}
	return paths, nil
}

// openFileNameW 即 Win32 OPENFILENAMEW（x64 布局，Go 自动对齐与 C 一致）。
type openFileNameW struct {
	lStructSize       uint32
	HwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       uintptr
	lpstrCustomFilter uintptr
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         uintptr
	nMaxFile          uint32
	lpstrFileTitle    uintptr
	nMaxFileTitle     uint32
	lpstrInitialDir   uintptr
	lpstrTitle        uintptr
	Flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       uintptr
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    uintptr
	pvReserved        uintptr
	dwReserved        uint32
	FlagsEx           uint32
}

func pickDialogNative(startDir, title string, pickFolder, allFilter, multi bool) ([]string, error) {
	type pickResult struct {
		paths []string
		err   error
	}
	ch := make(chan pickResult, 1)
	go func() {
		// COM/vtable 手工调用没有编译期保证，任何一处崩（空指针、坏 hr 分支）
		// 都会 panic；这颗协程没有 net/http 的兜底 recover，裸 panic 直接杀掉
		// 整个进程（桌面壳窗口瞬关）。在这里拦下、落日志、按失败返回。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[pick-dialog] 对话框协程 panic（已拦截）: %v\n%s", r, debug.Stack())
				ch <- pickResult{err: fmt.Errorf("文件选择框内部错误: %v", r)}
			}
		}()
		runtime.LockOSThread()
		paths, err := pickDialogSTA(startDir, title, pickFolder, allFilter, multi)
		runtime.UnlockOSThread()
		ch <- pickResult{paths, err}
	}()
	r := <-ch
	return r.paths, r.err
}

func pickDialogSTA(startDir, title string, pickFolder, allFilter, multi bool) ([]string, error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr != 0 && hr != 1 { // S_FALSE=1 表示本线程已初始化，同样可用
		return nil, fmt.Errorf("COM 初始化失败 (0x%x)", hr)
	}
	defer procCoUninitialize.Call()

	clsid, err := parseGUID("{DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}") // CLSID_FileOpenDialog
	if err != nil {
		return nil, err
	}
	iid, err := parseGUID("{D57C7288-D4AD-4768-BE02-9D969532D960}") // IID_IFileOpenDialog
	if err != nil {
		return nil, err
	}
	var dlg unsafe.Pointer
	if hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&dlg))); hr != 0 {
		return nil, fmt.Errorf("创建文件对话框失败 (0x%x)", hr)
	}
	vt := *(**[29]uintptr)(unsafe.Pointer(dlg))
	defer syscall.SyscallN(vt[2], uintptr(dlg)) // IUnknown::Release

	var opts uintptr
	if hr, _, _ = syscall.SyscallN(vt[vtGetOptions], uintptr(dlg), uintptr(unsafe.Pointer(&opts))); hr != 0 {
		return nil, fmt.Errorf("GetOptions 失败 (0x%x)", hr)
	}
	if pickFolder {
		opts |= fosPickFolders | fosForceFileSystem
	} else {
		opts |= fosForceFileSystem
		if multi {
			opts |= fosAllowMultiSelect
		}
	}
	if hr, _, _ = syscall.SyscallN(vt[vtSetOptions], uintptr(dlg), opts); hr != 0 {
		return nil, fmt.Errorf("SetOptions 失败 (0x%x)", hr)
	}
	if !pickFolder && !allFilter {
		// COMDLG_FILTERSPEC{lpwcsName, lpwcsSpec}；默认扩展名兜 .zip
		nameZip, _ := syscall.UTF16PtrFromString("ZIP 压缩包 (*.zip)")
		nameAll, _ := syscall.UTF16PtrFromString("所有文件 (*.*)")
		specZip, _ := syscall.UTF16PtrFromString("*.zip")
		specAll, _ := syscall.UTF16PtrFromString("*.*")
		specs := []comdlgFilterSpec{
			{unsafe.Pointer(nameZip), unsafe.Pointer(specZip)},
			{unsafe.Pointer(nameAll), unsafe.Pointer(specAll)},
		}
		if hr, _, _ = syscall.SyscallN(vt[vtSetFileTypes], uintptr(dlg), 2, uintptr(unsafe.Pointer(&specs[0]))); hr != 0 {
			return nil, fmt.Errorf("SetFileTypes 失败 (0x%x)", hr)
		}
		syscall.SyscallN(vt[vtSetDefaultExtension], uintptr(dlg), uintptr(unsafe.Pointer(specZip)))
	}
	if t, e := syscall.UTF16PtrFromString(title); e == nil {
		syscall.SyscallN(vt[vtSetTitle], uintptr(dlg), uintptr(unsafe.Pointer(t)))
	}
	if si := shellItemFromPath(startDir); si != nil {
		syscall.SyscallN(vt[vtSetFolder], uintptr(dlg), uintptr(unsafe.Pointer(si))) // 起始定位目录
		sivt := *(**[8]uintptr)(unsafe.Pointer(si))
		syscall.SyscallN(sivt[2], uintptr(si))
	}

	raiseDialogToForeground(title) // Show 期间把对话框提到前台（见函数注释）

	if hr, _, _ = syscall.SyscallN(vt[vtShow], uintptr(dlg), 0); hr != 0 {
		if hr == hresultCancelled {
			return nil, nil
		}
		return nil, fmt.Errorf("对话框显示失败 (0x%x)", hr)
	}

	if multi && !pickFolder {
		// 多选：GetResults 拿 IShellItemArray，逐项取文件系统路径
		var arr unsafe.Pointer
		if hr, _, _ = syscall.SyscallN(vt[vtGetResults], uintptr(dlg), uintptr(unsafe.Pointer(&arr))); hr != 0 {
			return nil, fmt.Errorf("GetResults 失败 (0x%x)", hr)
		}
		avt := *(**[8]uintptr)(unsafe.Pointer(arr))
		defer syscall.SyscallN(avt[2], uintptr(arr)) // IUnknown::Release
		var cnt uintptr
		if hr, _, _ = syscall.SyscallN(avt[vtArrayGetCount], uintptr(arr), uintptr(unsafe.Pointer(&cnt))); hr != 0 {
			return nil, fmt.Errorf("GetCount 失败 (0x%x)", hr)
		}
		paths := []string{}
		for i := uintptr(0); i < cnt; i++ {
			var item unsafe.Pointer
			if hr, _, _ = syscall.SyscallN(avt[vtArrayGetItemAt], uintptr(arr), i, uintptr(unsafe.Pointer(&item))); hr != 0 {
				continue
			}
			if p, ok := itemFilePath(item); ok {
				paths = append(paths, p)
			}
		}
		return paths, nil
	}

	var item unsafe.Pointer
	if hr, _, _ = syscall.SyscallN(vt[vtGetResult], uintptr(dlg), uintptr(unsafe.Pointer(&item))); hr != 0 {
		return nil, fmt.Errorf("GetResult 失败 (0x%x)", hr)
	}
	if p, ok := itemFilePath(item); ok {
		return []string{p}, nil
	}
	return nil, fmt.Errorf("GetDisplayName 失败")
}

// itemFilePath 从 IShellItem 取文件系统路径（SIGDN_FILESYSPATH），
// 无论成败都负责释放 item 的引用。
func itemFilePath(item unsafe.Pointer) (string, bool) {
	ivt := *(**[8]uintptr)(unsafe.Pointer(item))
	defer syscall.SyscallN(ivt[2], uintptr(item)) // IUnknown::Release
	var pstr *uint16
	if hr, _, _ := syscall.SyscallN(ivt[5], uintptr(item), sigdnFilesysPath, uintptr(unsafe.Pointer(&pstr))); hr != 0 || pstr == nil {
		return "", false
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pstr)))
	n, _, _ := procLstrlenW.Call(uintptr(unsafe.Pointer(pstr)))
	return syscall.UTF16ToString(unsafe.Slice(pstr, n)), true
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

var pickFolderMu sync.Mutex // 同时最多弹一个选择框（文件夹/文件共用），避免叠窗

// comdlgFilterSpec 即 Win32 COMDLG_FILTERSPEC：过滤器显示名 + 通配模式。
type comdlgFilterSpec struct {
	name unsafe.Pointer // LPCWSTR
	spec unsafe.Pointer // LPCWSTR
}

// pickFileSerialized 选文件的串行化包装，与文件夹选择框共用一把锁。
func pickFileSerialized(startDir, title string, allFilter, multi bool) ([]string, error) {
	pickFolderMu.Lock()
	defer pickFolderMu.Unlock()
	return pickFileNative(startDir, title, allFilter, multi)
}

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
		vkMenu         = 0x12
		keyeventfKeyUp = 0x0002
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
