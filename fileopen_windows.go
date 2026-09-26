//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// openHideWindow 给"只借用一下系统外壳"的子进程压掉控制台黑窗：桌面模式
// （-windowsgui 构建）没有控制台可供子进程复用，Windows 会为每个 console
// 程序新开一个窗口——打开文件/定位资源管理器这种操作一闪一闪很扎眼。
func openHideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// openPathWithDefaultApp 用系统默认程序打开一个文件（交付卡片的「打开」：
// 双击文件是什么程序，这里就是什么程序）。走 rundll32 的 FileProtocolHandler，
// 与启动时自动开浏览器同一路径（已验证在桌面/浏览器两种形态下都可用）。
func openPathWithDefaultApp(abs string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", abs)
	openHideWindow(cmd)
	return cmd.Start()
}

// revealInExplorer 在文件资源管理器里定位并选中该文件（交付卡片的
// 「在文件夹中查看」）。explorer /select, 需要把路径作为独立参数传入，
// 含空格也不会被拆开。
func revealInExplorer(abs string) error {
	cmd := exec.Command("explorer", "/select,"+abs)
	openHideWindow(cmd)
	// explorer.exe 定位成功也常以非零码退出（它把请求转给已存在的实例后
	// 立即返回），因此只看启动是否成功，不看退出码。
	return cmd.Start()
}
