//go:build !windows

package main

// 停止看门狗的子进程补刀（非 Windows）：按 PID 强杀单个进程。
// 整树终止在正常路径由进程组（proctree_other.go 的 Setpgid + 杀 -pgid）与
// WaitDelay 兜底；这里只是「ctx 已取消、进程却还活着」时的补刀，尽力而为。

import "syscall"

func killChildTree(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
