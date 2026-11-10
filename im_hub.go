package main

// IM 中枢（阶段 2）：把「聊天软件里的消息」接进 jx 的会话体系，把「数字经理的
// 最终答复」推回聊天软件。
//
// 三条设计原则（别为了省事绕开）：
//  1. **入站只有一条路**：一律走 deliverToSession（runtime.go:522），与前端发
//     消息完全同语义（空闲就落库并占执行权、执行中就插话）。IM 是「另一个用户
//     入口」，不是第二套投递机制——否则提权、插话、排队、停止的语义会分叉。
//  2. **出站只有一个点**：挂 sessionRun.publish（runtime.go:416）——所有执行体
//     （executeRun / kickSessionRound / 子代理 / 定时任务）的事件都从那里出去。
//     注意**不是** broadcast：broadcast 只承载 session_updated/done/审批，正文
//     事件（message）根本不在其中，挂那里会一条都推不出去。业务代码同样不需要
//     知道 IM 的存在。
//  3. **推什么要克制**：只推「当前数字经理会话的最终答复」（role=assistant 且
//     没有 tool_calls）。工具过程、子会话过程一律不推——任务完成/失败本来就经
//     「派发回报」汇总到经理那一轮，经理的最终答复就是用户在微信里该看到的东西。
//     多路推送只会把聊天窗口刷成日志流。
//
// 幂等：入站靠适配层的 seen 集合 + 平台 message_id；出站靠每个会话的 lastSeq
//（重启/重连后不会把历史答复重发一遍）。同一会话的推送走一条串行发件队列，
// 保证顺序，且发送失败不阻塞 broadcast、不影响后续消息。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// imOriginName 是 IM 入站消息的 origin 标记（kind 仍是 chat：前端按用户气泡显示，
// 但落库行上能看出「这句来自微信」）。
const imOriginName = "im"

// imBinding 是一条「聊天会话 ↔ 数字经理」的绑定：已推到的序号 + 该序号属于哪一
// 版数字经理。存 SessionID 只为一件事：数字经理换版本后消息序号会从头计，若不
// 认版本，旧绑定里的 lastSeq 会把新版本的答复永久吞掉（见 imRebaseBinding）。
type imBinding struct {
	Channel   string `json:"channel"`
	ChatID    string `json:"chatId"`
	SessionID string `json:"sessionId,omitempty"` // lastSeq 所属的数字经理会话
	LastSeq   int64  `json:"lastSeq"`
	UpdatedAt string `json:"updatedAt"`
	// Remark 是用户给这个联系人起的本地备注（微信 iLink 协议只给 ilink_user_id，
	// 拿不到昵称），面板左列优先显示它。
	Remark string `json:"remark,omitempty"`
	// ReadUpTo 是「已读到哪条流水」（im_messages.id），面板未读红点按它算。
	ReadUpTo int64 `json:"readUpTo,omitempty"`
}

type imBindingFile struct {
	Chats []*imBinding `json:"chats"`
}

// imOutItem 是一条待推送的答复。
type imOutItem struct {
	text      string
	seq       int64
	sessionID string // seq 所属会话（推送成功后据此推进 lastSeq）
	logID     int64  // 流水行 id（发送后回填 sent/failed，面板据此显示状态徽标）
}

// imOutbox 每个 IM 会话一条串行发件队列（保证顺序 + 单条失败不拖累后续）。
// 另记每个会话的连续失败次数与退避截止：平台开始拒发时（`code=-2 微信拒绝发送消息`
// 就是被限流的信号），每轮都硬推只会加剧限流，所以连续失败到阈值就先停一会儿。
type imOutbox struct {
	mu     sync.Mutex
	queues map[string]chan imOutItem
	fails  map[string]int       // key=渠道|聊天 → 连续失败次数
	until  map[string]time.Time // key=渠道|聊天 → 退避截止
}

func newIMOutbox() *imOutbox {
	return &imOutbox{
		queues: map[string]chan imOutItem{},
		fails:  map[string]int{},
		until:  map[string]time.Time{},
	}
}

