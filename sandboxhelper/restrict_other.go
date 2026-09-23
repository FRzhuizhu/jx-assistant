//go:build !linux

package sandboxhelper

// 非 Linux 平台的占位实现：保持包可编译；主程序在 Windows 上走
// sandbox_windows.go 的受限令牌后端，不会调用到这里。

import (
	"fmt"
	"os"
)

// Main 非 Linux 平台不可用。
func Main(args []string) int {
	fmt.Fprintln(os.Stderr, "JX_SANDBOX_UNAVAILABLE: 本 helper 仅支持 Linux")
	return 126
}
