//go:build !windows

package main

import "os/exec"

// gitHideWindow 非 Windows 平台无需处理（子进程跟随当前终端，不会弹窗）。
func gitHideWindow(cmd *exec.Cmd) {}