// 连续失败到这个次数就退避这么久（轻量：一次成功即清零）。
const (
	imPushBackoffAfter = 3
	imPushBackoffFor   = 60 * time.Second
)

// imPushCooldown 返回该会话的退避剩余时间与连续失败次数。
func (a *App) imPushCooldown(channel, chatID string) (time.Duration, int) {
	out := a.imOutbox()
	key := channel + "|" + chatID
	out.mu.Lock()
	defer out.mu.Unlock()
	left := time.Until(out.until[key])
	if left < 0 {
		left = 0
	}
	return left, out.fails[key]
}

// imPushNoteResult 记一次推送结果，返回记完之后的连续失败次数（成功清零）。
func (a *App) imPushNoteResult(channel, chatID string, ok bool) int {
	out := a.imOutbox()
	key := channel + "|" + chatID
	out.mu.Lock()
	defer out.mu.Unlock()
	if ok {
		delete(out.fails, key)
		delete(out.until, key)
		return 0
	}
	out.fails[key]++
	n := out.fails[key]
	if n >= imPushBackoffAfter {
		out.until[key] = time.Now().Add(imPushBackoffFor)
	}
	return n
}

// imPushErrCode 取错误里最能拿去查的那个码：优先平台原始码（ret/errcode，如 -2），
// 其次协议层自己的 code（network/rate-limited…）。人话文案留在 imLogUpdate 里。
func imPushErrCode(err error) string {
	var we *weixinErr
	if errors.As(err, &we) {
		if we.ProviderCode != "" {
			return we.ProviderCode
		}
		if we.Code != "" {
			return we.Code
		}
	}
	return "unknown"
}

// ---------- 入站 ----------

