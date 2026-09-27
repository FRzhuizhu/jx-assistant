package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
)

// 上下文管理：组装管线（系统提示词 + 活跃摘要 + 规则压缩视图 + 近期原文）
// 与两种压缩机制——规则压缩（零模型调用）与模型摘要（自动阈值 + 手动按钮）。
// 设计对齐 DSH 的哲学：原始历史 append-only 永久保留，折叠只发生在
// 「模型可见表面」上；模型随时可用历史查询工具读到未压缩原文。

const compactOmit = "omit" // 规则压缩的特殊标记：组装时整行跳过

const (
	argKeepLimit    = 500 // 工具调用参数保留上限（防 write 带整文件内容）
	resultKeepLimit = 200 // 工具结果保留的开头长度
)

// round 是一轮对话：一条用户消息（chat/report）到下一条用户消息之前的全部。
type round struct {
	startSeq, endSeq int64
}

// splitRounds 按用户消息分轮；开头若无用户消息（不应发生），并入第一轮。
// 继承标记行（inherit）不是对话内容，跳过——否则它会自成伪轮，
// 让子对话的压缩时机比同样长度的普通会话早一轮。
func splitRounds(msgs []*StoredMessage) []round {
	rounds := []round{}
	cur := round{}
	started := false
	for _, m := range msgs {
		if m.Kind == inheritKind {
			continue
		}
		if m.Kind == "chat" && m.Role == "user" || m.Kind == "report" {
			if started {
				cur.endSeq = m.Seq - 1
				if cur.endSeq < cur.startSeq {
					cur.endSeq = cur.startSeq
				}
				rounds = append(rounds, cur)
			}
			cur = round{startSeq: m.Seq}
			started = true
			continue
		}
		if !started {
			cur = round{startSeq: m.Seq}
			started = true
		}
	}
	if started {
		if len(msgs) > 0 {
			cur.endSeq = msgs[len(msgs)-1].Seq
		}
		rounds = append(rounds, cur)
	}
	return rounds
}

