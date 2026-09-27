//go:build android

package main

import (
	"log"
	"syscall"
)

// 引擎在安卓上以子进程方式被应用壳拉起。应用进程被系统杀死/崩溃时，
// 引擎会变成孤儿继续运行并占住端口——下次启动新引擎绑定失败、秒退、
// 被看护逻辑反复重启，用户表现为「永远卡在启动页」。设
// PR_SET_PDEATHSIG(SIGKILL)：父进程（应用壳）一死，内核即刻回收引擎，
// 从根上不产生孤儿。adb/Termux 手工跑也一样：shell 退出引擎即退出，
// 不会再留残尸占端口。
func setupParentDeathSignal() {
	// syscall 包在 linux 上没有导出 Prctl 包装，直接 RawSyscall：
	// prctl(PR_SET_PDEATHSIG, SIGKILL)
	if r, _, err := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGKILL), 0); r != 0 || err != 0 {
		log.Printf("PR_SET_PDEATHSIG 未生效（不影响运行）: r=%d err=%v", r, err)
	}
}
