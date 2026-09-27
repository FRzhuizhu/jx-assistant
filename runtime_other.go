//go:build !windows && !linux && !android

package main

const (
	isWindowsRuntime = false
	isLinuxRuntime   = false
	isAndroidRuntime = false
)
