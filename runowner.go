package main

// 会话执行的「归属登记」：解决多实例共用同一个 data 目录时的误判中断。
//
// 背景：sessions.status 是库里的共享状态，而 recoverInterrupted 在每次实例启动时
// 把 status='running' 一律当成「上次进程崩了」补一条【系统中断】。桌面形态的单实例
// 互斥体（desktop_windows.go 的 acquireSingleInstance）挡不住控制台形态实例，而
// 「桌面版 + 控制台版共用同一 data 目录」是常态（测试实例、-port 换端口的实例），
// 于是「A 正在跑、B 启动」会被 B 判成中断：凭空给 A 的会话补中断、置为空闲。
//
// 做法：谁在跑哪一轮，落一条归属（pid + 心跳时间戳）进 meta 表——复用
// Store.SetSessionData，不需要结构迁移。启动恢复只处理「归属不可达」的会话：
// pid 还活着且心跳新鲜 = 另一个实例正在跑它，跳过。
// 心跳由 StartRunHeartbeat 每 20s 刷新本实例所有在跑的会话；finishRun 时清掉。

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	runOwnerKey       = "runowner"       // meta 键：sessdata:runowner:<会话id>
	runOwnerTTL       = 90 * time.Second // 心跳超过它没刷新即视为旧账（崩溃残留靠它兜底）
	runHeartbeatEvery = 20 * time.Second // 心跳周期
)

// markRunOwner 记下「本实例正在跑这个会话」。
func (a *App) markRunOwner(sessionID string) {
	_ = a.Store.SetSessionData(sessionID, runOwnerKey,
		fmt.Sprintf("%d|%d", os.Getpid(), time.Now().UnixMilli()))
}

// clearRunOwner 清掉归属（本轮正常/停止/报错收口都走 finishRun，那里调它）。
func (a *App) clearRunOwner(sessionID string) {
	_ = a.Store.SetSessionData(sessionID, runOwnerKey, "")
}

// otherInstanceRunning 判断这个会话是不是正被另一个「活着的」实例执行。
// 归属缺失、心跳过期、pid 不可达都返回 false——那些是崩溃残留，该走中断恢复。
func (a *App) otherInstanceRunning(sessionID string) (int, bool) {
	raw := strings.TrimSpace(a.Store.GetSessionData(sessionID, runOwnerKey))
	if raw == "" {
		return 0, false
	}
	parts := strings.SplitN(raw, "|", 2)
	pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || pid <= 0 {
		return 0, false
	}
	at := int64(0)
	if len(parts) == 2 {
		at, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	}
	// 先把心跳过期的一律当残账：崩溃残留就是靠这一条兜底（同时也挡住 pid 被
	// 系统复用后「看起来还活着」的假活）
	if at <= 0 || time.Since(time.UnixMilli(at)) > runOwnerTTL {
		return pid, false
	}
	if pid == os.Getpid() {
		// 归属写的是我自己：只有本进程确实在跑它才算活；否则是崩溃残留 + pid 复用
		return pid, a.runOf(sessionID) != nil
	}
	if !processAlive(pid) {
		return pid, false // 进程没了：真正的崩溃残留
	}
	return pid, true
}

// runningIDs 本实例正在跑的会话 id 快照（心跳用；复制一份，不持锁跑 DB）。
func (a *App) runningIDs() []string {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	out := make([]string, 0, len(a.runners))
	for id := range a.runners {
		out = append(out, id)
	}
	return out
}

// StartRunHeartbeat 起后台心跳：每 20s 把本实例在跑的会话的归属时间戳刷新一遍，
// 让别的实例（以及重启后的自己）能分辨「有活实例在跑」与「崩溃残留」。
// 在 main.go 启动时调用一次即可，随进程退出。
func StartRunHeartbeat(a *App) {
	go func() {
		t := time.NewTicker(runHeartbeatEvery)
		defer t.Stop()
		for range t.C {
			ids := a.runningIDs()
			if len(ids) == 0 {
				continue
			}
			for _, id := range ids {
				a.markRunOwner(id)
			}
			log.Printf("[心跳] 已刷新 %d 个在跑会话的执行归属", len(ids))
		}
	}()
}
