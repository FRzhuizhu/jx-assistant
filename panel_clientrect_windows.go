//go:build windows

package main

import (
	"unsafe"
)

var procPanelGetClientRect = panelUser32.NewProc("GetClientRect")

// mainClientSize 桌面主窗口客户区尺寸（窗口单位）。主 WebView2 精确铺满
// 客户区（绑定库 WM_SIZE → GetClientRect → PutBounds），因此「客户区尺寸 ÷
// 前端上报的视口宽高」就是 CSS px → 子窗口坐标的实测比例——供
// handleBrowserPanelRect 做比例校准，不再依赖「恰好 1:1」的假设。
// 桌面壳未运行（网页形态）或窗口未就绪时返回 0,0，调用方跳过校准。
func mainClientSize() (int, int) {
	hwnd := mainWndHwnd.Load()
	if hwnd == 0 {
		return 0, 0
	}
	var r panelRect
	if ok, _, _ := procPanelGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return 0, 0
	}
	return int(r.Right - r.Left), int(r.Bottom - r.Top)
}
