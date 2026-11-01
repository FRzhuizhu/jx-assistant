package main

// 规则压缩重构的测试：折叠语义（丢结果/丢思考/丢环境快照、保留用户输入与
// 正文、调用留名+参数）、手动按钮按 upTo 划范围、自动阈值触发的保留窗口、
// 溢出兜底、以及历史查询工具的三列搜索与正向窗口预算。

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCompactTestApp 建一个带临时库的 App（测试用完即删，只落本目录）。
// DataDir 一并指向临时目录：DefaultConfig 的 "data" 是相对路径，不覆盖的话
// 经 DataDir 的东西（如远程工作区登记库）会读写仓库真实 data/ 目录。
func newCompactTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg := DefaultConfig()
	cfg.DataDir = dir
	return &App{Cfg: cfg, Store: s}
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

	// 按钮在第二轮结论上：折一、二轮（新轮，一级），第三轮不动
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
	// 同一位置再点：三轮种子没有工具调用行，一级视图已是最简（用户输入+
	// 最终输出），二级无可再压 → 报已折叠（带工具的轮才有二级可升）
	if _, err := a.ruleCompactTo(sess, final2); err == nil || !strings.Contains(err.Error(), "已折叠") {
		t.Fatalf("最简后重复点应报已折叠, got err=%v", err)
	}
	// 再点第三轮：折第三轮（新轮走一级，不带动已折叠的旧轮）
	if n, err := a.ruleCompactTo(sess, final3); err != nil || n != 1 {
		t.Fatalf("upTo=三轮 应折 1 轮, got n=%d err=%v", n, err)
	}
	// 自动入口（保留 2 轮）此刻没有可折的
	if n := a.ruleCompactAuto(sess, 2); n != 0 {
		t.Fatalf("全部折完后 auto 应 0, got %d", n)
	}
}

// 自动阈值触发（rule 模式零模型调用）与溢出兜底只留最近一轮。
// 保留窗口走字数预算 + 按轮对齐：每个带工具的种子轮约 870 字，预算 2000 时
// 命中点落在二轮的调用行 → 二轮整轮保留，只折一轮（四轮、三轮全在窗口内）。
func TestAutoCompactThresholdAndOverflow(t *testing.T) {
	a := newCompactTestApp(t)
	a.Cfg.KeepChars = 2000
	a.Cfg.CompactLlmPct = 0 // 专测规则路径：模型阈值关闭（默认 80% 会抢先把请求引去摘要）
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	_, _, _, final1 := seedRound(t, a, "s1", "一轮", true)
	r2user, r2call, r2tool, _ := seedRound(t, a, "s1", "二轮", true)
	_, _, _, final3 := seedRound(t, a, "s1", "三轮", true)
	seedRound(t, a, "s1", "四轮", true)

	// 估算超阈值（10000 字 ≈ 7500 token ≥ 5000×0.6）→ 触发规则折叠
	cp, err := a.maybeAutoCompact(sess, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 5000, false)
	if err != nil || !cp {
		t.Fatalf("阈值应触发 rule 折叠, got cp=%v err=%v", cp, err)
	}
	if got := rowBySeq(t, a, "s1", final1).Compact; got == "" {
		t.Fatal("窗口外的一轮应已折叠")
	}
	// 命中轮（二轮）整轮保留：开轮的用户行、调用行、工具结果都原样在窗口里
	for _, seq := range []int64{r2user, r2call, r2tool} {
		if got := rowBySeq(t, a, "s1", seq).Compact; got != "" {
			t.Fatalf("命中轮应整轮保留, seq=%d got compact=%q", seq, got)
		}
	}
	if got := rowBySeq(t, a, "s1", final3).Compact; got != "" {
		t.Fatalf("保留窗口内不应折叠, got %q", got)
	}
	// 再次触发：同轮冷却中（冷却命中即静默跳过，没有可折的同样返回 false）
	cp, err = a.maybeAutoCompact(sess, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 5000, false)
	if err != nil || cp {
		t.Fatalf("同轮再触发应被冷却挡住, got cp=%v err=%v", cp, err)
	}
	// 溢出兜底：只留最近 1 轮 → 三轮也被折叠
	if err := a.overflowCompact(sess); err != nil {
		t.Fatalf("溢出兜底失败: %v", err)
	}
	if got := rowBySeq(t, a, "s1", final3).Compact; got == "" {
		t.Fatal("溢出兜底应折叠三轮（只留最近1轮）")
	}
}

