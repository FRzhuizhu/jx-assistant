//go:build !windows

package main

// mainClientSize 网页/移动形态没有桌面主窗口：返回 0,0，调用方跳过比例
// 校准（handleBrowserPanelRect 在非桌面形态本就走不到原生面板分支）。
func mainClientSize() (int, int) { return 0, 0 }
