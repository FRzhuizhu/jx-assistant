package main

// 文件快照子系统（对齐 ZCode 的 workspace checkpoint 机制）：
//
//   - 采集：write_file / edit_file 写入时自动记录改动前内容（snapshotWrite）；
//     run_command、外部程序、Office 文档运行库等无法追踪的修改，由模型按
//     系统提示词的约定先调 snapshot_file 手动记录（见 tools.go 的工具定义）。
//   - 回退：编辑历史消息时带 workspaceMode=rewind（server.go 的 handleEdit），
//     或在文件改动面板上按轮撤销（handleFileRewind）。两者共用本文件的
//     planRewind / applyRewind：安全预检（fail-closed）→ 补偿 journal →
//     全部成功才动对话记录（文件与对话要么都退、要么都不退）。
//   - 保留策略：snapshotJanitor 周期清理——按天（SnapshotDays）超龄删除、
//     按量（SnapshotMaxMB）从最旧开始驱逐，保证库不无限膨胀。
//
// 路径口径：快照统一存"工作区内相对路径"（正斜杠），工作区改名/搬家后回退
// 仍能按相对路径重新解析；写到工作区之外的文件没有相对形态，存原样绝对路径。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// snapshotMaxFileBytes 自动快照的单文件上限：超过则跳过该文件的快照（回退时
// 它按未追踪处理）。防单个大文件把库撑爆；总量另由自动池预算与按天清理约束。
const snapshotMaxFileBytes = 32 << 20

// snapshotManualMaxFileBytes 手动快照（snapshot_file）的单文件上限。手动档
// 的用途恰恰是 Office 文档等"大而重要"的文件，上限放得很宽；清理主力是
// 按天过期（SnapshotDays），手动池默认不限量。
const snapshotManualMaxFileBytes = 256 << 20

// manualSnapshotTool 标记"模型手动打的快照"：这类快照之后文件会按预期被
// 不可追踪的方式修改，回退时不做哈希预检（信任快照时刻的内容）。
const manualSnapshotTool = "snapshot_file"

