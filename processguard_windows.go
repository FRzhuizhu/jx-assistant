//go:build windows

package main

// 顶层进程树兜底：启动时把自身挂进一个 kill-on-close Job Object。
//
// 为什么需要：Windows 的父子关系只是创建记录，父进程死亡并不会终止
// 子进程。沙箱内命令有每命令的 kill-on-close Job（sandbox_windows.go），
// 但完全访问模式（runUnconfinedLocal）、后台任务、chromedp 浏览器、
// 技能 python 走的是裸 exec——模型用 `start` 派生的孙进程、被强杀时
// 的 chromedp 浏览器都可能变成无人认领的后台残留。
//
// 挂入顶层 Job 后，子进程默认继承成员身份；Job 额外配了 BREAKAWAY_OK——
// 只有显式带 CREATE_BREAKAWAY_FROM_JOB 创建的进程能逃出去（全仓唯一使用处
// 是更新重启助手 update_restart_windows.go），模型用 start 派生的普通子进程
// 依旧一个都逃不掉：主进程无论正常退出、被任务管理器强杀还是崩溃，内核都会
// 立即终止 Job 内全部成员——它启动过的一切随之清场。
//
// 取舍：用户借 full 模式让助手「帮我起个一直跑的服务」时，关掉助手
// 该服务也会停——对沙箱型助手产品这是正确默认（宁可保守），且行为
// 可预期：助手不在，它开的东西就不在。

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var topLevelJob windows.Handle

// setupKillOnCloseJob 创建顶层 kill-on-close Job 并把当前进程挂入。
// 句柄故意持有到进程退出：句柄关闭的瞬间触发整树终止。
func setupKillOnCloseJob() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	// KILL_ON_JOB_CLOSE：兜底整树清场；BREAKAWAY_OK：放行更新重启助手显式
	// 逃逸（否则主进程一退助手陪葬，接力重启永远失败）
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("挂入 kill-on-close Job 失败: %w", err)
	}
	topLevelJob = job
	return nil
}
