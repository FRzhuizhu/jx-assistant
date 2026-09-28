package main

import (
	"strings"
	"testing"
	"time"
)

// 环境快照首行「用户最近发消息时间」自检（回归用；本文件在 .回收站/ 下——点开头，
// Go 工具链自动忽略，所以默认不参与编译。要跑就这么把它搬回去一次：
//
//	move .回收站\envclock_selfcheck_test.go . ; go test -count=1 -v -timeout 300s -run "TestEnvClock|TestEnvChange" . ; move envclock_selfcheck_test.go .回收站\
//
// 覆盖约定（见 agent.go 的 envPrompt 与 store.go 的 LastUserMessageAt）：
//  1. 这一行的值 = 最新一条真人用户消息的落库时刻；用户没发消息时，模型自己连续
//     执行多少步、跨过多少个分钟边界都不变，也不追加快照；
//  2. 用户真发了新消息才更新（出现新快照 + 一条只含这一行的「环境更新」）；
//  3. 工具结果、系统注入的 report 行落库都不改这一行；纯派发式子会话（没有用户
//     消息）干脆不出这一行；
//  4. 真实环境变化（这里是计划模式开关）仍照旧发一条只带变化行的「环境更新」。
//
// 注：TestEnvClockSurvivesRealMinuteRollover 真等挂钟翻页（最多 ~65 秒），
// 想跳过就 -run "TestEnvClockPinnedThroughModelSteps|TestEnvChange|TestEnvClockReanchors|TestEnvClockSkipsReportRows"。

func clockTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &Config{CompactMode: "rule", KeepRounds: 5, RuleInterval: 5, Workspace: "ws", DataDir: dir}
	a := &App{Store: store, Cfg: cfg}
	a.initRuntime()
	return a
}

func clockTestSession(t *testing.T, a *App, id string) *SessionMeta {
	t.Helper()
	sess := &SessionMeta{ID: id, Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	return sess
}

func envRows(t *testing.T, a *App, sid string) []string {
	t.Helper()
	raw, err := a.Store.RawMessagesAll(sid)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	out := []string{}
	for _, m := range raw {
		if m.Kind == "env" {
			out = append(out, m.Text())
		}
	}
	return out
}

// 1) 用户发过一条消息后，模型自己跑多少步、跨多少分钟边界，这一行都不变、不追加快照。
func TestEnvClockPinnedThroughModelSteps(t *testing.T) {
	a := clockTestApp(t)
	sess := clockTestSession(t, a, "env1")
	a.appendMessage(sess, TextMessage("user", "你好")) // 用户消息 → 取时
	want, ok := a.Store.LastUserMessageAt(sess.ID)
	if !ok {
		t.Fatal("应能取到用户消息时间")
	}

	first := a.ctxEngine().Assemble(sess, nil)
	if got := envRows(t, a, sess.ID); len(got) != 1 {
		t.Fatalf("首个快照应只落一条，现有 %d 条", len(got))
	}
	line := "- 用户最近发消息时间：" + want.Format("2006-01-02 15:04")
	if !strings.Contains(first.Env, line) {
		t.Fatalf("快照首行应为用户消息那一刻（%s）：%q", line, first.Env)
	}
	if strings.Contains(first.Env, "- 当前时间") {
		t.Fatalf("不应再出现「当前时间」这种挂钟项：%q", first.Env)
	}

	// 后续 6 步（工具调用之间）：文本逐字节不变、不追加快照、指纹不变
	for step := 2; step <= 7; step++ {
		asm := a.ctxEngine().Assemble(sess, nil)
		if asm.Env != first.Env {
			t.Fatalf("第 %d 步快照应逐字节不变：\n前=%q\n后=%q", step, first.Env, asm.Env)
		}
		if asm.EnvHash != first.EnvHash {
			t.Fatalf("第 %d 步环境指纹应不变", step)
		}
		if got := envRows(t, a, sess.ID); len(got) != 1 {
			t.Fatalf("第 %d 步不应追加快照，现有 %d 条", step, len(got))
		}
	}

	// 发给模型的尾部消息就是这份快照（首条整份呈现），且本轮不出现「环境更新」
	msgs := a.buildContext(sess, "SYS")
	if last := msgs[len(msgs)-1].Text(); !strings.Contains(last, line) {
		t.Fatalf("发给模型的尾部快照内容不对：%q", last)
	}
	if strings.Contains(strings.Join(msgTexts(msgs), "\n"), "# 环境更新") {
		t.Fatal("本轮不应出现「环境更新」消息")
	}
}

// 2) 真实跨过分钟边界：组装多次仍不出「环境更新」（旧逻辑正是在这里多发一条）。
func TestEnvClockSurvivesRealMinuteRollover(t *testing.T) {
	a := clockTestApp(t)
	sess := clockTestSession(t, a, "env2")
	a.appendMessage(sess, TextMessage("user", "你好"))

	first := a.ctxEngine().Assemble(sess, nil)
	before := envRows(t, a, sess.ID)

	wait := time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)) + 2*time.Second
	t.Logf("等 %.0f 秒跨过分钟边界后再组装…", wait.Seconds())
	time.Sleep(wait)

	for step := 2; step <= 3; step++ {
		asm := a.ctxEngine().Assemble(sess, nil)
		if asm.Env != first.Env {
			t.Fatalf("跨分钟边界后第 %d 步快照变了（旧逻辑在这里重发环境更新）：%q", step, asm.Env)
		}
	}
	if after := envRows(t, a, sess.ID); len(after) != len(before) {
		t.Fatalf("跨分钟边界后不应追加快照：前 %d 条 → 后 %d 条", len(before), len(after))
	}
	msgs := a.buildContext(sess, "SYS")
	if strings.Contains(strings.Join(msgTexts(msgs), "\n"), "# 环境更新") {
		t.Fatal("跨分钟边界不应出现「环境更新」消息")
	}
}

