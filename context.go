package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// 上下文管理：组装管线（系统提示词 + 活跃摘要 + 规则压缩视图 + 近期原文）
// 与两种压缩机制——规则压缩（零模型调用）与模型摘要（自动阈值 + 手动按钮）。
// 设计对齐 DSH 的哲学：原始历史 append-only 永久保留，折叠只发生在
// 「模型可见表面」上；模型随时可用历史查询工具读到未压缩原文。

const compactOmit = "omit" // 规则压缩的特殊标记：组装时整行跳过

// argKeepLimit 是折叠视图里工具调用参数的保留上限（防 write 带整文件内容）。
// 思考过程与工具结果在折叠视图里完全丢弃——原文 append-only 永久在库，
// 模型可经 search_sessions / read_session_messages 查回。
const argKeepLimit = 500

// foldBoundaryNote 是折叠边界的提示标签：组装时注入在「已折叠区 → 原文区」
// 的交界处（全折时缀在尾部），告知模型上方是压缩视图、细节可用历史查询工具
// 取回。文案是常量——同一段历史的组装逐字节稳定，前缀缓存不受扰动。
const foldBoundaryNote = "（以上为压缩后的历史：保留了用户输入与模型结论的要点；省略了思考过程、工具结果与环境快照等中间细节。需要查看某处的完整原文时，先用 search_sessions 按关键词搜索定位，再用 read_session_messages（from_seq=命中 seq）读取原文。）"

// round 是一轮对话：一条用户消息（chat/aiuser/report）到下一条用户消息之前的全部。
type round struct {
	startSeq, endSeq int64
}

