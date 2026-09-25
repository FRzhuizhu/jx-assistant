//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// gitHideWindow 阻止 git 子进程弹控制台黑窗：桌面模式（-windowsgui）下没有
// 控制台可供子进程复用，Windows 会为每个 console 程序新开一个窗口——
// CREATE_NO_WINDOW 压掉它，面板刷新才不会一闪一闪。
func gitHideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
