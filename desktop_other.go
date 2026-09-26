//go:build !windows

package main

import "fmt"

// 非 Windows 平台暂无桌面壳：网页形态（默认浏览器）不受影响。
func runDesktop(app *App, url string) error {
	return fmt.Errorf("桌面壳仅支持 Windows（本平台请用浏览器模式）")
}

func desktopWorkspaceDefault(cfg *Config) bool { return false }

func guiLogRedirect() {}

func attachParentConsole() {}
