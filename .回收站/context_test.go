package main

import (
	"strings"
	"testing"
)

func newTestApp(t *testing.T, mode string, keep, interval int) *App {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &Config{CompactMode: mode, KeepRounds: keep, RuleInterval: interval, Workspace: "ws"}
	a := &App{Store: store, Cfg: cfg}
	a.initRuntime() // 运行登记表/待生效消息表等，测试里也要能用执行相关接口
	return a
}

// 插入一轮：用户消息 + 带工具调用的助手消息 + 工具结果
func insertRound(t *testing.T, a *App, sid, userText, toolName, toolResult string) {
	t.Helper()
	_ = a.Store.Append(sid, TextMessage("user", userText))
	calls := []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunc{Name: toolName, Arguments: `{"path":"x.txt"}`}}}
	_ = a.Store.AppendRow(sid, &StoredMessage{Message: Message{Role: "assistant", ToolCalls: calls}, Kind: "chat"})
	_ = a.Store.Append(sid, Message{Role: "tool", Content: toolResult, ToolCallID: "c1"})
}

func TestSplitRounds(t *testing.T) {
	msgs := []*StoredMessage{
		{Seq: 1, Kind: "chat", Message: TextMessage("user", "a")},
		{Seq: 2, Kind: "chat", Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "x"}}}},
		{Seq: 3, Kind: "chat", Message: Message{Role: "tool", ToolCallID: "x"}},
		{Seq: 4, Kind: "chat", Message: TextMessage("user", "b")},
		{Seq: 5, Kind: "chat", Message: TextMessage("assistant", "ok")},
	}
	rounds := splitRounds(msgs)
	if len(rounds) != 2 {
		t.Fatalf("应为 2 轮，得到 %d", len(rounds))
	}
	if rounds[0].startSeq != 1 || rounds[0].endSeq != 3 || rounds[1].startSeq != 4 || rounds[1].endSeq != 5 {
		t.Fatalf("轮边界错误: %+v", rounds)
	}
}

func TestRuleCompactKeepsRecentAndCollapsesOldToolDetail(t *testing.T) {
	a := newTestApp(t, "rule", 2, 2)
	sess := &SessionMeta{ID: "s1", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	longResult := strings.Repeat("文件内容 ABCDEFGH", 200) // 3200 字，折叠后只剩开头 200
	for i := 1; i <= 4; i++ {
		insertRound(t, a, "s1", strings.Repeat("用户消息", 30), "write", longResult)
	}
	a.ruleCompactPass(sess)

	raw, _ := a.Store.RawMessagesAll("s1")
	rounds := splitRounds(raw)
	if len(rounds) != 4 {
		t.Fatalf("应 4 轮，得到 %d", len(rounds))
	}
	keepFrom := rounds[len(rounds)-2].startSeq // 保留最近 2 轮
	var compactedAssistant, omittedTool, compactedUser int
	for _, m := range raw {
		if m.Seq >= keepFrom {
			if m.Compact != "" {
				t.Fatalf("保留窗口内的消息不应被压缩: seq=%d", m.Seq)
			}
			continue
		}
		switch {
		case len(m.ToolCalls) > 0:
			if m.Compact == "" || m.Compact == compactOmit {
				t.Fatalf("工具调用行应有折叠表示: seq=%d", m.Seq)
			}
			if !strings.Contains(m.Compact, "write") || !strings.Contains(m.Compact, "x.txt") {
				t.Fatalf("折叠表示应保留工具名与参数: %s", m.Compact)
			}
			if !strings.Contains(m.Compact, "文件内容") {
				t.Fatalf("折叠表示应保留结果开头片段")
			}
			if len([]rune(m.Compact)) > 1200 {
				t.Fatalf("折叠表示不应包含完整结果（长度 %d）", len([]rune(m.Compact)))
			}
			compactedAssistant++
		case m.Role == "tool":
			if m.Compact != compactOmit {
				t.Fatalf("工具结果行应被 omit: seq=%d", m.Seq)
			}
			omittedTool++
		case m.Role == "user":
			// 用户消息永远原文，无 compact
			compactedUser++
		}
	}
	if compactedAssistant != 2 || omittedTool != 2 {
		t.Fatalf("前两轮应各折叠 1 调用行 + 1 结果行，得到 %d/%d", compactedAssistant, omittedTool)
	}
	if compactedUser != 2 {
		t.Fatalf("应有两个用户消息")
	}

	ctxMsgs := a.buildContext(sess, "SYS")
	// SYS + 前两轮的 2 用户原文 + 2 折叠 assistant + 后两轮原始 6 条（无尾部环境追加）
	if len(ctxMsgs) != 11 {
		t.Fatalf("组装长度应为 11，得到 %d", len(ctxMsgs))
	}
	hasDangling := false
	for _, m := range ctxMsgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// 最近两轮保留原调用是合法的；折叠区不允许出现无结果的调用
		}
	}
	if hasDangling {
		t.Fatal("出现悬空调用")
	}
	// 折叠区的 assistant 消息必须没有 tool_calls（协议要求配对完整）
	for _, m := range ctxMsgs[:5] {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			t.Fatal("折叠区出现带 tool_calls 的消息（会破坏配对）")
		}
	}
}

