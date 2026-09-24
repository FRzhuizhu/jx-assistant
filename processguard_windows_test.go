//go:build windows

package main

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 顶层 kill-on-close Job 探针：主进程必须是 Job 成员，且 Job 带
// KILL_ON_JOB_CLOSE——这两点成立时，主进程句柄关闭（任何形式的退出）
// 内核就会终止整棵进程树。孙进程按 Windows 规则自动继承成员身份。
func TestTopLevelKillOnCloseJob(t *testing.T) {
	if err := setupKillOnCloseJob(); err != nil {
		t.Fatalf("挂入 Job 失败: %v", err)
	}
	var inJob int32
	procIsProcessInJob := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	if r, _, _ := procIsProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob))); r == 0 {
		t.Fatal("IsProcessInJob 调用失败")
	}
	if inJob == 0 {
		t.Fatal("当前进程应已是 Job 成员")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var ret uint32
	if err := windows.QueryInformationJobObject(topLevelJob, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &ret); err != nil {
		t.Fatalf("QueryInformationJobObject: %v", err)
	}
	if info.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatal("Job 缺少 KILL_ON_JOB_CLOSE 标志，退出时不会清场子进程树")
	}
}
