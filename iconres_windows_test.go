//go:build windows

package main

// 桌面窗口图标资源探针：go-webview2 的 WindowOptions.IconId 走
// LoadImageW(hInst, id, IMAGE_ICON, LR_DEFAULTSIZE)，本测试按同一路径
// 断言 RT_GROUP_ICON 资源号 1 可加载——保证打出来的 exe 窗口/任务栏
// 真正显示 LOGO，而不是默认白图标。

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestDesktopIconResource(t *testing.T) {
	var h windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &h); err != nil {
		t.Fatalf("GetModuleHandleEx: %v", err)
	}
	procLoadImageW := desktopUser32.NewProc("LoadImageW")
	const (
		imageIcon = 1
		lrDefaultSize = 0x40
		lrShared      = 0x8000
	)
	icon, _, _ := procLoadImageW.Call(uintptr(h), uintptr(1), imageIcon, 0, 0, lrDefaultSize|lrShared)
	if icon == 0 {
		t.Fatal("RT_GROUP_ICON 资源 id=1 加载失败：桌面窗口将退回默认图标（检查 go-winres.json 图标配置）")
	}
}
