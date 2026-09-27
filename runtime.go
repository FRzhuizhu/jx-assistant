package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 后台运行时：会话执行注册表 + 全局事件总线。
// 派发的子任务在独立 goroutine 里跑；主对话的 SSE 断了也不影响执行，
// 进度通过 /api/events 全局事件流推给所有在线前端。
//
// 会话执行（sessionRun）拥有独立于 HTTP 请求的 ctx 与订阅者集合：发送消息的
// 请求只是「接上这条流」，浏览器刷新/关页只断开订阅，执行继续跑完并落库；
// 重新打开页面用 GET /api/sessions/{id}/stream 再接上继续看。只有显式停止
// （POST /stop）或进程退出才会终止执行。

type sessionRun struct {
	ctx       context.Context
	cancel    context.CancelFunc
	startedAt time.Time
	done      chan struct{} // 执行结束即关闭，订阅者据此退出等待

	mu       sync.Mutex
	subs     map[int]chan Event
	nextSub  int
	partial  string // 当前步骤已流出的正文（重连时补发，避免丢半句）
	thinking string // 当前步骤已流出的思考原文
	stopped  bool   // 是否由用户显式停止
}

// PendingMsg 是执行中的一条待生效用户消息：插话（下一步调用模型前注入）
// 或排队（本轮结束后作为独立一轮发送）。放在服务端，刷新页面不丢。
type PendingMsg struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

// PendingState 是某个会话当前的待生效消息快照（随会话详情与事件下发）。
type PendingState struct {
	Steer []*PendingMsg `json:"steer"`
	Queue []*PendingMsg `json:"queue"`
}

func (a *App) initRuntime() {
	a.runners = map[string]*sessionRun{}
	a.subscribers = map[chan Event]bool{}
	a.grants = map[string]*Grant{}
	a.approvals = map[string]*Approval{}
	a.questions = map[string]*UserQuestion{}
	a.remotes = map[string]*remoteClient{}
	a.pendingSteer = map[string][]*PendingMsg{}
	a.pendingQueue = map[string][]*PendingMsg{}
	a.runErrors = map[string]string{}
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

// beginRun 登记一次会话执行（ctx 独立于发起它的 HTTP 请求）；已在跑则返回 false。
func (a *App) beginRun(id string) (*sessionRun, bool) {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if _, ok := a.runners[id]; ok {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &sessionRun{ctx: ctx, cancel: cancel, startedAt: time.Now(), done: make(chan struct{}),
		subs: map[int]chan Event{}}
	a.runners[id] = r
	a.clearRunError(id) // 新一轮开始：上一次的报错条自动消失
	_ = a.Store.UpdateStatus(id, "running")
	return r, true
}

// finishRun 注销执行、关闭订阅者并置为空闲。
func (a *App) finishRun(id string) {
	a.runMu.Lock()
	r, ok := a.runners[id]
	if ok {
		delete(a.runners, id)
	}
	a.runMu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	for i, ch := range r.subs {
		close(ch)
		delete(r.subs, i)
	}
	r.mu.Unlock()
	close(r.done)
	r.cancel()
	_ = a.Store.UpdateStatus(id, "idle")
	// 本轮结束（正常/停止/报错收口都走这里）：执行已登记的会话后交付。
	// 异步执行避免大文件搬运拖住收口；认领机制保证与启动恢复不重不漏。
	go a.flushDeliveries(id)
}

// runOf 取会话执行中的运行体；未在执行返回 nil。
func (a *App) runOf(id string) *sessionRun {
	a.runMu.Lock()
	r := a.runners[id]
	a.runMu.Unlock()
	return r
}

// stopRun 请求停止会话执行（协作式取消：模型流、工具、等待都会收到 ctx 取消）。
func (a *App) stopRun(id string) bool {
	r := a.runOf(id)
	if r == nil {
		return false
	}
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.cancel()
	return true
}

// publish 把一个执行事件发给该会话的全部订阅者（订阅者跟不上就丢帧，绝不阻塞执行）。
func (r *sessionRun) publish(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// observe 记录当前步骤已流出的正文/思考，供重连补发。
func (r *sessionRun) observe(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch ev.Type {
	case "delta":
		r.partial += ev.Delta
	case "reasoning":
		r.partial += ""
		r.thinking += ev.Delta
	case "message", "done", "retry":
		r.partial, r.thinking = "", ""
	}
}

// snapshot 取当前进行中的流式文本（重连时先补上，再接后续增量）。
func (r *sessionRun) snapshot() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.partial, r.thinking
}

// subscribe 订阅该执行的后续事件；返回的收尾函数必须调用（断开订阅）。
func (r *sessionRun) subscribe() (chan Event, func()) {
	ch := make(chan Event, 128)
	r.mu.Lock()
	id := r.nextSub
	r.nextSub++
	r.subs[id] = ch
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		if _, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(ch)
		}
		r.mu.Unlock()
	}
}

