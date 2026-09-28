package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// isModelUnavailableErr 判定表：硬性不可用（余额/鉴权/模型不存在/连不上）才算，
// 纯限流与取消不算。
func TestIsModelUnavailableErr(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"模型服务返回 402：Insufficient Balance", true},
		{"模型服务返回 429：Insufficient Balance", true},
		{"模型服务返回 429：Rate limit exceeded, please retry later", false}, // 纯限流是瞬时的
		{"模型服务返回 401：Authentication Fails, Your api key is invalid", true},
		{"模型服务返回 404：Not Found", true},
		{"模型服务返回 400：Model Not Exist", true},
		{"模型服务返回 400：Invalid parameter: max_tokens", false}, // 请求本身的问题
		{"模型服务返回 500：Internal Server Error", false},         // 服务端瞬时错误
		{"无法连接模型服务：连接被拒绝，请确认 API 地址与端口可达", true},
		{"无法连接模型服务：请求已取消", false},
		{"模型服务返回 200：余额不足提示混进正文", true}, // 状态没匹配时按关键词兜底
		{"", false},
	}
	for _, c := range cases {
		var e error
		if c.err != "" {
			e = context.DeadlineExceeded // 占位，仅当载体；用自定义错误更直观
			e = &testErr{c.err}
		}
		if got := isModelUnavailableErr(e); got != c.want {
			t.Errorf("isModelUnavailableErr(%q) = %v，应为 %v", c.err, got, c.want)
		}
	}
}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

// 专家预设模型不可用 → 自动切回全局默认模型（主对话同款）续跑并落库；
// 回退只对专家会话生效（普通会话手选模型报错不静默降级）。
func TestRunAgentExpertModelFallback(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	// 全局默认 = provider0（mock，离线可跑）；专家预设挂 provider1（永远 402 欠费）
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"Insufficient Balance"}}`))
	}))
	defer dead.Close()
	a.Cfg.Providers = []ProviderCfg{
		{ID: "live", Name: "默认方", BaseURL: "mock", Models: []ModelCfg{{ID: "default-model"}}},
		{ID: "dead", Name: "欠费方", BaseURL: dead.URL, Models: []ModelCfg{{ID: "expensive-model"}}},
	}
	a.Cfg.Experts = []ExpertCfg{
		{ID: "e1", Name: "审查员", Description: "找问题", Prompt: "你是审查员。", Model: "dead/expensive-model", Enabled: true},
	}
	a.Cfg.MaxSteps = 30 // newTestApp 的字面量配置不带步数上限
	sess := &SessionMeta{ID: "fb1", Mode: "task", Workspace: t.TempDir(),
		Provider: "dead", Model: "expensive-model", Expert: "审查员",
		CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	var notices []string
	err := a.RunAgent(context.Background(), sess, func(ev Event) {
		if ev.Type == "tool_wait" && ev.Name == "model-fallback" {
			notices = append(notices, ev.Output)
		}
	})
	if err != nil {
		t.Fatalf("专家模型欠费时应回退默认模型跑完，不应失败: %v", err)
	}
	// 会话模型已切回全局默认并落库（后续步不再撞同一个错）
	got, _ := a.Store.Get(sess.ID)
	if got.Provider != "" || got.Model != "" {
		t.Fatalf("回退后 Provider/Model 应清空回落默认，得到 %q/%q", got.Provider, got.Model)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "切回默认模型") {
		t.Fatalf("应发一条回退提示：%v", notices)
	}

	// 对照组：普通会话（无专家）用同一个欠费模型 → 如实报错，不静默降级
	plain := &SessionMeta{ID: "fb2", Mode: "task", Workspace: t.TempDir(),
		Provider: "dead", Model: "expensive-model", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(plain); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	err = a.RunAgent(context.Background(), plain, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "402") {
		t.Fatalf("普通会话手选模型失败应报错而非静默回退：%v", err)
	}
	got2, _ := a.Store.Get(plain.ID)
	if got2.Provider != "dead" || got2.Model != "expensive-model" {
		t.Fatalf("普通会话的模型选择不应被改写：%q/%q", got2.Provider, got2.Model)
	}
}
