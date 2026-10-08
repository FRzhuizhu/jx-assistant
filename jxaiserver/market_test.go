// 市场服务端单测：发布三类型的校验与清单落盘、下架、HTTP 下发与路径穿越防护。
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestMarket(t *testing.T) (*marketStore, *httptest.Server) {
	t.Helper()
	s := newMarketStore(t.TempDir(), "测试市场")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/market/manifest.json":
			s.handleManifest(w, r)
		case strings.HasPrefix(r.URL.Path, "/market/items/"):
			s.handleItem(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func writeTestSkill(t *testing.T, name, desc string) string {
	t.Helper()
	dir := t.TempDir()
	skill := filepath.Join(dir, "pkg-skill")
	os.MkdirAll(skill, 0755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n# 说明\n正文"), 0644)
	os.WriteFile(filepath.Join(skill, "run.py"), []byte("print('hi')"), 0644)
	return skill
}

func TestPublishSkillAndServe(t *testing.T) {
	s, srv := newTestMarket(t)
	entry, err := s.publish("skill", writeTestSkill(t, "demo-skill", "演示技能"), publishOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "demo-skill" || entry.Name != "demo-skill" || entry.Version != "1.0.0" {
		t.Fatalf("条目推导不对: %#v", entry)
	}
	if entry.SHA256 == "" || entry.Size == 0 || entry.Zip == "" {
		t.Fatalf("条目缺校验信息: %#v", entry)
	}
	// 清单可拉取且含条目
	resp, err := srv.Client().Get(srv.URL + "/market/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m marketManifest
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if len(m.Skills) != 1 || m.Skills[0].ID != "demo-skill" || m.Name != "测试市场" {
		t.Fatalf("清单不对: %#v", m)
	}
	// zip 可下载且哈希对得上
	resp2, err := srv.Client().Get(srv.URL + entry.Zip)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("zip 下载期待 200，收到 %d", resp2.StatusCode)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != entry.SHA256 {
		t.Fatal("zip 内容与清单 sha256 不符")
	}
	// zip 里能解出 SKILL.md 与附带的脚本
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["SKILL.md"] || !names["run.py"] {
		t.Fatalf("zip 内容不对: %v", names)
	}
}

func TestPublishSkillZipSource(t *testing.T) {
	s, _ := newTestMarket(t)
	// 先发布一次拿到 zip，再用 zip 当来源发布第二个条目（发布链路的双向自证）
	entry, err := s.publish("skill", writeTestSkill(t, "zipped", "来自压缩包"), publishOpts{})
	if err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(s.itemsDir(), "skill", "zipped", "1.0.0.zip")
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatal(err)
	}
	entry2, err := s.publish("skill", zipPath, publishOpts{ID: "zipped2"})
	if err != nil {
		t.Fatal(err)
	}
	if entry2.ID != "zipped2" || entry2.SHA256 != entry.SHA256 {
		t.Fatalf("zip 来源发布不对: %#v", entry2)
	}
}

func TestPublishSkillRejectsMissing(t *testing.T) {
	s, _ := newTestMarket(t)
	dir := t.TempDir() // 空目录：没有 SKILL.md
	if _, err := s.publish("skill", dir, publishOpts{ID: "bad"}); err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Fatalf("缺 SKILL.md 应被拒: %v", err)
	}
}

func writeTestComponent(t *testing.T, host bool) string {
	t.Helper()
	dir := t.TempDir()
	comp := filepath.Join(dir, "pkg-comp")
	os.MkdirAll(comp, 0755)
	man := map[string]any{"id": "demo-comp", "name": "演示组件", "version": "0.2.0", "description": "一个纯前端组件",
		"client": map[string]string{"js": "client.js", "css": "client.css"}}
	if host {
		man["host"] = true
	}
	b, _ := json.Marshal(man)
	os.WriteFile(filepath.Join(comp, "manifest.json"), b, 0644)
	os.WriteFile(filepath.Join(comp, "client.js"), []byte("CX.register({id:'demo-comp',init(){}})"), 0644)
	os.WriteFile(filepath.Join(comp, "client.css"), []byte("body{}"), 0644)
	return comp
}

func TestPublishComponent(t *testing.T) {
	s, _ := newTestMarket(t)
	entry, err := s.publish("component", writeTestComponent(t, false), publishOpts{Tags: "前端,演示"})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "demo-comp" || entry.Version != "0.2.0" || entry.Name != "演示组件" {
		t.Fatalf("组件条目不对: %#v", entry)
	}
	if len(entry.Tags) != 2 {
		t.Fatalf("标签不对: %v", entry.Tags)
	}
	// 同 id 新版本：条目被替换、旧 zip 被清
	oldZip := filepath.Join(s.itemsDir(), "component", "demo-comp", "0.2.0.zip")
	if _, err := s.publish("component", writeTestComponent(t, false), publishOpts{Version: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldZip); !os.IsNotExist(err) {
		t.Fatal("旧版本 zip 应被清理")
	}
	if _, err := os.Stat(filepath.Join(s.itemsDir(), "component", "demo-comp", "0.3.0.zip")); err != nil {
		t.Fatal("新版本 zip 应存在")
	}
	m := s.load()
	if len(m.Components) != 1 || m.Components[0].Version != "0.3.0" {
		t.Fatalf("清单应只剩新版本: %#v", m.Components)
	}
}

func TestPublishComponentHostRejected(t *testing.T) {
	s, _ := newTestMarket(t)
	_, err := s.publish("component", writeTestComponent(t, true), publishOpts{})
	if err == nil || !strings.Contains(err.Error(), "宿主半边") {
		t.Fatalf("带宿主半边的组件应被拒收: %v", err)
	}
}

func writeTestPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	plug := filepath.Join(dir, "pkg-plugin")
	os.MkdirAll(filepath.Join(plug, ".claude-plugin"), 0755)
	os.MkdirAll(filepath.Join(plug, "skills", "greet"), 0755)
	os.WriteFile(filepath.Join(plug, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"demo-plugin","version":"1.1.0","description":"演示插件"}`), 0644)
	os.WriteFile(filepath.Join(plug, "skills", "greet", "SKILL.md"), []byte("---\nname: greet\ndescription: 打招呼\n---\n正文"), 0644)
	return plug
}

func TestPublishPlugin(t *testing.T) {
	s, _ := newTestMarket(t)
	entry, err := s.publish("plugin", writeTestPlugin(t), publishOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "demo-plugin" || entry.Version != "1.1.0" {
		t.Fatalf("插件条目不对: %#v", entry)
	}
}

func TestUnpublish(t *testing.T) {
	s, _ := newTestMarket(t)
	if _, err := s.publish("skill", writeTestSkill(t, "gone", "待下架"), publishOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := s.unpublish("skill", "gone"); err != nil {
		t.Fatal(err)
	}
	if m := s.load(); len(m.Skills) != 0 {
		t.Fatalf("下架后清单应为空: %#v", m.Skills)
	}
	if _, err := os.Stat(filepath.Join(s.itemsDir(), "skill", "gone")); !os.IsNotExist(err) {
		t.Fatal("下架后包目录应被删除")
	}
	if err := s.unpublish("skill", "gone"); err == nil {
		t.Fatal("重复下架应报错")
	}
}

func TestServeItemTraversal(t *testing.T) {
	_, srv := newTestMarket(t)
	for _, p := range []string{
		"/market/items/../../../etc/passwd",
		"/market/items/skill/../..",
		"/market/items/skill/id",
		"/market/items/skill/id/x.txt",
	} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatalf("%s 不应可访问", p)
		}
	}
}

func TestMarketID(t *testing.T) {
	cases := map[string]string{
		"Demo Skill":   "Demo-Skill",
		"  x.y-z_9 ":   "x.y-z_9",
		"中文技能":         "",
		"UPPER/Case\\": "UPPER-Case",
	}
	for in, want := range cases {
		if got := marketID(in); got != want {
			t.Fatalf("marketID(%q) = %q，期待 %q", in, got, want)
		}
	}
}
