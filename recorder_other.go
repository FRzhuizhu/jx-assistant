//go:build !windows

package main

// 演示记录器的非 Windows 空桩：全局键鼠钩子与麦克风录音目前只有 Windows
// 实现（recorder_windows.go）。其他平台起录请求如实报「当前平台不支持」
//（HTTP 端点经 recorderSupported 提前拦下，模型侧不再持有录制工具）。

import "fmt"

func recorderSupported() bool { return false }

func recorderStart(dir string) error {
	return fmt.Errorf("演示记录器仅支持 Windows 桌面（当前平台没有全局键鼠钩子与录音实现）；请让用户直接口述操作步骤")
}

func recorderStop() (*recorderStats, error) {
	return nil, fmt.Errorf("当前没有在录的演示记录（且当前平台不支持演示记录器）")
}

func recorderStatusText() string {
	return "当前平台不支持演示记录器（仅 Windows 桌面）。"
}

// recorderActiveInfo 非 Windows 恒为未在录（与真实现同签名，status 端点共用）。
func recorderActiveInfo() (bool, string) { return false, "" }
