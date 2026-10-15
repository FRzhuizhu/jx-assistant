//go:build windows

package main

// 停止看门狗的子进程补刀（Windows）：按 PID 强杀进程及其整棵子树。

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

// killChildTree 按 PID 强杀进程及其整棵子树（taskkill /T /F）。这是停止看门狗
// 硬终结（forceStopRun → killSessionChildren）的补刀手段，只在「ctx 已取消、
// 进程却还活着」的极端卡死时调用；常规终止一律走 ctx 取消（CommandContext
// Kill + Job Object 整树 + WaitDelay 收口），不走这里。
func killChildTree(pid int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	hideConsoleWindow(cmd) // 补刀也要压窗：不给桌面冒黑框
	_ = cmd.Run()
}
