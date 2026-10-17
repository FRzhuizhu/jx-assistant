package main

// 退出路径的主动收口与自检（配合 processguard_windows.go 的顶层 Job 兜底）：
//   · shutdownJobs：正常退出前把台账上的后台作业逐个停掉并限时等死透——
//     内核兜底之前的第一次收口（被强杀/崩溃不经过这里，直接靠顶层 Job）；
//   · logTopLevelJobRemaining：退出自检日志——查顶层 Job 此刻还有多少成员
//     进程，把「关软件不残留」从口头保证变成可审计的记录。
// 非 Windows 平台没有顶层 Job，自检为空实现（exitguard_other.go）。

import (
	"log"
	"time"
)

// shutdownJobs 退出主动收口：停止全部仍在运行的后台作业并等待结算。
// 每个作业限时 5 秒（stop 内部超时放行），整体上限近似 len(作业)×5s；
// 退出路径上这是可接受的最后一步——内核兜底不依赖它成功。
func (a *App) shutdownJobs() {
	a.jobsMu.Lock()
	all := make([]*jobRun, 0, len(a.jobs))
	for _, j := range a.jobs {
		all = append(all, j)
	}
	a.jobsMu.Unlock()
	stopped := 0
	for _, j := range all {
		select {
		case <-j.done:
			continue
		default:
		}
		j.stop(5 * time.Second)
		stopped++
	}
	if stopped > 0 {
		log.Printf("[退出收口] 已停止并结算 %d 个后台作业", stopped)
	}
	logTopLevelJobRemaining()
}
