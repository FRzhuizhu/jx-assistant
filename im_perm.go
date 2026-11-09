package main

// IM 审批/提问通道（阶段 3）：把「微信发起的那一轮任务」产生的审批与提问推到
// 微信，并允许用户在微信里回「批准/拒绝」或回答问题。
//
// 四条设计约束（安全相关，别为了顺手而放宽）：
//  1. **只推微信发起的那一轮**。判定落在「轮」上（sessionRun.origin，见 runtime.go）：
//     微信入站触发的那一轮带 im:<渠道>:<chatId>，派发出去的子任务继承同一标记
//     （imInheritRunOrigin，继承式——孙子任务同理）；桌面端发起的那一轮 origin 为空，
//     审批/提问照旧只在桌面端，一条都不外发。
//  2. **白名单是第一道闸**。imOnInbound 先过 imAllowWeixin（fail closed），
//     非白名单的消息连解析都不做——微信里回一句「批准」等于放行一个可能写文件/
//     执行命令的动作，绝不能凭「谁发来的都算」。
//  3. **只认明确的答复**。明确词（批准/同意/拒绝/驳回 + 可带序号）才执行；
//     「嗯」「可以」「行吧」这类模糊回复一律**追问**，不默认放行；序号对不上要
//     提示而不是猜。一次回复只消费一条。
//  4. **超时按拒绝（fail closed）**。审批队列本身的 approvalTTL 到点就是拒绝，
//     微信侧那条待办同时撤下并明说「已按拒绝处理」——绝不留「没回就是同意」的口子。
//
// 留痕：每次微信里的批准/拒绝/回答都落一条 kind=audit 行（type=im_decision，
// 带 via=im:<渠道>:<chatId>、审批 id、工具、时间），导出日志里能追到「谁、什么时候、
// 批的哪一项」。桌面端处理掉微信里挂着的待办时也补一条同样的审计行。
//
// 出站只走 imEnqueuePush 那条串行发件队列（seq 传 0：审批/提问不推进答复序号，
// 否则 lastSeq 语义会被搅乱）。

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// 待办种类（imPending.Kind）。
const (
	imPendingApproval = "approval" // 越权审批（perm.go 的 Approval）
	imPendingQuestion = "question" // 提问/进入计划模式确认（interact.go 的 UserQuestion）
)

// imPending 是一条「等微信那边处理」的审批或提问。序号不存字段：推送时按当前
// 列表位置现算（1 起），每次列表变动都重推一遍全文，用户看到的编号永远和现状一致。
type imPending struct {
	Kind         string
	ID           string // 审批 id（ap-…）/ 提问 id（q-…）
	Channel      string
	ChatID       string
	SessionID    string
	SessionTitle string
	Tool         string   // 审批：被拦的工具名
	Summary      string   // 审批：摘要 / 提问：问题正文
	Options      []string // 提问：可选项（可空）
	AllowFree    bool
	AskKind      string // 提问种类（ask / enter-plan）
	CreatedAt    string
	ExpiresAt    time.Time // 零值 = 不设时限（提问 timeout_sec=0）
}

// ---------- 待办表 ----------

func imPendingKey(channel, chatID string) string { return channel + "|" + chatID }

// imPendingAdd 登记一条待办（同 ID 同种类不重复登记）。
func (a *App) imPendingAdd(p *imPending) {
	if p == nil || p.ChatID == "" {
		return
	}
	a.imPendingMu.Lock()
	defer a.imPendingMu.Unlock()
	if a.imPendings == nil {
		a.imPendings = map[string][]*imPending{}
	}
	k := imPendingKey(p.Channel, p.ChatID)
	for _, x := range a.imPendings[k] {
		if x.Kind == p.Kind && x.ID == p.ID {
			return
		}
	}
	a.imPendings[k] = append(a.imPendings[k], p)
}

// imPendingSnapshot 复制一份该聊天的待办（按登记顺序 = 推送里的序号）。
func (a *App) imPendingSnapshot(channel, chatID string) []*imPending {
	a.imPendingMu.Lock()
	defer a.imPendingMu.Unlock()
	list := a.imPendings[imPendingKey(channel, chatID)]
	out := make([]*imPending, 0, len(list))
	return append(out, list...)
}

