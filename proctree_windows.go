//go:build windows

package main

// 不受限路径的进程树终止（Windows）：把 exec.Cmd 拉起的子进程挂进
// kill-on-close Job Object，解决「停止键杀不掉命令」的两个根因：
//
//   1. 默认取消只 TerminateProcess 直接子进程——命令拉起的孙进程
//      （adb server、npm→node、pip→compiler 这类常驻/子进程）毫发无损。
//      改成整树终止：Cancel 时 TerminateJobObject，job 句柄关闭时由
//      系统自动清场（引擎自身崩溃也不泄漏）。
//   2. 孙进程继承 stdout/stderr 管道句柄时，exec 的 Wait 一直等管道 EOF
//      ——直接子进程已死也等不到，Wait 永久阻塞，工具调用永不返回，
//      会话从此卡在「运行中」，停止/超时都救不回来。WaitDelay 到点
//      强制收口（Go 1.20+ 内建机制），保证取消后至多数秒必返回。
//
// 沙箱受限路径（sandbox_windows.go）本来就是 Job Object 方案；这里把
// 同样的纪律补到完全权限路径与后台作业。
//
// 用法（三个时机缺一不可）：
//
//	tree := armTreeKill(cmd)   // Start 前
//	cmd.Start()                // 成功后立刻 attach（进程入 job）
//	cmd.Wait(); tree.finish()  // Wait 返回后关 job 句柄：残留孙进程清场

import (
	"os/exec"
	"time"

	"golang.org/x/sys/windows"
)

// proctreeWaitDelay 是 Wait 在「进程已结束但管道仍被孙进程持有」时的
// 强制收口时限：正常命令（管道随进程关闭）永远到不了这里，行为不变；
// 残留守护进程时命令输出至多多等这几秒。
const proctreeWaitDelay = 3 * time.Second

// procTree 是一次子进程执行的整树终止句柄；零值安全（各方法均可空调用）。
type procTree struct{ job windows.Handle }

// armTreeKill 在 Start 之前武装一个 exec.Cmd：覆写 Cancel 为整树终止、
// 设 WaitDelay 强制收口。Job 建立失败时退回默认取消（只杀直接子进程），
// WaitDelay 兜底仍在。
func armTreeKill(cmd *exec.Cmd) *procTree {
	cmd.WaitDelay = proctreeWaitDelay
	job, err := createKillOnCloseJob()
	if err != nil {
		return &procTree{}
	}
	cmd.Cancel = func() error {
		// 进程尚未 attach（Start 与 attach 之间的极小窗口）时是空操作，
		// 由 attach 之后的 kill-on-close 句柄兜底清场
		_ = windows.TerminateJobObject(job, 1)
		return nil
	}
	return &procTree{job: job}
}

// attach 在 Start 成功后立刻调用：把子进程挂进 job（此后它派生的孙进程
// 天然都在 job 里）。挂接失败（极小概率）时保持 job 为空，不影响执行。
func (t *procTree) attach(cmd *exec.Cmd) {
	if t == nil || t.job == 0 || cmd.Process == nil {
		return
	}
	// os.Process 的原生句柄未导出，按 PID 重开一个：SET_QUOTA 是入 job 的
	// 必需权限，TERMINATE 供 TerminateJobObject 兜底
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(t.job, h)
}

// finish 在 Wait 返回后调用：关 job 句柄触发 kill-on-close，清掉已脱离
// 主进程但仍挂在 job 里的残留孙进程（如 adb server 这类守护进程）。
func (t *procTree) finish() {
	if t == nil || t.job == 0 {
		return
	}
	windows.CloseHandle(t.job)
	t.job = 0
}
