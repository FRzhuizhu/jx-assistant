//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32DLL        = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW = shell32DLL.NewProc("ShellExecuteW")
)

// openHideWindow 给"只借用一下系统外壳"的子进程压掉控制台黑窗：桌面模式
// （-windowsgui 构建）没有控制台可供子进程复用，Windows 会为每个 console
// 程序新开一个窗口——定位资源管理器这种操作一闪一闪很扎眼。
func openHideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// openPathWithDefaultApp 用系统默认程序打开一个文件（交付卡片的「打开」、右键
// 菜单的「在系统中打开」：双击文件是什么程序，这里就是什么程序）。
// 直接调 ShellExecuteW 而不经 rundll32 FileProtocolHandler 中转：rundll32 是
// console 程序，CREATE_NO_WINDOW 启动它会把 SW_HIDE 的显示状态传染给它
// ShellExecute 出来的目标——Win11 新版记事本这类打包应用被这么带着启动时
// 一个窗口都不建（真机复现：进程在、命令行带文件、EnumWindows 零窗口，见
// gui-test/ctxmenu-desktop-smoke.mjs 的 D4）。ShellExecuteW 本就是
// FileProtocolHandler 内部走的路径，由本进程（GUI 子系统）直接调用，显示
// 状态正确，也没有中间控制台进程可闪。
func openPathWithDefaultApp(abs string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	p, _ := windows.UTF16PtrFromString(abs)
	r, _, _ := procShellExecuteW.Call(
		0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(p)), 0, 0, 1) // SW_SHOWNORMAL
	if r <= 32 { // ShellExecute 惯例：返回值 >32 为成功
		return fmt.Errorf("ShellExecute 打开失败（码 %d）", r)
	}
	return nil
}

// revealInExplorer 在文件资源管理器里定位并选中该文件（交付卡片的
// 「在文件夹中查看」、右键菜单的「在文件夹中定位」）。explorer /select,
// 需要把路径作为独立参数传入，含空格也不会被拆开。
func revealInExplorer(abs string) error {
	cmd := exec.Command("explorer", "/select,"+abs)
	openHideWindow(cmd)
	// explorer.exe 定位成功也常以非零码退出（它把请求转给已存在的实例后
	// 立即返回），因此只看启动是否成功，不看退出码。CREATE_NO_WINDOW 对它
	// 无害：窗口由已存在的交互式 explorer 实例创建，不吃启动显示状态。
	return cmd.Start()
}
