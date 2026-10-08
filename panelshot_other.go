//go:build !windows

package main

import "fmt"

// captureRightPanel 的非 Windows 桩：网页/Linux/安卓形态没有桌面壳右栏。
func (a *App) captureRightPanel() ([]byte, error) {
	return nil, fmt.Errorf("右侧栏截图仅桌面版（Windows）支持")
}