// splitRounds 按用户消息分轮；开头若无用户消息（不应发生），并入第一轮。
// 继承标记行（inherit）不是对话内容，跳过——否则它会自成伪轮，
// 让子对话的压缩时机比同样长度的普通会话早一轮。
// aiuser（AI 发出的用户消息）与真人用户消息同权开轮：派发指令/派发回报
// 落地即开启一个新回合（前端同样按回合收口、给上一回合带出操作按钮）。
func splitRounds(msgs []*StoredMessage) []round {
	rounds := []round{}
	cur := round{}
	started := false
	for _, m := range msgs {
		if m.Kind == inheritKind {
			continue
		}
		if m.Kind == "chat" && m.Role == "user" || m.Kind == "report" || m.Kind == kindAiUser {
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
// 其后相邻快照按差分形状分流——纯新增渲染为短的「环境更新」补充，条目有减
// 或同 key 换值则完整重发新快照（见 envUpdateMessage）——变化永远以新消息
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
	foldSeen := false // 已见过折叠行：遇到首条未折叠可见行时在交界注入提示标签
	for _, m := range raw {
		if m.Seq <= boundary || m.Kind == "summary" || m.Kind == "prompt" || m.Kind == "inherit" || m.Kind == "request" || m.Kind == "audit" {
			continue
		}
		if ruleMode {
			if m.Compact == "" && foldSeen {
				out = append(out, TextMessage("user", foldBoundaryNote))
				foldSeen = false
			}
			if m.Compact != "" {
				foldSeen = true
			}
		}
		// 折叠丢弃的行先跳过——含压缩区内的环境快照（不再走差分渲染，
		// 保留区首个可见快照自然成为整份基线）
		if ruleMode && m.Compact == compactOmit {
			continue
		}
		if m.Kind == "env" {
			env := m.Text()
			if prevEnv == "" {
				out = append(out, TextMessage("user", env))
			} else if s := envUpdateMessage(prevEnv, env); s != "" {
				// 纯新增 → 短补充；有减/换值 → 完整重发（见 envUpdateMessage）
				out = append(out, TextMessage("user", s))
			}
			prevEnv = env
			continue
		}
		if ruleMode && m.Compact != "" {
			// 压缩表示：纯文本消息，不带 tool_calls（协议要求配对完整）。
			// 用户侧输入的折叠视图与原文一致，时间提示词照常派生
			out = append(out, Message{Role: m.Role, Content: m.Compact})
			out = append(out, a.timeAfter(m)...)
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
			t := m.Thinking
			msg.ReasoningContent = &t
		}
		out = append(out, msg)
		// 时间提示词（独立时间通道）：每条用户侧对话消息——真人用户（chat）与
		// AI 发出的用户消息（aiuser）——后面跟一条只含发送时刻的短 user 消息。
		// 组装期从该消息自身的落库时刻派生，**不落库**（历史查询工具与前端消息
		// 流都看不到噪音行）；同一份存储重复组装逐字节相同，前缀缓存不受影响。
		// 环境快照因此不再含时间行，用户发消息不再连带触发一条「环境更新」。
		out = append(out, a.timeAfter(m)...)
	}
	// 整段可见历史都已折叠（按钮把最后一轮也折了）：提示标签缀在尾部
	if ruleMode && foldSeen {
		out = append(out, TextMessage("user", foldBoundaryNote))
	}
	// 全序列修复工具调用配对（协议硬要求；残缺可能出现在任何位置，见函数注释）
	return repairToolPairing(out)
}

// timeAfter 是时间提示词通道的组装形态：用户侧对话消息（chat 真人发言与
// aiuser 派发指令/回报）后面跟的那条时间消息。返回单条或空——时刻解析不出
// （理论上不发生，所有行落库都带 now() 时间戳）时宁缺毋滥。
// 内容取该消息自己的落库时刻（用户在哪一刻说的话就记哪一刻），分钟粒度与
// 旧环境快照时间行一致；不是挂钟，模型自己执行期间不会凭空多出新的时间消息。
func (a *App) timeAfter(m *StoredMessage) []Message {
	if m.Role != "user" || (m.Kind != kindChat && m.Kind != kindAiUser) {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(m.CreatedAt), time.Local)
	if err != nil {
		return nil
	}
	return []Message{TextMessage("user", "（发送时间："+t.Format("2006-01-02 15:04")+"）")}
}

// repairToolPairing 修复工具调用配对。OpenAI 兼容网关硬校验「assistant 的每个
// tool_calls 调用都必须紧跟一条对应的 tool 响应，且 tool 响应必须有对应调用」，
// 违反一律 400（"must be followed by tool messages"）。残缺配对不只出现在尾部：
// 客户端中断在中间轮、进程崩溃/服务重启的恢复路径（repairDanglingToolCalls 补的
// 合成结尾落在历史尾部、而调用在中段）、规则压缩边界都可能留下悬空调用或孤儿
// 响应。这里做全序列视图修复（消费式重排）：
//   - 已答复的调用：响应重排为**紧跟**其 assistant 消息之后（按 calls 顺序）。
//     历史里响应本就紧随调用时，重排与原顺序逐字节等价（KV 前缀缓存不受影响）；
//     只有兜底补的合成行落在尾部这种错位场景才真正移动位置；
//   - 无响应的 tool_call 从 assistant 上剔除；清空后若有正文保留为普通消息，
//     纯调用且无正文则整条剔除；
//   - 孤儿 tool 响应（无对应调用或缺少 id）整条丢弃。
// 不改落库数据，未配对的原文仍可经历史查询工具读到。
func repairToolPairing(msgs []Message) []Message {
	replies := map[string]Message{} // 调用 id → 响应（有响应才进表）
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			replies[m.ToolCallID] = m
		}
	}
	fixed := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			var kept []ToolCall
			for _, tc := range m.ToolCalls {
				if _, ok := replies[tc.ID]; !ok {
					continue
				}
				kept = append(kept, tc)
			}
			if len(kept) == 0 && strings.TrimSpace(m.Text()) == "" {
				continue // 纯调用且全部无响应：整条剔除
			}
			m.ToolCalls = kept // 全无响应但有正文 → nil，退化为普通文本消息
			fixed = append(fixed, m)
			// 响应紧跟（按 calls 顺序）——原位置的响应行在后面遇到时跳过
			for _, tc := range kept {
				fixed = append(fixed, replies[tc.ID])
			}
		case m.Role == "tool":
			continue // 响应已随其调用带出；走到这里的只剩已消费/孤儿/缺 id 的响应
		default:
			fixed = append(fixed, m)
		}
	}
	return fixed
}

