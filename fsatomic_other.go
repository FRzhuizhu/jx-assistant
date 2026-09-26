//go:build !windows

package main

// 非 Windows：Unix rename 本身就是原子替换，无 ACL 复制步骤。
func copySecurityDescriptorBestEffort(src, dst string) {}
