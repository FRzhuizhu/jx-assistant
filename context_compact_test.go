package main

// 规则压缩重构的测试：折叠语义（丢结果/丢思考/丢环境快照、保留用户输入与
// 正文、调用留名+参数）、手动按钮按 upTo 划范围、自动阈值触发的保留窗口、
// 溢出兜底、以及历史查询工具的三列搜索与正向窗口预算。

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCompactTestApp 建一个带临时库的 App（测试用完即删，只落本目录）。
func newCompactTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &App{Cfg: DefaultConfig(), Store: s}
}

// seedRound 落一轮对话：用户输入 → 带工具调用的助手行（带思考）→ 工具结果 →
// 助手结论（带思考）。返回关键行 seq（user/call/tool/final）。
func seedRound(t *testing.T, a *App, sid, label string, withTool bool) (user, call, tool, final int64) {
	t.Helper()
	next := func() int64 {
		all, err := a.Store.RawMessagesAll(sid)
		if err != nil {
			t.Fatalf("读历史失败: %v", err)
		}
		return int64(len(all) + 1)
	}
	user = next()
	if err := a.Store.AppendRow(sid, &StoredMessage{
		Message: TextMessage("user", label+"的问题"), Kind: kindChat, CreatedAt: "2026-01-02 10:00:00",
	}); err != nil {
		t.Fatal(err)
	}
	if withTool {
		args, _ := json.Marshal(map[string]any{"path": label + ".txt", "content": strings.Repeat("字", 800)})
		call = next()
		if err := a.Store.AppendRow(sid, &StoredMessage{
			Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "c-" + label, Type: "function", Function: ToolCallFunc{Name: "write", Arguments: string(args)},
			}}}, Thinking: label + "的思考", Kind: kindChat, CreatedAt: "2026-01-02 10:00:01",
		}); err != nil {
			t.Fatal(err)
		}
		tool = next()
		if err := a.Store.AppendRow(sid, &StoredMessage{
			Message: Message{Role: "tool", Content: label + "写入成功1024字节", ToolCallID: "c-" + label},
			Kind:    kindChat, CreatedAt: "2026-01-02 10:00:02",
		}); err != nil {
			t.Fatal(err)
		}
	}
	final = next()
	if err := a.Store.AppendRow(sid, &StoredMessage{
		Message: TextMessage("assistant", label+"的结论"), Thinking: label + "的结论思考",
		Kind: kindChat, CreatedAt: "2026-01-02 10:00:03",
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func rowBySeq(t *testing.T, a *App, sid string, seq int64) *StoredMessage {
	t.Helper()
	all, err := a.Store.RawMessagesAll(sid)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	for _, m := range all {
		if m.Seq == seq {
			return m
		}
	}
	t.Fatalf("第 %d 行不存在", seq)
	return nil
}

// 折叠语义：用户输入与正文保留原文；调用行=正文+调用名+截断参数（无结果段）；
// 工具结果与环境快照整行 omit。
func TestRuleCompactFoldSemantics(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	user, call, tool, final := seedRound(t, a, "s1", "一轮", true)
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: TextMessage("system", "- 环境快照内容行"), Kind: kindEnv, CreatedAt: "2026-01-02 10:00:04",
	}); err != nil {
		t.Fatal(err)
	}
	// 第二轮（让第一轮可折叠）
	seedRound(t, a, "s1", "二轮", false)

	n, err := a.ruleCompactTo(sess, final)
	if err != nil || n != 1 {
		t.Fatalf("ruleCompactTo 想折 1 轮, got n=%d err=%v", n, err)
	}
	if got := rowBySeq(t, a, "s1", user).Compact; got != "一轮的问题" {
		t.Fatalf("用户输入折叠视图应=原文, got %q", got)
	}
	cmpt := rowBySeq(t, a, "s1", call).Compact
	if !strings.Contains(cmpt, "[调用 write 参数=") {
		t.Fatalf("调用行折叠应含调用名+参数: %q", cmpt)
	}
	if strings.Contains(cmpt, "结果=") || strings.Contains(cmpt, "写入成功") {
		t.Fatalf("调用行折叠不应保留结果: %q", cmpt)
	}
	if got := rowBySeq(t, a, "s1", tool).Compact; got != compactOmit {
		t.Fatalf("工具结果行应 omit, got %q", got)
	}
	if got := rowBySeq(t, a, "s1", final).Compact; got != "一轮的结论" {
		t.Fatalf("助手正文折叠视图应=原文, got %q", got)
	}
	envSeq := int64(5)
	if got := rowBySeq(t, a, "s1", envSeq).Compact; got != compactOmit {
		t.Fatalf("环境快照行应 omit, got %q", got)
	}
}

