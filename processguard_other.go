//go:build !windows

package main

// 顶层 kill-on-close Job 是 Windows 机制；POSIX 下子进程归属会话与
// 进程组，沙箱路径另有 Landlock/自重入约束（sandboxhelper）。
func setupKillOnCloseJob() error { return nil }
