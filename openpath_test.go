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

// postPath 直连同一 mux 请求 open-path/reveal-path，返回状态码与响应体。
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

// TestSessionOpenRevealBoundary open-path/reveal-path 的工作区边界分支，只走
// 不执行系统程序的路（不存在 404、工作区外未确认 403+outside）；工作区内
// 放行与确认后放行会真调 ShellExecute/explorer，由 gui-test/
// ctxmenu-desktop-smoke.mjs 在真机上覆盖。
func TestSessionOpenRevealBoundary(t *testing.T) {
	a, _ := newDispatchTestApp(t)
	m := &SessionMeta{ID: "t-open", Title: "open-path 边界", Mode: "task",
		Workspace: a.Cfg.Workspace, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Cfg.Workspace, "在的.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "外.txt") // 独立临时目录：必在工作区外且存在
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.apiMuxOf())
	defer srv.Close()

	// 不存在：open/reveal 都 404，且文案带完整解析路径（前端弹明确提示，
	// 用户可照着手动前往——不再裸 404 默默失败）。body 是 JSON，路径里的
	// 反斜杠已被转义，用 json.Marshal 造同形态的比对串
	jq := func(s string) string {
		raw, _ := json.Marshal(s)
		return strings.Trim(string(raw), `"`) // 去首尾引号：路径在错误文案中间，不带引号
	}
	for _, ep := range []string{"open-path", "reveal-path"} {
		want := filepath.Join(a.Cfg.Workspace, "不存在的文件.txt")
		code, body := postPath(t, srv, "t-open", ep, map[string]any{"path": "不存在的文件.txt"})
		if code != 404 || !strings.Contains(body, jq(want)) {
			t.Fatalf("%s 不存在应 404 且文案带完整路径 %s：%d %s", ep, want, code, body)
		}
	}

	// 工作区外已存在：open 未确认 → 403+outside（不执行，等前端确认后带
	// confirmOutside 重发），文案带完整路径
	code, body := postPath(t, srv, "t-open", "open-path", map[string]any{"path": outside})
	if code != 403 || !strings.Contains(body, `"outside":true`) || !strings.Contains(body, jq(outside)) {
		t.Fatalf("工作区外 open 未确认应 403+outside 且文案带完整路径 %s：%d %s", outside, code, body)
	}

	// 工作区内路径不落 403 确认分支（无需确认，直达执行，与旧行为一致）；
	// 403 分支条件是 !insideWorkspace，这里直接断言判定口径没把内路径误判
	if !insideWorkspace(&toolContext{app: a, sess: m}, filepath.Join(a.Cfg.Workspace, "在的.txt")) {
		t.Fatal("工作区内路径被误判为工作区外（会错误触发确认分支）")
	}
}
