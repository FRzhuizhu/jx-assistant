//go:build windows

package main

// 询问提醒的「叮」一声：user32 MessageBeep 播系统默认提示音——纯 syscall、
// 不走语音组件（朗读才走组件，见 voiceapi.go / 前端 ask_alert 处理）。

import "syscall"

var procMessageBeep = syscall.NewLazyDLL("user32.dll").NewProc("MessageBeep")

// systemBeep 播一声系统提示音（MB_OK：默认「叮」；无声卡/静音时静默失败）。
func systemBeep() { procMessageBeep.Call(0) }
