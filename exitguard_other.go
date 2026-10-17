//go:build !windows

package main

// 非 Windows 平台没有顶层 Job Object，退出自检为空实现（exitguard.go 的
// logTopLevelJobRemaining 调用点无需平台分支）。
func logTopLevelJobRemaining() {}
