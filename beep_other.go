//go:build !windows

package main

// 非 Windows 无 MessageBeep：提醒的叮声空实现（闪烁与朗读不受影响），
// 与 voiceglobal_other.go 同款跨平台桩纪律。

func systemBeep() {}
