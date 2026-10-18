// websearch.go 单测：服务端地址解析的回退链（searchURL 优先 → 市场同源 →
// 未配置报错）与结果渲染（列表形态、空结果走 answer、引用提醒在场）。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchEndpointBase(t *testing.T) {
	a := &App{Cfg: &Config{}}
	// 两者都没配：明确报错。
	if _, err := a.searchEndpointBase(); err == nil || !strings.Contains(err.Error(), "网络访问") {
		t.Fatalf("未配置应报错: %v", err)
	}
	// 只配市场地址：取同源。
	a.Cfg.MarketURL = "http://192.168.1.10:PORT/market/manifest.json"
	base, err := a.searchEndpointBase()
	if err != nil || base != "http://192.168.1.10:PORT" {
		t.Fatalf("市场同源不对: %s %v", base, err)
	}
	// 配了 searchURL：优先于市场。
	a.Cfg.SearchURL = "https://search.example.com:8443/任意路径"
	base, err = a.searchEndpointBase()
	if err != nil || base != "https://search.example.com:8443" {
		t.Fatalf("searchURL 优先不对: %s %v", base, err)
	}
	// searchURL 非法：报 searchURL 而不是回落市场。
	a.Cfg.SearchURL = "不是地址"
	if _, err := a.searchEndpointBase(); err == nil || !strings.Contains(err.Error(), "searchURL") {
		t.Fatalf("非法 searchURL 应报错: %v", err)
	}
}

func TestRenderSearchResults(t *testing.T) {
	out := searchResponse{Engine: "zhipu-mcp"}
	out.Results = []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
		Site    string `json:"site"`
		Date    string `json:"date"`
	}{
		{Title: "结果一", URL: "https://a.com/1", Snippet: "摘要一", Site: "A站", Date: "2026-10-01"},
		{Title: "结果二", URL: "https://b.com/2", Snippet: strings.Repeat("长", 300), Site: "B站"},
	}
	text := renderSearchResults("测试", out)
	for _, want := range []string{"搜索「测试」：2 条结果", "[结果一](https://a.com/1)（2026-10-01）", "…", "web_fetch"} {
		if !strings.Contains(text, want) {
			t.Fatalf("渲染缺 %q:\n%s", want, text)
		}
	}
	// 空结果但有 answer：转交文本。
	out2 := searchResponse{Engine: "zhipu-mcp", Answer: "直接回答文本"}
	if text := renderSearchResults("q", out2); !strings.Contains(text, "直接回答文本") {
		t.Fatalf("answer 应转交:\n%s", text)
	}
}

// 请求头契约：搜索请求带 X-Jxai-Conversation（值=会话 id，服务端按它对
// 对话轮询搜索引擎；工具执行时 sess 恒非空——authorize 更早就依赖它）。
func TestWebSearchConversationHeader(t *testing.T) {
	var conv string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conv = r.Header.Get(searchConversationHeader)
		w.Write([]byte(`{"results":[{"title":"t","url":"https://a","snippet":"s"}],"engine":"zhipu-mcp"}`))
	}))
	defer srv.Close()
	a := &App{Cfg: &Config{SearchURL: srv.URL}}
	if _, err := a.runWebSearch(&toolContext{ctx: context.Background(), app: a, sess: &SessionMeta{ID: "sess-conv-1"}},
		json.RawMessage(`{"query":"测试"}`)); err != nil {
		t.Fatalf("runWebSearch 出错: %v", err)
	}
	if conv != "sess-conv-1" {
		t.Fatalf("应带对话标识头 X-Jxai-Conversation=sess-conv-1，实得 %q", conv)
	}
}