// envUpdateMessage 把相邻两份环境快照渲染成发给模型的消息，按差分形状分流：
//   - 纯新增条目 → 短「环境更新」只列新增行（叠加语义，旧快照仍然有效，
//     前缀缓存友好）；
//   - 条目有减（被移除），或同 key 换值（= 有增有减的变更）→ 完整重发新快照
//     （标题行标明「完整重发」）——让模型自己按「不再适用」清单脑内合并旧快照
//     容易读错状态，减法一律给全量，一眼即最新。
//
// 确定性纯函数（存储文本唯一决定输出），同文快照的标题行会被精确匹配自然滤掉。
// 纯注记行（非 "- " 条目，如技能变更说明）过期后静默消失不触发全量重发，
// 新出现的注记行按新增进补充消息。
func envUpdateMessage(prev, cur string) string {
	prevCount := map[string]int{}
	prevKeys := map[string]bool{} // 旧快照的条目 key 集：识别「同 key 换值」
	for _, l := range strings.Split(prev, "\n") {
		prevCount[l]++
		if k := envLineKey(l); k != "" && strings.HasPrefix(strings.TrimSpace(l), "- ") {
			prevKeys[k] = true
		}
	}
	changed := []string{}
	keyReplaced := false
	for _, l := range strings.Split(cur, "\n") {
		if prevCount[l] > 0 {
			prevCount[l]--
			continue // 整行未变
		}
		if k := envLineKey(l); k != "" && prevKeys[k] {
			keyReplaced = true // 同一条目换了取值：按变更处理
		}
		changed = append(changed, l)
	}
	entryRemoved := false
	for l, n := range prevCount {
		if n > 0 && strings.HasPrefix(strings.TrimSpace(l), "- ") {
			entryRemoved = true // 有 "- " 条目在新快照里没了（含被换值的旧值）
			break
		}
	}
	if entryRemoved || keyReplaced {
		return fullResendEnv(cur)
	}
	if len(changed) == 0 {
		return "" // 完全同文（防御：落库层等值比较已保证不会走到这）
	}
	sort.Strings(changed) // 无序收集，排序保证确定性
	var b strings.Builder
	b.WriteString("# 环境更新（新增条目，叠加在更早的快照与更新之上；条目减少或变更时会完整重发快照）\n")
	for _, l := range changed {
		b.WriteString(l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// fullResendEnv 把新快照渲染成一次完整重发：正文用新快照原文，首行标题换成
// 「完整重发」声明，取代此前所有快照与差分更新（模型无需再回头合并旧文）。
func fullResendEnv(cur string) string {
	body := cur
	if i := strings.IndexByte(cur, '\n'); i >= 0 && strings.HasPrefix(cur, "# ") {
		body = cur[i+1:]
	}
	return "# 运行环境快照（完整重发，取代更早的快照与更新）\n" + strings.TrimRight(body, "\n")
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
	// 输出速度（吐字速度）：最近一次带 token 统计的成功输出。分子=厂家 usage
	// 报的 completion_tokens，分母=输出窗口时长（首个输出事件到末个输出事件，
	// 不含首字前的排队等待）。
	Speed struct {
		TokPerSec float64 `json:"tokPerSec"`
		Tokens    int     `json:"tokens"` // 最后一次输出的 token 数（厂家口径）
		Ms        int64   `json:"ms"`     // 最后一次输出的窗口时长
	} `json:"speed"`
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

	var out ctxStats
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
		foldSeen := false // 与 buildContext 同一折叠边界口径：交界处注入提示标签
		for _, m := range raw {
			if m.Seq <= boundary || m.Kind == "summary" || m.Kind == "prompt" || m.Kind == inheritKind || m.Kind == "request" || m.Kind == "audit" {
				continue
			}
			if ruleMode {
				if m.Compact == "" && foldSeen {
					add("消息", TextMessage("user", foldBoundaryNote))
					foldSeen = false
				}
				if m.Compact != "" {
					foldSeen = true
				}
			}
			// 与 buildContext 同一折叠口径：omit 行（含压缩区环境快照）不进上下文
			if ruleMode && m.Compact == compactOmit {
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
				add("消息", Message{Role: m.Role, Content: m.Compact})
				// 与 buildContext 同一派生口径：折叠的用户侧行同样跟时间消息
				for _, tm := range a.timeAfter(m) {
					add("消息", tm)
				}
				continue
			}
			add("消息", m.Message)
			// 时间提示词与 buildContext 同一派生口径（用户侧对话消息后跟一条
			// 时间消息），计入「消息」类——它确实随请求发出、占 token
			for _, tm := range a.timeAfter(m) {
				add("消息", tm)
			}
		}
		// 与 buildContext 同一口径：整段可见历史都已折叠时提示标签缀尾
		if ruleMode && foldSeen {
			add("消息", TextMessage("user", foldBoundaryNote))
		}
		// 输出速度：倒序找最近一次有效的成功输出——audit 行带输出窗口 outMs 且
		// 厂家报了 completion_tokens。窗口过短（<100ms，瞬发完成）与纯工具调用
		// 步没有速度意义，跳过取更早一次的真实文本输出
		for i := len(raw) - 1; i >= 0; i-- {
			if raw[i].Kind != kindAudit {
				continue
			}
			var row auditRow
			if json.Unmarshal([]byte(raw[i].Text()), &row) != nil ||
				row.Type != "call" || row.Scope != "" || row.Error != "" ||
				row.OutMs < 100 || row.Usage == nil || row.Usage.Completion <= 0 {
				continue
			}
			out.Speed.Tokens = row.Usage.Completion
			out.Speed.Ms = row.OutMs
			out.Speed.TokPerSec = float64(row.Usage.Completion) * 1000 / float64(row.OutMs)
			break
		}
	}

	order := []string{"消息", "系统工具", "系统提示词", "摘要", "环境快照", "其他"}
	total := 0
	for _, v := range cats {
		total += v
	}
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

// 规则压缩不随轮次自动跑（每轮改写历史前缀会持续打断 KV 缓存），只在两种
// 时机执行：上下文估算达到阈值时（maybeAutoCompact）与手动压缩时
//（handleCompact 的 upTo）。折叠语义分两级（见 compactRound）：一级保留用户
// 输入与正文、调用名+参数，丢弃思考/工具结果/环境快照；二级（已折过的轮再被
// 更大的压缩范围覆盖）只留用户输入与模型最终输出。组装时在折叠边界注入
// foldBoundaryNote 提示模型「以上已压缩、细节可查历史」；以轮为单位整体折叠，
// 绝不拆散调用/结果配对。
// visibleSize 是一行消息在「模型可见表面」上的字数（rune）：折叠过的行按
// 折叠视图计（omit=0），原文行计正文+思考+工具参数（这些都会随请求发出）；
// 视图外的归档行（summary/prompt/request/audit/inherit）不计。
func visibleSize(m *StoredMessage) int {
	switch {
	case m.Kind == "summary" || m.Kind == "prompt" || m.Kind == "request" || m.Kind == "audit" || m.Kind == inheritKind:
		return 0
	case m.Compact == compactOmit:
		return 0
	case m.Compact != "":
		return len([]rune(m.Compact))
	}
	n := len([]rune(m.Text())) + len([]rune(m.Thinking))
	for _, tc := range m.ToolCalls {
		n += len([]rune(tc.Function.Arguments))
	}
	return n
}

// keepRoundsByBudget 按保留预算（KeepChars，字）算自动压缩保留几轮：从最新
// 一轮往前累计模型可见字数，装得下的整轮都保留；命中点可能落在某一轮中间，
// 轮是原子单位——往上扩到整轮保留，宁可略超预算也不劈半轮。至少保 1 轮
//（最近一轮独大时只留它，更早的全部折叠）。
func (a *App) keepRoundsByBudget(raw []*StoredMessage) int {
	rounds := splitRounds(raw)
	if len(rounds) == 0 {
		return 0
	}
	sizeOf := func(r round) int {
		n := 0
		for _, m := range raw {
			if m.Seq >= r.startSeq && m.Seq <= r.endSeq {
				n += visibleSize(m)
			}
		}
		return n
	}
	kept, total := 1, sizeOf(rounds[len(rounds)-1])
	for i := len(rounds) - 2; i >= 0; i-- {
		s := sizeOf(rounds[i])
		if total+s > a.Cfg.KeepChars {
			break
		}
		total += s
		kept++
	}
	return kept
}

// ruleCompactAuto 阈值触发/溢出兜底的自动入口：把保留窗口之外、尚未折叠的
// 轮次全部折叠。keep<=0 表示自动触发——按 KeepChars 字数预算保留最近的完整
// 轮（规则与模型摘要两种压缩共用同一预算口径）；keep=1 是溢出兜底的最大化
// 压缩（只留最近一轮）。返回折叠的轮数。
func (a *App) ruleCompactAuto(sess *SessionMeta, keep int) int {
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return 0
	}
	rounds := splitRounds(raw)
	if keep <= 0 {
		keep = a.keepRoundsByBudget(raw)
	}
	if len(rounds) <= keep {
		return 0
	}
	// 保留窗口起点的前一条即压缩终点
	return a.foldRounds(sess, raw, rounds, rounds[len(rounds)-keep].startSeq-1)
}

// ruleCompactTo 手动压缩：折叠 upTo 所在轮（含）及更早、尚未折叠的全部轮，
// upTo 之后的轮保持原文。返回折叠的轮数。
func (a *App) ruleCompactTo(sess *SessionMeta, upTo int64) (int, error) {
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		return 0, err
	}
	rounds := splitRounds(raw)
	// 找 upTo 所在轮：该轮整体折叠（一轮是不可分割的压缩单位，拆半轮会让
	// 正文与调用分家）；upTo 落在轮外的行（inherit 标记等）时并给下一轮
	end := int64(0)
	for _, r := range rounds {
		if upTo >= r.startSeq && upTo <= r.endSeq {
			end = r.endSeq
			break
		}
	}
	if end == 0 {
		for _, r := range rounds {
			if r.startSeq > upTo {
				end = r.endSeq
				break
			}
		}
	}
	if end == 0 {
		return 0, fmt.Errorf("第 %d 条之前没有可折叠的回合", upTo)
	}
	if n := a.foldRounds(sess, raw, rounds, end); n > 0 {
		return n, nil
	}
	return 0, fmt.Errorf("第 %d 条之前的回合都已折叠过了", upTo)
}

// foldRounds 折叠 endSeq 及更早、尚未折叠的全部轮，返回**新**折叠轮数。
// 已折叠边界 = 带任意 compact 标记的行（含 omit 的工具结果与环境快照）的最大
// seq——compactRound 对每轮至少标一行（用户输入也标），边界因此总能推进。
// 二级升级：本次有新折叠时，边界之前的旧折叠轮同步升级为二级压缩（只留用户
// 输入与模型最终输出）——压缩标签以上的部分进一步丢弃中间过程；标签以下
//（本次新折的）按一级规则。无新折叠时不做任何事（重复点击保持幂等报错）。
func (a *App) foldRounds(sess *SessionMeta, raw []*StoredMessage, rounds []round, endSeq int64) int {
	ruleSeq := int64(0)
	for _, m := range raw {
		if m.Compact != "" && m.Seq > ruleSeq {
			ruleSeq = m.Seq
		}
	}
	folded, upgraded, from := 0, 0, int64(0)
	for _, r := range rounds {
		if r.startSeq <= ruleSeq || r.endSeq > endSeq {
			continue
		}
		a.compactRound(sess.ID, r, raw)
		if folded == 0 {
			from = r.startSeq
		}
		folded++
	}
	if folded > 0 {
		for _, r := range rounds {
			if r.startSeq <= ruleSeq && r.endSeq <= endSeq {
				a.compactRoundAgain(sess.ID, r, raw)
				upgraded++
			}
		}
		// 审计留痕：规则压缩改写模型可见前缀，是缓存命中率突降的常见原因
		a.auditWrite(sess, map[string]any{"type": "compact", "rounds": folded, "upgraded": upgraded, "from": from, "to": endSeq})
		// 通知前端重取：compact 标记变了，对话区要在真实位置翻新「已折叠」行
		a.broadcast(Event{Type: "session_updated", SessionID: sess.ID})
	}
	return folded
}

// compactRound 把一整轮**首次**折叠为一级轻量表示并落库（幂等，不改原文；
// 已折过的轮由 foldRounds 路由到 compactRoundAgain 做二级升级）：
//   - 用户侧输入（chat/aiuser/report）：Compact=原文（视图逐字节不变，标记只为推进边界）
//   - 助手正文行：Compact=原文（思考过程丢弃——折叠视图不带 ReasoningContent）
//   - 带工具调用的助手行：正文 + [调用 名 参数=clip]（工具结果不保留）
//   - 工具结果行与环境快照行：omit（组装时整行跳过）
func (a *App) compactRound(sid string, r round, raw []*StoredMessage) {
	for _, m := range raw {
		if m.Seq < r.startSeq || m.Seq > r.endSeq {
			continue
		}
		switch {
		case m.Kind == "summary" || m.Kind == "prompt" || m.Kind == inheritKind || m.Kind == "request" || m.Kind == "audit":
			// 视图外的归档行不标（组装本来就跳过）
		case len(m.ToolCalls) > 0:
			var b strings.Builder
			if s := m.Text(); strings.TrimSpace(s) != "" {
				b.WriteString(s + "\n")
			}
			for _, tc := range m.ToolCalls {
				b.WriteString(fmt.Sprintf("[调用 %s", tc.Function.Name))
				if args := clipText(tc.Function.Arguments, argKeepLimit); args != "" {
					b.WriteString(" 参数=" + args)
				}
				b.WriteString("]\n")
			}
			_ = a.Store.SetCompact(sid, m.Seq, strings.TrimRight(b.String(), "\n"))
		case m.Role == "tool" || m.Kind == "env":
			_ = a.Store.SetCompact(sid, m.Seq, compactOmit)
		case m.Role == "assistant" || m.Role == "user":
			if s := m.Text(); strings.TrimSpace(s) != "" {
				_ = a.Store.SetCompact(sid, m.Seq, s)
			}
		}
	}
}

// compactRoundAgain 是二级压缩：对已折叠过的轮再压，只留用户输入与模型最终
// 输出（= 该轮最后一条纯正文助手行），其余整行 omit。
func (a *App) compactRoundAgain(sid string, r round, raw []*StoredMessage) {
	finalSeq := int64(0)
	for _, m := range raw {
		if m.Seq < r.startSeq || m.Seq > r.endSeq {
			continue
		}
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Text()) != "" {
			finalSeq = m.Seq
		}
	}
	for _, m := range raw {
		if m.Seq < r.startSeq || m.Seq > r.endSeq {
			continue
		}
		switch {
		case m.Kind == "summary" || m.Kind == "prompt" || m.Kind == inheritKind || m.Kind == "request" || m.Kind == "audit":
			// 视图外的归档行不标
		case m.Role == "user":
			// 用户输入保留原文（一级时已标，幂等；只补漏）
			if m.Compact == "" && strings.TrimSpace(m.Text()) != "" {
				_ = a.Store.SetCompact(sid, m.Seq, m.Text())
			}
		case m.Seq == finalSeq:
			// 模型最终输出保留原文
			_ = a.Store.SetCompact(sid, m.Seq, m.Text())
		default:
			// 中间过程全部丢弃：调用行、过程性正文、工具结果、环境快照
			_ = a.Store.SetCompact(sid, m.Seq, compactOmit)
		}
	}
}

