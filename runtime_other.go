//go:build !windows && !linux

package main

const (
	isWindowsRuntime = false
	isLinuxRuntime   = false
)