// imPendingTake 按 ID 摘掉一条待办并返回它（不在表里 = nil）。
// 「谁先摘到谁处理」：微信与桌面端并发时，后到的一方拿到 nil 就回「已被处理」，
// 不会出现两边都生效、或都没生效的情况。
func (a *App) imPendingTake(kind, id string) *imPending {
	if id == "" {
		return nil
	}
	a.imPendingMu.Lock()
	defer a.imPendingMu.Unlock()
	for k, list := range a.imPendings {
		for i, p := range list {
			if p.Kind != kind || p.ID != id {
				continue
			}
			a.imPendings[k] = append(list[:i:i], list[i+1:]...)
			if len(a.imPendings[k]) == 0 {
				delete(a.imPendings, k)
			}
			return p
		}
	}
	return nil
}

// imPendingCount 该聊天还有几条待办（推送前判断「还有别的没处理」）。
func (a *App) imPendingCount(channel, chatID string) int {
	return len(a.imPendingSnapshot(channel, chatID))
}

// ---------- 推送 ----------

// imPendingPush 把该聊天当前的待办整份推一遍（新来一条、或处理掉一条之后）。
// 整份重推而不是「增量追加」：序号会随列表变动而重排，只发增量会让用户按旧编号
// 回错项——审批回错项的代价是放行了另一件事。
func (a *App) imPendingPush(channel, chatID, reason string) {
	list := a.imPendingSnapshot(channel, chatID)
	if len(list) == 0 {
		return
	}
	a.imEnqueuePush(channel, chatID, imPendingText(reason, list), 0, "")
}