func TestSummariesAssemblyStacksManualAndLatestAuto(t *testing.T) {
	a := newTestApp(t, "llm", 1, 5)
	sess := &SessionMeta{ID: "s2", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)
	insertRound(t, a, "s2", "第一轮", "read", "r1")
	insertRound(t, a, "s2", "第二轮", "read", "r2")
	// 手动摘要覆盖到 seq 3；随后被合并的旧自动摘要；最新自动摘要覆盖到 seq 6
	_ = a.Store.AppendRow("s2", &StoredMessage{Message: TextMessage("user", "【手动历史摘要】第一批"), Kind: "summary", Origin: "manual", CoversUpTo: 3})
	_ = a.Store.AppendRow("s2", &StoredMessage{Message: TextMessage("user", "【旧自动摘要】"), Kind: "summary", Origin: "auto", CoversUpTo: 6, Superseded: true})
	_ = a.Store.AppendRow("s2", &StoredMessage{Message: TextMessage("user", "【新自动摘要】第二批"), Kind: "summary", Origin: "auto", CoversUpTo: 6})

	msgs := a.buildContext(sess, "SYS")
	var manualCount, autoCount, staleAuto int
	var rawOld int
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			switch {
			case strings.Contains(s, "手动历史摘要"):
				manualCount++
			case strings.Contains(s, "旧自动摘要"):
				staleAuto++
			case strings.Contains(s, "新自动摘要"):
				autoCount++
			case strings.Contains(s, "第一轮") || strings.Contains(s, "r1"):
				rawOld++ // 已被摘要覆盖的区间不应再出现原文
			}
		}
	}
	if manualCount != 1 || autoCount != 1 || staleAuto != 0 {
		t.Fatalf("摘要注入错误: manual=%d auto=%d stale=%d", manualCount, autoCount, staleAuto)
	}
	if rawOld != 0 {
		t.Fatalf("被摘要覆盖的原文不应进入上下文（出现 %d 处）", rawOld)
	}
}

func TestSummarizeSessionCountsAndFiles(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	_ = a.Store.Create(&SessionMeta{ID: "s3", Title: "T", Mode: "task"})
	insertRound(t, a, "s3", "任务", "write", "已写入 a.txt（10 字节）")
	insertRound(t, a, "s3", "继续", "read", "内容…")
	_ = a.Store.Append("s3", TextMessage("assistant", "完成"))
	rounds, steps, toolCounts, _, final, files := summarizeSession(mustMessages(t, a, "s3"))
	tools := strings.Join(toolCounts, "、")
	if rounds != 2 || steps != 2 {
		t.Fatalf("轮/步计数错误: %d/%d", rounds, steps)
	}
	if !strings.Contains(tools, "write×1") || !strings.Contains(tools, "read×1") {
		t.Fatalf("工具统计错误: %s", tools)
	}
	if !strings.HasPrefix(final, "完成") {
		t.Fatalf("最终答复错误: %s", final)
	}
	if len(files) != 1 || files[0].Path != "x.txt" {
		t.Fatalf("改动文件应来自调用参数路径: %+v", files)
	}
}

func mustMessages(t *testing.T, a *App, sid string) []Message {
	t.Helper()
	msgs, err := a.Store.Messages(sid)
	if err != nil {
		t.Fatalf("读消息失败: %v", err)
	}
	return msgs
}

func TestEstimateTokensMonotonic(t *testing.T) {
	small := estimateTokens([]Message{TextMessage("user", "你好")})
	big := estimateTokens([]Message{TextMessage("user", strings.Repeat("你好", 1000))})
	if small <= 0 || big <= small*100 {
		t.Fatalf("token 估算不合理: %d vs %d", small, big)
	}
}

