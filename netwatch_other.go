//go:build !windows

package main

// watchNetworkChange 非 Windows 平台暂不实现（P2P/中继主要跑在桌面端）；
// 链路仍由 20s ping + 僵死看门狗 + 退避重连兜底。
func watchNetworkChange(stop <-chan struct{}, onChange func()) {}