// imPendingText 渲染待办清单（reason 是首行的由头）。
func imPendingText(reason string, list []*imPending) string {
	b := &strings.Builder{}
	b.WriteString("【需要你处理】" + reason + "\n")
	approvals, questions := 0, 0
	for i, p := range list {
		switch p.Kind {
		case imPendingApproval:
			approvals++
			b.WriteString(fmt.Sprintf("%d) 审批 · 会话「%s」· %s：%s\n",
				i+1, p.SessionTitle, p.Tool, imTruncate(p.Summary, 120)))
		case imPendingQuestion:
			questions++
			line := fmt.Sprintf("%d) 提问 · 会话「%s」：%s", i+1, p.SessionTitle, imTruncate(p.Summary, 160))
			if len(p.Options) > 0 {
				line += "（可选：" + strings.Join(p.Options, " / ") + "）"
			}
			b.WriteString(line + "\n")
		}
	}
	if approvals > 0 {
		b.WriteString("审批回复「1 批准」或「1 拒绝」")
		if approvals > 1 {
			b.WriteString("（有多项，回复时带上序号）")
		}
		b.WriteString("；超时未回复按拒绝处理，不会默认放行。\n")
	}
	if questions > 0 {
		b.WriteString("提问直接回复内容即可")
		if len(list) > 1 {
			b.WriteString("；多项并存时用「2 你的回答」")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// imTrackApproval 把一条审批登记到微信待办并推一条消息。origin 不是 IM 来源
// （桌面端任务）时什么都不做——这是「桌面端的审批绝不外发」的唯一判据。
func (a *App) imTrackApproval(origin string, ap *Approval) bool {
	channel, chatID, ok := imParseOrigin(origin)
	if !ok || ap == nil {
		return false
	}
	a.imPendingAdd(&imPending{
		Kind: imPendingApproval, ID: ap.ID, Channel: channel, ChatID: chatID,
		SessionID: ap.SessionID, SessionTitle: ap.SessionTitle, Tool: ap.Tool,
		Summary: ap.Summary, CreatedAt: ap.CreatedAt, ExpiresAt: ap.expiresAt,
	})
	log.Printf("[IM] 审批 %s 已推到 %s（会话 %s，工具 %s）", ap.ID, maskIMID(chatID), ap.SessionID, ap.Tool)
	a.imPendingPush(channel, chatID, "有一项操作在等你审批")
	return true
}

// imTrackQuestion 把一条提问登记到微信待办并推一条消息。计划审批（kind=plan）
// 例外：它的答复是一份计划本身，微信里没法好好审——只提示去桌面端处理，不登记
// 待办（登记了就得支持在微信里批，那等于把「审计划」降级成一个「批准」词）。
func (a *App) imTrackQuestion(origin string, q *UserQuestion) bool {
	channel, chatID, ok := imParseOrigin(origin)
	if !ok || q == nil {
		return false
	}
	if q.Kind == askKindPlan {
		a.imEnqueuePush(channel, chatID, "【请在桌面端处理】会话「"+q.SessionTitle+
			"」提交了一份执行计划等你审批（计划审批不设时限，微信里不支持审批）。", 0, "")
		return false
	}
	a.imPendingAdd(&imPending{
		Kind: imPendingQuestion, ID: q.ID, Channel: channel, ChatID: chatID,
		SessionID: q.SessionID, SessionTitle: q.SessionTitle, Summary: q.Question,
		Options: q.Options, AllowFree: q.AllowFree, AskKind: q.Kind,
		CreatedAt: q.CreatedAt, ExpiresAt: q.expiresAt,
	})
	log.Printf("[IM] 提问 %s 已推到 %s（会话 %s）", q.ID, maskIMID(chatID), q.SessionID)
	a.imPendingPush(channel, chatID, "有一件事要你决定")
	return true
}

// imDropPendingOnExpire 收尾一条没被任何入口处理的待办（超时/本轮结束）：从表里
// 摘掉并在微信里明说后果——审批一律「按拒绝处理」，提问按各自的默认口径。
// 返回是否真的摘到（没摘到说明已有人处理过，不重复说）。
func (a *App) imDropPendingOnExpire(kind, id, text string) bool {
	p := a.imPendingTake(kind, id)
	if p == nil {
		return false
	}
	a.imEnqueuePush(p.Channel, p.ChatID, text, 0, "")
	return true
}

// ---------- 微信侧的回复解析 ----------

// 回复种类。
const (
	imDecisionNone = iota
	imDecisionApprove
	imDecisionReject
	imDecisionAmbiguous // 像在答复，但不够明确：追问，绝不默认放行
)

// imDecision 是一条解析出来的答复：Kind + 序号（0 = 没带序号）。
type imDecision struct {
	Kind  int
	Index int
}

// 明确词：只认这些，才可能真正放行/拒绝。
var (
	imApproveWords = map[string]bool{
		"批准": true, "同意": true, "允许": true, "通过": true, "放行": true, "可以批准": true,
		"y": true, "yes": true, "ok批准": true, "approve": true, "allow": true,
	}
	imRejectWords = map[string]bool{
		"拒绝": true, "驳回": true, "不同意": true, "不允许": true, "不通过": true, "取消": true,
		"n": true, "no": true, "reject": true, "deny": true,
	}
	// 模糊词：语气上像「同意」但信息量不足（正是审批最危险的那类回复）——追问。
	imAmbiguousWords = map[string]bool{
		"行": true, "行吧": true, "可以": true, "好": true, "好的": true, "好吧": true,
		"嗯": true, "嗯嗯": true, "恩": true, "中": true, "ok": true, "okay": true,
		"你看着办": true, "看着办": true, "随便": true, "都行": true, "没问题": true,
		"继续": true, "执行": true, "办吧": true, "是": true, "对": true, "可以可以": true,
	}
)

// imParseDecision 解析微信里的一条回复（纯函数，单测直接覆盖）。
// 认的形态：「批准」「拒绝」「批准 1」「1 批准」「1批准」「y」「n」。
// 其余（含模糊词）按 none/ambiguous 返回——调用方据此决定追问还是当普通消息。
func imParseDecision(text string) imDecision {
	s := strings.ToLower(strings.TrimSpace(text))
	s = strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return '0' + (r - '０')
		}
		return r
	}, s)
	s = strings.Trim(s, " \t\r\n。！!？?，,、~～.：:")
	if s == "" {
		return imDecision{}
	}
	idx, rest := imSplitIndex(s)
	word := strings.Join(strings.Fields(rest), "") // "批准 1" 去空格后还剩「批准」
	word = strings.Trim(word, " \t\r\n。！!？?，,、~～.：:")
	switch {
	case imApproveWords[word]:
		return imDecision{Kind: imDecisionApprove, Index: idx}
	case imRejectWords[word]:
		return imDecision{Kind: imDecisionReject, Index: idx}
	case imAmbiguousWords[word]:
		return imDecision{Kind: imDecisionAmbiguous, Index: idx}
	}
	// 认不出决定词：序号照样带回去——「1 换个名字」这种回复要能对上第 1 项
	// （对不上就提示，而不是悄悄当成别的项的答案）。
	return imDecision{Kind: imDecisionNone, Index: idx}
}