// 3) 真正变化仍发「环境更新」，且只带变化行（没变的用户消息时间行不重复报）。
func TestEnvChangeStillUpdatesButTimeLineDoesNot(t *testing.T) {
	a := clockTestApp(t)
	sess := clockTestSession(t, a, "env3")
	a.appendMessage(sess, TextMessage("user", "你好"))
	first := a.ctxEngine().Assemble(sess, nil)

	sess.PlanMode = true // 真实变化：进入计划模式（轮内权限提升/模式切换同属此类）
	asm := a.ctxEngine().Assemble(sess, nil)
	if got := envRows(t, a, sess.ID); len(got) != 2 {
		t.Fatalf("真实变化应追加一条快照，现有 %d 条", len(got))
	}
	if !strings.Contains(asm.Env, "计划模式") {
		t.Fatalf("新快照应含变化后的条目：%q", asm.Env)
	}
	msgs := a.buildContext(sess, "SYS")
	last := msgs[len(msgs)-1].Text()
	if !strings.Contains(last, "# 环境更新") || !strings.Contains(last, "计划模式") {
		t.Fatalf("变化应以「环境更新」消息出现在尾部：%q", last)
	}
	if strings.Contains(last, "用户最近发消息时间") || strings.Contains(last, "工作区目录") {
		t.Fatalf("更新消息不应重复没变的行（含用户消息时间）：%q", last)
	}
	if first.Env == asm.Env {
		t.Fatal("变化后快照文本应不同")
	}
}