// IsRunning 判断会话是否在执行（内存注册表为准，数据库状态只作启动恢复用）。
func (a *App) IsRunning(id string) bool {
	return a.runOf(id) != nil
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

// pushPending 记下一条待生效消息：mode=steer 插话（下一步调用模型前注入），
// 其他按排队（本轮结束后作为独立一轮发送）。
func (a *App) pushPending(sessionID, mode, content string) *PendingMsg {
	pm := &PendingMsg{ID: newID(), Content: content, CreatedAt: now()}
	a.pendMu.Lock()
	if mode == "steer" {
		a.pendingSteer[sessionID] = append(a.pendingSteer[sessionID], pm)
	} else {
		a.pendingQueue[sessionID] = append(a.pendingQueue[sessionID], pm)
	}
	a.pendMu.Unlock()
	a.broadcastPending(sessionID)
	return pm
}

// drainSteer 取走并清空会话的插话队列（agent 循环步间排水）。
func (a *App) drainSteer(sessionID string) []*PendingMsg {
	a.pendMu.Lock()
	q := a.pendingSteer[sessionID]
	delete(a.pendingSteer, sessionID)
	a.pendMu.Unlock()
	return q
}

// popQueue 取走排队队列里的第一条（本轮结束后串行发送）。
func (a *App) popQueue(sessionID string) *PendingMsg {
	a.pendMu.Lock()
	defer a.pendMu.Unlock()
	q := a.pendingQueue[sessionID]
	if len(q) == 0 {
		return nil
	}
	first := q[0]
	if len(q) == 1 {
		delete(a.pendingQueue, sessionID)
	} else {
		a.pendingQueue[sessionID] = q[1:]
	}
	return first
}

// requeueSteer 把本轮没来得及注入的插话转为排队（不丢消息，下一轮生效）。
func (a *App) requeueSteer(sessionID string) {
	left := a.drainSteer(sessionID)
	if len(left) == 0 {
		return
	}
	a.pendMu.Lock()
	a.pendingQueue[sessionID] = append(a.pendingQueue[sessionID], left...)
	a.pendMu.Unlock()
	a.broadcastPending(sessionID)
}

// pendingOf 取会话当前的待生效消息快照（永不返回 nil，前端可直接取用）。
func (a *App) pendingOf(sessionID string) *PendingState {
	a.pendMu.Lock()
	defer a.pendMu.Unlock()
	st := &PendingState{Steer: []*PendingMsg{}, Queue: []*PendingMsg{}}
	st.Steer = append(st.Steer, a.pendingSteer[sessionID]...)
	st.Queue = append(st.Queue, a.pendingQueue[sessionID]...)
	return st
}

// updatePending 处理排队坞上的操作：移除，或把排队项提前为插话。
func (a *App) updatePending(sessionID, msgID, action string) bool {
	a.pendMu.Lock()
	moved := (*PendingMsg)(nil)
	found := false
	for i, pm := range a.pendingQueue[sessionID] {
		if pm.ID != msgID {
			continue
		}
		found = true
		a.pendingQueue[sessionID] = append(a.pendingQueue[sessionID][:i], a.pendingQueue[sessionID][i+1:]...)
		if len(a.pendingQueue[sessionID]) == 0 {
			delete(a.pendingQueue, sessionID)
		}
		if action == "steer" {
			moved = pm
		}
		break
	}
	if !found {
		for i, pm := range a.pendingSteer[sessionID] {
			if pm.ID != msgID {
				continue
			}
			found = true
			a.pendingSteer[sessionID] = append(a.pendingSteer[sessionID][:i], a.pendingSteer[sessionID][i+1:]...)
			if len(a.pendingSteer[sessionID]) == 0 {
				delete(a.pendingSteer, sessionID)
			}
			break
		}
	}
	if moved != nil {
		a.pendingSteer[sessionID] = append(a.pendingSteer[sessionID], moved)
	}
	a.pendMu.Unlock()
	if found {
		a.broadcastPending(sessionID)
	}
	return found
}

// broadcastPending 把待生效消息的最新快照推给所有在线前端（多窗口/刷新后一致）。
func (a *App) broadcastPending(sessionID string) {
	a.broadcast(Event{Type: "pending", SessionID: sessionID, Pending: a.pendingOf(sessionID)})
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


// maxGoalRounds 是目标模式自动续跑的轮数上限（防止无限消耗）。
const maxGoalRounds = 8

// runGoalRounds 是目标模式的续跑驱动：跑一轮 RunAgent；轮末若目标仍
// active 且未达上限，注入一条 goal_round 消息自动开下一轮，直到模型调用
// update_goal 标记 complete/blocked、达到上限、出错或被取消。
// 未开目标模式时就是普通的一轮 RunAgent。
func (a *App) runGoalRounds(ctx context.Context, sess *SessionMeta, emit func(Event)) error {
	for round := 0; ; round++ {
		err := a.RunAgent(ctx, sess, emit)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !sess.GoalMode || sess.GoalStatus != "active" {
			return nil
		}
		if round+1 >= maxGoalRounds {
			sm := a.appendRow(sess, StoredMessage{
				Message: TextMessage("user", "【目标续跑】已达到自动续跑上限（"+itoa(maxGoalRounds)+" 轮），暂停自动推进；目标尚未标记完成。可补充指示后继续。"),
				Kind: kindReport, Origin: "goal", CreatedAt: now(),
			})
			emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: kindReport, Origin: "goal"})
			return nil
		}
		sm := a.appendRow(sess, StoredMessage{
			Message: TextMessage("user", "<goal_round> 继续推进当前目标（第 "+itoa(round+2)+" 轮）。若已达成或确认无法推进，调用 update_goal 汇报并写最终总结；否则继续执行下一步，保持进度、不要重复已完成的步骤。"),
			Kind: kindReport, Origin: "goal", CreatedAt: now(),
		})
		emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: kindReport, Origin: "goal"})
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// executeRun 是会话执行体（在独立 goroutine 里跑，与发起它的 HTTP 请求无关）：
// 先把 opening 事件发出去（用户消息/标题），再跑当前这一轮（含目标模式续跑），
// 然后把排队消息逐条作为独立一轮发出，最后给出明确的终态交代。
// 异常结束一律落一条可见记录并写日志——绝不出现「模型没输出、对话就停在
// 半路」的静默停止。
func (a *App) executeRun(m *SessionMeta, run *sessionRun, opening []Event) {
	defer a.finishRun(m.ID)
	emit := func(ev Event) {
		ev.SessionID = m.ID
		run.observe(ev)
		run.publish(ev)
		// 只在「消息级」里程碑广播会话更新（前端据此刷侧栏/列表）；
		// delta/reasoning 这类高频事件不广播，否则会打出一串无效请求
		switch ev.Type {
		case "delta", "reasoning", "tool_start", "tool_end", "tool_wait", "resume", "pending":
		case "done", "stopped":
			// 终态原样进全局总线：前端据此点亮「完成未查看」绿点并刷新侧栏
			a.broadcast(ev)
		default:
			a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
		}
	}
	defer func() { // 执行体自身出问题时也要留下痕迹，不能静默消失
		if p := recover(); p != nil {
			log.Printf("[对话] 会话 %s 执行体异常: %v", m.ID, p)
			emit(Event{Type: "error", Error: fmt.Sprintf("执行体异常: %v", p)})
			a.auditWrite(m, map[string]any{"type": "error", "error": fmt.Sprintf("执行体异常: %v", p)})
		}
	}()
	for _, ev := range opening {
		emit(ev)
	}

	err := a.runGoalRounds(run.ctx, m, emit)
	// 本轮结束仍没被注入的插话转为排队：作为独立一轮接着发，消息不丢
	a.requeueSteer(m.ID)

	// 排队消息逐条续跑（停止/出错后不再自动续，留给用户决定）
	for err == nil && run.ctx.Err() == nil {
		pm := a.popQueue(m.ID)
		if pm == nil {
			break
		}
		a.broadcastPending(m.ID)
		sm := a.appendMessage(m, TextMessage("user", pm.Content))
		emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: kindChat})
		err = a.runGoalRounds(run.ctx, m, emit)
		a.requeueSteer(m.ID)
	}
	a.broadcastPending(m.ID)

	// 终态：正常完成与用户停止都不往界面塞话（对话里只有真实对话）；
	// 只有真实的失败（模型/网络/费用等）才进前端的报错区，并带重试按钮。
	// 三种终态都在审计日志留痕（导出日志时间线可见，不发给模型）。
	switch {
	case err == nil:
		emit(Event{Type: "done"})
	case a.isStopped(run) || run.ctx.Err() != nil:
		log.Printf("[对话] 会话 %s 已被用户停止", m.ID)
		a.clearRunError(m.ID)
		a.auditWrite(m, map[string]any{"type": "stop"})
		emit(Event{Type: "stopped"})
	default:
		log.Printf("[对话] 会话 %s 本轮结束于错误: %v", m.ID, err)
		a.setRunError(m.ID, err.Error())
		a.auditWrite(m, map[string]any{"type": "error", "error": err.Error()})
		emit(Event{Type: "error", Error: err.Error()})
	}
	emit(Event{Type: "session_updated"})
}