// imSplitIndex 从文本开头或结尾取出序号（「1 批准」/「批准 1」/「1批准」），
// 返回序号与剩余文本；没有序号时返回 0 与原文本。
func imSplitIndex(s string) (int, string) {
	if n, rest, ok := imTrimInt(s, false); ok {
		return n, rest
	}
	if n, rest, ok := imTrimInt(s, true); ok {
		return n, rest
	}
	return 0, s
}

// imTrimInt 取开头（fromEnd=false）或结尾（fromEnd=true）的正整数。
func imTrimInt(s string, fromEnd bool) (int, string, bool) {
	t := []rune(strings.TrimSpace(s))
	if fromEnd {
		for i, j := 0, len(t)-1; i < j; i, j = i+1, j-1 {
			t[i], t[j] = t[j], t[i]
		}
	}
	digits := 0
	for digits < len(t) && t[digits] >= '0' && t[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, s, false
	}
	n, err := strconv.Atoi(string(t[:digits]))
	if err != nil || n <= 0 {
		return 0, s, false
	}
	rest := string(t[digits:])
	if fromEnd { // 翻回来
		r := []rune(rest)
		for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
			r[i], r[j] = r[j], r[i]
		}
		rest = string(r)
	}
	return n, rest, true
}

// ---------- 入站处理 ----------

// imHandleInboundDecision 处理微信里的一条「批准/拒绝/回答」。返回 true 表示这条
// 消息已被消费（不再当成任务投给数字经理）。
//
// 顺序刻意如此：没有待办时，只有**明确**的决定词才会被消费并回一句「没有待审批的
// 请求」（否则「批准」会被当成一个任务派给模型，用户等半天没有反应）；含糊的词
// 与普通任务一律放行——不能因为「有个审批在等」就把用户的话全吞掉。
func (a *App) imHandleInboundDecision(conv IMConversation, text string) bool {
	list := a.imPendingSnapshot(conv.Channel, conv.ChatID)
	if len(list) == 0 {
		if d := imParseDecision(text); d.Kind == imDecisionApprove || d.Kind == imDecisionReject {
			if d.Index == 0 { // 带序号的更像在说别的事，不抢
				a.imSay(conv, "现在没有等你审批或回答的请求（可能已超时，或已在桌面端处理过）。")
				return true
			}
		}
		return false
	}
	d := imParseDecision(text)
	switch d.Kind {
	case imDecisionApprove, imDecisionReject:
		if p := imPickApproval(list, d.Index); p != nil {
			return a.imApplyApproval(conv, p, d.Kind == imDecisionApprove, list)
		}
		// 没有审批可批，但有待回答的提问：把「批准/拒绝」当回答（进入计划模式的
		// 确认就是这个语义：批准 = 同意进入计划模式）。
		if q := imPickQuestion(list, d.Index); q != nil {
			return a.imApplyAnswer(conv, q, imDecisionAnswerText(d.Kind), list)
		}
		a.imSay(conv, imPendingPickHint(list, d.Index))
		return true
	case imDecisionAmbiguous:
		a.imSay(conv, "这条回复不够明确，我不会替你放行：\n"+imPendingText("请明确回复", list))
		return true
	}
	// 不是审批决定：有待回答的提问就把这条当回答；只有审批在等（用户明显在说别的
	// 事）就原样放行成任务消息——不能因为「有个审批挂着」就把用户的话全吞掉。
	var q *imPending
	if d.Index > 0 {
		if q = imPickQuestion(list, d.Index); q == nil && d.Index <= len(list) {
			// 序号指向审批项：说清楚，别让人以为回答被吃掉了
			a.imSay(conv, fmt.Sprintf("第 %d 项是审批，不是提问：请回「%d 批准」或「%d 拒绝」。", d.Index, d.Index, d.Index))
			return true
		}
	} else {
		q = imPickQuestion(list, 0)
	}
	if q == nil {
		if d.Index == 0 && countIMPending(list, imPendingQuestion) > 1 {
			a.imSay(conv, "现在有多项待回答，请带上序号回复（如「1 你的回答」）。\n"+imPendingText("当前待处理", list))
			return true
		}
		return false
	}
	answer := strings.TrimSpace(text)
	if d.Index > 0 {
		if _, rest := imSplitIndex(strings.TrimSpace(text)); strings.TrimSpace(rest) != "" {
			answer = strings.TrimSpace(rest)
		}
	}
	return a.imApplyAnswer(conv, q, answer, list)
}

