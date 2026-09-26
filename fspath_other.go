//go:build !windows

package main

// 非 Windows 无 junction：canonicalFor 直接走 filepath.EvalSymlinks。
func canonicalNative(p string) (string, bool) { return "", false }