// ---------- 模型摘要压缩 ----------

const summarizeInstruction = `请把下面这段对话历史压缩成一份结构化中文摘要，供同一对话的后续轮次作为上下文使用。
要求：
1. 输出以下小节（没有内容的写「无」）：主要请求与意图 / 关键概念与决定 / 涉及的文件与改动 / 错误与修复 / 未完成事项 / 当前进行中的工作 / 下一步。
2. 保留精确的路径、命令、错误信息、数字与名称，不得改写。
3. 只输出摘要本身，不要评论、不要提及压缩这件事。`

// doSummarize 把 [最后摘要边界, 压缩终点] 的区间压成一条摘要。
// origin=auto 时合并旧自动摘要（DSH 式单一检查点）；origin=manual 时独立叠加、永不重压。
// upTo>0（手动按钮）时压缩终点=upTo 所在轮（含），之后的轮保持原文；
// 否则按保留窗口从尾部倒数——keepTail<=0 表示自动触发，与规则压缩共用同一
// 字数预算（keepRoundsByBudget）；溢出兜底显式传 1（只留最近一轮）。
func (a *App) doSummarize(sess *SessionMeta, origin string, keepTail int, upTo int64) (string, error) {
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
		keep = a.keepRoundsByBudget(raw)
	}
	rounds := splitRounds(raw)
	segEnd := int64(0)
	if upTo > 0 {
		for _, r := range rounds {
			if upTo >= r.startSeq && upTo <= r.endSeq {
				segEnd = r.endSeq
				break
			}
		}
		if segEnd == 0 {
			for _, r := range rounds {
				if r.startSeq > upTo {
					segEnd = r.endSeq
					break
				}
			}
		}
		if segEnd == 0 {
			return "", fmt.Errorf("第 %d 条之前没有可压缩的回合", upTo)
		}
	} else if len(rounds) > keep {
		segEnd = rounds[len(rounds)-keep].startSeq - 1
	}
	if segEnd <= boundary {
		return "", fmt.Errorf("没有可压缩的区间（该范围之前的内容已压缩过）")
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

	summary, err := a.ctxEngine().CallSummarizer(sess, summarizeInstructionFor(sess)+"\n\n"+b.String())
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

// summarizeInstructionAssistant 是数字助理主对话（mode=assistant）专属的摘要
// 指令。设计意图（为什么要有这一份）：其他会话是一次任务一个对话，压掉过程
// 细节无妨，按模型自己的压缩习惯就好；数字助理主对话不同——它是用户与常驻
// 助理的长期对话，跨任务、跨天数，历史里沉淀着用户的偏好、习惯与定下的约定，
// 这些一旦被摘要压掉，助理就「不认识这个用户」了。所以它的摘要侧重点要
// 先记人再记事：小节里专门加了「用户偏好与习惯」「长期有效的约定」两栏，
// 其余小节与其他会话一致。
const summarizeInstructionAssistant = `请把「数字助理主对话」的这段历史压缩成一份结构化中文摘要，供同一对话的后续轮次作为上下文使用。这是用户与常驻数字助理的长期对话，跨任务、跨天数——摘要首先要让助理"还认识这个用户、还记得这些事"。
要求：
1. 输出以下小节（没有内容的写「无」）：用户偏好与习惯 / 长期有效的约定 / 任务与结果 / 关键事实与决定 / 涉及的文件与改动 / 错误与教训 / 进行中与未完成 / 下一步。
2. 「用户偏好与习惯」写：称呼与身份、沟通偏好（详略/语言/格式）、常用工具与工作流、常用路径/设备/软件线索、明确表达过的喜欢与不喜欢。
3. 「长期有效的约定」逐条保留用户定下的做事方式、禁忌与必须遵守的口径，保留原意、不概括掉。
4. 只输出摘要本身，不要评论、不要提及压缩这件事。`

// summarizeInstructionFor 按会话模式选摘要指令——只是选「发给模型的提示词
// 文字」，不改变压缩方式/路径/保留窗口（设置里的压缩模式照旧）：数字助理主
// 对话用上面的专属指令，其余会话用通用指令 summarizeInstruction。自动摘要
//（llm 模式）与手动压缩（消息级「压缩」按钮、设置页「压缩数字助理主对话」）
// 的摘要调用都从这里取指令。
func summarizeInstructionFor(sess *SessionMeta) string {
	if sess != nil && sess.Mode == "assistant" {
		return summarizeInstructionAssistant
	}
	return summarizeInstruction
}

// callSummarizer 已并入上下文引擎（ContextEngine.CallSummarizer）：摘要调用
// 要把对话历史发给模型，拼装与发送归引擎统一口径，此处不再保留旁路。

// maybeAutoCompact 在每次请求组装后检查：估算 token 超过阈值（CompactThreshold×
// 窗口）就自动压缩——rule 模式做规则折叠（零模型调用），llm 模式生成摘要。
// 返回是否发生了压缩：true 时调用方应重新组装请求，避免带着超限前的旧消息发出。
func (a *App) maybeAutoCompact(sess *SessionMeta, msgs []Message, ctxWindow int) (bool, error) {
	if a.Cfg.CompactMode == "off" || ctxWindow <= 0 {
		return false, nil
	}
	if estimateTokens(msgs) < int(float64(ctxWindow)*a.Cfg.CompactThreshold) {
		return false, nil
	}
	if a.Cfg.CompactMode == "rule" {
		return a.ruleCompactAuto(sess, 0) > 0, nil
	}
	_, err := a.doSummarize(sess, "auto", 0, 0)
	return err == nil, err
}

// overflowCompact 是上下文超限的兜底：最大化压缩一次。rule 模式规则折叠到
// 只留最近 1 轮（零模型调用）；llm 模式生成只留最近 1 轮的摘要。
func (a *App) overflowCompact(sess *SessionMeta) error {
	if a.Cfg.CompactMode == "off" {
		return fmt.Errorf("上下文超限且压缩已关闭，请手动精简会话或开启压缩")
	}
	if a.Cfg.CompactMode == "rule" {
		if a.ruleCompactAuto(sess, 1) > 0 {
			return nil
		}
		return fmt.Errorf("没有可折叠的回合（上下文超限可能来自系统提示词或工具目录本身）")
	}
	_, err := a.doSummarize(sess, "manual", 1, 0)
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
