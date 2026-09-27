package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRepairDanglingToolCalls 覆盖悬空工具调用的兜底补齐：部分落库（崩溃时
// 只落了前几个结果的典型形态）、完全悬空、事件下发、内容与 origin 标记，
// 以及幂等（已补齐的不重复补）。
func TestRepairDanglingToolCalls(t *testing.T) {
	a := newTestApp(t, "llm", 99, 99)
	sess := &SessionMeta{ID: "d1", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	_ = a.Store.Create(sess)

	_ = a.Store.Append("d1", TextMessage("user", "干活"))
	// t1 有响应、t2 悬空
	_ = a.Store.AppendRow("d1", &StoredMessage{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "t1", Type: "function", Function: ToolCallFunc{Name: "read"}},
		{ID: "t2", Type: "function", Function: ToolCallFunc{Name: "read"}},
	}}, Kind: "chat"})
	_ = a.Store.Append("d1", Message{Role: "tool", Content: "r1", ToolCallID: "t1"})
	// 另一条完全悬空的调用（无正文，最接近进程崩溃时的落库形态）
	_ = a.Store.AppendRow("d1", &StoredMessage{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "t3", Type: "function", Function: ToolCallFunc{Name: "run_command"}},
	}}, Kind: "chat"})

	var events []Event
	a.repairDanglingToolCalls(sess, func(ev Event) { events = append(events, ev) })

	raw, _ := a.Store.RawMessagesAll("d1")
	answers := map[string]*StoredMessage{}
	for _, m := range raw {
		if m.Role == "tool" && m.ToolCallID != "" {
			answers[m.ToolCallID] = m
		}
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		if answers[id] == nil {
			t.Fatalf("悬空调用 %s 未补齐", id)
		}
	}
	if got := answers["t1"].Text(); got != "r1" {
		t.Fatalf("已有响应不应被改动: %q", got)
	}
	for _, id := range []string{"t2", "t3"} {
		if got := answers[id].Text(); got != interruptNoteNotRun {
			t.Fatalf("%s 的合成结尾内容不对: %q", id, got)
		}
		if answers[id].Origin != "interrupt" || answers[id].Kind != kindChat {
			t.Fatalf("%s 的合成行形态不对: origin=%q kind=%q", id, answers[id].Origin, answers[id].Kind)
		}
	}
	// 每条补行各下发一条 message 事件（t2、t3 共两条）
	if len(events) != 2 {
		t.Fatalf("应下发 2 条 message 事件，得到 %d 条", len(events))
	}
	for _, ev := range events {
		if ev.Type != "message" || ev.Kind != kindChat || ev.Origin != "interrupt" || ev.Message == nil {
			t.Fatalf("事件形态不对: %+v", ev)
		}
	}

	// 幂等：第二次运行不新增任何行
	before := len(raw)
	a.repairDanglingToolCalls(sess, nil)
	after, _ := a.Store.RawMessagesAll("d1")
	if len(after) != before {
		t.Fatalf("幂等性破坏：第二次补了 %d 行", len(after)-before)
	}
}

// TestIsRetryableModelErr 覆盖自动重试的判定：瞬态网络类（连接失败/超时/
// 流中断/5xx/429）可重试；请求级错误（400/401/402/403/404）与上下文超限
// 不重试；nil 不重试。
func TestIsRetryableModelErr(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"无法连接模型服务：连接被拒绝，请确认 API 地址与端口可达", true},
		{"无法连接模型服务：请求超时，模型服务没有在预期时间内响应", true},
		{"无法连接模型服务：域名解析失败，请检查 API 地址（baseURL）是否正确", true},
		{"unexpected EOF", true},
		{"read tcp 127.0.0.1:1234->1.2.3.4:443: connection reset by peer", true},
		{"模型服务返回 500：internal server error", true},
		{"模型服务返回 502：bad gateway", true},
		{"模型服务返回 503：service unavailable", true},
		{"模型服务返回 429：rate limit exceeded", true},
		{"模型服务返回 400：invalid request body", false},
		{"模型服务返回 401：invalid api key", false},
		{"模型服务返回 402：insufficient balance 余额不足", false},
		{"模型服务返回 403：forbidden", false},
		{"模型服务返回 404：model not found", false},
		{"模型服务返回 400：This model's maximum context length is exceeded", false}, // 超限走压缩兜底
	}
	for _, c := range cases {
		if got := isRetryableModelErr(errors.New(c.err)); got != c.want {
			t.Fatalf("isRetryableModelErr(%q)=%v，应为 %v", c.err, got, c.want)
		}
	}
	if isRetryableModelErr(nil) {
		t.Fatal("nil 错误不应可重试")
	}
}

