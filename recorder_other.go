//go:build !windows

package main

// 演示记录器的非 Windows 空桩：全局键鼠钩子与麦克风录音目前只有 Windows
// 实现（recorder_windows.go）。其他平台工具定义照常注册（请求形状跨平台
// 稳定），调用时如实报「当前平台不支持」；环境快照会提前告知模型。

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
