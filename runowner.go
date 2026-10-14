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
	"sync"
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

// 心跳循环的启停状态（进程内唯一，一个进程一个 App，故不挂 App 字段）。
var (
	hbMu sync.Mutex
	hbOn bool
)

// ensureRunHeartbeat 保证「有会话在跑」期间有一个心跳在刷新归属；空闲时零唤醒。
// 由 beginRun 调用：起一轮就确保心跳在跑，连续两拍发现没有在跑的会话就自己收工
// —— 用户啥也不干的时候，不该有任何周期性动作。
func (a *App) ensureRunHeartbeat() {
	hbMu.Lock()
	if hbOn {
		hbMu.Unlock()
		return
	}
	hbOn = true
	hbMu.Unlock()
	go func() {
		defer func() {
			hbMu.Lock()
			hbOn = false
			hbMu.Unlock()
		}()
		t := time.NewTicker(runHeartbeatEvery)
		defer t.Stop()
		idleBeats := 0
		for range t.C {
			ids := a.runningIDs()
			if len(ids) == 0 {
				if idleBeats++; idleBeats >= 2 {
					return // 连续两拍没有在跑的会话：收工（此后零唤醒）
				}
				continue
			}
			idleBeats = 0
			for _, id := range ids {
				a.markRunOwner(id) // 刷新归属：别的实例据此分辨「活实例在跑」与「崩溃残账」
			}
		}
	}()
}

// runSweepDelay 是启动后补扫「孤儿会话」的延迟：跑完 recoverInterrupted 再等一会儿，
// 让上一个实例（可能还在退出中）彻底消失。
const runSweepDelay = 30 * time.Second

// StartRunSweepOnce 启动后 30s 补扫一次孤儿会话，**只扫这一次**，不做持续轮询。
//
// 为什么要有它：recoverInterrupted 只判启动那一刻，而「上一个实例还没死透」这种
// 竞态会让它把会话误判成「另一个实例正在跑」而跳过——那样悬空工具调用不会被补上
// 结尾、也少一条中断交代。30s 后再看，那个进程已经没了，漏掉的就能补上。
// 之后不再扫：正常情况要么有活实例（它自己的心跳一直在刷），要么是残账（由下次
// 启动的恢复兜底）；持续轮询没有额外收益，只是白白唤醒。
func StartRunSweepOnce(a *App) {
	go func() {
		time.Sleep(runSweepDelay)
		a.sweepOrphanSessions()
	}()
}

// sweepOrphanSessions 扫一遍：status=running 但本实例没在跑、且归属不可达（pid 已死
// 或心跳过期）的会话，补中断收口。刻意只认「有归属行且不可达」——没有归属行的
// 会话留给启动恢复处理，免得把还在跑的老实例的会话误判成孤儿。
func (a *App) sweepOrphanSessions() {
	sessions, err := a.Store.RunningSessions()
	if err != nil || len(sessions) == 0 {
		return
	}
	for _, s := range sessions {
		if a.runOf(s.ID) != nil {
			continue // 本实例正在跑它（还没收口），不动
		}
		pid, live := a.otherInstanceRunning(s.ID)
		if live {
			continue
		}
		if strings.TrimSpace(a.Store.GetSessionData(s.ID, runOwnerKey)) == "" {
			continue // 没有归属行：不是本套机制管的会话，交给启动恢复
		}
		log.Printf("[清扫] 会话 %s 无人执行（归属 pid=%d 已不可达），补中断收口", s.ID, pid)
		a.reapInterruptedSession(s)
	}
}

