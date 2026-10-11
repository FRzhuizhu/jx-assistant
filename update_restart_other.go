//go:build !windows

package main

import "fmt"

// spawnRestartHelper 非 Windows 平台暂不支持接力重启（桌面版只发 Windows）；
// 分块交换仍有效——用户自然重启后生效。
func spawnRestartHelper(a *App, st updateBlockState) error {
	return fmt.Errorf("当前平台不支持立即重启，请手动重启软件生效")
}