// imOnInbound 是一条 IM 入站消息的唯一落点（适配层解析完就交到这里）。
// 这里同时记一条入站流水（im_log.go）：白名单拒绝、群消息、只带图片的消息也记——
// 「我在微信发了，助手没反应」正是最需要能回看的那类情况。
func (a *App) imOnInbound(in IMInbound) {
	if a == nil || a.Cfg == nil {
		return
	}
	logID := a.imLogAppend(imLogEntry{
		Channel: in.Conv.Channel, ChatID: in.Conv.ChatID, Direction: imDirIn,
		Kind: imInboundKind(in), Text: in.Text, PlatformID: in.MessageID,
	})
	// 结局在这条消息处理完后回填（两条 SQL 总成对出现，面板不会停在「处理中」）
	status, statusErr := imStatusAccepted, ""
	defer func() { a.imLogUpdate(logID, status, statusErr) }()
	if in.Conv.ChatType == "group" {
		status, statusErr = imStatusGroup, "群聊暂不支持"
		a.imSay(in.Conv, "群聊暂不支持：请私聊我，我才能按你的身份判断权限。")
		return
	}
	// 白名单：空白名单 = 谁都不受理（fail closed），但绑定者默认放行——三条自动
	// 信任入口见 im_allow.go（绑定即写 owner / 启动补齐 / 下面这条兜底 TOFU）。
	if !a.Cfg.imAllowWeixin(in.Conv.UserID) {
		if a.imAllowFirstSender(in.Conv.Channel, in.Conv.UserID) {
			// 刚把你的号信任了：说清楚，并给「如果不是我」留出口（解绑）。
			a.imSay(in.Conv, "已把你的账号加入白名单；如果不是你本人操作，请立即在桌面端解绑微信。")
		} else {
			status, statusErr = imStatusRejected, "不在白名单"
			log.Printf("[IM] 拒绝未授权账号 %s 的消息（白名单 %d 条）", maskIMID(in.Conv.UserID), a.Cfg.imAllowCount())
			a.imSay(in.Conv, "未授权：这个微信账号不在白名单里，消息没有处理。\n"+
				"机器人只认绑定它的那个微信账号：请用扫码绑定的号发消息；要换号，在桌面端打开「IM」面板 →「绑定」抽屉里解除绑定后重新扫码。\n"+
				"你的 id："+in.Conv.UserID)
			return
		}
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		status, statusErr = imStatusRejected, "只收到图片/文件/语音"
		a.imSay(in.Conv, "只收到图片/文件/语音：文件与图片暂时还处理不了（阶段 4 接），先用文字告诉我。")
		return
	}
	if a.imHandleCommand(in.Conv, text) {
		status = imStatusCommand
		return
	}
	// 审批/提问的答复：微信发起的任务产生的待办，就在微信里回「批准/拒绝/回答」。
	// 放在白名单之后（非白名单连解析都不做）、命令之后（/status /stop 照旧优先）。
	if a.imHandleInboundDecision(in.Conv, text) {
		status = imStatusCommand // 控制指令：已消费，不落会话
		return
	}
	m, err := a.assistantSession()
	if err != nil || m == nil {
		status, statusErr = imStatusRejected, "助手主对话不可用"
		a.imSay(in.Conv, "助手主对话不可用："+errTextOf(err))
		return
	}
	// 会话视图要给「来自微信」的入站消息打来源徽标、给答复打送达状态，
	// 都按 session_id 查流水——入站这条落库时还不知道投给谁，这里补上。
	a.imLogSetSession(logID, m.ID)
	b := a.imBindChat(in.Conv.Channel, in.Conv.UserID)
	log.Printf("[IM] 投递入站消息到数字经理（%s，会话 %s）：%s", maskIMID(in.Conv.UserID), m.ID, imTruncate(text, 60))
	d := a.deliverToSession(m, text, kindChat, imOriginName)
	if d.Started {
		// 给这一轮打来源标记：它产生的审批/提问要回到这个聊天。
		d.Run.setOrigin(imOriginTag(in.Conv.Channel, in.Conv.ChatID))
		kickSessionRound(a, m, d.Run)
	} else {
		// 正在执行：已插话，它下一步就会看到（与前端「对话中途发消息」同语义）。
		// 注意这一轮本身的来源标记不改：插话进的是「已经开跑的那一轮」，它的审批
		// 该回哪就回哪（桌面端发起的那一轮不会因为一句微信插话就改成推微信）。
		log.Printf("[IM] 数字经理正在执行，消息已插话（%s）", maskIMID(b.ChatID))
	}
}

// imHandleCommand 处理 /help /status /stop（返回 true 表示已消费，不再进会话）。
func (a *App) imHandleCommand(conv IMConversation, text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "/help", "/?", "/帮助":
		a.imSay(conv, "在微信里直接说话就行：我会把话转给数字经理，它安排任务，完成后把结果回给你。\n"+
			"微信发起的任务要审批/提问时会推到这里：回「批准」或「拒绝」（多项时带序号，如「1 批准」），提问直接回内容；「始终允许」这类常设授权只能在桌面端点。\n"+
			"/status 看当前状态　/stop 叫停正在跑的这一轮\n"+
			"长答复会分几条发（微信单条上限 1800 字）。")
		return true
	case "/status", "/状态":
		a.imSay(conv, a.imStatusText())
		return true
	case "/stop", "/停":
		m := a.currentAssistant()
		if m == nil {
			a.imSay(conv, "当前没有主对话，没什么可停的。")
			return true
		}
		if a.stopRunBy(m.ID, imOriginName, "用户在微信里叫停") {
			a.imSay(conv, "已叫停这一轮。")
		} else {
			a.imSay(conv, "现在没有在跑的回合。")
		}
		return true
	}
	return false
}

