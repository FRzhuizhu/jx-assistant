//go:build !linux

package main

// 非 Linux 平台的编译占位：运行时不会走到。

import "fmt"

func spawnConfinedLinuxReexec(r sandboxRun) (string, error) {
	return "", fmt.Errorf("sandbox unavailable: Linux 沙箱后端不可用")
}