// countIMPending 数某类待办有几条。
func countIMPending(list []*imPending, kind string) int {
	n := 0
	for _, p := range list {
		if p.Kind == kind {
			n++
		}
	}
	return n
}

// imPickApproval 按序号取要处理的审批（0 = 没带序号）：只有一条审批时才认无序号
// 的回复；多条并存必须带序号——猜错就是放行了另一件事。
func imPickApproval(list []*imPending, idx int) *imPending {
	return imPickOf(list, idx, imPendingApproval)
}

// imPickQuestion 按序号取要回答的提问（0 = 没带序号）：只有一条提问时才认。
func imPickQuestion(list []*imPending, idx int) *imPending {
	return imPickOf(list, idx, imPendingQuestion)
}

// imPickOf 是两者的共用体：带序号就按位置取（类型不符算没取到），没带序号则
// 只有该类型恰好一条时才认——宁可追问，也不猜。
func imPickOf(list []*imPending, idx int, kind string) *imPending {
	if idx > 0 {
		if idx > len(list) || list[idx-1].Kind != kind {
			return nil
		}
		return list[idx-1]
	}
	if countIMPending(list, kind) != 1 {
		return nil
	}
	for _, p := range list {
		if p.Kind == kind {
			return p
		}
	}
	return nil
}

// imPendingPickHint 序号对不上/没带序号时的提示：说清现状，让用户照着重回。
func imPendingPickHint(list []*imPending, idx int) string {
	var head string
	switch {
	case idx > len(list):
		head = fmt.Sprintf("没有第 %d 项（当前共 %d 项）", idx, len(list))
	case idx > 0:
		head = fmt.Sprintf("第 %d 项不是这个意思", idx)
	default:
		n := countIMPending(list, imPendingApproval)
		if n > 1 {
			head = fmt.Sprintf("有 %d 项待审批，请在回复里带上序号（如「1 批准」）", n)
		} else {
			head = "这条回复对不上任何一项待办"
		}
	}
	return head + "：\n" + imPendingText("当前待处理", list)
}

// imDecisionAnswerText 把「批准/拒绝」翻译成提问的回答文本（进入计划模式的确认
// 认 __approve__ 这个固定词，其余提问拿到的是人话）。
func imDecisionAnswerText(kind int) string {
	if kind == imDecisionApprove {
		return "__approve__"
	}
	return "拒绝"
}

