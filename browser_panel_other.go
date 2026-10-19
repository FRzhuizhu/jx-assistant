//go:build !windows

package main

// 非 Windows 桌面形态桩：桌面壳本身只在 Windows 提供（desktop_other.go），
// 原生浏览器面板随之恒为 nil（embeddedTarget 回退到无头 chromedp 宿主）。

func newBrowserPanel(app *App, dispatch func(f func()), mainWnd uintptr, sid string) browserTarget {
	return nil
}
