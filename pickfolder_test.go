//go:build windows

package main

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// 不弹窗的冒烟测试：验证 GUID 解析、COM 实例化与 vtable 槽位调用正确。
// 不调用会阻塞等待用户的 Show。
func TestPickFolderCOMSmoke(t *testing.T) {
	g, err := parseGUID("{DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}")
	if err != nil {
		t.Fatal(err)
	}
	if g.Data1 != 0xDC1C5A9C || g.Data2 != 0xE88A || g.Data3 != 0x4dde {
		t.Fatalf("parseGUID 字段错误: %+v", g)
	}
	if want := [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}; g.Data4 != want {
		t.Fatalf("parseGUID Data4 错误: %X", g.Data4)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr != 0 && hr != 1 {
		t.Fatalf("CoInitializeEx 失败: 0x%x", hr)
	}
	defer procCoUninitialize.Call()

	clsid, _ := parseGUID("{DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}")
	iid, _ := parseGUID("{D57C7288-D4AD-4768-BE02-9D969532D960}")
	var dlg unsafe.Pointer
	if hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&dlg))); hr != 0 {
		t.Fatalf("CoCreateInstance 失败: 0x%x", hr)
	}
	vt := *(**[29]uintptr)(unsafe.Pointer(dlg))
	defer syscall.SyscallN(vt[2], uintptr(dlg))

	var opts uintptr
	if hr, _, _ = syscall.SyscallN(vt[vtGetOptions], uintptr(dlg), uintptr(unsafe.Pointer(&opts))); hr != 0 {
		t.Fatalf("GetOptions 失败: 0x%x", hr)
	}
	if hr, _, _ = syscall.SyscallN(vt[vtSetOptions], uintptr(dlg), opts|fosPickFolders|fosForceFileSystem); hr != 0 {
		t.Fatalf("SetOptions 失败: 0x%x", hr)
	}
	if p, err := syscall.UTF16PtrFromString("测试标题"); err == nil {
		if hr, _, _ = syscall.SyscallN(vt[vtSetTitle], uintptr(dlg), uintptr(unsafe.Pointer(p))); hr != 0 {
			t.Fatalf("SetTitle 失败: 0x%x", hr)
		}
	}

	// shellItemFromPath：给一个一定存在的目录
	if si := shellItemFromPath(`C:\Windows`); si == nil {
		t.Fatal("shellItemFromPath(C:\\Windows) 返回 nil")
	} else {
		sivt := *(**[8]uintptr)(unsafe.Pointer(si))
		syscall.SyscallN(sivt[2], uintptr(si))
	}
	if si := shellItemFromPath(`Z:\不存在的目录\xyz`); si != nil {
		sivt := *(**[8]uintptr)(unsafe.Pointer(si))
		syscall.SyscallN(sivt[2], uintptr(si))
		t.Fatal("不存在路径应返回 nil")
	}
}
