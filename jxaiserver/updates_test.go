// 更新分发单测：发布 → /updates/latest（Gitea 兼容形状 + 直链按请求 Host 生成）
// → /updates/file 本体下载 → 客户服务端从上游同步镜像 → 版本比较双体系。
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestUpdatesServer(t *testing.T, store *updateStore) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /updates/latest", store.handleLatest)
	mux.Handle("GET /updates/file/", http.HandlerFunc(store.handleUpdateFile))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeFakeInstaller(t *testing.T, dir, name, marker string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("FAKE-INSTALLER:"+marker), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 发布 → latest 形状（tag_name/assets 直链）→ 直链按请求 Host 生成 → 本体可下载。
func TestPublishUpdateAndServe(t *testing.T) {
	dir := t.TempDir()
	store := newUpdateStore(dir, "")
	if _, err := store.publishUpdate(writeFakeInstaller(t, dir, "坚祥数字助理GO-安装包-20261008V1.exe", "v1"), "20261008V1", "修复若干问题"); err != nil {
		t.Fatal(err)
	}
	srv := newTestUpdatesServer(t, store)

	// 未带 Host 特征的默认请求：直链应指向测试服务地址
	resp, err := srv.Client().Get(srv.URL + "/updates/latest")
	if err != nil {
		t.Fatal(err)
	}
	var rel map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rel["tag_name"] != "20261008V1" || rel["body"] != "修复若干问题" {
		t.Fatalf("latest 形状不对: %#v", rel)
	}
	assets := rel["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("应恰有一个附件: %#v", assets)
	}
	asset := assets[0].(map[string]any)
	dl, _ := asset["browser_download_url"].(string)
	if !strings.HasPrefix(dl, srv.URL+"/updates/file/20261008V1/") {
		t.Fatalf("直链应按请求 Host 生成: %s", dl)
	}
	// 自定义 Host（模拟客户端经别的地址到达）：直链跟着请求方走
	req, _ := http.NewRequest("GET", srv.URL+"/updates/latest", nil)
	req.Host = "192.0.2.210:PORT"
	resp2, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var rel2 map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&rel2)
	resp2.Body.Close()
	dl2 := rel2["assets"].([]any)[0].(map[string]any)["browser_download_url"].(string)
	if !strings.HasPrefix(dl2, "http://192.0.2.210:PORT/updates/file/") {
		t.Fatalf("直链应跟随请求 Host: %s", dl2)
	}
	// 本体可下载且内容一致
	resp3, err := srv.Client().Get(dl)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 200 || !bytes.HasPrefix(body, []byte("FAKE-INSTALLER:v1")) {
		t.Fatalf("本体下载不对: %d %q", resp3.StatusCode, body)
	}
	// 旧版本目录应被清理（只留当前版）
	if _, err := store.publishUpdate(writeFakeInstaller(t, dir, "坚祥数字助理GO-安装包-20261008V2.exe", "v2"), "20261008V2", "再修"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "updates", "files", "20261008V1")); !os.IsNotExist(err) {
		t.Fatal("旧版本包应被清理")
	}
}

// 未发布过任何版本 → 404（客户端静默视为无更新，与 Gitea 无 release 同语义）。
func TestUpdatesLatestEmpty404(t *testing.T) {
	srv := newTestUpdatesServer(t, newUpdateStore(t.TempDir(), ""))
	resp, err := srv.Client().Get(srv.URL + "/updates/latest")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("期待 404，收到 %d", resp.StatusCode)
	}
}

// 直链路径穿越/旧版本/未知名 → 404。
func TestUpdatesFileGuards(t *testing.T) {
	dir := t.TempDir()
	store := newUpdateStore(dir, "")
	if _, err := store.publishUpdate(writeFakeInstaller(t, dir, "a.exe", "x"), "20261008V1", ""); err != nil {
		t.Fatal(err)
	}
	srv := newTestUpdatesServer(t, store)
	for _, p := range []string{
		"/updates/file/",
		"/updates/file/20261008V1",
		"/updates/file/20260101V1/a.exe", // 旧版本（已清理/不存在）
		"/updates/file/20261008V1/b.exe", // 文件名不符
	} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatalf("%s 不应可下载", p)
		}
	}
	// 穿越 ../ 的路径先被 net/http 规范化重定向（301 → 清洗后的路径），
	// 处理器自身的逐段校验是对直连处理时的兜底——不跟随重定向断言它不直接命中
	noRedirect := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(srv.URL + "/updates/file/20261008V1/../20261008V1/a.exe")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("穿越路径不应直接命中文件")
	}
}