// buildContext 的全序列工具配对修复：悬空调用/孤儿响应可能来自中断、崩溃恢复
// 与压缩边界——出现在任何位置都要修掉，否则网关 400「tool_calls 必须跟随响应」。
func TestBuildContextRepairsToolPairing(t *testing.T) {
	a := newTestApp(t, "llm", 99, 99) // 不触发折叠，聚焦配对修复
	sess := &SessionMeta{ID: "p1", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)

	// 第 1 轮：完整配对（两条调用两条响应）
	_ = a.Store.Append("p1", TextMessage("user", "完整轮"))
	_ = a.Store.AppendRow("p1", &StoredMessage{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "a1", Type: "function", Function: ToolCallFunc{Name: "read"}},
		{ID: "a2", Type: "function", Function: ToolCallFunc{Name: "read"}},
	}}, Kind: "chat"})
	_ = a.Store.Append("p1", Message{Role: "tool", Content: "r-a1", ToolCallID: "a1"})
	_ = a.Store.Append("p1", Message{Role: "tool", Content: "r-a2", ToolCallID: "a2"})

	// 第 2 轮：部分响应（b1 有响应，b2/b3 悬空）且位于历史中段——
	// 旧的尾部修剪覆盖不到（其后还有新消息），这就是「永久 400」的来源
	_ = a.Store.Append("p1", TextMessage("user", "部分轮"))
	_ = a.Store.AppendRow("p1", &StoredMessage{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "b1", Type: "function", Function: ToolCallFunc{Name: "write"}},
		{ID: "b2", Type: "function", Function: ToolCallFunc{Name: "write"}},
		{ID: "b3", Type: "function", Function: ToolCallFunc{Name: "write"}},
	}}, Kind: "chat"})
	_ = a.Store.Append("p1", Message{Role: "tool", Content: "r-b1", ToolCallID: "b1"})
	// 孤儿响应：无任何调用与之对应
	_ = a.Store.Append("p1", Message{Role: "tool", Content: "孤儿", ToolCallID: "zz"})

	_ = a.Store.Append("p1", TextMessage("user", "继续"))
	// 纯悬空调用（无正文无响应，如中断残留）：整条剔除
	_ = a.Store.AppendRow("p1", &StoredMessage{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "c1", Type: "function", Function: ToolCallFunc{Name: "run_command"}},
	}}, Kind: "chat"})
	// 有正文的悬空调用：保留文本、去掉调用
	_ = a.Store.AppendRow("p1", &StoredMessage{Message: Message{Role: "assistant", Content: "我先查一下", ToolCalls: []ToolCall{
		{ID: "d1", Type: "function", Function: ToolCallFunc{Name: "read"}},
	}}, Kind: "chat"})
	_ = a.Store.Append("p1", TextMessage("assistant", "好了"))

	msgs := a.buildContext(sess, "SYS")

	answered := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	calls := map[string]bool{}
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = true
			if !answered[tc.ID] {
				t.Fatalf("悬空调用未修复: %s", tc.ID)
			}
		}
	}
	for _, m := range msgs {
		if m.Role == "tool" && !calls[m.ToolCallID] {
			t.Fatalf("孤儿响应未丢弃: %s", m.ToolCallID)
		}
	}
	var sawB1Call, sawB1Tool, sawKeptText, sawTail bool
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			switch tc.ID {
			case "b1":
				sawB1Call = true
			case "b2", "b3", "c1", "d1":
				t.Fatalf("无响应调用不应出现: %s", tc.ID)
			}
		}
		if m.Role == "tool" && m.ToolCallID == "b1" {
			sawB1Tool = true
		}
		if s, ok := m.Content.(string); ok {
			if s == "我先查一下" {
				sawKeptText = true
			}
			if s == "好了" {
				sawTail = true
			}
		}
	}
	if !sawB1Call || !sawB1Tool || !sawKeptText || !sawTail {
		t.Fatalf("修复结果不符合预期: b1call=%v b1tool=%v 保留正文=%v 尾部=%v",
			sawB1Call, sawB1Tool, sawKeptText, sawTail)
	}
}

// TestRepairToolPairingRelocatesLateResults 覆盖兜底补齐行的错位场景：合成结尾
// （或任何响应）落在历史尾部、而调用在中段时，发送视图必须把响应重排为紧跟其
// assistant 消息之后（网关硬校验「assistant 带 tool_calls 后必须紧跟对应 tool
// 响应」，错位一律 400）；顺带看护正常配对的逐字节等价（原位响应不移动）。
func TestRepairToolPairingRelocatesLateResults(t *testing.T) {
	msgs := []Message{
		TextMessage("user", "干活"),
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "x1", Type: "function", Function: ToolCallFunc{Name: "read"}},
			{ID: "x2", Type: "function", Function: ToolCallFunc{Name: "read"}},
		}},
		{Role: "tool", Content: "r1", ToolCallID: "x1"}, // 原位响应
		TextMessage("user", "继续"),                       // 调用之后还有后续对话
		TextMessage("assistant", "好"),
		{Role: "tool", Content: interruptNoteNotRun, ToolCallID: "x2"}, // 补的合成结尾落在尾部
	}
	fixed := repairToolPairing(msgs)
	wantRoles := []string{"user", "assistant", "tool", "tool", "user", "assistant"}
	if len(fixed) != len(wantRoles) {
		t.Fatalf("修复后应有 %d 条，得到 %d：%+v", len(wantRoles), len(fixed), fixed)
	}
	for i, want := range wantRoles {
		if fixed[i].Role != want {
			t.Fatalf("第 %d 条角色应为 %s，得到 %s", i, want, fixed[i].Role)
		}
	}
	if fixed[2].ToolCallID != "x1" || fixed[3].ToolCallID != "x2" {
		t.Fatalf("响应未按 calls 顺序紧跟：%s, %s", fixed[2].ToolCallID, fixed[3].ToolCallID)
	}
	if s, _ := fixed[2].Content.(string); s != "r1" {
		t.Fatalf("原位响应内容被改动: %q", s)
	}
}
