//go:build !windows

package main

// 不受限路径的进程树终止（非 Windows 平台）：与 proctree_windows.go 同一
// 用法面。Linux/安卓用进程组（Setpgid + 杀 -pgid）实现整树终止；调用方
// 已自设 SysProcAttr 的（termux）不抢，只保留 WaitDelay 强制收口——
// 「孙进程持有管道句柄导致 Wait 永久阻塞」的收口兜底全平台一致。

import (
	"os/exec"
	"syscall"
	"time"
)

const proctreeWaitDelay = 3 * time.Second

type procTree struct{ pgid bool }

func armTreeKill(cmd *exec.Cmd) *procTree {
	cmd.WaitDelay = proctreeWaitDelay
	if cmd.SysProcAttr != nil {
		return &procTree{} // 调用方自管进程属性：只留收口兜底
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // 整进程组
		}
		return nil
	}
	return &procTree{pgid: true}
}

func (t *procTree) attach(*exec.Cmd) {} // 进程组由内核维护，无需挂接

func (t *procTree) finish() {} // 无 job 句柄；组内残留进程随组语义自然收
