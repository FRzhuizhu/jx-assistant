package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postPath 直连同一 mux 请求会话级端点，返回状态码与响应体。
func postPath(t *testing.T, srv *httptest.Server, sid, ep string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/api/sessions/"+sid+"/"+ep, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// TestUserGestureOpenRevealDirect 用户手势链路（open-path / reveal-path /
// delivery 的 open·reveal）只保留自然错误：路径不存在 → 404 且文案带完整
// 解析路径（前端弹明确提示、用户可手动前往）；其余一律放行（工作区内外、
// 是否登记过交付都不再校验——放行分支会真调 ShellExecute/explorer，由
// gui-test/ctxmenu-desktop-smoke.mjs 在真机上覆盖，这里只测拒绝分支）。
func TestUserGestureOpenRevealDirect(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	m := &SessionMeta{ID: "t-open", Title: "用户手势直通", Mode: "task",
		Workspace: a.Cfg.Workspace, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Cfg.Workspace, "在的.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.apiMuxOf())
	defer srv.Close()

	// 不存在：四条端点都 404，文案带完整解析路径（body 是 JSON，反斜杠已
	// 转义，用 json.Marshal 造同形态的比对串）
	jq := func(s string) string {
		raw, _ := json.Marshal(s)
		return strings.Trim(string(raw), `"`)
	}
	for _, ep := range []string{"open-path", "reveal-path", "delivery/open", "delivery/reveal"} {
		want := filepath.Join(a.Cfg.Workspace, "不存在的文件.txt")
		code, body := postPath(t, srv, "t-open", ep, map[string]any{"path": "不存在的文件.txt"})
		if code != 404 || !strings.Contains(body, jq(want)) {
			t.Fatalf("%s 不存在应 404 且文案带完整路径 %s：%d %s", ep, want, code, body)
		}
	}

	// 预览（open-view）同口径：不存在 → 404 自然错误
	resp, err := http.Post(srv.URL+"/api/browser/open-view", "application/json",
		strings.NewReader(`{"session":"t-open","path":"不存在的文件.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 404 || !strings.Contains(string(raw), "文件不存在") {
		t.Fatalf("open-view 不存在应 404 自然错误：%d %s", resp.StatusCode, raw)
	}
}