// setRunError 记下最近一次失败原因（前端在输入框上方显示报错条 + 重试按钮）。
func (a *App) setRunError(id, msg string) {
	a.errMu.Lock()
	a.runErrors[id] = msg
	a.errMu.Unlock()
}

// clearRunError 清掉报错（新一轮开始/重试成功即消失）。
func (a *App) clearRunError(id string) {
	a.errMu.Lock()
	delete(a.runErrors, id)
	a.errMu.Unlock()
}

// runError 取会话最近一次失败原因；没有则返回空串。
func (a *App) runError(id string) string {
	a.errMu.Lock()
	defer a.errMu.Unlock()
	return a.runErrors[id]
}

// isStopped 判断本次执行是否由用户显式停止。
func (a *App) isStopped(run *sessionRun) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.stopped
}

// runSubAgent 是派发子任务的后台执行体：执行 → 广播进度 → 回报主对话 →
// 主对话空闲时自动跑一轮面向用户的汇报。run 由派发方通过 beginRun 登记后传入。
func (a *App) runSubAgent(parent *SessionMeta, sub *SessionMeta, run *sessionRun) {
	defer a.finishRun(sub.ID)
	a.broadcast(Event{Type: "sub_start", SessionID: sub.ID, Title: sub.Title})
	err := a.runGoalRounds(run.ctx, sub, func(ev Event) {
		if ev.Type == "tool_start" || ev.Type == "tool_end" {
			a.broadcast(Event{Type: "sub_tool", SessionID: sub.ID, Name: ev.Name, Args: ev.Args, Output: ev.Output})
		}
		ev.SessionID = sub.ID
		run.observe(ev)
		run.publish(ev)
	})
	if err != nil {
		a.broadcast(Event{Type: "sub_end", SessionID: sub.ID, Error: err.Error()})
		_ = a.Store.AppendRow(sub.ID, &StoredMessage{
			Message: TextMessage("user", "【执行中断】上一轮执行中断："+err.Error()+"。可发「继续」接续。"),
			Kind:    kindReport, Origin: "interrupt", CreatedAt: now(),
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
	// 回报指令按父会话形态分流：数字助理向用户汇报；工作任务带着结果继续推进
	followUp := "请用用户听得懂的话汇报这件事的结果。"
	if parent.Mode != "assistant" {
		followUp = "请结合这个结果继续推进当前任务；若已全部完成，给出最终答复。"
	}
	_ = a.Store.AppendRow(parent.ID, &StoredMessage{
		Message: TextMessage("user", "【派发回报】任务会话「"+sub.Title+"」（"+sub.ID+"）本轮结束。\n\n"+summary+
			"\n\n"+followUp),
		Kind: kindReport, Origin: "dispatch", CreatedAt: now(),
	})
	// 先登记消化回报的续跑轮再广播回报就绪：前端收到 report_ready 重查会话时
	// 必见 running，能立刻接上这轮流、实时看输出。父会话空闲才续跑（助理＝
	// 面向用户汇报，工作任务＝带着结果续推）；正被用户占用就等下轮对话自然带出
	prun, started := a.beginRun(parent.ID)
	a.broadcast(Event{Type: "report_ready", SessionID: parent.ID, Title: sub.Title})
	if started {
		go func() {
			defer a.finishRun(parent.ID)
			prun.publish(Event{Type: "run_start"})
			_ = a.RunAgent(prun.ctx, parent, func(ev Event) {
				ev.SessionID = parent.ID
				prun.observe(ev)
				prun.publish(ev)
			})
			a.broadcast(Event{Type: "session_updated", SessionID: parent.ID})
			a.broadcast(Event{Type: "done", SessionID: parent.ID}) // 汇报轮结束：未查看的会话点亮绿点
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
			Kind:    kindReport, Origin: "interrupt", CreatedAt: now(),
		})
		_ = a.Store.UpdateStatus(s.ID, "idle")
	// 中断回报的归属：优先派发链（dispatched_by），子对话退回父会话——
	// 助理与工作任务都会派发，统一回报给派发方（重启后不自动续跑，等用户接续）
	owner := s.DispatchedBy
	if owner == "" {
		owner = s.ParentID
	}
	if owner != "" {
		if _, perr := a.Store.Get(owner); perr == nil {
			_ = a.Store.AppendRow(owner, &StoredMessage{
				Message: TextMessage("user", "【派发回报】任务会话「"+s.Title+"」（"+s.ID+"）因服务重启被中断，未完成。"),
				Kind:    kindReport, Origin: "dispatch", CreatedAt: now(),
			})
		}
	}
	}
}
