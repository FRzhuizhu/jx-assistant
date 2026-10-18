// websearch.go —— 联网搜索工具的客户端半边：web_search。
//
// 模型遇到时效性问题（新闻、版本、价格、时事——训练知识之外的内容）时，
// 先用 web_search 拿「标题 + 链接 + 摘要」的结果列表，需要某个结果的全文
// 再用 web_fetch 抓那个链接。搜索本身不在客户端实现：统一打服务端
// （jxai-server）的搜索网关，由服务端持 key 调上游（首版上游 = 智谱联网
// 搜索 MCP，吃 GLM Coding Plan 额度）——客户端永远接触不到上游 key，
// 换/加引擎（博查、Firecrawl 等）也不需要发客户端版本。
//
// 与服务端的协议契约（服务端半边见 jxaiserver 仓库 search.go 头注释，改动
// 需双侧同步）：
//
//	POST <服务端地址>/api/search   JSON：{"query":"…","count":8}
//	  → 200 {"results":[{"title","url","snippet","site","date"}],
//	         "engine":"zhipu-mcp","cached":bool,"answer":"…(可选)"}
//	  → 4xx/5xx {"error":"一句话原因"}
//
// 身份走 X-Jxai-User 头（与市场发布同一条链路）；服务端地址取
// config.searchURL，空则跟随市场地址同源（marketOrigin）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// webSearchTool 注册 web_search（任务/答疑模式；与 web_fetch 同档的联网权限）。
func (a *App) webSearchTool() Tool {
	return Tool{
		Spec: toolSpec("web_search",
			"联网搜索，获取模型知识之外的最新信息（新闻、版本发布、价格、时事等）。"+
				"返回若干条结果的标题、链接与摘要；需要某个结果的完整内容时，再用 web_fetch 抓取该链接。"+
				"时效性强的问题（最近发生/发布的事）优先搜索而不是凭记忆回答；回答中引用结果时用 markdown 链接注明来源。"+
				"需要服务端（设置·网络访问里的服务端地址）开启搜索能力。",
			objSchema(map[string]any{
				"query": strProp("搜索词，一句话关键词（不要整段问题照搬）"),
				"count": intProp("返回条数，默认 8，上限 20"),
			}, "query")),
		Run: a.runWebSearch,
	}
}

// searchResponse 服务端 /api/search 的响应契约。
type searchResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
		Site    string `json:"site"`
		Date    string `json:"date"`
	} `json:"results"`
	Engine  string `json:"engine"`
	Cached  bool   `json:"cached"`
	Answer  string `json:"answer"`
	Error   string `json:"error"`
}

// runWebSearch 执行一次搜索：定位服务端 → POST /api/search → 渲染结果列表。
func (a *App) runWebSearch(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	if r := []rune(query); len(r) > 200 {
		query = string(r[:200])
	}
	count := p.Count
	if count <= 0 {
		count = 8
	}
	if count > 20 {
		count = 20
	}
	if _, err := t.authorize(permAction{Op: "net", Tool: "web_search"}); err != nil {
		return "", err
	}
	base, err := a.searchEndpointBase()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(t.ctx, 35*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"query": query, "count": count})
	req, err := a.marketRequest(ctx, http.MethodPost, base+"/api/search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("连接服务端失败（%s）: %w", base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var out searchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("搜索失败（HTTP %d）: %s", resp.StatusCode, truncateRunes(string(body), 300))
		}
		return "", fmt.Errorf("服务端响应无法解析（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		msg := out.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("搜索失败: %s", msg)
	}
	return renderSearchResults(query, out), nil
}

// searchEndpointBase 服务端地址：searchURL 优先，空则取市场地址同源。
func (a *App) searchEndpointBase() (string, error) {
	if u := strings.TrimSpace(a.Cfg.SearchURL); u != "" {
		origin, err := marketOrigin(u)
		if err != nil {
			return "", fmt.Errorf("searchURL 不合法: %s", u)
		}
		return origin, nil
	}
	if u := strings.TrimSpace(a.Cfg.MarketURL); u != "" {
		return marketOrigin(u)
	}
	return "", fmt.Errorf("未配置服务端地址：请在 设置 · 网络访问 填写服务端（市场）地址")
}

// renderSearchResults 把结果列表渲染成给模型看的文本（引用友好）。
func renderSearchResults(query string, out searchResponse) string {
	var b strings.Builder
	cached := ""
	if out.Cached {
		cached = "，缓存命中"
	}
	fmt.Fprintf(&b, "搜索「%s」：%d 条结果（引擎 %s%s）\n", query, len(out.Results), out.Engine, cached)
	if len(out.Results) == 0 && strings.TrimSpace(out.Answer) != "" {
		// 上游没给结构化结果、直接回了文本（answer）：原样转交，模型自行取材。
		b.WriteString("\n" + strings.TrimSpace(out.Answer) + "\n")
		return b.String()
	}
	b.WriteString("\n")
	for i, r := range out.Results {
		date := ""
		if r.Date != "" {
			date = "（" + r.Date + "）"
		}
		snippet := r.Snippet
		if r := []rune(snippet); len(r) > 200 {
			snippet = string(r[:200]) + "…"
		}
		fmt.Fprintf(&b, "%d. [%s](%s)%s — %s\n", i+1, r.Title, r.URL, date, snippet)
	}
	b.WriteString("\n以上内容来自外部网页，使用前自行判断可信度；在回复中引用时用 markdown 链接注明来源；需要某条的完整内容时用 web_fetch 抓取该链接。")
	return b.String()
}