// 组装视图：折叠区的工具结果与环境快照不进上下文，用户输入/正文/调用名在，
// 思考不回传；折叠的用户行时间提示词照常派生。
func TestBuildContextAfterFold(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	_, call, tool, final := seedRound(t, a, "s1", "一轮", true)
	seedRound(t, a, "s1", "二轮", false)

	if _, err := a.ruleCompactTo(sess, final); err != nil {
		t.Fatal(err)
	}
	_ = call
	_ = tool
	msgs := a.buildContext(sess, "SYS")
	joined := ""
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			joined += s + "\n"
		}
	}
	for _, want := range []string{"一轮的问题", "[调用 write 参数=", "一轮的结论", "二轮的问题", "二轮的结论"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("折叠后视图缺 %q:\n%s", want, joined)
		}
	}
	for _, ban := range []string{"写入成功1024字节", "环境快照内容行", "的思考"} {
		if strings.Contains(joined, ban) {
			t.Fatalf("折叠后视图不应再含 %q:\n%s", ban, joined)
		}
	}
	// 时间提示词：两条用户消息各一条（折叠的用户行也照常派生）
	if n := strings.Count(joined, "（发送时间："); n != 2 {
		t.Fatalf("时间提示词应 2 条, got %d:\n%s", n, joined)
	}
	if msgs[0].Role != "system" || msgs[0].Content.(string) != "SYS" {
		t.Fatalf("首条应是系统提示词, got %+v", msgs[0])
	}
}

// 手动按钮的范围与幂等：upTo 所在轮（含）及更早折叠，之后不动；重复点报错；
// 自动入口按保留窗口折叠。
func TestRuleCompactScopeAndIdempotent(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	_, _, _, final1 := seedRound(t, a, "s1", "一轮", false)
	_, _, _, final2 := seedRound(t, a, "s1", "二轮", false)
	_, _, _, final3 := seedRound(t, a, "s1", "三轮", false)

	// 按钮在第二轮结论上：折一、二轮，第三轮不动
	n, err := a.ruleCompactTo(sess, final2)
	if err != nil || n != 2 {
		t.Fatalf("upTo=二轮结论 应折 2 轮, got n=%d err=%v", n, err)
	}
	if got := rowBySeq(t, a, "s1", final3).Compact; got != "" {
		t.Fatalf("第三轮不应被折叠, got compact=%q", got)
	}
	if got := rowBySeq(t, a, "s1", final1).Compact; got == "" {
		t.Fatal("第一轮应已折叠")
	}
	// 同一位置重复点：报已折叠
	if _, err := a.ruleCompactTo(sess, final2); err == nil || !strings.Contains(err.Error(), "已折叠") {
		t.Fatalf("重复压缩应报已折叠, got err=%v", err)
	}
	// 再点第三轮：折第三轮
	if n, err := a.ruleCompactTo(sess, final3); err != nil || n != 1 {
		t.Fatalf("upTo=三轮 应折 1 轮, got n=%d err=%v", n, err)
	}
	// 自动入口（保留 2 轮）此刻没有可折的
	if n := a.ruleCompactAuto(sess, 2); n != 0 {
		t.Fatalf("全部折完后 auto 应 0, got %d", n)
	}
}

