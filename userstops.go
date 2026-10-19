package main

// 用户从界面发起的「终止」操作（右侧栏 ✕ 关浏览器页面、进度面板停运行中的
// 程序）：与 AI 工具路径（browser_close / job_kill / 停止会话）共用同一批
// 底层收口，但多两件用户路径专属的事——
//   1. 打「用户关闭」标记 / 写明退出文案，让之后的工具报错与作业状态把
//      「用户主动终止」和「自身崩溃」分开，模型不误判；
//   2. 向所属会话投一条 report 注记（deliverToSessionNoKick：会话空闲只
//      落库、不拉起新一轮；运行中则进排队/插话队列，本轮结束自然看到），
//      模型下次醒来就知道发生了什么，而不是被用户消息触发一次对话。

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// browserUserClosed 该会话的用户是否在右栏主动关闭过浏览器页面（且此后未
// 再打开）：activeHost（tool_browser.go）据此把「浏览器未在运行」区分为用户
// 主动关闭（非崩溃），避免模型把用户操作误判成异常。按会话记账——A 会话
// 关掉自己的面板不该影响 B 会话的报错口径。
func (a *App) browserUserClosed(sid string) bool {
	a.browserMu.Lock()
	defer a.browserMu.Unlock()
	return !a.browserUserClosedAt[browserProfileKey(sid)].IsZero()
}

// handleBrowserClose 右栏浏览器视图 ✕ 的「真关闭」端点：只作用于该会话的
// 内嵌实例（桌面原生面板 / Electron webview / 网页形态共享无头），有头独立
// 窗口不碰——它自带关闭按钮，用户自己管。与 AI 的 browser_close 工具同走
// Close()（销毁实例、释放资源，重开时地址与登录态仍在），但这里是用户发起：
//   · 打「用户关闭」标记（browserUserClosed，browserFor 重新打开时清零）；
//   · 给该会话投 report 注记——只投给真正在用浏览器的会话（browserActive
//     有记账）；没开过浏览器的对话收到「用户关了浏览器」只会一头雾水。
func (a *App) handleBrowserClose(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Session string `json:"session"`
	}
	_ = json.NewDecoder(r.Body).Decode(&p) // body 可省：没有会话上下文就只关实例
	key := browserProfileKey(p.Session)
	emb := a.embeddedTarget(p.Session) // 锁外取：panelFor/Browser 内部拿锁，持锁调用会死锁
	if emb == nil || !emb.Started() {
		writeJSON(w, http.StatusOK, map[string]any{"closed": false}) // 本来就没开：幂等成功
		return
	}
	a.browserMu.Lock()
	if a.browserUserClosedAt == nil {
		a.browserUserClosedAt = map[string]time.Time{}
	}
	a.browserUserClosedAt[key] = time.Now()
	_, used := a.browserActive[key]
	a.browserMu.Unlock()
	emb.Close()
	if a.panelVisibleKey() == key { // 关的是当前显示中的面板：显示记账一并清空
		a.panelSetVisible("")
	}
	a.broadcast(Event{Type: "browser_nav", URL: "", Title: "", SessionID: p.Session}) // 工具栏地址/标题复位（面板形态 Close 自带广播，无头形态这里统一补）
	if sid := strings.TrimSpace(p.Session); sid != "" && used {
		if m, err := a.Store.Get(sid); err == nil {
			a.deliverToSessionNoKick(m, "【用户关闭浏览器】用户在右栏浏览器面板点 ✕ 关闭了页面（用户主动关闭，非崩溃）。"+
				"浏览器实例已销毁；下次 browser_open 会重新打开，各实例保留的地址与登录态仍在。若你正在依赖该页面的内容，请与用户确认后续。",
				kindReport, "webview")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"closed": true})
}

// handleStopChild 进度面板「运行中的程序」的停止端点：用户点名停某一个。
// 两类目标共用同一返回语义（stopped=true / 404）：
//   · 后台作业（job-*）：j.cancel() 优雅取消——与 job_kill、会话停止同一路径；
//   · 前台子进程（child-*）：killChildTree 按进程树强杀——与 stopRun 的硬终结
//     兜底 killSessionChildren 同款手法。
// 停止后投 report 注记告知模型：程序是被用户手动停止的（非自身出错），别误判。
func (a *App) handleStopChild(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || strings.TrimSpace(p.ID) == "" {
		writeErr(w, http.StatusBadRequest, "需要 id")
		return
	}
	id := strings.TrimSpace(p.ID)
	desc := ""
	// 后台作业：先记退出文案再 cancel（setExit 只认首个，晚到的「被终止」不会覆盖）
	a.jobsMu.Lock()
	j := a.jobs[id]
	a.jobsMu.Unlock()
	if j != nil && j.SessionID == m.ID {
		j.setExit("被用户在进度面板终止")
		j.stop(5 * time.Second) // 发终止并限时等进程树死透（结算纪律），超时由顶层 Job 兜底
		desc = "后台作业 " + j.Command + "（" + j.ID + "，已取消）"
	}
	// 前台子进程：按进程树强杀（killSessionChildren 的单点版）
	if desc == "" {
		childRegMu.Lock()
		c := childReg[id]
		childRegMu.Unlock()
		if c == nil || c.SessionID != m.ID || c.Exit() != "" {
			writeErr(w, http.StatusNotFound, "程序不存在或已结束（或属于其他会话）")
			return
		}
		if c.PID > 0 {
			killChildTree(c.PID)
		}
		c.setExit("被用户在进度面板终止")
		desc = "子进程 " + c.Command + "（" + c.Label + "，已强制结束）"
	}
	a.broadcast(Event{Type: "children", SessionID: m.ID}) // 进度面板立即刷新
	a.deliverToSessionNoKick(m, "【用户停止程序】用户在进度面板手动停止了正在运行的"+desc+
		"。这是用户主动操作，不是程序自身出错或崩溃；后台作业已产生的输出仍可用 job_output 读取。如需继续该任务，请先与用户确认。", kindReport, "stop")
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true})
}
