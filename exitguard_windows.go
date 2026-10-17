//go:build windows

package main

// 退出自检（Windows）：读顶层 Job 的当前成员 PID 清单，把残留情况记进日志。
// 正常应是 0（退出收口已停干净）或个位数正在退场的进程；数字异常大值得排查。
// 内核兜底（KILL_ON_JOB_CLOSE）在本进程退出后仍会清掉它们——自检不是防线，
// 是防线的仪表盘：把「关软件不残留」从口头保证变成可审计的日志记录。

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobPidListMax 自检一次最多列出 1024 个成员 PID（远超正常规模，纯防呆）。
const jobPidListMax = 1024

func logTopLevelJobRemaining() {
	if topLevelJob == 0 {
		return
	}
	type pidList struct {
		Assigned uint32 // 输入：ProcessIdList 容量
		InList   uint32 // 输出：实际成员数
		Pids     [jobPidListMax]uint32
	}
	b := pidList{Assigned: jobPidListMax}
	var ret uint32
	if err := windows.QueryInformationJobObject(topLevelJob, int32(windows.JobObjectBasicProcessIdList),
		uintptr(unsafe.Pointer(&b)), uint32(unsafe.Sizeof(b)), &ret); err != nil {
		return // 查询失败（极端态）：不阻塞退出，内核兜底照常
	}
	if b.InList == 0 {
		return // 干净退场：不打扰日志
	}
	n := b.InList
	if n > jobPidListMax {
		n = jobPidListMax
	}
	pids := make([]int, 0, n)
	for i := uint32(0); i < n; i++ {
		pids = append(pids, int(b.Pids[i]))
	}
	if b.InList > jobPidListMax {
		log.Printf("[退出自检] 顶层 Job 仍有 %d 个进程待内核清场（前 %d 个：%v）——本进程退出后由系统强制终止", b.InList, jobPidListMax, pids)
		return
	}
	log.Printf("[退出自检] 顶层 Job 仍有 %d 个进程待内核清场（%v）——本进程退出后由系统强制终止", b.InList, pids)
}