// 自动阈值触发（rule 模式零模型调用）与溢出兜底只留最近一轮。
// 保留窗口走字数预算：每个带工具的种子轮约 870 字，预算 2000 → 恰好保 2 轮。
func TestAutoCompactThresholdAndOverflow(t *testing.T) {
	a := newCompactTestApp(t)
	a.Cfg.KeepChars = 2000
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	seedRound(t, a, "s1", "一轮", true)
	seedRound(t, a, "s1", "二轮", true)
	_, _, _, final3 := seedRound(t, a, "s1", "三轮", true)
	seedRound(t, a, "s1", "四轮", true)

	// 估算超阈值（10000 字 ≈ 7500 token ≥ 5000×0.8）→ 触发规则折叠
	cp, err := a.maybeAutoCompact(sess, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 5000)
	if err != nil || !cp {
		t.Fatalf("阈值应触发 rule 折叠, got cp=%v err=%v", cp, err)
	}
	if got := rowBySeq(t, a, "s1", final3).Compact; got != "" {
		t.Fatalf("保留窗口（最近2轮）内不应折叠, got %q", got)
	}
	// 再次触发：没有可折的 → false
	cp, err = a.maybeAutoCompact(sess, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 5000)
	if err != nil || cp {
		t.Fatalf("无可折应返回 false, got cp=%v err=%v", cp, err)
	}
	// 溢出兜底：只留最近 1 轮 → 三轮也被折叠
	if err := a.overflowCompact(sess); err != nil {
		t.Fatalf("溢出兜底失败: %v", err)
	}
	if got := rowBySeq(t, a, "s1", final3).Compact; got == "" {
		t.Fatal("溢出兜底应折叠三轮（只留最近1轮）")
	}
}

// 三列搜索：正文、工具调用参数、思考过程都能命中，片段取自命中列。
func TestSearchMessagesThreeColumns(t *testing.T) {
	a := newCompactTestApp(t)
	if err := a.Store.Create(&SessionMeta{ID: "s1", Mode: "task", Title: "搜索源"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{Message: TextMessage("user", "关于部署脚本的讨论"), Kind: kindChat}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"cmd": "deploy --key=deploy_key_773"})
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunc{Name: "run_command", Arguments: string(args)}}}},
		Kind:    kindChat,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: TextMessage("assistant", "部署完成"), Thinking: "我在考虑uniqueThought配置优先级",
		Kind: kindChat,
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"deploy_key_773", "run_command"},
		{"uniqueThought", "配置优先级"},
		{"部署脚本", "讨论"},
	} {
		hits, err := a.Store.SearchMessages(tc.query, 5, 0)
		if err != nil {
			t.Fatalf("搜 %s 失败: %v", tc.query, err)
		}
		if len(hits) != 1 {
			t.Fatalf("搜 %s 应 1 命中, got %d", tc.query, len(hits))
		}
		if !strings.Contains(hits[0].Snippet, tc.want) {
			t.Fatalf("搜 %s 的片段应含 %q, got %q", tc.query, tc.want, hits[0].Snippet)
		}
	}
	// 上下文窗口参数放大片段半径
	hits, _ := a.Store.SearchMessages("uniqueThought", 5, 2000)
	if len(hits) != 1 || len([]rune(hits[0].Snippet)) < 20 {
		t.Fatalf("大半径片段应更长, got %q", hits[0].Snippet)
	}
}