// hashBytes 计算内容的十六进制 sha256（安全预检用）。
func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// displayPath 把解析后的绝对路径折算回"工作区内相对路径"（正斜杠）；
// 不在工作区内则返回原样绝对路径。工作区可能整体改名/搬家，快照里只存
// 相对形态才能在改名后继续回退。
func (t *toolContext) displayPath(abs string) string {
	ws := t.workspace()
	if t.isRemote() {
		ws = path.Clean(ws)
		if strings.HasPrefix(abs, ws+"/") {
			return strings.TrimPrefix(abs, ws+"/")
		}
		return filepath.ToSlash(abs)
	}
	// 配置里的工作区可能是相对路径（基于进程 CWD），先转绝对再求相对
	if wabs, err := filepath.Abs(ws); err == nil {
		ws = wabs
	}
	rel, err := filepath.Rel(ws, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// snapshotsEnabled 文件快照总开关（设置页「文件备份」）。配置缺省视为开。
func (a *App) snapshotsEnabled() bool {
	if a.Cfg == nil || a.Cfg.SnapshotEnabled == nil {
		return true
	}
	return *a.Cfg.SnapshotEnabled
}

// jxAIName 工作区内的系统目录名：快照、模型临时文件（.jxai/tmp）都归这里。
const jxAIName = ".jxai"

// snapshotFilePath 一条快照的正文文件位置（工作区相对路径）：
// .jxai/snapshots/<会话id>/<checkpoint id>.bin
func snapshotFilePath(sessionID string, id int64) string {
	return fmt.Sprintf("%s/snapshots/%s/%d.bin", jxAIName, sessionID, id)
}

// writeSnapshotBody 把快照正文写进 .jxai/snapshots/<会话>/<id>.bin。
func (t *toolContext) writeSnapshotBody(id int64, content []byte) error {
	p, err := t.resolve(snapshotFilePath(t.sess.ID, id))
	if err != nil {
		return err
	}
	return t.envWriteFile(p, content)
}

// loadBefore 取一条快照的正文：旧库内联 BLOB 优先，否则读 .jxai 里的落盘文件。
func (t *toolContext) loadBefore(cp *Checkpoint) []byte {
	if cp.Before != nil {
		return cp.Before
	}
	if !cp.ExistedBefore {
		return nil // 改动前不存在：本就没有正文
	}
	p, err := t.resolve(snapshotFilePath(cp.SessionID, cp.ID))
	if err != nil {
		return nil
	}
	b, err := t.envReadFile(p)
	if err != nil {
		return nil
	}
	return b
}

// deleteSnapshotFiles 删除快照正文文件（行已删或将删；文件不存在视为成功）。
func (t *toolContext) deleteSnapshotFiles(ids []int64) {
	for _, id := range ids {
		p, err := t.resolve(snapshotFilePath(t.sess.ID, id))
		if err != nil {
			continue
		}
		if err := t.envDeleteFile(p); err != nil {
			log.Printf("[快照] 会话 %s 清理正文文件 %d 失败: %v", t.sess.ID, id, err)
		}
	}
}

// copySnapshotFiles 分支复制快照时同步复制正文文件（pairs = 源id → 新id）。
// 源正文可能是旧库内联 BLOB（读到再写）或 .jxai 落盘文件，loadBefore 统一处理。
func (a *App) copySnapshotFiles(from, to *SessionMeta, pairs [][2]int64) {
	if len(pairs) == 0 {
		return
	}
	ftx := &toolContext{app: a, sess: from}
	ttx := &toolContext{app: a, sess: to}
	for _, pr := range pairs {
		src, err := ftx.resolve(snapshotFilePath(from.ID, pr[0]))
		if err != nil {
			continue
		}
		body, rerr := ftx.envReadFile(src)
		if rerr != nil {
			// 源正文可能是内联 BLOB：按 checkpoint 元数据再取一次
			cps, _ := a.Store.CheckpointsAfter(from.ID, 0)
			for _, cp := range cps {
				if cp.ID == pr[0] {
					body = ftx.loadBefore(cp)
					break
				}
			}
		}
		if body == nil {
			continue
		}
		if err := ttx.writeSnapshotBody(pr[1], body); err != nil {
			log.Printf("[快照] 分支复制正文 %d→%d 失败: %v", pr[0], pr[1], err)
		}
	}
}

// ensureJxAI 确保工作区里有 .jxai/tmp（模型临时文件的约定去处，见系统提示词）。
// 选定工作目录后尽快调用一次；模型用 write_file 写 .jxai/tmp 也会自动建目录，
// 这里只是让目录从一开始就存在、可被发现。
func (t *toolContext) ensureJxAI() {
	p, err := t.resolve(jxAIName + "/tmp")
	if err != nil {
		return
	}
	if err := t.envMkdirAll(p); err != nil {
		log.Printf("[.jxai] 会话 %s 建临时目录失败: %v", t.sess.ID, err)
	}
}

// snapshotWrite 带快照的文件写入（write_file / edit_file 共用）：先读旧内容，
// 写入成功后回读算哈希，落一条 checkpoint 并把正文写进 .jxai/snapshots/。
// 旧文件读不了、超过单文件上限、正文属于 .jxai 自身或备份开关关闭时跳过快照
// （写入照常）。快照失败只记日志，不让正常写入报错。
func (t *toolContext) snapshotWrite(abs string, grant *Grant, data []byte) error {
	var before []byte
	existed := true
	track := t.app.snapshotsEnabled()
	if isDir, size, err := t.envStat(abs); err != nil {
		if os.IsNotExist(err) {
			existed = false
		} else {
			track = false // 状态不明：不建快照，避免留下错误基线
		}
	} else if isDir || size > snapshotMaxFileBytes {
		track = false
	} else if b, rerr := t.envReadFile(abs); rerr != nil {
		track = false
	} else {
		before = b
	}
	if track && strings.HasPrefix(t.displayPath(abs), jxAIName+"/") {
		track = false // 系统目录自身的文件不做快照（快照/临时文件无需回退）
	}
	if err := t.envWriteFileWithGrant(abs, data, grant); err != nil {
		return err
	}
	if !track {
		return nil
	}
	after, err := t.envReadFile(abs)
	if err != nil {
		after = data // 远程回读失败时退回写入内容（本地写即所得）
	}
	cp := &Checkpoint{
		SessionID: t.sess.ID, Seq: t.seq, ToolCallID: t.callID, ToolName: t.toolName,
		Path: t.displayPath(abs), ExistedBefore: existed, BeforeSize: int64(len(before)),
		AfterHash: hashBytes(after), AfterSize: int64(len(after)), CreatedAt: now(),
	}
	id, err := t.app.Store.AppendCheckpoint(cp)
	if err != nil {
		log.Printf("[快照] 会话 %s 写 %s 的快照落库失败: %v", t.sess.ID, cp.Path, err)
		return nil
	}
	if existed {
		if err := t.writeSnapshotBody(id, before); err != nil {
			// 正文写不进去就把元数据行一并撤掉，避免留下"有快照却没正文"的坑
			log.Printf("[快照] 会话 %s 写 %s 的快照正文失败: %v", t.sess.ID, cp.Path, err)
			_ = t.app.Store.DeleteCheckpoints(t.sess.ID, []int64{id})
		}
	}
	return nil
}

// rewindPlan 是一个文件的回退计划：恢复到 before 状态，或删除（改动前不存在）。
type rewindPlan struct {
	Path          string `json:"path"`
	Action        string `json:"action"`           // restore=恢复旧内容 delete=撤回新建
	Reason        string `json:"reason,omitempty"` // unsafe 时的人类可读原因
	Manual        bool   `json:"manual"`           // 基线来自手动快照（不做哈希预检）
	ExistedBefore bool   `json:"existedBefore"`
	Before        []byte
}

// planRewind 为一组快照生成回退计划。seqs 为空表示"seq 之后全部"（编辑重发），
// 非空表示按轮撤销（只回退这些 seq 内的改动）。同一文件多次改动时：基线取
// 最早的快照（改动前状态），预检对最晚的快照（最后一次记录的修改之后文件
// 不应再被外部改过）。返回 safe（可执行）与 unsafe（拒绝执行，带原因）。
func (a *App) planRewind(t *toolContext, sessionID string, seq int64, seqs []int64) (safe, unsafe []*rewindPlan, err error) {
	var cps []*Checkpoint
	if len(seqs) > 0 {
		cps, err = a.Store.CheckpointsIn(sessionID, seqs)
	} else {
		cps, err = a.Store.CheckpointsAfter(sessionID, seq)
	}
	if err != nil {
		return nil, nil, err
	}
	type fileState struct {
		first, last *Checkpoint
	}
	order := []string{}
	byPath := map[string]*fileState{}
	for _, cp := range cps {
		st, ok := byPath[cp.Path]
		if !ok {
			st = &fileState{first: cp}
			byPath[cp.Path] = st
			order = append(order, cp.Path)
		}
		st.last = cp
	}
	for _, p := range order {
		st := byPath[p]
		plan := &rewindPlan{
			Path: p, ExistedBefore: st.first.ExistedBefore,
			Manual: st.last.ToolName == manualSnapshotTool,
		}
		if plan.ExistedBefore {
			plan.Action = "restore"
			plan.Before = t.loadBefore(st.first)
			if plan.Before == nil {
				plan.Reason = "快照正文缺失（.jxai/snapshots 里的文件不在了），拒绝盲恢复"
				unsafe = append(unsafe, plan)
				continue
			}
		} else {
			plan.Action = "delete"
		}
		abs, rerr := t.resolve(p)
		if rerr != nil {
			plan.Reason = "路径无法解析"
			unsafe = append(unsafe, plan)
			continue
		}
		cur, cerr := t.envReadFile(abs)
		switch {
		case cerr != nil && os.IsNotExist(cerr):
			if !plan.ExistedBefore {
				// 要撤回的新建文件已经不在了：目标状态已达成，跳过
				continue
			}
			if plan.Manual {
				// 手动快照的文件：改后的删除可能正是不可追踪修改的一部分，照常恢复
				safe = append(safe, plan)
				continue
			}
			plan.Reason = "文件已不存在（可能被移动或删除）"
			unsafe = append(unsafe, plan)
			continue
		case cerr != nil && !plan.Manual:
			plan.Reason = "当前内容无法读取：" + cerr.Error()
			unsafe = append(unsafe, plan)
			continue
		}
		if cerr == nil && !plan.Manual && hashBytes(cur) != st.last.AfterHash {
			plan.Reason = "文件在最后一次记录的修改之后又被改过（外部修改），拒绝盲恢复"
			unsafe = append(unsafe, plan)
			continue
		}
		safe = append(safe, plan)
	}
	return safe, unsafe, nil
}

// applyRewind 执行回退计划，带补偿 journal：每个文件动手前先记下当前状态，
// 中途失败按逆序恢复，不留半截状态。journal 补偿也失败时返回不可恢复错误
// （如实上抛，由调用方写日志与提示）。
func (a *App) applyRewind(t *toolContext, plans []*rewindPlan) error {
	type journalEntry struct {
		abs      string
		existed  bool
		content  []byte
		readFail bool
	}
	journal := []journalEntry{}
	rollback := func() {
		for i := len(journal) - 1; i >= 0; i-- {
			e := journal[i]
			if e.readFail {
				continue // 动手前就读不到的文件，补偿无从谈起
			}
			if e.existed {
				_ = t.envWriteFile(e.abs, e.content)
			} else {
				_ = t.envDeleteFile(e.abs)
			}
		}
	}
	for _, p := range plans {
		abs, err := t.resolve(p.Path)
		if err != nil {
			rollback()
			return fmt.Errorf("回退 %s 失败：路径无法解析", p.Path)
		}
		cur, curErr := t.envReadFile(abs)
		entry := journalEntry{abs: abs, readFail: curErr != nil}
		if curErr == nil {
			entry.existed, entry.content = true, cur
			journal = append(journal, entry)
		} else if os.IsNotExist(curErr) {
			entry.existed = false
			journal = append(journal, entry)
		} else {
			journal = append(journal, entry)
		}
		if p.Action == "delete" {
			if err := t.envDeleteFile(abs); err != nil {
				rollback()
				return fmt.Errorf("撤回新建文件 %s 失败: %w", p.Path, err)
			}
			continue
		}
		if err := t.envWriteFile(abs, p.Before); err != nil {
			rollback()
			return fmt.Errorf("恢复 %s 失败: %w", p.Path, err)
		}
	}
	return nil
}

// snapshotJanitor 快照保留策略的周期执行：启动后先跑一轮，之后每天一轮。
// 主力是按天清理（SnapshotDays=0/-1 表示永不过期）；限量驱逐是兜底——自动池
// SnapshotMaxMB 默认 32MB，手动池 SnapshotManualMaxMB 默认 -1 不限量（确有
// 磁盘压力再配 500/1024 级别）。两者都按文件原子清理（见 store.go 注释），
// 且同步删除 .jxai/snapshots/ 里的正文文件。备份开关只停新采集，不停清理——
// 关掉之后存量快照仍按天数退场，不留孤儿文件。
func (a *App) snapshotJanitor() {
	run := func() {
		days := a.Cfg.SnapshotDays
		sessions, err := a.Store.List()
		if err != nil {
			return
		}
		for _, m := range sessions {
			tctx := &toolContext{app: a, sess: m}
			if days > 0 {
				cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02T15:04:05")
				if ids, err := a.Store.PruneCheckpointsOlderThan(m.ID, cutoff); err != nil {
					log.Printf("[快照] 会话 %s 超龄清理失败: %v", m.ID, err)
				} else if len(ids) > 0 {
					tctx.deleteSnapshotFiles(ids)
					log.Printf("[快照] 会话 %s 清理 %d 条超龄快照（>%d 天）", m.ID, len(ids), days)
				}
			}
			ids, err := a.Store.EvictCheckpointsOver(m.ID,
				int64(a.Cfg.SnapshotMaxMB)<<20, int64(a.Cfg.SnapshotManualMaxMB)<<20)
			if err != nil {
				log.Printf("[快照] 会话 %s 限量驱逐失败: %v", m.ID, err)
			} else if len(ids) > 0 {
				tctx.deleteSnapshotFiles(ids)
				log.Printf("[快照] 会话 %s 超量，已按文件驱逐最旧快照 %d 条", m.ID, len(ids))
			}
		}
	}
	time.Sleep(time.Minute)
	run()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		run()
	}
}
