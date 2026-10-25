// websearch.go 单测：服务端地址解析的回退链（searchURL 优先 → 市场同源 →
// 未配置报错）与结果渲染（列表形态、空结果走 answer、引用提醒在场）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// mkSkillTestApp 建一个带临时技能目录/数据目录的 App：enableLocal 决定是否
// 放置 web-search-local 技能；disabled 再把它停用（写 skills.json）。
func mkSkillTestApp(t *testing.T, enableLocal, disabled bool) *App {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if enableLocal {
		skill := filepath.Join(dir, "user", localSearchSkillID)
		if err := os.MkdirAll(skill, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: web-search-local\ndisplayName: 本机网络搜索\ndescription: 兜底\n---\n\n# 本机网络搜索\n正文"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{Cfg: &Config{SkillsDir: dir, DataDir: t.TempDir()}}
	if disabled {
		st := skillsState{Disabled: map[string]bool{localSearchSkillID: true}}
		if err := a.saveSkillsState(st); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// searchErrHint 三态：技能不存在/被停用→不指引；存在且启用→附加改道指引。
// 服务端搜索失败的每种错误都要带上这道桥（searchLocalHint 的挂点在
// runWebSearch 的全部错误出口），技能摘除后指引必须干净消失。
func TestSearchErrHint(t *testing.T) {
	// 未放置技能：无指引。
	a := mkSkillTestApp(t, false, false)
	if a.userSkillAvailable(localSearchSkillID) {
		t.Fatal("未放置技能不应可用")
	}
	err := a.searchErrHint(fmt.Errorf("连接服务端失败: x"))
	if err == nil || strings.Contains(err.Error(), "本机网络搜索") {
		t.Fatalf("技能不存在时不应附加指引: %v", err)
	}
	// 放置且启用：userSkillAvailable 成立，指引附加。
	a = mkSkillTestApp(t, true, false)
	if !a.userSkillAvailable(localSearchSkillID) {
		t.Fatal("技能存在且未停用应可用")
	}
	err = a.searchErrHint(fmt.Errorf("连接服务端失败: x"))
	if err == nil || !strings.Contains(err.Error(), "本机网络搜索") || !strings.Contains(err.Error(), "read_skill") {
		t.Fatalf("应附加本机搜索指引: %v", err)
	}
	// 停用：指引消失（摘除干净）。
	a = mkSkillTestApp(t, true, true)
	if a.userSkillAvailable(localSearchSkillID) {
		t.Fatal("停用的技能不应可用")
	}
	err = a.searchErrHint(fmt.Errorf("连接服务端失败: x"))
	if err == nil || strings.Contains(err.Error(), "本机网络搜索") {
		t.Fatalf("停用后不应再指引: %v", err)
	}
}

// 未配置服务端地址 + 技能可用：runWebSearch 的错误出口带上兜底指引。
func TestWebSearchUnconfiguredHint(t *testing.T) {
	a := mkSkillTestApp(t, true, false)
	_, err := a.runWebSearch(&toolContext{ctx: context.Background(), app: a, sess: &SessionMeta{ID: "s1"}},
		json.RawMessage(`{"query":"测试"}`))
	if err == nil || !strings.Contains(err.Error(), "网络访问") {
		t.Fatalf("未配置应报错: %v", err)
	}
	if !strings.Contains(err.Error(), "本机网络搜索") {
		t.Fatalf("未配置且技能可用时应指引本机兜底: %v", err)
	}
}

// 连不上服务端（关掉的 httptest 地址）+ 技能可用：连接失败错误也带指引。
func TestWebSearchUnreachableHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // 立即关闭：地址必连不通
	a := mkSkillTestApp(t, true, false)
	a.Cfg.SearchURL = base
	_, err := a.runWebSearch(&toolContext{ctx: context.Background(), app: a, sess: &SessionMeta{ID: "s2"}},
		json.RawMessage(`{"query":"测试"}`))
	if err == nil || !strings.Contains(err.Error(), "连接服务端失败") {
		t.Fatalf("应报连接失败: %v", err)
	}
	if !strings.Contains(err.Error(), "本机网络搜索") {
		t.Fatalf("连接失败且技能可用时应指引本机兜底: %v", err)
	}
}

// web-search-local 技能要进用户技能清单（环境快照的清单行是模型发现兜底
// 能力的第一入口；清单行的描述里必须带「仅当 web_search 失败时使用」的
// 分流语义，避免绕开服务端搜索）。
func TestLocalSearchSkillListed(t *testing.T) {
	a := mkSkillTestApp(t, true, false)
	list, err := a.listSkills()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list.User {
		if s.ID == localSearchSkillID {
			found = s.Enabled && s.DisplayName == "本机网络搜索"
		}
	}
	if !found {
		t.Fatalf("清单应含启用中的本机网络搜索技能: %+v", list.User)
	}
	block := a.userSkillsPromptBlock()
	if !strings.Contains(block, "本机网络搜索") || !strings.Contains(block, localSearchSkillID) {
		t.Fatalf("技能清单段应含显示名与 id:\n%s", block)
	}
}