// 正向窗口读取：from_seq 起从旧到新、思考过程可见、字数预算用尽即停并给续读起点。
func TestReadSessionMessagesForwardBudget(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{Message: TextMessage("user", "第一问"), Kind: kindChat}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: TextMessage("assistant", "答A"), Thinking: "思考A", Kind: kindChat,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{Message: TextMessage("user", strings.Repeat("巨", 900)), Kind: kindChat}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{Message: TextMessage("assistant", "最后的结论"), Kind: kindChat}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{Message: TextMessage("system", "- 环境行不该出现"), Kind: kindEnv}); err != nil {
		t.Fatal(err)
	}

	tk := &toolContext{app: a, sess: sess}
	// 预算充足：全部对话行可见（含思考），环境快照被滤掉
	out, err := runReadSessionMessages(tk, json.RawMessage(`{"session_id":"s1","from_seq":1,"chars":6000}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"【思考】思考A", "第一问", "最后的结论"} {
		if !strings.Contains(out, want) {
			t.Fatalf("正向读取缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "环境行") {
		t.Fatalf("环境快照不应出现在历史读取:\n%s", out)
	}
	// 预算收紧：读到巨型消息前停止并给出续读起点
	out, err = runReadSessionMessages(tk, json.RawMessage(`{"session_id":"s1","from_seq":1,"chars":100}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "最后的结论") {
		t.Fatalf("小预算不应读到末尾:\n%s", out)
	}
	if !strings.Contains(out, "继续读用 from_seq=") {
		t.Fatalf("预算截断应给续读起点:\n%s", out)
	}
}

