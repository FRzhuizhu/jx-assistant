//go:build !windows

package main

import "syscall"

// processAlive 判断某个 pid 是否还活着（见 proc_alive_windows.go 的说明）。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
