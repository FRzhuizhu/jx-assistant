//go:build windows

package main

import "golang.org/x/sys/windows"

// processAlive 判断某个 pid 是否还活着（启动恢复用它区分「另一个实例正在跑」
// 与「上次进程崩了」）。取不到信息一律当「不活」——恢复逻辑走保守路径（补中断），
// 不会因为权限/句柄问题把活实例的会话判成健康。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}
