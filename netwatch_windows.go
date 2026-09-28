//go:build windows

package main

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// watchNetworkChange 监听本机地址表变化（iphlpapi!NotifyAddrChange：挂起重叠 I/O，
// 地址一变就返回），变化时回调 onChange——「网络环境变化 → 立刻重新注册中继」。
//
// 零轮询：没变化时这个调用一直挂在系统里，不占 CPU、不产生任何周期性动作。
// 任何失败一律静默降级（没有它，链路也有 20s ping + 僵死看门狗兜底，只是重新
// 注册慢一点）——增值能力不许影响主流程。
func watchNetworkChange(stop <-chan struct{}, onChange func()) {
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return
	}
	go func() {
		defer windows.CloseHandle(ev)
		iphlpapi := windows.NewLazySystemDLL("iphlpapi.dll")
		notifyAddrChange := iphlpapi.NewProc("NotifyAddrChange")
		cancelIPChangeNotify := iphlpapi.NewProc("CancelIPChangeNotify")

		// ov 必须在请求挂起期间一直有效；显式 KeepAlive 防止被 GC 提前回收
		var ov windows.Overlapped
		ov.HEvent = ev
		r, _, _ := notifyAddrChange.Call(0, uintptr(unsafe.Pointer(&ov)))
		if r != 0 && windows.Errno(r) != windows.ERROR_IO_PENDING {
			return
		}
		changed := make(chan struct{})
		go func() {
			_, _ = windows.WaitForSingleObject(ev, windows.INFINITE)
			close(changed)
		}()
		select {
		case <-changed:
			runtime.KeepAlive(&ov)
			onChange()
		case <-stop:
			// 取消挂起的变更通知，别把请求留在系统里
			_, _, _ = cancelIPChangeNotify.Call(uintptr(unsafe.Pointer(&ov)))
		}
	}()
}
