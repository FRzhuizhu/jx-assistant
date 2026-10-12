package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUsageCacheHitNormalize 看护「缓存命中量按端点字段名归一」：OpenAI/智谱
// GLM 报 prompt_tokens_details.cached_tokens，DeepSeek 报 prompt_cache_hit_tokens，
// 只认单一字段会让另一种端点的命中率恒为 0%（GLM 实测：同一个长前缀第二次
// 请求 cached_tokens=3008，而响应里根本没有 prompt_cache_hit_tokens 字段）。
func TestUsageCacheHitNormalize(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		hit     int
		miss    int
		reports bool
	}{
		{"OpenAI/智谱 命中",
			`{"prompt_tokens":3044,"completion_tokens":3,"total_tokens":3047,"prompt_tokens_details":{"cached_tokens":3008}}`,
			3008, 36, true},
		{"OpenAI/智谱 未命中",
			`{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":0}}`,
			0, 100, true},
		{"DeepSeek 命中",
			`{"prompt_tokens":10074,"prompt_cache_hit_tokens":7168,"prompt_cache_miss_tokens":2906}`,
			7168, 2906, true},
		{"Anthropic 兼容层",
			`{"prompt_tokens":100,"cache_read_input_tokens":64}`,
			64, 36, true},
		{"端点不报缓存统计",
			`{"prompt_tokens":100,"completion_tokens":5}`,
			0, 0, false},
	}
	for _, c := range cases {
		var u Usage
		if err := json.Unmarshal([]byte(c.raw), &u); err != nil {
			t.Fatalf("%s: 解析失败: %v", c.name, err)
		}
		if got := u.CacheHit(); got != c.hit {
			t.Errorf("%s: CacheHit=%d，期望 %d", c.name, got, c.hit)
		}
		if got := u.CacheMiss(); got != c.miss {
			t.Errorf("%s: CacheMiss=%d，期望 %d", c.name, got, c.miss)
		}
		if got := u.ReportsCache(); got != c.reports {
			t.Errorf("%s: ReportsCache=%v，期望 %v", c.name, got, c.reports)
		}
	}
}

// TestParseSSEUsageOpenAIShape 看护流尾 usage 的取出与归一：SSE 最后一帧带
// prompt_tokens_details 时，OnUsage 收到的命中量不为 0（缓存命中率的数据源）。
func TestParseSSEUsageOpenAIShape(t *testing.T) {
	const sse = "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3044,\"completion_tokens\":3,\"total_tokens\":3047," +
		"\"prompt_tokens_details\":{\"cached_tokens\":3008}}}\n\ndata: [DONE]\n\n"
	var got *Usage
	var text strings.Builder
	err := parseSSE(strings.NewReader(sse), StreamCallbacks{
		OnText:  func(s string) { text.WriteString(s) },
		OnUsage: func(u Usage) { uu := u; got = &uu },
	})
	if err != nil {
		t.Fatalf("parseSSE 失败: %v", err)
	}
	if text.String() != "OK" {
		t.Errorf("正文 = %q，期望 \"OK\"", text.String())
	}
	if got == nil {
		t.Fatal("没有收到 usage 回调")
	}
	if got.PromptTokens != 3044 || got.CacheHit() != 3008 || got.CacheMiss() != 36 {
		t.Errorf("usage 归一错误: prompt=%d hit=%d miss=%d", got.PromptTokens, got.CacheHit(), got.CacheMiss())
	}
}
