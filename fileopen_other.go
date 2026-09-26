//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

// openHideWindow 非 Windows 平台无需处理（子进程跟随当前终端，不会弹窗）。
func openHideWindow(cmd *exec.Cmd) {}

// openPathWithDefaultApp 用系统默认程序打开一个文件（macOS 用 open，其余
// 桌面 Linux 用 xdg-open）。不支持的平台返回明确错误，由上层转成提示。
func openPathWithDefaultApp(abs string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("open", abs)
		openHideWindow(cmd)
		return cmd.Start()
	case "linux":
		cmd := exec.Command("xdg-open", abs)
		openHideWindow(cmd)
		return cmd.Start()
	}
	return fmt.Errorf("当前平台（%s）不支持用默认程序打开文件", runtime.GOOS)
}

// revealInExplorer 非 Windows 平台没有"资源管理器定位"这一概念，退回用
// 系统文件管理器打开该文件所在目录。
func revealInExplorer(abs string) error {
	dir := filepath.Dir(abs)
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("open", "-R", abs)
		openHideWindow(cmd)
		return cmd.Start()
	case "linux":
		cmd := exec.Command("xdg-open", dir)
		openHideWindow(cmd)
		return cmd.Start()
	}
	return fmt.Errorf("当前平台（%s）不支持在文件夹中定位文件", runtime.GOOS)
}
