// 市场管理页单测：页面下发、清单 API、multipart 上传发布（与 CLI 同链路）、下架。
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestAdminServer(t *testing.T) (*marketStore, *httptest.Server) {
	t.Helper()
	s := newMarketStore(t.TempDir(), "测试市场")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin", s.handleAdminPage)
	mux.HandleFunc("GET /admin/api/list", s.handleAdminList)
	mux.HandleFunc("POST /admin/api/publish", s.handleAdminPublish)
	mux.HandleFunc("POST /admin/api/unpublish", s.handleAdminUnpublish)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func zipSkillForTest(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("SKILL.md")
	w.Write([]byte("---\nname: admin-skill\ndescription: 管理页上传的技能\n---\n正文"))
	zw.Close()
	return buf.Bytes()
}

func TestAdminPageServed(t *testing.T) {
	_, srv := newTestAdminServer(t)
	resp, err := srv.Client().Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte("坚祥市场管理")) {
		t.Fatalf("管理页下发不对: %d %q", resp.StatusCode, body[:min(len(body), 80)])
	}
	if !bytes.Contains(body, []byte("暂无访问控制")) {
		t.Fatal("管理页应有「暂无访问控制」的过渡提示")
	}
}

func TestAdminPublishAndUnpublish(t *testing.T) {
	store, srv := newTestAdminServer(t)

	// multipart 上传发布
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("type", "skill")
	_ = mw.WriteField("desc", "经由管理页")
	fw, _ := mw.CreateFormFile("file", "admin-skill.zip")
	fw.Write(zipSkillForTest(t))
	mw.Close()
	resp, err := srv.Client().Post(srv.URL+"/admin/api/publish", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Entry marketEntry `json:"entry"`
		Error string      `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || out.Error != "" {
		t.Fatalf("上传发布失败: %d %s", resp.StatusCode, out.Error)
	}
	if out.Entry.ID != "admin-skill" || out.Entry.Version != "1.0.0" {
		t.Fatalf("条目推导不对: %#v", out.Entry)
	}
	// 清单 API 可见
	resp2, _ := srv.Client().Get(srv.URL + "/admin/api/list")
	var m marketManifest
	_ = json.NewDecoder(resp2.Body).Decode(&m)
	resp2.Body.Close()
	if len(m.Skills) != 1 || m.Skills[0].ID != "admin-skill" {
		t.Fatalf("清单应含新条目: %#v", m.Skills)
	}
	if !strings.Contains(m.Skills[0].Description, "管理页") {
		t.Fatalf("表单元数据未生效: %#v", m.Skills[0])
	}
	// 客户端市场清单同步可见（同一个 store）
	if mm := store.load(); len(mm.Skills) != 1 {
		t.Fatal("store 与管理 API 应同源")
	}
	// 下架
	resp3, err := srv.Client().Post(srv.URL+"/admin/api/unpublish", "application/json",
		strings.NewReader(`{"type":"skill","id":"admin-skill"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("下架失败: %d", resp3.StatusCode)
	}
	if m := store.load(); len(m.Skills) != 0 {
		t.Fatalf("下架后清单应为空: %#v", m.Skills)
	}
	if _, err := os.Stat(filepath.Join(store.itemsDir(), "skill", "admin-skill")); !os.IsNotExist(err) {
		t.Fatal("下架后包目录应被删除")
	}
}

func TestAdminPublishGuards(t *testing.T) {
	_, srv := newTestAdminServer(t)
	// 非 zip
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("type", "skill")
	fw, _ := mw.CreateFormFile("file", "not-a-zip.txt")
	fw.Write([]byte("x"))
	mw.Close()
	resp, _ := srv.Client().Post(srv.URL+"/admin/api/publish", mw.FormDataContentType(), &buf)
	if resp.StatusCode == 200 {
		resp.Body.Close()
		t.Fatal("非 zip 应被拒")
	}
	resp.Body.Close()
	// 非法类型
	var buf2 bytes.Buffer
	mw2 := multipart.NewWriter(&buf2)
	_ = mw2.WriteField("type", "money")
	fw2, _ := mw2.CreateFormFile("file", "a.zip")
	fw2.Write(zipSkillForTest(t))
	mw2.Close()
	resp2, _ := srv.Client().Post(srv.URL+"/admin/api/publish", mw2.FormDataContentType(), &buf2)
	if resp2.StatusCode == 200 {
		resp2.Body.Close()
		t.Fatal("非法类型应被拒")
	}
	resp2.Body.Close()
}