// imApplyApproval 执行一条微信里的批准/拒绝：先从待办里摘掉（原子：谁先摘到谁
// 处理），再交给审批队列；桌面端与微信并发时后到的一方拿到「已失效」。
func (a *App) imApplyApproval(conv IMConversation, p *imPending, approved bool, list []*imPending) bool {
	if a.imPendingTake(imPendingApproval, p.ID) == nil {
		a.imSay(conv, "这条审批已经处理过或已失效，本次回复没有生效。")
		return true
	}
	via := imOriginTag(conv.Channel, conv.ChatID)
	if err := a.resolveApproval(p.ID, approved, false); err != nil {
		a.imAuditDecision(p, "expired", via, err.Error())
		a.imSay(conv, "这条审批已经失效（"+err.Error()+"），本次回复没有生效。")
		return true
	}
	word := "已批准"
	if !approved {
		word = "已拒绝"
	}
	a.imAuditDecision(p, word, via, "")
	a.imSay(conv, fmt.Sprintf("%s：%s（会话「%s」· %s）。", word, imTruncate(p.Summary, 80), p.SessionTitle, p.Tool))
	// 还有别的待办：重推一份（序号已重排），免得用户按旧编号回。
	if rest := a.imPendingSnapshot(conv.Channel, conv.ChatID); len(rest) > 0 {
		a.imPendingPush(conv.Channel, conv.ChatID, "还有这些在等你处理")
	}
	return true
}

// imApplyAnswer 把一条微信回复当成提问的回答。
func (a *App) imApplyAnswer(conv IMConversation, p *imPending, answer string, list []*imPending) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		a.imSay(conv, "这条回复是空的，请重新回答：\n"+imPendingText("当前待处理", list))
		return true
	}
	via := imOriginTag(conv.Channel, conv.ChatID)
	if a.imPendingTake(imPendingQuestion, p.ID) == nil {
		a.imSay(conv, "这个问题已经处理过或已失效，本次回复没有生效。")
		return true
	}
	if err := a.resolveQuestion(p.ID, answer); err != nil {
		a.imAuditDecision(p, "expired", via, err.Error())
		a.imSay(conv, "这个问题已经失效（"+err.Error()+"），本次回复没有生效。")
		return true
	}
	a.imAuditDecision(p, "已回答", via, answer)
	shown := answer
	if p.AskKind == askKindEnter && answer == "__approve__" {
		shown = "同意进入计划模式"
	}
	a.imSay(conv, "已收到回答："+imTruncate(shown, 80)+"（会话「"+p.SessionTitle+"」）")
	if rest := a.imPendingSnapshot(conv.Channel, conv.ChatID); len(rest) > 0 {
		a.imPendingPush(conv.Channel, conv.ChatID, "还有这些在等你处理")
	}
	return true
}

// imAuditDecision 给微信里的一次批准/拒绝/回答留痕：谁批的（via）、批的哪一项
// （审批 id + 工具 + 摘要）、什么时候。落 kind=audit 行（导出日志可见，不发给
// 模型）+ 一行引擎日志。
func (a *App) imAuditDecision(p *imPending, action, via, detail string) {
	if p == nil {
		return
	}
	log.Printf("[IM] %s %s（%s，会话 %s，via=%s）", action, p.ID, imTruncate(p.Summary, 40), p.SessionID, via)
	if a.Store == nil || p.SessionID == "" {
		return
	}
	sess, err := a.Store.Get(p.SessionID)
	if err != nil || sess == nil {
		return
	}
	a.auditWrite(sess, map[string]any{
		"type": "im_decision", "action": action, "via": via, "at": imNow(),
		"itemKind": p.Kind, "itemId": p.ID, "tool": p.Tool,
		"summary": imTruncate(p.Summary, 200), "detail": imTruncate(detail, 200),
	})
}

// ---------- 来源标记传播 ----------

// imInheritRunOrigin 把派发方这一轮的来源标记复制给被派发出去的那一轮。
// 继承式：子任务再派孙子任务时同样走这里（toolContext.deliverSub），所以「微信
// 发起 → 子任务 → 孙任务」整条链上的审批/提问都会回到同一个 chatId；不传的话
// 子任务的审批只能在桌面端等，用户人在手机上看不到，5 分钟后就按拒绝作废了。
func (a *App) imInheritRunOrigin(fromSessionID string, to *sessionRun) {
	if to == nil {
		return
	}
	if o := a.runOriginOf(fromSessionID); o != "" {
		to.setOrigin(o)
	}
}
