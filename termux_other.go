//go:build !android

package main

import (
	"context"
	"fmt"
	"os/exec"
)

// 非安卓平台：无内置运行时（桌面技能走 技能/_runtime 便携 Python，见 skills.go）。

func termuxEnsureFor(a *App, emit func(string)) (string, error) {
	return "", fmt.Errorf("内置运行时仅安卓平台可用")
}

// runTermuxBash 非安卓桩：调用点均在 isAndroidRuntime 分支内且 prefix 非空时
// 才可达，正常编译矩阵下不会执行。
func runTermuxBash(ctx context.Context, prefix string, r sandboxRun) (string, error) {
	return "", fmt.Errorf("内置运行时仅安卓平台可用")
}

// applyTermuxProcAttr / killTermuxProcGroup 非安卓桩（命令均可正常退出，无需组管理）。
func applyTermuxProcAttr(cmd *exec.Cmd)  {}
func killTermuxProcGroup(cmd *exec.Cmd) {}