// TestRunAgentAutoRetry 端到端：假模型服务先故障后恢复，RunAgent 应自动重试
// 到成功——只落最终成功那一轮的输出，调用审计每次尝试各一条，事件流带
// retry（半截作废）与 auto_retry（重试提示 / 成功清除）。间隔用毫秒级替身，
// 避免测试真的等 1+2 秒。
func TestRunAgentAutoRetry(t *testing.T) {
	old := modelRetryDelays
	modelRetryDelays = [modelTries - 1]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { modelRetryDelays = old }()

	os.RemoveAll("data-e2e-retry")
	defer os.RemoveAll("data-e2e-retry")
	store, err := NewStore("data-e2e-retry")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.db.Close()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n <= 2 { // 前两次故障：网关 5xx
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"重试后完成\"}}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := &App{Store: store, Cfg: DefaultConfig()}
	a.Cfg.BaseURL = srv.URL
	a.Cfg.DataDir = "data-e2e-retry"
	a.Cfg.Workspace = t.TempDir()
	sess := &SessionMeta{ID: "retry", Title: "重试", Mode: "task", Model: "test-model",
		Permission: "workspace-write", CreatedAt: now()}
	if err := store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := store.Append("retry", TextMessage("user", "打个招呼")); err != nil {
		t.Fatalf("写用户消息失败: %v", err)
	}

	var events []Event
	if err := a.RunAgent(context.Background(), sess, func(ev Event) { events = append(events, ev) }); err != nil {
		t.Fatalf("自动重试后应成功，得到: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("应为 3 次尝试（2 败 1 成），服务端收到 %d 次", got)
	}

	// 事件流：两次失败各发 retry + auto_retry（有提示），成功补一条 auto_retry 清提示
	retries, hints, clears := 0, 0, 0
	for _, ev := range events {
		switch ev.Type {
		case "retry":
			retries++
		case "auto_retry":
			if ev.Output != "" {
				hints++
			} else {
				clears++
			}
		}
	}
	if retries != 2 || hints != 2 || clears != 1 {
		t.Fatalf("重试事件不符: retry=%d 提示=%d 清除=%d", retries, hints, clears)
	}

	// 调用审计：3 次尝试各一条
	raw, _ := store.RawMessagesAll("retry")
	auditCalls := 0
	for _, m := range raw {
		if m.Kind == "audit" && strings.Contains(m.Text(), `"type":"call"`) {
			auditCalls++
		}
	}
	if auditCalls != 3 {
		t.Fatalf("应落 3 条调用审计行（每次尝试一条），实际 %d", auditCalls)
	}

	// 最终输出落库完整、无“中断”残留
	var last string
	for _, m := range raw {
		if m.Role == "assistant" {
			last = m.Text()
		}
	}
	if !strings.Contains(last, "重试后完成") || strings.Contains(last, "中断") {
		t.Fatalf("最终正文不符: %q", last)
	}
}

// TestRunAgentAutoRetryExhausted 端到端：故障持续时按 modelTries 上限收手，
// 返回错误交给上层报错条（人工处理环境后再点重试）——不无限重试。
func TestRunAgentAutoRetryExhausted(t *testing.T) {
	old := modelRetryDelays
	modelRetryDelays = [modelTries - 1]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { modelRetryDelays = old }()

	os.RemoveAll("data-e2e-retry2")
	defer os.RemoveAll("data-e2e-retry2")
	store, err := NewStore("data-e2e-retry2")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.db.Close()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"still down"}}`))
	}))
	defer srv.Close()

	a := &App{Store: store, Cfg: DefaultConfig()}
	a.Cfg.BaseURL = srv.URL
	a.Cfg.DataDir = "data-e2e-retry2"
	a.Cfg.Workspace = t.TempDir()
	sess := &SessionMeta{ID: "retry2", Title: "重试2", Mode: "task", Model: "test-model",
		Permission: "workspace-write", CreatedAt: now()}
	if err := store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := store.Append("retry2", TextMessage("user", "打个招呼")); err != nil {
		t.Fatalf("写用户消息失败: %v", err)
	}

	var events []Event
	err = a.RunAgent(context.Background(), sess, func(ev Event) { events = append(events, ev) })
	if err == nil {
		t.Fatal("持续故障应收手报错，不能返回成功")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("报错应带上游原因，得到: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != modelTries {
		t.Fatalf("应尝试 %d 次后收手，服务端收到 %d 次", modelTries, got)
	}
	// 事件：4 次重试 + 4 条提示 + 耗尽时一条清除（避免与报错条并存）
	retries, hints, clears := 0, 0, 0
	for _, ev := range events {
		switch ev.Type {
		case "retry":
			retries++
		case "auto_retry":
			if ev.Output != "" {
				hints++
			} else {
				clears++
			}
		}
	}
	if retries != modelTries-1 || hints != modelTries-1 || clears != 1 {
		t.Fatalf("重试耗尽事件不符: retry=%d 提示=%d 清除=%d", retries, hints, clears)
	}
}

// TestRunAgentCancelStopsBetweenRetries 端到端：重试等待期间用户点停止（ctx
// 取消）——立即收手、不再发起新尝试，返回取消错误交给上层做 stop 收尾。
func TestRunAgentCancelStopsBetweenRetries(t *testing.T) {
	old := modelRetryDelays
	modelRetryDelays = [modelTries - 1]time.Duration{time.Second, time.Second, time.Second, time.Second}
	defer func() { modelRetryDelays = old }()

	os.RemoveAll("data-e2e-retry3")
	defer os.RemoveAll("data-e2e-retry3")
	store, err := NewStore("data-e2e-retry3")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.db.Close()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"still down"}}`))
	}))
	defer srv.Close()

	a := &App{Store: store, Cfg: DefaultConfig()}
	a.Cfg.BaseURL = srv.URL
	a.Cfg.DataDir = "data-e2e-retry3"
	a.Cfg.Workspace = t.TempDir()
	sess := &SessionMeta{ID: "retry3", Title: "重试3", Mode: "task", Model: "test-model",
		Permission: "workspace-write", CreatedAt: now()}
	if err := store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := store.Append("retry3", TextMessage("user", "打个招呼")); err != nil {
		t.Fatalf("写用户消息失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 64)
	go func() {
		for ev := range events {
			if ev.Type == "retry" { // 第一次失败已进入重试等待：在等待窗口中途取消
				time.Sleep(50 * time.Millisecond)
				cancel()
				return
			}
		}
	}()
	err = a.RunAgent(ctx, sess, func(ev Event) {
		select {
		case events <- ev:
		default:
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("等待期间取消应立即收手并返回取消，得到: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("取消后不应再发起尝试，服务端收到 %d 次", got)
	}
}
