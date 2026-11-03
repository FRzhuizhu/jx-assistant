package main

// 插话立即打断模型调用的隔离验收测试（用户 2026-11-03 需求）：
//
//   模型调用中（流式思考/正文输出/重试等待）插话 → 立即掐断当前模型流，
//   半截输出带打断标注落库，下一步带着插话重组上下文重新调模型；
//   工具执行中插话 → 不打断，照旧等下一步调用模型前注入（现有行为，不回归）。
//
// 打断路径的完整链路走真实组件：executeRun → RunAgent → StreamChat →
// httptest 假网关（第一次请求流一段正文后挂起到客户端断开，第二次请求正常
// 收尾），插话走 pushPending(steer)（kick 在 pushPending 内统一触发，与
// handleInterject / deliverToSession 同一入口）。
//
// 验收点：
//  1. 第一次模型流被掐断：历史里有半截 assistant 消息，带「（被插话打断，
//     输出提前结束）」标注；
//  2. 插话消息在半截之后落库（顺序对，模型下一轮看到的就是这个顺序）；
//  3. 第二次模型请求（假网关收到的第 2 个 body）包含插话内容——插话真的
//     进了重组后的上下文；
//  4. 本轮正常收尾（最后一条 assistant 是第二次请求的完整回复），不是报错中断。

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSteerInterruptsModelStream 模型流式输出中途插话 → 立即打断生效。
func TestSteerInterruptsModelStream(t *testing.T) {
	var mu sync.Mutex
	bodies := []string{}          // 假网关收到的每个请求体（按到达顺序）
	firstFlushing := make(chan struct{}) // 第一次请求已流出半截正文（模型调用进行中的信号）

	writeSSE := func(w http.ResponseWriter, txt string) {
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", txt)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		if n == 1 {
			// 第一次请求：流出半截正文后挂起——直到客户端（stepCtx 取消）断开连接
			writeSSE(w, "半截正文：我正在慢慢")
			fl.Flush()
			close(firstFlushing)
			<-r.Context().Done() // 客户端断开（打断生效）才返回
			return
		}
		// 后续请求：正常完整收尾（模型消化插话后的新回复）
		writeSSE(w, "收到插话后的完整回复")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(srv.Close)

	a := newCompactTestApp(t)
	a.initRuntime()
	a.sessLocks = map[string]*sync.Mutex{}
	a.Cfg.Workspace = t.TempDir()
	a.Cfg.Providers = []ProviderCfg{{ID: "fakep", Name: "假渠道", BaseURL: srv.URL,
		Models: []ModelCfg{{ID: "fake-m"}}}}
	a.Components = &ComponentRegistry{}
	m := &SessionMeta{ID: "steerkick", Title: "插话打断测试", Mode: "task",
		Workspace: a.Cfg.Workspace, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(m); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	a.appendMessage(m, TextMessage("user", "开始"))

	run, ok := a.beginRun(m.ID)
	if !ok {
		t.Fatal("beginRun 失败")
	}
	go a.executeRun(m, run, nil)

	select { // 等模型流真正开始输出（半截正文已从假网关发出）
	case <-firstFlushing:
	case <-time.After(10 * time.Second):
		t.Fatal("模型流迟迟未建立")
	}
	a.pushPending(m.ID, "steer", "插话：先停下，改听这句", "", "")

	select { // 等执行体收尾
	case <-run.done:
	case <-time.After(20 * time.Second):
		t.Fatal("执行体迟迟未收尾（打断没有生效？）")
	}

	all, err := a.Store.RawMessagesAll(m.ID)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	var halfAI, steerUser, finalAI = -1, -1, -1
	for i, msg := range all {
		txt, _ := msg.Content.(string)
		if msg.Role == "assistant" && strings.Contains(txt, "半截正文") {
			halfAI = i
			if !strings.Contains(txt, "（被插话打断，输出提前结束）") {
				t.Errorf("半截 assistant 消息缺少打断标注: %q", txt)
			}
		}
		if msg.Role == "user" && txt == "插话：先停下，改听这句" {
			steerUser = i
		}
		if msg.Role == "assistant" && strings.Contains(txt, "收到插话后的完整回复") {
			finalAI = i
		}
	}
	if halfAI < 0 || steerUser < 0 || finalAI < 0 {
		t.Fatalf("历史不完整：半截=%d 插话=%d 完整回复=%d（应有这三条）", halfAI, steerUser, finalAI)
	}
	if !(halfAI < steerUser && steerUser < finalAI) {
		t.Errorf("落库顺序不对：半截(%d) 应在插话(%d) 之前、插话应在完整回复(%d) 之前", halfAI, steerUser, finalAI)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 {
		t.Fatalf("假网关只收到 %d 次请求，打断后应重调模型", len(bodies))
	}
	if !strings.Contains(bodies[1], "插话：先停下，改听这句") {
		t.Errorf("第二次模型请求没有带上插话内容")
	}
	t.Logf("链路验证：首次请求被掐断 → 半截落库 → 插话注入 → 重调模型（共 %d 次请求）", len(bodies))
}
