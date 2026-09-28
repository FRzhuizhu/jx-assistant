package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 端到端：假模型服务（SSE）+ 真实 RunAgent 主循环。验证两件事——
//   1. 落库的最近请求体与服务端实际收到的请求体逐字节一致（「导出原始数据」的可信度）；
//   2. 模型调用审计行带上了流尾 usage（缓存命中率数据源），并进入运行日志时间线。
func TestRunAgentCapturesRequestAndAudit(t *testing.T) {
	os.RemoveAll("data-e2e-export")
	defer os.RemoveAll("data-e2e-export")
	store, err := NewStore("data-e2e-export")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.db.Close()

	var gotBody []byte // 服务端实际收到的请求体
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"测试完成\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":100,\"prompt_cache_hit_tokens\":50,\"prompt_cache_miss_tokens\":50,\"completion_tokens\":10,\"total_tokens\":110}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := &App{Store: store, Cfg: DefaultConfig()}
	a.Cfg.BaseURL = srv.URL
	a.Cfg.DataDir = "data-e2e-export"
	a.Cfg.Workspace = t.TempDir()
	sess := &SessionMeta{ID: "e2e", Title: "端到端", Mode: "task", Model: "test-model",
		Permission: "workspace-write", CreatedAt: now()}
	if err := store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := store.Append("e2e", TextMessage("user", "打个招呼")); err != nil {
		t.Fatalf("写用户消息失败: %v", err)
	}

	if err := a.RunAgent(context.Background(), sess, func(Event) {}); err != nil {
		t.Fatalf("RunAgent 失败: %v", err)
	}

	// 1. 最近请求体：与服务端收到的逐字节一致
	rows, err := store.RecentRequestRows("e2e", keepRequests)
	if err != nil || len(rows) != 1 {
		t.Fatalf("应落库 request 行: rows=%d err=%v", len(rows), err)
	}
	row := rows[0]
	var rec lastRequestRec
	if err := json.Unmarshal([]byte(row.Text()), &rec); err != nil {
		t.Fatalf("request 行解析失败: %v", err)
	}
	if !bytes.Equal(rec.Body, gotBody) {
		t.Errorf("落库请求体与服务端收到的不一致:\n落库: %s\n收到: %s", rec.Body, gotBody)
	}
	if !strings.HasPrefix(rec.URL, srv.URL) || !strings.HasSuffix(rec.URL, "/chat/completions") {
		t.Errorf("请求行 URL 不符: %s", rec.URL)
	}
	out, err := a.ctxEngine().ExportRawRequests(sess)
	if err != nil {
		t.Fatalf("导出最近请求失败: %v", err)
	}
	for _, want := range []string{"\"system\"", "\"user\"", "\"tools\"", "打个招呼", "stream_options"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("导出的请求体缺少 %q:\n%s", want, out)
		}
	}

	// 2. 审计行与时间线
	raw, _ := store.RawMessagesAll("e2e")
	auditCalls := 0
	for _, m := range raw {
		if m.Kind == "audit" && strings.Contains(m.Text(), `"type":"call"`) {
			auditCalls++
			if !strings.Contains(m.Text(), `"hit":50`) {
				t.Errorf("调用审计行缺少缓存命中最少 usage: %s", m.Text())
			}
		}
	}
	if auditCalls != 1 {
		t.Fatalf("应落 1 条调用审计行，实际 %d", auditCalls)
	}
	md := a.ctxEngine().ExportTimeline(sess)
	for _, want := range []string{
		"模型调用 #1（本轮第 1 步）",
		"缓存命中 50（50.0%）",
		"上下文组装（第 1 轮）",
		"系统提示词全文（请求里的第一条 system 消息）",
		"用户消息",
		"模型输出：正文 4 字",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("运行日志缺少 %q\n--- 渲染结果 ---\n%s", want, md)
		}
	}
	
}
