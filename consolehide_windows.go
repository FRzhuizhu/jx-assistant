//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsoleWindow 压掉子进程的控制台窗口：桌面形态（-windowsgui 构建）自身
// 没有控制台可供子进程复用，Windows 会为每个 console 程序（cmd / powershell /
// sh / python / icacls…）新开一个控制台窗口——run_command、后台作业、技能脚本、
// 环境探测每跑一次，桌面上就闪一个黑窗（或终端窗）。用户在桌面上看到的「一直
// 往外冒窗口」就是它。
// CREATE_NO_WINDOW 给子进程一个「没有窗口的控制台」：标准输入输出照旧走我们
// 给的管道，取输出、超时、job_kill 都不受影响；HideWindow 兼顾个别外壳自己
// 弹窗的情况。
// 纪律：凡 exec.Command* 起 console 程序，落 Start/Run/Output 之前都要过一下
// 这个函数——漏一处就漏一处弹窗。本产品所有子进程都是后台跑、结果回给模型的，
// 没有需要用户看的窗口。
// （`fileopen_windows.go` 的 openHideWindow 与 `gitpanel_windows.go` 的
// gitHideWindow 是同款历史副本，只服务于各自那几处调用；新增代码一律用本函数。）
func hideConsoleWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}
