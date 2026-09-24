//go:build !windows

package main

// 非 Windows 平台无常驻合成 ACE/密级标签残留（Landlock 是内核规则，随进程
// 生命周期消失），审计恒为空。

func auditSandboxGrants(roots []string, revoke, oursOnly bool) ([]string, error) { return nil, nil }

func cleanupResidualGrants(roots []string) (int, error) { return 0, nil }