// 自动压缩保留窗口按轮对齐的另一半：轮首可以是数字助理转发的消息（aiuser/
// report）——主对话里大部分回合由转发消息开启，若只认用户亲发的消息开轮，
// 几十个回合会被算成一个超长轮、窗口形同虚设；命中轮整轮保留的语义下，
// 边界落在转发消息上即可。
func TestAutoCompactRoundStartsAtForwardedMessage(t *testing.T) {
	a := newCompactTestApp(t)
	a.Cfg.KeepChars = 2000
	sess := &SessionMeta{ID: "s1", Mode: "assistant"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	// 每回合 = 开轮行 + 调用行(思考+800字参数) + 工具结果 + 结论，约 840 字
	round := func(label, openKind string) (open, call, tool, final int64) {
		next := func() int64 {
			all, err := a.Store.RawMessagesAll("s1")
			if err != nil {
				t.Fatalf("读历史失败: %v", err)
			}
			return int64(len(all) + 1)
		}
		args, _ := json.Marshal(map[string]any{"content": strings.Repeat("字", 800)})
		open = next()
		if err := a.Store.AppendRow("s1", &StoredMessage{
			Message: TextMessage("user", label+"的指令"), Kind: openKind, CreatedAt: "2026-01-02 10:00:00",
		}); err != nil {
			t.Fatal(err)
		}
		call = next()
		if err := a.Store.AppendRow("s1", &StoredMessage{
			Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "c-" + label, Type: "function", Function: ToolCallFunc{Name: "write", Arguments: string(args)},
			}}}, Thinking: label + "的思考", Kind: kindChat, CreatedAt: "2026-01-02 10:00:01",
		}); err != nil {
			t.Fatal(err)
		}
		tool = next()
		if err := a.Store.AppendRow("s1", &StoredMessage{
			Message: Message{Role: "tool", Content: label + "写入成功", ToolCallID: "c-" + label},
			Kind:    kindChat, CreatedAt: "2026-01-02 10:00:02",
		}); err != nil {
			t.Fatal(err)
		}
		final = next()
		if err := a.Store.AppendRow("s1", &StoredMessage{
			Message: TextMessage("assistant", label+"的结论"), Thinking: label + "的结论思考",
			Kind: kindChat, CreatedAt: "2026-01-02 10:00:03",
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	// 只有第一回合是真人用户消息开轮，其余都由数字助理的转发消息开启
	_, _, _, final1 := round("甲", kindChat)
	r2open, r2call, r2tool, r2final := round("乙", kindAiUser)
	r3open, _, _, _ := round("丙", kindReport)
	round("丁", kindAiUser)

	raw, err := a.Store.RawMessagesAll("s1")
	if err != nil {
		t.Fatal(err)
	}
	// 预算 2000：丁丙两轮(约1680)装得下，命中点落在乙轮的调用行 → 乙轮整轮
	// 保留，保留区起点 = 乙轮的 aiuser 开轮行（转发消息即可作边界）
	if got := a.keepStartSeq(raw); got != r2open {
		t.Fatalf("保留区起点应=乙轮的转发消息行(%d), got %d", r2open, got)
	}
	if n := a.ruleCompactAuto(sess, -1); n == 0 {
		t.Fatal("自动压缩应折掉甲轮")
	}
	if got := rowBySeq(t, a, "s1", final1).Compact; got == "" {
		t.Fatal("窗口外的甲轮应已折叠")
	}
	for _, seq := range []int64{r2open, r2call, r2tool, r2final} {
		if got := rowBySeq(t, a, "s1", seq).Compact; got != "" {
			t.Fatalf("命中轮(乙)应整轮保留, seq=%d got compact=%q", seq, got)
		}
	}
	if got := rowBySeq(t, a, "s1", r2open).Kind; got != kindAiUser {
		t.Fatalf("边界行应是 aiuser 转发消息, got kind=%q", got)
	}
	if got := rowBySeq(t, a, "s1", r3open).Kind; got != kindReport {
		t.Fatalf("丙轮开轮行应是 report, got kind=%q", got)
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

// 双阈值选路：估算 token 在两阈值之间走规则折叠（零模型调用）；达到模型阈值
// 优先生成摘要（mock 提供方，离线不发请求）；任一阈值为 0 即关闭该路。
func TestMaybeAutoCompactDualThreshold(t *testing.T) {
	a := newCompactTestApp(t)
	a.Cfg.Providers = []ProviderCfg{{
		ID: "mock", Name: "离线演示", BaseURL: "mock",
		Models: []ModelCfg{{ID: "mock-1", Name: "演示模型"}},
	}}
	a.Cfg.KeepChars = 1000 // 保留窗口收紧，压掉一轮之外的早期轮
	s1 := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(s1); err != nil {
		t.Fatal(err)
	}
	s2 := &SessionMeta{ID: "s2", Mode: "task"}
	if err := a.Store.Create(s2); err != nil {
		t.Fatal(err)
	}
	_, _, _, final1 := seedRound(t, a, "s1", "一轮", true)
	seedRound(t, a, "s1", "二轮", true)
	seedRound(t, a, "s1", "三轮", true)
	// 窗口 10000：规则线 6000、模型线 8000。估算 10000 字 ≈ 7500 token，两线之间
	// → 规则折叠：一轮折掉、不产生摘要行
	cp, err := a.maybeAutoCompact(s1, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 10000, false)
	if err != nil || !cp {
		t.Fatalf("两线之间应走规则折叠, got cp=%v err=%v", cp, err)
	}
	if got := rowBySeq(t, a, "s1", final1).Compact; got == "" {
		t.Fatal("规则路径应折掉一轮")
	}
	if raw, _ := a.Store.RawMessagesAll("s1"); countKind(raw, kindSummary) != 0 {
		t.Fatal("两线之间不该生成摘要")
	}
	// 估算 11000 字 ≈ 8250 token，两线都超 → **规则先折**（同一时刻只走一条通道）；
	// 再查一次（同轮冷却挡住规则通道、上下文估算未变仍超模型线）→ 摘要兜底
	for _, label := range []string{"甲", "乙", "丙"} {
		seedRound(t, a, "s2", label, true)
	}
	cp, err = a.maybeAutoCompact(s2, []Message{TextMessage("user", strings.Repeat("长", 11000))}, 10000, false)
	if err != nil || !cp {
		t.Fatalf("两线都超第一遍应走规则折叠, got cp=%v err=%v", cp, err)
	}
	if raw, _ := a.Store.RawMessagesAll("s2"); countKind(raw, kindSummary) != 0 {
		t.Fatal("第一遍只折叠，不该生成摘要")
	}
	cp, err = a.maybeAutoCompact(s2, []Message{TextMessage("user", strings.Repeat("长", 11000))}, 10000, false)
	if err != nil || !cp {
		t.Fatalf("第二遍（规则冷却中、仍超模型线）应走摘要, got cp=%v err=%v", cp, err)
	}
	raw, _ := a.Store.RawMessagesAll("s2")
	if countKind(raw, kindSummary) != 1 {
		t.Fatalf("应落一条自动摘要, got %d", countKind(raw, kindSummary))
	}
	// 任一阈值为 0 即关闭该路：规则关、模型开，两线之间不再压缩
	s3 := &SessionMeta{ID: "s3", Mode: "task"}
	if err := a.Store.Create(s3); err != nil {
		t.Fatal(err)
	}
	seedRound(t, a, "s3", "一轮", true)
	seedRound(t, a, "s3", "二轮", true)
	a.Cfg.CompactRulePct = 0
	cp, err = a.maybeAutoCompact(s3, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 10000, false)
	if err != nil || cp {
		t.Fatalf("规则阈值 0 且未达模型线应不压缩, got cp=%v err=%v", cp, err)
	}
	// 阈值反着配（规则 80 / 模型 60）：估算 65% 达模型线而未达规则线 → 直接走
	// 摘要，规则通道不参与
	s4 := &SessionMeta{ID: "s4", Mode: "task"}
	if err := a.Store.Create(s4); err != nil {
		t.Fatal(err)
	}
	seedRound(t, a, "s4", "一轮", true)
	seedRound(t, a, "s4", "二轮", true)
	seedRound(t, a, "s4", "三轮", true)
	a.Cfg.CompactRulePct = 80
	a.Cfg.CompactLlmPct = 60
	cp, err = a.maybeAutoCompact(s4, []Message{TextMessage("user", strings.Repeat("长", 8700))}, 10000, false)
	if err != nil || !cp {
		t.Fatalf("阈值反配时达模型线应走摘要, got cp=%v err=%v", cp, err)
	}
	raw, _ = a.Store.RawMessagesAll("s4")
	if countKind(raw, kindSummary) != 1 {
		t.Fatalf("应落一条自动摘要, got %d", countKind(raw, kindSummary))
	}
	for _, m := range raw {
		if m.Compact != "" {
			t.Fatalf("未达规则线不该动折叠标记, seq=%d got %q", m.Seq, m.Compact)
		}
	}
}

// countKind 数历史里某 kind 的行数（测试断言用）。
func countKind(msgs []*StoredMessage, kind string) int {
	n := 0
	for _, m := range msgs {
		if m.Kind == kind {
			n++
		}
	}
	return n
}

// 规则压缩按轮冷却：轮中一次尝试后同轮冷却（审计行每轮最多一条）、轮末检查
// 固定放行、新轮恢复；冷却 0 = 不冷却。
func TestRuleCompactCooldown(t *testing.T) {
	a := newCompactTestApp(t) // DefaultConfig：冷却 1 轮
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	seedRound(t, a, "s1", "一轮", false)
	if !a.ruleCompactTryEnter(sess) {
		t.Fatal("首次尝试应放行")
	}
	if a.ruleCompactTryEnter(sess) {
		t.Fatal("同轮第二次应冷却")
	}
	if a.ruleCompactTryEnter(sess) {
		t.Fatal("同轮第三次仍应冷却")
	}
	raw, _ := a.Store.RawMessagesAll("s1")
	n := 0
	for _, m := range raw {
		if m.Kind == kindAudit && strings.Contains(m.Text(), "compact_cooldown") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("冷却审计行应每轮最多一条, got %d", n)
	}
	// 轮末固定放行（并以此为下一轮的冷却基准）
	a.ruleCompactRoundEndEnter(sess)
	// 新一轮恢复
	seedRound(t, a, "s1", "二轮", false)
	if !a.ruleCompactTryEnter(sess) {
		t.Fatal("新轮应放行")
	}
	// 冷却 0 = 不冷却：同轮连续放行
	a.Cfg.CompactCooldown = 0
	if !a.ruleCompactTryEnter(sess) || !a.ruleCompactTryEnter(sess) {
		t.Fatal("冷却 0 应每步放行")
	}
}

// 双阈值迁移：老配置（单模式+单阈值）读入时补默认 规则60%/模型80%/冷却1轮，
// 旧 off 保持自动压缩全关并把模式归一为 rule（手动按钮的默认行为总要有一个）；
// 新键齐全的配置原样保留（含显式 0）。旧字段 compactMode/compactThreshold
// 原样保留，供旧版 exe 回滚。
func TestConfigCompactDualThresholdMigration(t *testing.T) {
	run := func(content string) *Config {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig 失败: %v", err)
		}
		return cfg
	}
	cfg := run(`{"compactMode":"llm","compactThreshold":0.9}`)
	if cfg.CompactRulePct != 60 || cfg.CompactLlmPct != 80 || cfg.CompactCooldown != 1 {
		t.Fatalf("老配置应迁移为 60/80/1, got %d/%d/%d", cfg.CompactRulePct, cfg.CompactLlmPct, cfg.CompactCooldown)
	}
	if cfg.CompactMode != "llm" || cfg.CompactThreshold != 0.9 {
		t.Fatalf("旧字段应原样保留, got mode=%q threshold=%v", cfg.CompactMode, cfg.CompactThreshold)
	}
	cfg = run(`{"compactMode":"off","compactThreshold":0.8}`)
	if cfg.CompactRulePct != 0 || cfg.CompactLlmPct != 0 {
		t.Fatalf("旧 off 应迁移为自动压缩全关, got %d/%d", cfg.CompactRulePct, cfg.CompactLlmPct)
	}
	if cfg.CompactMode != "rule" {
		t.Fatalf("off 应归一为 rule, got %q", cfg.CompactMode)
	}
	cfg = run(`{"compactMode":"rule","compactRulePct":50,"compactLlmPct":0,"compactCooldown":2}`)
	if cfg.CompactRulePct != 50 || cfg.CompactLlmPct != 0 || cfg.CompactCooldown != 2 {
		t.Fatalf("显式新键应原样保留, got %d/%d/%d", cfg.CompactRulePct, cfg.CompactLlmPct, cfg.CompactCooldown)
	}
}

// 设置页两个压缩阈值「改不动、保存不生效」（2026-11-01 实测）：GET /api/config
// 的返回体缺 compactRulePct/compactLlmPct/compactCooldown，前端回显永远落默认
// 60/80/1，之后任意一次保存又把默认值写回——用户改的值活不过一次开关设置页。
// 锁定：POST 保存（响应即 GET 同一视图）后三个键必须原样回传，且落盘重载不失真。
func TestConfigAPICompactKeysRoundTrip(t *testing.T) {
	a := newCompactTestApp(t)
	a.CfgPath = filepath.Join(t.TempDir(), "config.json")
	// 设置页保存体：阈值 90 / 留空视作 0（不启用）由前端 clampPct 完成，这里直发 0
	body := strings.NewReader(`{"compact":{"mode":"llm","rulePct":90,"llmPct":0,"cooldown":3}}`)
	w := httptest.NewRecorder()
	a.handleSetConfig(w, httptest.NewRequest("POST", "/api/config", body))
	if w.Code != 200 {
		t.Fatalf("保存应成功, got %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if got["compactRulePct"] != float64(90) || got["compactLlmPct"] != float64(0) || got["compactCooldown"] != float64(3) {
		t.Fatalf("压缩三键应原样回传, got rule=%v llm=%v cooldown=%v",
			got["compactRulePct"], got["compactLlmPct"], got["compactCooldown"])
	}
	if got["compactMode"] != "llm" {
		t.Fatalf("compactMode 应为 llm, got %v", got["compactMode"])
	}
	// 回显口径要活过重启：落盘文件重载后三个值原样
	cfg2, err := LoadConfig(a.CfgPath)
	if err != nil {
		t.Fatalf("重载配置失败: %v", err)
	}
	if cfg2.CompactRulePct != 90 || cfg2.CompactLlmPct != 0 || cfg2.CompactCooldown != 3 {
		t.Fatalf("落盘值应保持 90/0/3, got %d/%d/%d", cfg2.CompactRulePct, cfg2.CompactLlmPct, cfg2.CompactCooldown)
	}
}

// 字数预算保留算法：从最新一条往前累计模型可见字数，预算耗尽处（命中点）所在
// 的轮整轮保留——命中轮不超预算时保留区起点对齐到轮首；巨轮自身超预算时回退
// 行级窗口，保最近一条原文（保留窗口不能为空）。
func TestKeepStartSeq(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	// 三个小轮（无工具）：每轮 = 用户输入(4字) + 结论(4字+6字思考) = 14 字，两行
	//（user 4 字 + assistant 10 字）
	seedRound(t, a, "s1", "甲", false)
	seedRound(t, a, "s1", "乙", false)
	seedRound(t, a, "s1", "丙", false)
	raw, err := a.Store.RawMessagesAll("s1")
	if err != nil {
		t.Fatal(err)
	}
	// 预算 30：丙乙两轮(28)装得下，命中点落在甲轮的结论行——甲轮(14字)自身
	// 不超预算，整轮保留 → 保留区从第 1 条起（宁可略超预算，也不留半轮）
	a.Cfg.KeepChars = 30
	if got := a.keepStartSeq(raw); got != 1 {
		t.Fatalf("预算 30 的保留区应整轮保到第 1 条, got %d", got)
	}
	// 预算大到装下全部 → 从第一条起
	a.Cfg.KeepChars = 1000
	if got := a.keepStartSeq(raw); got != 1 {
		t.Fatalf("预算 1000 的保留区应从第 1 条起, got %d", got)
	}
	// 巨型最后一轮：预算装不下它 → 只保留它最后一条（不按轮，也不留空窗口）。
	// 这是「百万级单轮压不动」的根因修复点：按轮硬保整轮会让超长上下文压不动。
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
	a.Cfg.CompactLlmPct = 0 // 专测规则路径
	if got := a.keepStartSeq(raw); got != 8 {
		t.Fatalf("巨轮独大时保留区应只剩最后一条(8), got %d", got)
	}
	// 端到端：阈值触发后按预算行级折叠——巨轮自身超预算，整轮前面的行都要折，
	// 否则上下文压不动（回归：此前报「没有可折叠的回合」但上下文仍超长）。
	cp, err := a.maybeAutoCompact(sess, []Message{TextMessage("user", strings.Repeat("长", 10000))}, 5000, false)
	if err != nil || !cp {
		t.Fatalf("阈值应触发折叠, cp=%v err=%v", cp, err)
	}
	if got := rowBySeq(t, a, "s1", 7).Compact; got == "" {
		t.Fatalf("保留区之前的巨轮用户行应被折叠, got %q", got)
	}
	if got := rowBySeq(t, a, "s1", 8).Compact; got != "" {
		t.Fatalf("保留区内的巨轮结论行应原样保留, got %q", got)
	}
	if got := rowBySeq(t, a, "s1", 1).Compact; got == "" {
		t.Fatal("预算外的旧轮应已折叠")
	}
}

// 回归：上下文超长（最近一轮自身远超保留预算）时，手动压缩必须产生实质削减。
// 之前按轮硬保 1 轮，百万级单轮永远压不动——报「没有可折叠的回合」但上下文仍满
//（ERP 会话实测：单轮 130 万字 ≈ 99 万 token）。修复后空 body 手动压缩（走自动
// 预算的行级保留窗口）应把巨轮前面的行也折叠，工具结果整行 omit。
func TestManualCompactHugeRoundReducesContext(t *testing.T) {
	a := newCompactTestApp(t)
	a.Cfg.KeepChars = 10000
	sess := &SessionMeta{ID: "s1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	// 一个普通带工具的小轮（折叠后显著缩减）
	seedRound(t, a, "s1", "旧轮", true)
	// 一个巨型轮：用户输入 + 带调用的助手行 + 巨大工具结果（折叠时整行 omit）
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: TextMessage("user", "巨轮问题"), Kind: kindChat, CreatedAt: "2026-01-02 10:10:00",
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "gc", Type: "function", Function: ToolCallFunc{Name: "run_command", Arguments: `{"command":"cat huge.log"}`}}}},
		Kind:    kindChat, CreatedAt: "2026-01-02 10:10:01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: Message{Role: "tool", Content: strings.Repeat("巨", 200000), ToolCallID: "gc"},
		Kind:    kindChat, CreatedAt: "2026-01-02 10:10:02",
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := a.Store.RawMessagesAll("s1")
	giantFinal := int64(len(all) + 1)
	if err := a.Store.AppendRow("s1", &StoredMessage{
		Message: TextMessage("assistant", "巨轮结论"), Kind: kindChat, CreatedAt: "2026-01-02 10:10:03",
	}); err != nil {
		t.Fatal(err)
	}

	totalVisible := func() int {
		raw, _ := a.Store.RawMessagesAll("s1")
		n := 0
		for _, m := range raw {
			n += visibleSize(m)
		}
		return n
	}
	before := totalVisible()

	// 手动压缩（空 body 与「压缩」按钮空请求同路径，走自动预算保留窗口）
	if n := a.ruleCompactAuto(sess, -1); n == 0 {
		t.Fatal("超长上下文手动压缩应折叠轮次，而不是报没有可折叠的回合")
	}
	after := totalVisible()
	if after >= before/10 {
		t.Fatalf("手动压缩应显著削减上下文: before=%d after=%d", before, after)
	}
	if got := rowBySeq(t, a, "s1", giantFinal).Compact; got != "" {
		t.Fatalf("保留区内的巨轮结论行应原样保留, got %q", got)
	}
	// 巨大工具结果被 omit（折叠的核心收益）。再压一次：范围内已无新轮
	//（巨轮前半已行级折叠、整轮不重折），旧轮按新语义升级二级；第三次点
	// 才幂等无变化报已折叠
	if n, err := a.ruleCompactTo(sess, giantFinal); err != nil || n != 1 {
		t.Fatalf("无新轮时应升级旧轮二级, n=%d err=%v", n, err)
	}
	if _, err := a.ruleCompactTo(sess, giantFinal); err == nil || !strings.Contains(err.Error(), "已折叠") {
		t.Fatalf("二级后再压应报已折叠, got err=%v", err)
	}
}

// 模式切换不影响历史压缩的应用：压缩发生了就是发生了——规则折叠的历史在
// llm（AI 摘要）模式下仍按折叠视图组装，摘要历史在 rule 模式下仍照常注入。
// 设置里的压缩模式只决定未来的压缩行为。回归测试：此前组装与 Cfg.CompactMode
// 耦合，切到摘要模式后历史折叠标记被无视、按原始消息整包重组，主对话上下文
// 从 20 多万 token 飙回 70 多万（压缩等于失效）。
func TestCompactHistoryIndependentOfMode(t *testing.T) {
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

	join := func(mode string) string {
		a.Cfg.CompactMode = mode
		var b strings.Builder
		for _, m := range a.buildContext(sess, "SYS") {
			if s, ok := m.Content.(string); ok {
				b.WriteString(s + "\n")
			}
		}
		return b.String()
	}

	// 规则压缩历史 + 摘要模式：折叠视图照常生效（工具结果/思考不回流）
	llm := join("llm")
	for _, want := range []string{"一轮的问题", "[调用 write 参数=", "一轮的结论", "二轮的问题"} {
		if !strings.Contains(llm, want) {
			t.Fatalf("llm 模式下折叠历史视图缺 %q:\n%s", want, llm)
		}
	}
	for _, ban := range []string{"写入成功1024字节", "的思考"} {
		if strings.Contains(llm, ban) {
			t.Fatalf("llm 模式下折叠历史不应回流 %q:\n%s", ban, llm)
		}
	}
	if n := strings.Count(llm, "以上为压缩后的历史"); n != 1 {
		t.Fatalf("llm 模式下折叠边界提示应 1 条, got %d:\n%s", n, llm)
	}
	if strings.Index(llm, "以上为压缩后的历史") > strings.Index(llm, "二轮的问题") {
		t.Fatalf("llm 模式下边界提示应在未折叠原文之前:\n%s", llm)
	}
	// 上下文面板口径：同一段历史在两种模式下 token 总量必须一致
	join("rule")
	ruleTotal := a.contextStats(sess).Total
	join("llm")
	llmTotal := a.contextStats(sess).Total
	if ruleTotal != llmTotal {
		t.Fatalf("切换模式不应改变历史组装口径: rule=%d llm=%d", ruleTotal, llmTotal)
	}

	// 对称场景：摘要历史 + 规则模式 → 摘要照常注入、边界内行不进上下文
	s2 := &SessionMeta{ID: "s2", Mode: "task"}
	if err := a.Store.Create(s2); err != nil {
		t.Fatal(err)
	}
	seedRound(t, a, "s2", "旧一轮", true)  // seq 1-4，被摘要覆盖
	seedRound(t, a, "s2", "旧二轮", false) // seq 5-6，被摘要覆盖
	if err := a.Store.AppendRow("s2", &StoredMessage{
		Message: TextMessage("system", "【手动历史摘要（覆盖至第 6 条）】先前对话的摘要正文。"),
		Kind:    kindSummary, Origin: "manual", CoversUpTo: 6, CreatedAt: "2026-01-02 11:00:00",
	}); err != nil {
		t.Fatal(err)
	}
	seedRound(t, a, "s2", "新一轮", false) // seq 8-9，边界后原文
	a.Cfg.CompactMode = "rule"
	var b strings.Builder
	for _, m := range a.buildContext(s2, "SYS") {
		if s, ok := m.Content.(string); ok {
			b.WriteString(s + "\n")
		}
	}
	rule := b.String()
	for _, want := range []string{"先前对话的摘要正文", "新一轮的问题", "新一轮的结论"} {
		if !strings.Contains(rule, want) {
			t.Fatalf("rule 模式下摘要历史视图缺 %q:\n%s", want, rule)
		}
	}
	for _, ban := range []string{"旧一轮的问题", "旧二轮", "写入成功1024字节"} {
		if strings.Contains(rule, ban) {
			t.Fatalf("rule 模式下已摘要覆盖的原文不应回流 %q:\n%s", ban, rule)
		}
	}
	if n := strings.Count(rule, "以上为压缩后的历史"); n != 0 {
		t.Fatalf("无折叠标记时不应注入折叠边界提示, got %d:\n%s", n, rule)
	}
}

// 二级压缩与边界提示标签：新轮优先一级折叠；同一范围内已没有新轮可折时再点，
// 才升级二级（只留用户输入与模型最终输出）；组装时在折叠边界注入提示（告知
// 模型以上已压缩、细节可查历史原文），全折时提示缀尾。
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

	// 新轮优先一级：压到三轮末 → 只折第三轮（新轮），一、二轮保持一级不跳级
	if n, err := a.ruleCompactTo(sess, final3); err != nil || n != 1 {
		t.Fatalf("有新轮应只折新轮(一级), n=%d err=%v", n, err)
	}
	if got := rowBySeq(t, a, "s1", call1).Compact; !strings.Contains(got, "[调用 write") {
		t.Fatalf("折新轮不应顺带升二级, 调用行: %q", got)
	}
	// 同一位置再点（范围内已无新轮）：升级二级（只留用户输入与最终输出）。
	// 一、二轮带工具调用有实质变化；三轮没有工具行，一级已是最简，升不了
	if n, err := a.ruleCompactTo(sess, final3); err != nil || n != 2 {
		t.Fatalf("无新轮时应升级二级 2 轮(三轮已最简), n=%d err=%v", n, err)
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
	// 再点一次：已二级幂等无变化，报已折叠
	if _, err := a.ruleCompactTo(sess, final3); err == nil || !strings.Contains(err.Error(), "已折叠") {
		t.Fatalf("已二级后重复点应报已折叠, got err=%v", err)
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
