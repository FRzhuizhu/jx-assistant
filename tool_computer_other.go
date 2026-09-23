//go:build !windows

package main

import "fmt"

// 电脑操作组件的非 Windows 占位：组件工具面向 Windows 桌面（本产品
// 的目标交付形态），其他平台注册时报错而不是静默失败。

func computerCaptureScreen() ([]byte, int, int, error) {
	return nil, 0, 0, fmt.Errorf("电脑操作组件目前仅支持 Windows")
}

func computerClickAt(x, y int, button string, double bool) error {
	return fmt.Errorf("电脑操作组件目前仅支持 Windows")
}

func computerTypeText(text string) error {
	return fmt.Errorf("电脑操作组件目前仅支持 Windows")
}

func computerSendKey(key string, ctrl, alt, shift bool) error {
	return fmt.Errorf("电脑操作组件目前仅支持 Windows")
}

func computerScroll(notches int) error {
	return fmt.Errorf("电脑操作组件目前仅支持 Windows")
}

func computerListWindows() ([]string, error) {
	return nil, fmt.Errorf("电脑操作组件目前仅支持 Windows")
}

func computerActivateWindow(substring string) error {
	return fmt.Errorf("电脑操作组件目前仅支持 Windows")
}
