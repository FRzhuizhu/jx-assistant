//go:build !windows

package main

// 非 Windows 平台暂无 DPAPI：加密直通（返回原值），保持配置可读写。
// 密文迁移只在 Windows 上发生；Linux 侧敏感字段仍是明文落盘（如实文档）。

func dpapiProtect(plain []byte) ([]byte, error) { return plain, nil }

func dpapiUnprotect(cipher []byte) ([]byte, error) { return cipher, nil }
