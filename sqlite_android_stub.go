//go:build !(android && amd64)

package main

// 非 android/amd64 平台：modernc sqlite 不需要 seccomp 补丁（arm64 内核
// 无旧式 stat 调用；桌面 Linux/Windows/macOS 无此 seccomp 白名单）。

func patchSqliteSyscallsForAndroid() {}