// activeSummaries 取未过期的摘要：手动摘要全部保留（叠加、永不重压），
// 自动摘要只取最新一条（DSH 式：新自动摘要合并旧自动摘要）。
func activeSummaries(msgs []*StoredMessage) []*StoredMessage {
	manual := []*StoredMessage{}
	var auto *StoredMessage
	for _, m := range msgs {
		if m.Kind != "summary" || m.Superseded {
			continue
		}
		if m.Origin == "manual" {
			manual = append(manual, m)
		} else if auto == nil || m.Seq > auto.Seq {
			auto = m
		}
	}
	out := manual
	if auto != nil {
		out = append(out, auto)
	}
	// 按覆盖边界升序，保证注入顺序与时间线一致
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CoversUpTo < out[j-1].CoversUpTo; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// buildContext 是组装管线：系统提示词 + 活跃摘要 + 边界后消息
// （规则压缩视图生效，最近轮保持原文），并修剪结尾悬空的工具调用。
// 环境快照行（kind=env）按落位进上下文，**追加式**（已发送的前缀绝不改写）：
// 可见的首条快照整份呈现（摘要折叠后以边界后首条重新作为完整基线），
// 其后相邻快照按行差分渲染为一条短的「环境更新」消息——变化永远以新消息
// 出现在尾部，系统提示词与历史前缀逐字节稳定，前缀缓存命中最大化。
func (a *App) buildContext(sess *SessionMeta, sys string) []Message {
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return []Message{TextMessage("system", sys)}
	}
	summaries := activeSummaries(raw)
	boundary := int64(0)
	for _, s := range summaries {
		if s.CoversUpTo > boundary {
			boundary = s.CoversUpTo
		}
	}
	ruleMode := a.Cfg.CompactMode == "rule"

	out := make([]Message, 0, len(raw)+2)
	out = append(out, TextMessage("system", sys))
	for _, s := range summaries {
		out = append(out, TextMessage("user", s.Text()))
	}
	prevEnv := "" // 上一条可见快照；空 = 下一条整份呈现（首轮，或摘要折叠后的首条）
	for _, m := range raw {
		if m.Seq <= boundary || m.Kind == "summary" || m.Kind == "prompt" || m.Kind == "inherit" || m.Kind == "request" || m.Kind == "audit" {
			continue
		}
		if m.Kind == "env" {
			env := m.Text()
			if prevEnv == "" {
				out = append(out, TextMessage("user", env))
			} else {
				out = append(out, TextMessage("user", envUpdateMessage(prevEnv, env)))
			}
			prevEnv = env
			continue
		}
		if ruleMode && m.Compact != "" {
			if m.Compact == compactOmit {
				continue // 整行跳过（工具结果行）
			}
			// 压缩表示：纯文本消息，不带 tool_calls（协议要求配对完整）
			out = append(out, Message{Role: m.Role, Content: m.Compact})
			continue
		}
		// 空正文且无工具调用（例如只留下思考过程的行）：对模型没有信息，
		// 直接跳过，避免发出空 content 被网关拒绝
		if m.Role == "assistant" && strings.TrimSpace(m.Text()) == "" && len(m.ToolCalls) == 0 {
			continue
		}
		msg := m.Message
		// 思考模式历史回传：DeepSeek V4/flash 带 tools 的请求要求此前回合的
		// reasoning_content 原样传回（见 Message 结构注释），落库的 thinking 正是它
		if m.Role == "assistant" && m.Thinking != "" {
			msg.ReasoningContent = m.Thinking
		}
		out = append(out, msg)
	}
	// 修剪尾部悬空的工具调用（客户端中断可能留下未配对的调用）
	for len(out) > 0 {
		last := out[len(out)-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	return out
}

// envUpdateMessage 把相邻两份环境快照渲染成一条短的「环境更新」：只含有变化
// 的行（新增/修改按新文原文），被移除的条目归入「不再适用」清单。确定性纯函数
// （存储文本唯一决定输出），同文快照的标题行会被精确匹配自然滤掉。
// 被替换条目的旧行（如旧时间 "- 当前时间：22:00" → 新值已在上文）不再重复
// 报进「不再适用」——按 envLineKey 抑制；纯注记行（非 "- " 条目，如技能变更
// 说明）过期后静默消失，不进清单。
func envUpdateMessage(prev, cur string) string {
	prevCount := map[string]int{}
	for _, l := range strings.Split(prev, "\n") {
		prevCount[l]++
	}
	changed := []string{}
	curKeys := map[string]bool{}
	for _, l := range strings.Split(cur, "\n") {
		if prevCount[l] > 0 {
			prevCount[l]--
			continue // 整行未变
		}
		if k := envLineKey(l); k != "" {
			curKeys[k] = true
		}
		changed = append(changed, l)
	}
	removed := []string{}
	for l, n := range prevCount {
		if n == 0 || strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(l), "- ") {
			continue // 纯注记行（标题/技能变更说明等）：过期即消失，不作条目公告
		}
		if curKeys[envLineKey(l)] {
			continue // 同条目已有新值在上文，旧值不重复报
		}
		removed = append(removed, l)
	}
	sort.Strings(removed)
	sort.Strings(changed) // 均来自 map/无序收集，排序保证确定性

	var b strings.Builder
	b.WriteString("# 环境更新（取代更早的快照与更新）\n")
	for _, l := range changed {
		b.WriteString(l + "\n")
	}
	if len(removed) > 0 {
		b.WriteString("（以下条目已不再适用）\n")
		for _, l := range removed {
			b.WriteString(l + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// envLineKey 取环境快照行的条目键："- key：value" 形式取 key（同条目的新旧
// 取值归并），其余行整行作键；空行返回空串。
func envLineKey(l string) string {
	s := strings.TrimSpace(l)
	if !strings.HasPrefix(s, "- ") {
		return s
	}
	rest := s[len("- "):]
	if i := strings.Index(rest, "："); i > 0 {
		return rest[:i]
	}
	return s
}

// ---------- 子对话的上下文继承（分支克隆） ----------

// inheritMark 是继承标记行的正文（前端只需 count 来切分继承段与自有消息）。
type inheritMark struct {
	Parent string `json:"parent"`
	Count  int    `json:"count"`
}

// copyInheritedContext 把父会话的完整消息历史复制进子会话，作为子对话继承的上下文。
// 复制的是**原始行**（含摘要与压缩标记，逐行保序、seq 重排），不是渲染成的一段文本——
// 这样复制后子会话的「模型可见序列」与父会话此前的请求逐条一致（同一套摘要边界、
// 同一套规则压缩视图），DeepSeek 的前缀缓存才能命中整段父上下文。
// 首行写一条 kind=inherit 标记行（前端折叠显示、组装时跳过），返回复制的行数。
func (a *App) copyInheritedContext(parentID, childID string) (int, error) {
	all, err := a.Store.RawMessagesAll(parentID)
	if err != nil {
		return 0, err
	}
	rows := make([]*StoredMessage, 0, len(all))
	for _, x := range all {
		if x.Kind == "request" || x.Kind == "audit" {
			continue // 模型输入快照与审计行是本会话的诊断工件，不随上下文继承
		}
		rows = append(rows, x)
	}
	if a.IsRunning(parentID) {
		// 父会话正在执行：只复制到最后一条已完成答复，避免把悬空的工具调用带进来
		// （与「分支对话」同一判定：最后一条带正文且无工具调用的助手消息）
		last := -1
		for i, x := range rows {
			if x.Role == "assistant" && len(x.ToolCalls) == 0 && strings.TrimSpace(x.Text()) != "" {
				last = i
			}
		}
		if last < 0 {
			return 0, nil // 一条完整答复都还没有，没什么可继承的
		}
		rows = rows[:last+1]
	}
	if len(rows) == 0 {
		return 0, nil
	}

	// 先算好整张新旧 seq 映射再落库：摘要的覆盖边界要按新 seq 重写，
	// 否则子会话的组装边界会对不上（摘要按旧 seq 覆盖、新行全是新 seq）。
	last, err := a.Store.LastSeq(childID)
	if err != nil {
		return 0, err
	}
	newOfOld := make(map[int64]int64, len(rows))
	seq := last + 1 // 标记行占一个 seq
	for _, x := range rows {
		seq++
		newOfOld[x.Seq] = seq
	}
	// 覆盖边界重映射：取「最后一个 oldSeq ≤ 边界」的已复制行。
	// 摘要的 covers_up_to 必定 ≤ 它自己的 seq（摘要只覆盖排在它之前的行），
	// 因此被复制的摘要总能找到对应行。
	remap := func(oldCover int64) int64 {
		var out int64
		for _, x := range rows {
			if x.Seq > oldCover {
				break
			}
			if n, ok := newOfOld[x.Seq]; ok {
				out = n
			}
		}
		return out
	}

	mark, _ := json.Marshal(inheritMark{Parent: parentID, Count: len(rows)})
	if err := a.Store.AppendRow(childID, &StoredMessage{
		Message: TextMessage("user", string(mark)), Kind: inheritKind, CreatedAt: now(),
	}); err != nil {
		return 0, err
	}
	for _, x := range rows {
		cp := &StoredMessage{
			Message:    x.Message,
			Kind:       x.Kind,
			Origin:     x.Origin,
			Compact:    x.Compact,
			Superseded: x.Superseded,
			Thinking:   x.Thinking,
			CreatedAt:  x.CreatedAt,
			Seq:        newOfOld[x.Seq],
		}
		if x.Kind == "summary" {
			cp.CoversUpTo = remap(x.CoversUpTo)
		}
		if err := a.Store.AppendRow(childID, cp); err != nil {
			return 0, err
		}
	}
	// 上传登记也随上下文继承（uid 原样保留）：继承来的注记里若引用了
	// u 编号，子会话里的 deliver_file 仍能解析到原件路径。失败只记日志。
	if err := a.Store.CopyUploadsOnBranch(parentID, childID); err != nil {
		log.Printf("[上传] 继承 %s → %s 复制上传登记失败（子对话交付目标将不可解析）: %v", parentID, childID, err)
	}
	return len(rows), nil
}

// estimateTokens 粗估一组消息的 token 量（中文为主的启发式）。
func estimateTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			total += len([]rune(s)) * 3 / 4
		}
		for _, tc := range m.ToolCalls {
			total += len([]rune(tc.Function.Arguments)) * 3 / 4
		}
		total += 8
	}
	return total
}

// ---------- 上下文占用统计（前端「上下文」面板的数据源） ----------

type ctxCategory struct {
	Name   string  `json:"name"`
	Tokens int     `json:"tokens"`
	Share  float64 `json:"share"` // 占总上下文的百分比（0-100）
}

type ctxStats struct {
	Window     int           `json:"window"` // 模型上下文窗口（token）
	Total      int           `json:"total"`  // 下一次请求的估算总量
	Categories []ctxCategory `json:"categories"`
	Cache      struct {
		PromptTokens int64   `json:"promptTokens"` // 历次调用 prompt_tokens 总和（输入）
		CacheHit     int64   `json:"cacheHit"`     // 历次调用缓存命中总和（缓存读取）
		Uncached     int64   `json:"uncached"`     // 未缓存输入 = prompt − hit
		Output       int64   `json:"output"`       // 历次调用 completion_tokens 总和（输出）
		Rate         float64 `json:"rate"`         // 缓存命中率（hit/prompt；无数据为 0）
		Calls        int64   `json:"calls"`        // 模型调用次数
	} `json:"cache"`
}

// contextStats 按与 buildContext 完全相同的组装规则，把下一次请求的上下文
// 拆成分类 token 估算（同一启发式）。注意：buildContext 的组装规则改动时需同步此处。
func (a *App) contextStats(sess *SessionMeta) ctxStats {
	cats := map[string]int{}
	add := func(name string, msgs ...Message) {
		for _, m := range msgs {
			cats[name] += estimateTokens([]Message{m})
		}
	}

	sys := a.systemPrompt(sess)
	add("系统提示词", TextMessage("system", sys))

	// 工具定义按序列化后的 JSON 体积估算（与消息同一 rune*3/4 启发式）
	if tools := a.toolsFor(sess.Mode); len(tools) > 0 {
		if raw, err := json.Marshal(toolDefsOf(tools)); err == nil {
			cats["系统工具"] += len([]rune(string(raw))) * 3 / 4
		}
	}

	if raw, err := a.Store.RawMessagesAll(sess.ID); err == nil {
		summaries := activeSummaries(raw)
		boundary := int64(0)
		for _, s := range summaries {
			if s.CoversUpTo > boundary {
				boundary = s.CoversUpTo
			}
		}
		ruleMode := a.Cfg.CompactMode == "rule"
		for _, s := range summaries {
			add("摘要", TextMessage("user", s.Text()))
		}
		prevEnv := "" // 与 buildContext 同一套环境快照渲染口径（首条整份、后续差分）
		for _, m := range raw {
			if m.Seq <= boundary || m.Kind == "summary" || m.Kind == "prompt" || m.Kind == inheritKind || m.Kind == "request" || m.Kind == "audit" {
				continue
			}
			if m.Kind == "env" {
				env := m.Text()
				if prevEnv == "" {
					add("环境快照", TextMessage("user", env))
				} else {
					add("环境快照", TextMessage("user", envUpdateMessage(prevEnv, env)))
				}
				prevEnv = env
				continue
			}
			if ruleMode && m.Compact != "" {
				if m.Compact == compactOmit {
					continue
				}
				add("消息", Message{Role: m.Role, Content: m.Compact})
				continue
			}
			add("消息", m.Message)
		}
	}

	order := []string{"消息", "系统工具", "系统提示词", "摘要", "环境快照", "其他"}
	total := 0
	for _, v := range cats {
		total += v
	}
	var out ctxStats
	out.Window = a.resolveTarget(sess).ContextWindow
	out.Total = total
	for _, name := range order {
		tok := cats[name]
		share := 0.0
		if total > 0 {
			share = float64(tok) * 100 / float64(total)
		}
		out.Categories = append(out.Categories, ctxCategory{Name: name, Tokens: tok, Share: math.Round(share*10) / 10})
	}
	out.Cache.PromptTokens = sess.UsagePrompt
	out.Cache.CacheHit = sess.UsageCacheHit
	out.Cache.Output = sess.UsageOutput
	out.Cache.Uncached = sess.UsagePrompt - sess.UsageCacheHit
	if out.Cache.Uncached < 0 {
		out.Cache.Uncached = 0
	}
	out.Cache.Calls = sess.UsageCalls
	if sess.UsagePrompt > 0 {
		out.Cache.Rate = float64(sess.UsageCacheHit) / float64(sess.UsagePrompt)
	}
	return out
}

// ---------- 规则压缩（零模型调用） ----------

// ruleCompactPass 在每轮对话结束后调用：新增轮数达到间隔阈值时，
// 把「保留窗口之外、尚未压缩」的轮次折叠为轻量表示。
// 用户消息与模型正文永远保留原文；工具调用保留名字+参数（超长截断）；
// 工具结果只留开头片段。以轮为单位整体折叠，绝不拆散调用/结果配对。
func (a *App) ruleCompactPass(sess *SessionMeta) {
	if a.Cfg.CompactMode != "rule" {
		return
	}
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return
	}
	rounds := splitRounds(raw)
	if len(rounds) <= a.Cfg.KeepRounds {
		return
	}
	// 已压缩边界：最后一条带 compact 标记的消息 seq
	ruleSeq := int64(0)
	for _, m := range raw {
		if m.Compact != "" && m.Seq > ruleSeq && m.Kind == "chat" {
			ruleSeq = m.Seq
		}
	}
	// 统计边界后的新增轮数，达到间隔才做一次
	newRounds := 0
	for _, r := range rounds {
		if r.startSeq > ruleSeq {
			newRounds++
		}
	}
	if newRounds < a.Cfg.RuleInterval {
		return
	}
	// 压缩 [已压缩边界之后, 保留窗口之前] 的所有轮
	keepFrom := rounds[len(rounds)-a.Cfg.KeepRounds].startSeq
	folded, from, to := 0, int64(0), int64(0)
	for _, r := range rounds {
		if r.startSeq <= ruleSeq || r.startSeq >= keepFrom {
			continue
		}
		a.compactRound(sess.ID, r, raw)
		if folded == 0 {
			from = r.startSeq
		}
		to = r.endSeq
		folded++
	}
	if folded > 0 {
		// 审计留痕：规则压缩改写模型可见前缀，是缓存命中率突降的常见原因
		a.auditWrite(sess, map[string]any{"type": "compact", "rounds": folded, "from": from, "to": to})
	}
}

// compactRound 把一轮里的工具调用/结果折叠为轻量表示并落库。
func (a *App) compactRound(sid string, r round, raw []*StoredMessage) {
	// 该轮内 assistant 的工具调用行：正文保留 + 调用清单（参数限长、结果开头）
	for _, m := range raw {
		if m.Seq < r.startSeq || m.Seq > r.endSeq {
			continue
		}
		switch {
		case len(m.ToolCalls) > 0:
			var b strings.Builder
			if s := m.Text(); strings.TrimSpace(s) != "" {
				b.WriteString(s + "\n")
			}
			results := toolResultsOf(raw, r, m)
			for i, tc := range m.ToolCalls {
				b.WriteString(fmt.Sprintf("[调用 %s", tc.Function.Name))
				if args := clipText(tc.Function.Arguments, argKeepLimit); args != "" {
					b.WriteString(" 参数=" + args)
				}
				if i < len(results) {
					b.WriteString(" 结果=" + clipText(results[i], resultKeepLimit))
				}
				b.WriteString("]\n")
			}
			_ = a.Store.SetCompact(sid, m.Seq, strings.TrimRight(b.String(), "\n"))
		case m.Role == "tool":
			_ = a.Store.SetCompact(sid, m.Seq, compactOmit)
		}
	}
}

// toolResultsOf 取与调用行配对的结果文本（按 ToolCallID 对齐）。
func toolResultsOf(raw []*StoredMessage, r round, callRow *StoredMessage) []string {
	byID := map[string]string{}
	for _, m := range raw {
		if m.Seq >= r.startSeq && m.Seq <= r.endSeq && m.Role == "tool" && m.ToolCallID != "" {
			byID[m.ToolCallID] = m.Text()
		}
	}
	out := []string{}
	for _, tc := range callRow.ToolCalls {
		out = append(out, byID[tc.ID])
	}
	return out
}

// ---------- 模型摘要压缩 ----------

const summarizeInstruction = `请把下面这段对话历史压缩成一份结构化中文摘要，供同一对话的后续轮次作为上下文使用。
要求：
1. 输出以下小节（没有内容的写「无」）：主要请求与意图 / 关键概念与决定 / 涉及的文件与改动 / 错误与修复 / 未完成事项 / 当前进行中的工作 / 下一步。
2. 保留精确的路径、命令、错误信息、数字与名称，不得改写。
3. 只输出摘要本身，不要评论、不要提及压缩这件事。`

// doSummarize 把 [最后摘要边界, 保留窗口之前] 的区间压成一条摘要。
// origin=auto 时合并旧自动摘要（DSH 式单一检查点）；origin=manual 时独立叠加、永不重压。
// keepTail=0 表示溢出兜底（保留尾为零，最大化压缩）。
func (a *App) doSummarize(sess *SessionMeta, origin string, keepTail int) (string, error) {
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return "", err
	}
	summaries := activeSummaries(raw)
	boundary := int64(0)
	var prevAuto *StoredMessage
	for _, s := range summaries {
		if s.CoversUpTo > boundary {
			boundary = s.CoversUpTo
		}
		if s.Origin == "auto" {
			prevAuto = s
		}
	}
	keep := keepTail
	if keep <= 0 {
		keep = a.Cfg.KeepRounds
	}
	rounds := splitRounds(raw)
	segEnd := int64(0)
	if len(rounds) > keep {
		segEnd = rounds[len(rounds)-keep].startSeq - 1
	}
	if segEnd <= boundary {
		return "", fmt.Errorf("没有可压缩的区间（保留最近 %d 轮）", keep)
	}
	var b strings.Builder
	if origin == "auto" && prevAuto != nil {
		b.WriteString("【先前的自动摘要（请合并进新摘要，保留仍成立的事实、丢弃过时的）】\n" + prevAuto.Text() + "\n\n")
	}
	b.WriteString("【待压缩的对话历史】\n")
	for _, m := range raw {
		if m.Seq <= boundary || m.Seq > segEnd || m.Kind == "summary" || m.Kind == "env" || m.Kind == "prompt" || m.Kind == inheritKind || m.Kind == "request" || m.Kind == "audit" {
			continue
		}
		role := map[string]string{"user": "用户", "assistant": "助手", "tool": "工具结果"}[m.Role]
		if role == "" {
			role = m.Role
		}
		text := m.Text()
		for _, tc := range m.ToolCalls {
			text += "\n[调用 " + tc.Function.Name + " " + clipText(tc.Function.Arguments, argKeepLimit) + "]"
		}
		b.WriteString(fmt.Sprintf("%s：%s\n", role, clipText(strings.TrimSpace(text), 4000)))
	}

	summary, err := a.ctxEngine().CallSummarizer(sess, summarizeInstruction+"\n\n"+b.String())
	if err != nil {
		return "", err
	}
	if origin == "auto" {
		_ = a.Store.SupersedeAutoSummaries(sess.ID)
	}
	label := "手动"
	if origin == "auto" {
		label = "自动"
	}
	content := fmt.Sprintf("【%s历史摘要（覆盖至第 %d 条，原文可用历史查询工具读取）】\n%s", label, segEnd, summary)
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{
		Message: TextMessage("system", content), Kind: kindSummary, // 上下文材料，非用户发言；发送时现场构造 user 视图
		Origin: origin, CoversUpTo: segEnd, CreatedAt: now(),
	})
	return summary, nil
}