// 4) 只有「用户真发消息」才更新：工具结果、系统注入的 report 行都不算；
//    纯派发式子会话（没有用户消息）不出这一行。
func TestEnvClockSkipsReportRowsAndAbsent(t *testing.T) {
	a := clockTestApp(t)
	sess := clockTestSession(t, a, "env4")
	// 纯派发式会话：第一条是系统代发的 report 行（如 dispatch / todo）
	a.appendRow(sess, StoredMessage{Message: TextMessage("user", "【派发】把日志整理一下"), Kind: kindReport, Origin: "dispatch", CreatedAt: now()})
	asm := a.ctxEngine().Assemble(sess, nil)
	if strings.Contains(asm.Env, "用户最近发消息时间") {
		t.Fatalf("还没有用户消息时不应出现该行：%q", asm.Env)
	}

	// 用户真发一条消息 → 出现该行，取的是那一刻
	a.appendMessage(sess, TextMessage("user", "开始吧"))
	if !strings.Contains(a.ctxEngine().Assemble(sess, nil).Env, "用户最近发消息时间：") {
		t.Fatal("用户发消息后应出现该行")
	}
	pin, _ := a.Store.LastUserMessageAt(sess.ID)

	// 工具结果、目标续跑 report 行、审计行落库都不该改这一行
	a.appendMessage(sess, Message{Role: "tool", Content: "结果", ToolCallID: "c1"})
	a.appendRow(sess, StoredMessage{Message: TextMessage("user", "<goal_round> 继续推进"), Kind: kindReport, Origin: "goal", CreatedAt: now()})
	a.auditWrite(sess, map[string]any{"type": "call", "step": 1})
	got, _ := a.Store.LastUserMessageAt(sess.ID)
	if !got.Equal(pin) {
		t.Fatalf("这些行都不该改「用户最近发消息时间」：%v ≠ %v", got, pin)
	}
	if n := len(envRows(t, a, sess.ID)); n != 2 {
		t.Fatalf("这些行落库后不应追加环境快照，现有 %d 条", n)
	}

	// 用户又发了一条（显式落一个 2 分钟后的时间，避免依赖真实等待）→ 这一行随之更新，
	// 出现新快照 + 一条只含该行的「环境更新」
	later := pin.Add(2 * time.Minute)
	a.appendRow(sess, StoredMessage{Message: TextMessage("user", "接着来"), Kind: kindChat, CreatedAt: later.Format("2006-01-02 15:04:05")})
	want, _ := a.Store.LastUserMessageAt(sess.ID)
	if !want.After(pin) {
		t.Fatalf("新用户消息时间应晚于旧值：%v → %v", pin, want)
	}
	asm2 := a.ctxEngine().Assemble(sess, nil)
	if got := envRows(t, a, sess.ID); len(got) != 3 {
		t.Fatalf("用户新发一条应追加一条快照，现有 %d 条", len(got))
	}
	if !strings.Contains(asm2.Env, "- 用户最近发消息时间："+want.Format("2006-01-02 15:04")) {
		t.Fatalf("快照应带新的用户消息时间：%q", asm2.Env)
	}
	msgs := a.buildContext(sess, "SYS")
	last := msgs[len(msgs)-1].Text()
	if !strings.Contains(last, "# 环境更新") || !strings.Contains(last, "用户最近发消息时间："+want.Format("2006-01-02 15:04")) {
		t.Fatalf("用户新消息的取时应以环境更新消息随消息注入：%q", last)
	}

	// 同一分钟内再发一条：值本身没变（分钟粒度）→ 不追加快照、不出「环境更新」
	a.appendRow(sess, StoredMessage{Message: TextMessage("user", "同一分钟内再说一句"), Kind: kindChat,
		CreatedAt: later.Add(20 * time.Second).Format("2006-01-02 15:04:05")})
	if got := envRows(t, a, sess.ID); len(got) != 3 {
		t.Fatalf("同一分钟内再发消息不该追加环境快照，现有 %d 条", len(got))
	}

	// 继承/分支复制过来的用户消息保留原 created_at（子会话沿用父会话的用户消息时间）
	child := clockTestSession(t, a, "env4-child")
	if _, err := a.copyInheritedContext(sess.ID, child.ID); err != nil {
		t.Fatalf("继承失败：%v", err)
	}
	newest, _ := a.Store.LastUserMessageAt(sess.ID)
	cgot, ok := a.Store.LastUserMessageAt(child.ID)
	if !ok || !cgot.Equal(newest) {
		t.Fatalf("子会话应沿用继承来的用户消息时间：%v（期望 %v）", cgot, newest)
	}
}

func msgTexts(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text()
	}
	return out
}