// 分支继承：从已折叠的会话 fork，压缩标记与思考过程必须随分支复制过去——
// 否则分支的上下文视图会瞬间膨胀回未压缩状态（E2E 实测抓出的存量 bug）。
func TestForkPreservesCompactAndThinking(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	_, _, _, final := seedRound(t, a, "s1", "一轮", true)
	seedRound(t, a, "s1", "二轮", false)
	if _, err := a.ruleCompactTo(sess, final); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/sessions/s1/fork", strings.NewReader(
		fmt.Sprintf(`{"upTo":%d}`, final)))
	r.SetPathValue("id", "s1")
	a.handleFork(w, r)
	if w.Code != 200 {
		t.Fatalf("fork 失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.ID == "" {
		t.Fatalf("fork 响应异常: %s", w.Body.String())
	}
	fk, err := a.Store.RawMessagesAll(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	sawOmit, sawFold, sawThinking := false, false, false
	for _, m := range fk {
		if m.Role == "tool" && m.Compact == compactOmit {
			sawOmit = true
		}
		if strings.Contains(m.Compact, "[调用 write 参数=") {
			sawFold = true
		}
		if m.Role == "assistant" && m.Thinking != "" {
			sawThinking = true
		}
	}
	if !sawOmit || !sawFold || !sawThinking {
		t.Fatalf("分支继承不完整: omit=%v fold=%v thinking=%v", sawOmit, sawFold, sawThinking)
	}
}

// 字数预算保留算法：从最新一轮往前累计模型可见字数，装得下的整轮保留；
// 命中点在轮中间时扩到整轮；最近一轮独大时只保它（至少 1 轮）。
func TestKeepRoundsByBudget(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	// 三个小轮（无工具）：每轮 = 用户输入(4字) + 结论(4字) + 思考(6字) = 14 字
	seedRound(t, a, "s1", "甲", false)
	seedRound(t, a, "s1", "乙", false)
	seedRound(t, a, "s1", "丙", false)
	raw, err := a.Store.RawMessagesAll("s1")
	if err != nil {
		t.Fatal(err)
	}
	// 预算 30：丙(14) + 乙(14)=28 装得下，再装甲=42 超 → 保 2 轮
	a.Cfg.KeepChars = 30
	if got := a.keepRoundsByBudget(raw); got != 2 {
		t.Fatalf("预算 30 应保 2 轮, got %d", got)
	}
	// 预算大到装下全部
	a.Cfg.KeepChars = 1000
	if got := a.keepRoundsByBudget(raw); got != 3 {
		t.Fatalf("预算 1000 应保 3 轮, got %d", got)
	}
	// 巨型最后一轮：预算再大也只保 1 轮（轮是原子单位，更早的全折）
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: TextMessage("user", "巨轮问题"), Kind: kindChat,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: TextMessage("assistant", strings.Repeat("巨", 50000)), Kind: kindChat,
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ = a.Store.RawMessagesAll("s1")
	a.Cfg.KeepChars = 10000
	if got := a.keepRoundsByBudget(raw); got != 1 {
		t.Fatalf("巨轮独大应只保 1 轮, got %d", got)
	}
	// 端到端：阈值触发后按预算折叠，保留的最近一轮（巨轮，第7-8行）原文完整
	cp, err := a.maybeAutoCompact(sess, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 5000)
	if err != nil || !cp {
		t.Fatalf("阈值应触发折叠, cp=%v err=%v", cp, err)
	}
	if got := rowBySeq(t, a, "s1", 7).Compact; got != "" {
		t.Fatalf("保留的巨轮用户行不应折叠, got %q", got)
	}
	if got := rowBySeq(t, a, "s1", 8).Compact; got != "" {
		t.Fatalf("保留的巨轮结论行不应折叠, got %q", got)
	}
	if got := rowBySeq(t, a, "s1", 1).Compact; got == "" {
		t.Fatal("预算外的旧轮应已折叠")
	}
}

// 二级压缩与边界提示标签：已折叠过的轮再次被更大的压缩范围覆盖时，升级为
// 只留用户输入与模型最终输出；组装时在折叠边界注入提示（告知模型以上已压缩、
// 细节可查历史原文），全折时提示缀尾。
func TestSecondLevelFoldAndBoundaryNote(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	user1, call1, _, final1 := seedRound(t, a, "s1", "一轮", true)
	_, _, _, final2 := seedRound(t, a, "s1", "二轮", true)
	_, _, _, final3 := seedRound(t, a, "s1", "三轮", false)

	// 一级：压到二轮末（一、二轮首次折叠，调用行=正文+[调用 …]）
	if n, err := a.ruleCompactTo(sess, final2); err != nil || n != 2 {
		t.Fatalf("一级应折 2 轮, n=%d err=%v", n, err)
	}
	if got := rowBySeq(t, a, "s1", call1).Compact; !strings.Contains(got, "[调用 write") {
		t.Fatalf("一级后调用行应保留调用名: %q", got)
	}
	// 组装：折叠边界注入提示，位于折叠内容之后、三轮原文之前
	msgs := a.buildContext(sess, "SYS")
	joined := ""
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			joined += s + "\n"
		}
	}
	if n := strings.Count(joined, "以上为压缩后的历史"); n != 1 {
		t.Fatalf("折叠边界提示应恰好 1 条, got %d:\n%s", n, joined)
	}
	if !strings.Contains(joined, "search_sessions") || !strings.Contains(joined, "read_session_messages") {
		t.Fatalf("提示应告知查询工具:\n%s", joined)
	}
	if strings.Index(joined, "以上为压缩后的历史") > strings.Index(joined, "三轮的问题") {
		t.Fatalf("提示应在三轮原文之前:\n%s", joined)
	}

	// 二级：压到三轮末 → 一、二轮升级为二级（只留用户输入与最终输出）
	if n, err := a.ruleCompactTo(sess, final3); err != nil || n != 1 {
		t.Fatalf("二级应再折 1 轮, n=%d err=%v", n, err)
	}
	if got := rowBySeq(t, a, "s1", call1).Compact; got != compactOmit {
		t.Fatalf("二级后调用行应 omit, got %q", got)
	}
	if got := rowBySeq(t, a, "s1", user1).Compact; got != "一轮的问题" {
		t.Fatalf("二级后用户输入应保留原文, got %q", got)
	}
	if got := rowBySeq(t, a, "s1", final1).Compact; got != "一轮的结论" {
		t.Fatalf("二级后最终输出应保留原文, got %q", got)
	}
	// 全折（三轮也折了）：提示缀在尾部，仍恰好 1 条
	msgs = a.buildContext(sess, "SYS")
	joined = ""
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			joined += s + "\n"
		}
	}
	if n := strings.Count(joined, "以上为压缩后的历史"); n != 1 {
		t.Fatalf("全折时提示应缀尾 1 条, got %d:\n%s", n, joined)
	}
	if strings.Index(joined, "三轮的结论") >= strings.Index(joined, "以上为压缩后的历史") {
		t.Fatalf("全折时提示应在最后:\n%s", joined)
	}
}
