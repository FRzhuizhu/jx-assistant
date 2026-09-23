//go:build windows

package main

import (
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var (
	tu32                     = syscall.NewLazyDLL("user32.dll")
	procGetForegroundWindow  = tu32.NewProc("GetForegroundWindow")
	procTGetWindowThreadPid  = tu32.NewProc("GetWindowThreadProcessId")
	procTPostMessage         = tu32.NewProc("PostMessageW")
	procTFindWindowW         = tu32.NewProc("FindWindowW")
)

// 实弹测试（跑的时候屏幕上会闪现一个选择框并自动关闭）：
// 验证对话框弹出后真的成为前台窗口——这是用户“点了没反应”问题的关键。
func TestPickFolderForegroundLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live test")
	}
	go func() {
		time.Sleep(3000 * time.Millisecond)
		cls, _ := syscall.UTF16PtrFromString("#32770")
		ttl, _ := syscall.UTF16PtrFromString("选择工作目录")
		h, _, _ := procTFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(ttl)))
		if h == 0 {
			t.Log("3 秒时对话框已不在（可能被手动关闭），跳过前台检查")
			return
		}
		fg, _, _ := procGetForegroundWindow.Call()
		var fpid, wpid uint32
		if fg != 0 {
			procTGetWindowThreadPid.Call(fg, uintptr(unsafe.Pointer(&fpid)))
		}
		procTGetWindowThreadPid.Call(h, uintptr(unsafe.Pointer(&wpid)))
		if fg == h || fpid == wpid {
			t.Log("对话框已在前台 ✓")
		} else {
			t.Errorf("对话框不在前台: fg=%x(fpid=%d) dlg=%x(wpid=%d)", fg, fpid, h, wpid)
		}
		procTPostMessage.Call(h, 0x0010, 0, 0) // WM_CLOSE 关掉
	}()
	path, err := pickFolderNative("", "选择工作目录")
	if err != nil {
		t.Fatalf("pickFolderNative: %v", err)
	}
	t.Logf("返回 path=%q（应为空=取消）err=%v", path, err)
}
