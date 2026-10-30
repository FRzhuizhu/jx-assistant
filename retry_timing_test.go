package main

// 模型自动重试计时的隔离验收测试（用户反馈疑似：重试间隔没有 1s/2s/3s/4s
// 递增，好像每次只隔 1 秒、总等待仅 5 秒）。走真实 RunAgent → StreamChat
// 路径，恒 429 与拒连两个场景，记录每次尝试的实际时刻，验证相邻间隔
// 依次约 1s/2s/3s/4s（累计约 10s）——计时起点是「上一次失败返回」，
// 不是「第一次失败」。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// runRetryTimingApp 搭好假渠道 App 并跑一轮 RunAgent，返回 auto_retry 提示
// 出现的时刻与文案；onHit 在假网关收到每次请求时回调（拒连场景无请求，不触发）。
func runRetryTimingApp(t *testing.T, baseURL string, onHit func()) (notices []time.Time, texts []string) {
	t.Helper()
	a := newCompactTestApp(t)
	a.initRuntime()
	a.Cfg.Workspace = t.TempDir()
	a.Cfg.Providers = []ProviderCfg{{ID: "fakep", Name: "假渠道", BaseURL: baseURL,
		Models: []ModelCfg{{ID: "fake-m"}}}}
	sess := &SessionMeta{ID: "retrytiming", Title: "重试计时", Mode: "task", Workspace: a.Cfg.Workspace,
		CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	var mu sync.Mutex
	runErr := a.RunAgent(context.Background(), sess, func(ev Event) {
		if ev.Type == "auto_retry" {
			mu.Lock()
			notices = append(notices, time.Now())
			texts = append(texts, ev.Output)
			mu.Unlock()
		}
	})
	t.Logf("RunAgent 返回: %v", runErr)
	return notices, texts
}

// assertSpacing 校验相邻尝试时刻依次间隔约 1s/2s/3s/4s。
func assertSpacing(t *testing.T, label string, ts []time.Time) {
	t.Helper()
	if len(ts) != modelTries {
		t.Fatalf("%s：时刻数 = %d，期望 %d", label, len(ts), modelTries)
	}
	want := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}
	for i := 1; i < len(ts); i++ {
		got := ts[i].Sub(ts[i-1])
		if got < want[i-1]-400*time.Millisecond || got > want[i-1]+400*time.Millisecond {
			t.Errorf("%s：第 %d→%d 次尝试间隔 = %v，期望约 %v", label, i, i+1, got.Round(time.Millisecond), want[i-1])
		}
		t.Logf("%s：第 %d→%d 次尝试间隔 = %v（期望约 %v）", label, i, i+1, got.Round(time.Millisecond), want[i-1])
	}
	total := ts[len(ts)-1].Sub(ts[0]).Round(time.Millisecond)
	t.Logf("%s：首次到末次尝试总跨度 = %v（期望约 10s）", label, total)
}

// TestRetryTiming429 恒 429 限流：isRetryableModelErr 判定可重试的主场景。
func TestRetryTiming429(t *testing.T) {
	var mu sync.Mutex
	var hits []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit, too many requests"}}`))
	}))
	t.Cleanup(srv.Close)

	notices, texts := runRetryTimingApp(t, srv.URL, func() {
		mu.Lock()
		hits = append(hits, time.Now())
		mu.Unlock()
	})
	for _, tx := range texts {
		if tx == "" {
			continue // 重试耗尽/成功时发空 auto_retry 收掉提示条，不是文案
		}
		if !strings.Contains(tx, "秒后自动重试") {
			t.Errorf("auto_retry 提示不含重试文案: %q", tx)
		}
		t.Logf("提示条: %s", tx)
	}
	mu.Lock()
	assertSpacing(t, "429 服务器侧", hits)
	mu.Unlock()
	if len(notices) > 0 { // 提示时刻紧跟失败返回，同样应呈递增间隔
		assertSpacing(t, "429 事件侧", notices)
	}
}

// TestRetryTimingConnRefused 拒连（断网形态）：connFailure 路径，每次尝试
// 本身近乎零耗时，间隔应同样 1s/2s/3s/4s。
func TestRetryTimingConnRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // 立即关掉：留下一个必然拒连的本地端口
	notices, _ := runRetryTimingApp(t, srv.URL, nil)
	assertSpacing(t, "拒连 事件侧", notices)
}