// imStatusText 是 /status 的正文（运行中会话数 + IM 连接态 + 白名单规模）。
func (a *App) imStatusText() string {
	b := &strings.Builder{}
	b.WriteString("【状态】" + imNow() + "\n")
	running := a.runningIDs()
	b.WriteString(fmt.Sprintf("运行中会话：%d 个\n", len(running)))
	if m := a.currentAssistant(); m != nil {
		b.WriteString("数字经理：" + m.Title + "\n")
	} else {
		b.WriteString("数字经理：未创建\n")
	}
	for _, st := range a.imStatuses() {
		line := fmt.Sprintf("渠道 %s：%s", st.Channel, imStateText(st.State))
		if st.AccountID != "" {
			line += "（账号 " + st.AccountID + "）"
		}
		if st.LastError != "" {
			line += "，最近错误：" + st.LastError
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(fmt.Sprintf("白名单：%d 个账号可下指令", a.Cfg.imAllowCount()))
	return b.String()
}

// imStateText 把渠道状态翻译成给人看的一句话。
func imStateText(state string) string {
	switch state {
	case "connected":
		return "已连接"
	case "connecting":
		return "连接中"
	case "waiting_scan":
		return "等待扫码绑定"
	case "failed":
		return "连接失败"
	case "disabled":
		return "未启用"
	}
	if state == "" {
		return "未启用"
	}
	return state
}

// ---------- 出站 ----------

// imOnEvent 消费执行事件流（挂在 sessionRun.publish 上，只看「数字经理的最终答复」）。
// 钩子在 publish 的订阅者锁之外调用：这里要读配置、查库、碰文件。
//
// origin 是**这一轮**的来源标记（`r.originOf()`，见 im_push.go 顶部）：只有
// `im:weixin:<chatId>`（微信入站触发）与 `todo:weixin`（明确标记的定时任务）才推，
// 桌面端/子任务/普通定时任务一律不推——推太多会被微信判骚扰并拒发。
func (a *App) imOnEvent(ev Event, origin string) {
	if a == nil || a.Cfg == nil || !a.Cfg.imWeixinOn() {
		return
	}
	// 热路径：delta/reasoning 等高频事件也走这里，先做零成本判断。
	if !imIsFinalAnswer(ev) {
		return
	}
	// 来源闸：不是「微信问的」或「明确要求推微信的定时任务」就到此为止。
	todoPush := origin == imOriginTodoWeixin
	originChannel, originChat, fromIM := imParseOrigin(origin)
	if !fromIM && !todoPush {
		return
	}
	// 没有任何绑定就不必查库算「当前数字经理」。
	binds := a.imBindingList()
	if len(binds) == 0 {
		return
	}
	cur := a.currentAssistantID()
	if !imShouldPush(ev, cur, origin) {
		return
	}
	text := imCapPushText(strings.TrimSpace(ev.Message.Text()))
	// 「桌面在线不推」是策略性跳过，不是故障——但也要在面板里留痕，否则用户
	// 事后只会看到「微信没收到这条」，误以为是丢了。
	quiet := a.imQuietNow()
	// 微信问的就回给**问话的那个聊天**：别的绑定不该收到别人的答复。
	// （定时任务不知道是谁问的，按绑定表全员推——它本来就是「推给人看」的。）
	// 找不到对应绑定时宁可退回全员推，也别让微信里问的话一条答复都收不到。
	targets := binds
	if fromIM {
		only := make([]imBinding, 0, 1)
		for _, b := range binds {
			if b.Channel == originChannel && b.ChatID == originChat {
				only = append(only, b)
			}
		}
		if len(only) > 0 {
			targets = only
		} else {
			log.Printf("[IM] 来源聊天 %s 在绑定表里没有记录，按现有绑定推送", maskIMID(originChat))
		}
	}
	for _, b := range targets {
		if b.Channel != "weixin" {
			continue
		}
		if b.SessionID != cur {
			// 换了数字经理版本：序号重新计数，绑定得跟着重置，否则新答复全被吞。
			b = a.imRebaseBinding(b.Channel, b.ChatID, cur)
		}
		if ev.Seq > 0 && ev.Seq <= b.LastSeq {
			continue // 已经推过（重启/重连后的事件重放）
		}
		if quiet {
			a.imLogAppend(imLogEntry{
				Channel: b.Channel, ChatID: b.ChatID, Direction: imDirOut, Kind: imKindAnswer,
				Text: text, Status: imStatusSkipped, Error: "桌面端在线，按「静默」设置未推送",
				SessionID: cur, Seq: ev.Seq,
			})
			continue
		}
		// 定时任务每日闸：一个聊天一天最多 imTodoPushDailyMax 条，超了不发。
		if todoPush && !a.imTodoPushAllow(b.ChatID) {
			log.Printf("[IM] 定时推送超过每日上限（%d 条），已跳过（%s）", imTodoPushDailyMax, maskIMID(b.ChatID))
			a.imLogAppend(imLogEntry{
				Channel: b.Channel, ChatID: b.ChatID, Direction: imDirOut, Kind: imKindAnswer,
				Text: text, Status: imStatusSkipped,
				Error:     fmt.Sprintf("定时推送超过每日上限（%d 条），已跳过", imTodoPushDailyMax),
				SessionID: cur, Seq: ev.Seq,
			})
			continue
		}
		a.imEnqueuePush(b.Channel, b.ChatID, text, ev.Seq, cur)
	}
}

// imIsFinalAnswer 只按事件内容判断「这是不是一段该给人看的答复」（不查库）。
//   - 只认 message 事件里已落库的 assistant 正文（kind=chat）；
//   - 带 tool_calls 的是「准备调工具」的中间态，推出去等于把过程当结论发给人。
func imIsFinalAnswer(ev Event) bool {
	if ev.Type != "message" || ev.Message == nil || ev.Kind != kindChat {
		return false
	}
	if ev.Message.Role != "assistant" || len(ev.Message.ToolCalls) > 0 {
		return false
	}
	return strings.TrimSpace(ev.Message.Text()) != ""
}

// imShouldPush 在 imIsFinalAnswer 之上再加两道闸：
//  1. **来源**：只有微信入站触发的那一轮（`im:<渠道>:<chatId>`）或明确要求推微信
//     的定时任务（`todo:weixin`）才推——桌面端/子任务/普通定时任务一律不推
//     （2026-11-10 用户定的规则，见 im_push.go）；
//  2. **是不是当前数字经理会话**：子会话/任务会话的过程不进聊天窗口——它们的
//     结果本来就会经「派发回报」汇总进经理那一轮，经理的答复才是用户该看的。
// 纯函数，单测直接覆盖。
func imShouldPush(ev Event, curID, origin string) bool {
	if !imIsFinalAnswer(ev) {
		return false
	}
	if !imPushableOrigin(origin) {
		return false
	}
	return curID != "" && ev.SessionID == curID
}

// imMaxPushChars 单条答复推送上限：超过就截断并提示去桌面端看全文。微信按 1800
// 字分段，6000 字已经是 4 条——再长就不是「聊天里能读」的东西了。
const imMaxPushChars = 6000

// imCapPushText 按上限截断答复（保留换行：长答复的层次感全靠换行）。
func imCapPushText(s string) string {
	r := []rune(s)
	if len(r) <= imMaxPushChars {
		return s
	}
	return string(r[:imMaxPushChars]) + "\n…（答复过长，已截断；完整内容请在桌面端查看）"
}

// imQuietNow 判断「桌面在线时是否不推」：开关打开且此刻有前端订阅者（有人在看
// 界面）就不推，避免同一条答复在两处重复打扰。
func (a *App) imQuietNow() bool {
	if a.Cfg == nil || a.Cfg.IM.QuietWhenDesktop == nil || !*a.Cfg.IM.QuietWhenDesktop {
		return false
	}
	a.evtMu.Lock()
	defer a.evtMu.Unlock()
	return len(a.subscribers) > 0
}

// imSay 是「回一句即时话」的入口（命令回执、拒绝说明、审批提示），走同一条发件
// 队列，保证与后续答复的顺序。
func (a *App) imSay(conv IMConversation, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.imEnqueuePushKind(conv.Channel, conv.UserID, text, 0, "", imKindNotice)
}

// imEnqueuePush 把一条文本排进该会话的串行发件队列（首次使用就起一个 worker）。
// sessionID 与 seq 一起用于推送成功后推进 lastSeq（imSay 这类即时话不推进）。
// 语意类别按序号推断：带序号的是「数字经理的答复」，不带的是命令回执/系统提示。
func (a *App) imEnqueuePush(channel, chatID, text string, seq int64, sessionID string) {
	kind := imKindNotice
	if seq > 0 {
		kind = imKindAnswer
	}
	a.imEnqueuePushKind(channel, chatID, text, seq, sessionID, kind)
}

// imEnqueuePushKind 是 imEnqueuePush 的带类别版本。
// 入队同时记一条 pending 流水：推送成功/失败再回填——「这条到底发出去了没有」
// 是用户在面板里最想确认的事，光有日志文件不算。
func (a *App) imEnqueuePushKind(channel, chatID, text string, seq int64, sessionID, kind string) {
	if chatID == "" || strings.TrimSpace(text) == "" {
		return
	}
	if kind == "" {
		kind = imKindNotice
	}
	logID := a.imLogAppend(imLogEntry{
		Channel: channel, ChatID: chatID, Direction: imDirOut, Kind: kind,
		Text: text, Status: imStatusPending, SessionID: sessionID, Seq: seq,
	})
	out := a.imOutbox()
	key := channel + "|" + chatID
	out.mu.Lock()
	q, ok := out.queues[key]
	if !ok {
		q = make(chan imOutItem, 32)
		out.queues[key] = q
	}
	out.mu.Unlock()
	if !ok {
		go a.imPushWorker(channel, chatID, q)
	}
	select {
	case q <- imOutItem{text: text, seq: seq, sessionID: sessionID, logID: logID}:
	default:
		log.Printf("[IM] 发件队列已满，丢弃一条推送（%s）", maskIMID(chatID))
		a.imLogUpdate(logID, imStatusFailed, "发件队列已满，已丢弃")
	}
}

// imOutbox 取发件队列表（惰性创建，加锁避免并发首次使用时建出两份）。
func (a *App) imOutbox() *imOutbox {
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	if a.imOut == nil {
		a.imOut = newIMOutbox()
	}
	return a.imOut
}

// imPushWorker 串行发一个会话的所有推送；成功才推进 lastSeq（失败的那条不推进，
// 但也不重试——答复是「当时的结论」，过期重发比丢更让人困惑）。
func (a *App) imPushWorker(channel, chatID string, q chan imOutItem) {
	for it := range q {
		// 退避中：不硬推（平台已经在拒了，每轮再推只会加剧限流），留痕说明原因。
		if wait, fails := a.imPushCooldown(channel, chatID); wait > 0 {
			log.Printf("[IM] 推送退避中（%s 连续失败 %d 次，还剩 %s），本条未发送",
				maskIMID(chatID), fails, wait.Round(time.Second))
			a.imLogUpdate(it.logID, imStatusFailed,
				fmt.Sprintf("推送退避中（连续失败 %d 次），本条未发送", fails))
			continue
		}
		if err := a.imSendText(channel, chatID, it.text); err != nil {
			fails := a.imPushNoteResult(channel, chatID, false)
			// 日志里带上平台原始错误码与原文：「微信服务返回错误（-2）」这种
			// 只有人话的版本，事后根本查不出是限流还是内容被拒。
			log.Printf("[IM] 推送被拒 code=%s（%s/%s，连续 %d 次）: %v",
				imPushErrCode(err), channel, maskIMID(chatID), fails, err)
			a.imNotePush(channel, it.seq, false)
			// 失败原因存人话（面板红字就显示这一句），别把原始 JSON 甩给用户
			a.imLogUpdate(it.logID, imStatusFailed, imCutText(weixinErrorOf(err), 300))
			continue
		}
		a.imPushNoteResult(channel, chatID, true)
		// 成功也留一行：只看 lastSeq 分不清「推成功」与「根本没推」，真机联调
		// 时这一点最要命（答复到底有没有出去，日志里必须能直接看到）。
		log.Printf("[IM] 已推送 seq=%d → %s（会话 %s）", it.seq, maskIMID(chatID), it.sessionID)
		a.imNotePush(channel, it.seq, true)
		a.imLogUpdate(it.logID, imStatusSent, "")
		if it.seq > 0 {
			a.imMarkPushed(channel, chatID, it.sessionID, it.seq)
		}
	}
}

// imNotePush 把推送结果记到渠道上（渠道没实现就跳过，状态页少这一块而已）。
func (a *App) imNotePush(channel string, seq int64, ok bool) {
	type pushRecorder interface{ notePush(seq int64, ok bool) }
	if ch := a.imChannelOf(channel); ch != nil {
		if r, isRec := ch.(pushRecorder); isRec {
			r.notePush(seq, ok)
		}
	}
}

// imSendText 通过渠道适配层发一段文本（分段在适配层内部做）。
func (a *App) imSendText(channel, chatID, text string) error {
	ch := a.imChannelOf(channel)
	if ch == nil {
		return fmt.Errorf("渠道 %s 未运行", channel)
	}
	sender, ok := ch.(IMSender)
	if !ok {
		return fmt.Errorf("渠道 %s 不支持发送文本", channel)
	}
	// 出站也留一行，并标出这条主动消息有没有上下文凭据：平台对没有
	// context_token 的主动消息照样 200 但不投递，「发了没收到」全靠这一位判断。
	ctxFlag := "n/a"
	if t, isCtx := ch.(interface{ hasContextToken(string) bool }); isCtx {
		ctxFlag = "no"
		if t.hasContextToken(chatID) {
			ctxFlag = "yes"
		}
	}
	log.Printf("[IM] 出站 → %s ctx=%s", maskIMID(chatID), ctxFlag)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, err := sender.SendText(ctx, IMConversation{
		Channel: channel, ChatID: chatID, ChatType: "dm", UserID: chatID,
	}, text)
	return err
}

// ---------- 绑定表（data/im_bindings.json） ----------

func (a *App) imBindingsPath() string {
	return filepathAbsOf(filepath.Join(a.Cfg.DataDir, "im_bindings.json"))
}

// ensureIMBindingsLocked 首次访问时从磁盘装载（调用方持 imBindMu）。
func (a *App) ensureIMBindingsLocked() {
	if a.imBindings != nil {
		return
	}
	a.imBindings = []*imBinding{}
	raw, err := os.ReadFile(a.imBindingsPath())
	if err != nil {
		return
	}
	var f imBindingFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	for _, b := range f.Chats {
		if b != nil && b.ChatID != "" {
			a.imBindings = append(a.imBindings, b)
		}
	}
}

func (a *App) saveIMBindingsLocked() {
	raw, _ := json.MarshalIndent(imBindingFile{Chats: a.imBindings}, "", " ")
	if err := os.MkdirAll(filepath.Dir(a.imBindingsPath()), 0o755); err != nil {
		return
	}
	if err := writeFileAtomic(a.imBindingsPath(), raw); err != nil {
		log.Printf("[IM] 绑定表落盘失败: %v", err)
	}
}

// imBindingList 复制一份绑定表（不持锁返回，调用方随便读）。
func (a *App) imBindingList() []imBinding {
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	a.ensureIMBindingsLocked()
	out := make([]imBinding, 0, len(a.imBindings))
	for _, b := range a.imBindings {
		out = append(out, *b)
	}
	return out
}

// imBindChat 登记（或刷新）一条绑定，返回副本。
func (a *App) imBindChat(channel, chatID string) imBinding {
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	a.ensureIMBindingsLocked()
	for _, b := range a.imBindings {
		if b.Channel == channel && b.ChatID == chatID {
			b.UpdatedAt = imNow()
			a.saveIMBindingsLocked()
			return *b
		}
	}
	b := &imBinding{Channel: channel, ChatID: chatID, UpdatedAt: imNow()}
	a.imBindings = append(a.imBindings, b)
	a.saveIMBindingsLocked()
	return *b
}

// imSetRemark 给联系人设/清本地备注（微信 iLink 协议只给 ilink_user_id，拿不到
// 昵称，只能靠用户自己起名）。返回更新后的绑定与「是否找到该聊天」。
func (a *App) imSetRemark(channel, chatID, remark string) (imBinding, bool) {
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	a.ensureIMBindingsLocked()
	for _, b := range a.imBindings {
		if b.Channel == channel && b.ChatID == chatID {
			b.Remark = strings.TrimSpace(remark)
			b.UpdatedAt = imNow()
			a.saveIMBindingsLocked()
			return *b, true
		}
	}
	return imBinding{}, false
}

// imMarkRead 记「这个聊天已读到哪条流水」（面板未读红点按它算）。
// 只增不减：回看旧消息不该把红点又点亮。
func (a *App) imMarkRead(channel, chatID string, upTo int64) {
	if upTo <= 0 {
		return
	}
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	a.ensureIMBindingsLocked()
	for _, b := range a.imBindings {
		if b.Channel == channel && b.ChatID == chatID {
			if upTo > b.ReadUpTo {
				b.ReadUpTo = upTo
				a.saveIMBindingsLocked()
			}
			return
		}
	}
}

// imMarkPushed 记录「这个会话已经推到这个序号」（序号属于哪个数字经理版本一并记下）。
func (a *App) imMarkPushed(channel, chatID, sessionID string, seq int64) {
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	a.ensureIMBindingsLocked()
	for _, b := range a.imBindings {
		if b.Channel == channel && b.ChatID == chatID {
			// 绑定还没有版本（imBindChat 刚建出来的）：采纳调用方的版本。否则第一次
			// 记录会被「版本不一致」挡掉，序号白记。
			if b.SessionID == "" {
				b.SessionID = sessionID
			}
			if b.SessionID == sessionID && seq > b.LastSeq {
				b.LastSeq = seq
				b.UpdatedAt = imNow()
				a.saveIMBindingsLocked()
			}
			return
		}
	}
}

// imRebaseBinding 把绑定挪到新的数字经理版本上：序号从头计，所以 lastSeq 归零。
// 不做这一步，换版本后新会话的 seq（从小数字重新开始）会一直小于旧 lastSeq，
// 答复会被永久吞掉。
func (a *App) imRebaseBinding(channel, chatID, sessionID string) imBinding {
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	a.ensureIMBindingsLocked()
	for _, b := range a.imBindings {
		if b.Channel == channel && b.ChatID == chatID {
			if b.SessionID != sessionID {
				b.SessionID = sessionID
				b.LastSeq = 0
				b.UpdatedAt = imNow()
				a.saveIMBindingsLocked()
				log.Printf("[IM] 绑定 %s 已跟到新的数字经理会话 %s（序号重置）", maskIMID(chatID), sessionID)
			}
			return *b
		}
	}
	b := &imBinding{Channel: channel, ChatID: chatID, SessionID: sessionID, UpdatedAt: imNow()}
	a.imBindings = append(a.imBindings, b)
	a.saveIMBindingsLocked()
	return *b
}

// imUnbindChannel 清掉某个渠道的全部绑定（解绑账号时调用：换了微信账号，旧会话
// 不该继续收到答复）。
func (a *App) imUnbindChannel(channel string) {
	a.imBindMu.Lock()
	defer a.imBindMu.Unlock()
	a.ensureIMBindingsLocked()
	kept := a.imBindings[:0]
	for _, b := range a.imBindings {
		if b.Channel == channel {
			continue
		}
		kept = append(kept, b)
	}
	a.imBindings = kept
	a.saveIMBindingsLocked()
}

// errTextOf 把可能为 nil 的错误转成一句人话（避免 "%v" 打出 "<nil>"）。
func errTextOf(err error) string {
	if err == nil {
		return "未知原因"
	}
	return err.Error()
}
