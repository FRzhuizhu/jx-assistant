//go:build !windows

package main

import "fmt"

// 非 Windows 平台暂无桌面壳：网页形态（默认浏览器）不受影响。
func runDesktop(app *App, url string) error {
	return fmt.Errorf("桌面壳仅支持 Windows（本平台请用浏览器模式）")
}

func desktopWorkspaceDefault(cfg *Config) (string, bool) { return "", false }

// 渲染模式（设置页「界面渲染」）在非 Windows 平台没有桌面壳，恒为不可用。
func (a *App) displayMode() map[string]any {
	return map[string]any{"desktop": false, "software": false, "reason": ""}
}

func (a *App) setDisplayMode(software bool) error {
	return fmt.Errorf("仅 Windows 桌面版支持渲染模式切换")
}

func guiLogRedirect() {}

func attachParentConsole() {}