// callSummarizer 已并入上下文引擎（ContextEngine.CallSummarizer）：摘要调用
// 要把对话历史发给模型，拼装与发送归引擎统一口径，此处不再保留旁路。

// maybeAutoCompact 在 llm 模式下请求前检查：估算 token 超过阈值就自动压缩。
func (a *App) maybeAutoCompact(sess *SessionMeta, msgs []Message, ctxWindow int) error {
	if a.Cfg.CompactMode != "llm" || ctxWindow <= 0 {
		return nil
	}
	if estimateTokens(msgs) < int(float64(ctxWindow)*a.Cfg.CompactThreshold) {
		return nil
	}
	_, err := a.doSummarize(sess, "auto", a.Cfg.KeepRounds)
	return err
}

// overflowCompact 是上下文超限的兜底：保留尾为零强制压缩一次（DSH 式）。
func (a *App) overflowCompact(sess *SessionMeta) error {
	if a.Cfg.CompactMode == "off" {
		return fmt.Errorf("上下文超限且压缩已关闭，请手动精简会话或开启压缩")
	}
	_, err := a.doSummarize(sess, "manual", 0)
	return err
}

// isOverflowError 判断模型错误是否为上下文超限。
func isOverflowError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context length") || strings.Contains(s, "maximum context") ||
		strings.Contains(s, "too long") || strings.Contains(s, "context window")
}