// 两级链路：总服务端发布 → 客户服务端 syncOnce 镜像（版本、说明、包本体），
// 上游无新版时不重复下载，上游 404 时不动现有版本。
func TestUpdateSyncFromUpstream(t *testing.T) {
	hqDir := t.TempDir()
	hq := newUpdateStore(hqDir, "")
	hqSrv := newTestUpdatesServer(t, hq)
	if _, err := hq.publishUpdate(writeFakeInstaller(t, hqDir, "坚祥数字助理GO-安装包-20261008V1.exe", "hq-v1"), "20261008V1", "第一版"); err != nil {
		t.Fatal(err)
	}

	local := newUpdateStore(t.TempDir(), hqSrv.URL)
	local.syncOnce()
	info, err := local.load()
	if err != nil || info.Version != "20261008V1" {
		t.Fatalf("同步后本地应为上游版本: %#v err=%v", info, err)
	}
	if !strings.Contains(info.Source, "127.0.0.1") {
		t.Fatalf("应记录上游来源: %#v", info)
	}
	localSrv := newTestUpdatesServer(t, local)
	// 客户端从「客户服务端」拿到的直链指向客户服务端自己（不是总服务端）
	resp, err := localSrv.Client().Get(localSrv.URL + "/updates/latest")
	if err != nil {
		t.Fatal(err)
	}
	var rel map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rel)
	resp.Body.Close()
	dl := rel["assets"].([]any)[0].(map[string]any)["browser_download_url"].(string)
	if !strings.HasPrefix(dl, localSrv.URL+"/updates/file/") || strings.Contains(dl, hqSrv.URL) {
		t.Fatalf("客户端直链应指向客户服务端: %s", dl)
	}
	// 本体内容与总服务端一致
	resp2, _ := localSrv.Client().Get(dl)
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !bytes.HasPrefix(body, []byte("FAKE-INSTALLER:hq-v1")) {
		t.Fatalf("镜像包内容不对: %q", body)
	}

	// 上游无新版：本地不重复下载（latest.json 的 PublishedAt 原样保留）
	before, _ := os.ReadFile(local.latestPath())
	local.syncOnce()
	after, _ := os.ReadFile(local.latestPath())
	if !bytes.Equal(before, after) {
		t.Fatal("上游无新版时本地 latest.json 不应变化")
	}

	// 上游发新版 → 再同步升级
	if _, err := hq.publishUpdate(writeFakeInstaller(t, hqDir, "坚祥数字助理GO-安装包-20261009V1.exe", "hq-v2"), "20261009V1", "第二版"); err != nil {
		t.Fatal(err)
	}
	local.syncOnce()
	if info, _ := local.load(); info.Version != "20261009V1" {
		t.Fatalf("应跟到新版本: %#v", info)
	}
	if _, err := os.Stat(filepath.Join(local.filesDir(), "20261008V1")); !os.IsNotExist(err) {
		t.Fatal("同步后旧版本包应被清理")
	}

	// 上游 404（还没发过版）：本地版本不动
	emptyUpstream := newTestUpdatesServer(t, newUpdateStore(t.TempDir(), ""))
	l2 := newUpdateStore(t.TempDir(), emptyUpstream.URL)
	l2.syncOnce()
	if info, err := l2.load(); err == nil && info.Version != "" {
		t.Fatalf("上游无版本时本地应为空: %#v", info)
	}
}

func TestVersionCompareServer(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"20261008V1", "20261008V1", 0},
		{"20261008V2", "20261008V1", 1},
		{"20261008V10", "20261008V9", 1},
		{"20261009V1", "20261008V99", 1},
		{"20260101V1", "99.0.0", 1}, // 新体系恒新
		{"1.2.3", "1.2.2", 1},
		{"1.2", "1.2.0", 0},
	}
	for _, c := range cases {
		if got := versionCompare(c.a, c.b); got != c.want {
			t.Fatalf("versionCompare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}
