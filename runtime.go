package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

// 后台运行时：会话执行注册表 + 全局事件总线。
// 派发的子任务在独立 goroutine 里跑；主对话的 SSE 断了也不影响执行，
// 进度通过 /api/events 全局事件流推给所有在线前端。

type runner struct {
	cancel    context.CancelFunc
	startedAt time.Time
}

func (a *App) initRuntime() {
	a.runners = map[string]*runner{}
	a.subscribers = map[chan Event]bool{}
	a.grants = map[string]*Grant{}
	a.approvals = map[string]*Approval{}
	a.remotes = map[string]*remoteClient{}
	// 受保护路径：助手配置文件本身与数据目录（含 API Key 与会话记录），
	// 任何模式禁访问。注意只保护配置文件本身，不保护其所在目录——默认
	// 工作区就是程序目录，整目录保护会让开箱即用的会话什么都写不了。
	a.protectPaths = []string{}
	if abs, err := filepath.Abs(a.CfgPath); err == nil {
		a.protectPaths = append(a.protectPaths, strings.ToLower(filepath.Clean(abs)))
	}
	if abs, err := filepath.Abs(a.Cfg.DataDir); err == nil {
		a.protectPaths = append(a.protectPaths, strings.ToLower(filepath.Clean(abs)))
	}
}

// StartRun 登记一个会话执行；已在跑则返回 false。
func (a *App) StartRun(id string) bool {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if _, ok := a.runners[id]; ok {
		return false
	}
	_, cancel := context.WithCancel(context.Background())
	a.runners[id] = &runner{cancel: cancel, startedAt: time.Now()}
	_ = a.Store.UpdateStatus(id, "running")
	return true
}

// EndRun 结束会话执行并置为空闲。
func (a *App) EndRun(id string) {
	a.runMu.Lock()
	r, ok := a.runners[id]
	if ok {
		delete(a.runners, id)
	}
	a.runMu.Unlock()
	if ok && r.cancel != nil {
		r.cancel()
	}
	_ = a.Store.UpdateStatus(id, "idle")
}

// IsRunning 判断会话是否在执行（优先内存注册表，其次数据库状态）。
func (a *App) IsRunning(id string) bool {
	a.runMu.Lock()
	_, ok := a.runners[id]
	a.runMu.Unlock()
	return ok
}

// broadcast 向所有在线前端广播事件（无订阅者时丢弃，不阻塞）。
func (a *App) broadcast(ev Event) {
	a.evtMu.Lock()
	defer a.evtMu.Unlock()
	for ch := range a.subscribers {
		select {
		case ch <- ev:
		default: // 订阅者跟不上就丢帧，事件流只做进度通知
		}
	}
}

func (a *App) subscribe() (chan Event, func()) {
	ch := make(chan Event, 64)
	a.evtMu.Lock()
	a.subscribers[ch] = true
	a.evtMu.Unlock()
	return ch, func() {
		a.evtMu.Lock()
		delete(a.subscribers, ch)
		a.evtMu.Unlock()
	}
}

// runSubAgent 是派发子任务的后台执行体：执行 → 广播进度 → 回报主对话 →
// 主对话空闲时自动跑一轮面向用户的汇报。
func (a *App) runSubAgent(parent *SessionMeta, sub *SessionMeta) {
	defer a.EndRun(sub.ID)
	a.broadcast(Event{Type: "sub_start", SessionID: sub.ID, Title: sub.Title})
	err := a.RunAgent(context.Background(), sub, func(ev Event) {
		if ev.Type == "tool_start" || ev.Type == "tool_end" {
			a.broadcast(Event{Type: "sub_tool", SessionID: sub.ID, Name: ev.Name, Args: ev.Args, Output: ev.Output})
		}
	})
	if err != nil {
		a.broadcast(Event{Type: "sub_end", SessionID: sub.ID, Error: err.Error()})
		_ = a.Store.AppendRow(sub.ID, &StoredMessage{
			Message: TextMessage("user", "【执行中断】上一轮执行中断："+err.Error()+"。可发「继续」接续。"),
			Kind:    "report", CreatedAt: now(),
		})
	} else {
		a.broadcast(Event{Type: "sub_end", SessionID: sub.ID})
	}
	a.broadcast(Event{Type: "session_updated", SessionID: sub.ID})
	if parent == nil {
		return
	}
	msgs, _ := a.Store.Messages(sub.ID)
	summary := renderSummary(a, sub, msgs)
	_ = a.Store.AppendRow(parent.ID, &StoredMessage{
		Message: TextMessage("user", "【派发回报】子会话「"+sub.Title+"」（"+sub.ID+"）本轮结束。\n\n"+summary+
			"\n\n请用用户听得懂的话汇报这件事的结果。"),
		Kind: "report", CreatedAt: now(),
	})
	a.broadcast(Event{Type: "report_ready", SessionID: parent.ID, Title: sub.Title})
	// 主对话空闲则自动跑一轮汇报；正被用户占用就等下轮对话自然带出
	if parent.Mode == "assistant" && a.StartRun(parent.ID) {
		go func() {
			defer a.EndRun(parent.ID)
			a.RunAgent(context.Background(), parent, func(ev Event) {
				ev.SessionID = parent.ID
				a.broadcast(ev)
			})
			a.broadcast(Event{Type: "session_updated", SessionID: parent.ID})
		}()
	}
}

// recoverInterrupted 服务启动时的中断恢复：把重启前 running 的会话补一条
// 中断记录并置为空闲；被派发的子会话同时给主对话补中断回报。
func (a *App) recoverInterrupted() {
	sessions, err := a.Store.RunningSessions()
	if err != nil {
		return
	}
	for _, s := range sessions {
		_ = a.Store.AppendRow(s.ID, &StoredMessage{
			Message: TextMessage("user", "【系统中断】服务在执行期间重启，本会话当时被中断。可发「继续」接续执行。"),
			Kind:    "report", CreatedAt: now(),
		})
		_ = a.Store.UpdateStatus(s.ID, "idle")
		if s.ParentID != "" {
			if p, perr := a.Store.Get(s.ParentID); perr == nil && p.Mode == "assistant" {
				_ = a.Store.AppendRow(p.ID, &StoredMessage{
					Message: TextMessage("user", "【派发回报】子会话「"+s.Title+"」（"+s.ID+"）因服务重启被中断，未完成。"),
					Kind:    "report", CreatedAt: now(),
				})
			}
		}
	}
}