// reapInterruptedSession 给一个「没人跑、但状态还是 running」的会话补中断收口：
// 先补悬空工具调用的合成结尾（历史配对完整），再按**落库的停止原因**补交代、
// 置空闲、留「⚠ 已中断」标记，并给派发方补一条派发回报。
// 启动恢复与 30s 补扫共用这一处，避免两套收口逻辑漂移。
//
// 中断原因分两种口径（这就是本函数比「一律按服务重启」更准的地方）：
//   - 人为停止过、且这一轮尚未收口（StopRecord.Manual，落库读回；人工点按钮与
//     数字助理用 stop_session 按用户指示代停都记在这里，Actor 分得开是谁停的）：
//     按「用户手动停止」认。人点了停止紧接着服务重启时，绝不能报成
//     「因服务重启被中断」——那会让数字助理以为任务是意外打断的而重新派发。
//   - 其余：系统异常中断（服务重启 / 进程崩溃），文案与原来一致，只补上
//     中断时间、触发者与进度。
func (a *App) reapInterruptedSession(s *SessionMeta) {
	// 先把上一进程崩溃时留下的悬空工具调用补上合成结尾（历史配对完整），
	// 再补系统中断报告——顺序反了的话报告会插在悬空调用与补行之间
	a.repairDanglingToolCalls(s, nil)
	// 停止原因只认落库的那一份：重启后内存里的状态全丢了，只有 meta 表里的
	// StopRecord 还留着「用户点过停止」这件事（旧数据没有记录 = 未记录，
	// 走下面的默认口径）
	rec := a.Store.GetStopRecord(s.ID)
	userStop := rec.Kind == stopByUser && rec.Manual
	rounds, steps := a.sessionProgress(s.ID)
	at := strings.TrimSpace(rec.At)
	if at == "" {
		at = now()
	}
	reasonText := "【系统中断】服务在执行期间重启，本会话当时被中断。可发「继续」接续执行。"
	reportText := "【派发回报】任务会话「" + s.Title + "」（" + s.ID + "）因服务重启被中断，未完成（中断时间 " +
		at + "｜触发者：系统（服务重启）｜进度：已完成 " + itoa(rounds) + " 轮 / " + itoa(steps) + " 步）。"
	if userStop {
		// 人先叫了停（人工点按钮 / 数字助理按用户指示代停）、随后服务重启：以人为停止为准。
		// 触发者口径照落库的停止记录写（Actor 是 user 还是 assistant 分得开）
		note := strings.TrimSpace(rec.Note)
		if note == "" {
			note = stopTriggerNote(rec.Actor)
		}
		log.Printf("[恢复] 会话 %s 的重启中断按「%s叫停」收口（停止时间 %s，停止原因落库）", s.ID, stopActorLabel(rec.Actor), at)
		reasonText = "【系统中断】服务在执行期间重启，但本会话此前已由用户手动停止（" + note + "，" + at +
			"）。按人为停止处理：未完成的部分不自动续跑，需要时发新消息继续。"
		reportText = "【派发回报】任务会话「" + s.Title + "」（" + s.ID + "）已由用户手动停止（" + note + "，停止时间 " +
			at + "｜触发者：" + stopActorLabel(rec.Actor) + "｜进度：已完成 " + itoa(rounds) + " 轮 / " + itoa(steps) +
			" 步）。这是用户主动叫停，不是异常中断：不要再自动重新派发、续跑或补做这个任务。"
	} else {
		log.Printf("[恢复] 会话 %s 按「系统异常中断（服务重启）」收口", s.ID)
	}
	_ = a.Store.AppendRow(s.ID, &StoredMessage{
		Message: TextMessage("user", reasonText),
		Kind:    kindReport, Origin: "interrupt", CreatedAt: now(),
	})
	_ = a.Store.UpdateStatus(s.ID, "idle")
	if userStop {
		// 用户主动停止不算异常，不留「⚠ 已中断」标记（与 executeRun 的停止分支一致）
		_ = a.Store.SetLastStop(s.ID, "")
	} else {
		// 重启中断是「异常收尾」：侧栏留「⚠ 已中断」标记（内存里的 runError
		// 重启就没了，标记落库才不会漏掉这一类）
		_ = a.Store.SetLastStop(s.ID, stopRestart)
		_ = a.Store.SetStopRecord(s.ID, StopRecord{
			Kind: stopBySystem, Actor: "system", At: at, AtMs: time.Now().UnixMilli(),
			Note: "服务在执行期间重启（或上一进程崩溃），本轮被中断",
		})
	}
	// 这一轮已经收口：待收口标志作废（记录里的时间/次数保留，历史仍查得出来）
	_ = a.Store.ClearStopPending(s.ID)
	// 中断回报的归属：优先派发链（dispatched_by），子对话退回父会话——
	// 助理与工作任务都会派发，统一回报给派发方（重启后不自动续跑，等用户接续）。
	// 归属解析与收口续发的 serveQueue 共用 dispatchOwner 一处口径，不各写一份。
	if owner := a.dispatchOwner(s); owner != nil {
		_ = a.Store.AppendRow(owner.ID, &StoredMessage{
			Message: TextMessage("user", reportText),
			Kind:    kindAiUser, Origin: "dispatch", CreatedAt: now(),
		})
	}
}

// sessionProgress 取会话当前进度（已完成几轮 / 几步）：轮数 = 用户侧消息数、
// 步数 = 带工具调用的助手消息数，口径与压缩摘要（renderSummary）完全一致，
// 所以回报里的进度和摘要里那句「共 N 轮 / M 步」永远对得上。
func (a *App) sessionProgress(id string) (int, int) {
	msgs, err := a.Store.Messages(id)
	if err != nil {
		return 0, 0
	}
	rounds, steps := summarizeSession(msgs)
	return rounds, steps
}
