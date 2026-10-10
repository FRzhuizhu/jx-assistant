package main

// 市场发布单测（market_publish.go）：假市场服务器的 /admin/api/publish 收端，
// 覆盖 zip 透传、文件夹打包、版本号解析、各种拒绝路径与元信息补全。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/* ---------- 假市场（清单 + 发布端点） ---------- */

type fakePublishMarket struct {
	srv    *httptest.Server
	fields map[string]string
	file   string
	zip    []byte
	code   int
	errMsg string
	hits   int
}

func newFakePublishMarket(t *testing.T) *fakePublishMarket {
	t.Helper()
	fm := &fakePublishMarket{fields: map[string]string{}, code: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /market/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"测试市场","updatedAt":"2026-10-09T00:00:00Z","skills":[]}`)
	})
	mux.HandleFunc("POST /admin/api/publish", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		fm.hits++
		for _, k := range []string{"type", "id", "name", "version", "desc", "tags"} {
			fm.fields[k] = r.FormValue(k)
		}
		if f, hdr, err := r.FormFile("file"); err == nil {
			fm.file = hdr.Filename
			fm.zip, _ = io.ReadAll(f)
			f.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		if fm.code >= 400 {
			w.WriteHeader(fm.code)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": fm.errMsg})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entry": map[string]any{
			"id": fm.fields["id"], "version": fm.fields["version"],
			"zip": "/market/items/skill/" + fm.fields["id"] + "/" + fm.fields["version"] + ".zip",
		}})
	})
	fm.srv = httptest.NewServer(mux)
	t.Cleanup(fm.srv.Close)
	return fm
}

func newPublishTestApp(t *testing.T, marketURL string) *App {
	return newMarketTestApp(t, marketURL) // builtinSkillIDs 是包级常量表（pdf/ocr/office/…），无需另外点
}

/* ---------- 请求/包体构造 ---------- */

type pubFile struct {
	name string
	data []byte
}

func publishReq(t *testing.T, a *App, meta any, files []pubFile) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := mw.WriteField("meta", string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if len(files) > 0 {
		// 包内相对路径单独给（multipart 的 filename 只保留 basename）
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.name)
		}
		b, err := json.Marshal(names)
		if err != nil {
			t.Fatal(err)
		}
		if err := mw.WriteField("paths", string(b)); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		base := f.name
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		w, err := mw.CreateFormFile("files", base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/market/publish", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	a.handleMarketPublish(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func zipBytesForTest(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipNamesForTest(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("发布出去的包不是合法 zip: %v", err)
	}
	out := []string{}
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

const okSkillMD = "---\nname: wechat-chat-ops\ndescription: 驱动真实微信 PC 端\n---\n\n正文\n"

/* ---------- 名字 → 版本号（用户定的规则） ---------- */

func TestSkillVersionFromName(t *testing.T) {
	cases := map[string]string{
		"微信操作_技能包_v1.3.1_20261009.zip": "1.3.1", // v 后一定是版本号，_时间戳不是
		"微信操作_技能包_v1.3.0_20261009.zip": "1.3.0",
		"wechat-chat-ops-1.2.0.zip":    "1.2.0", // 无 v 的裸版本
		"工具_v2_20261009.zip":           "2",
		"技能包_1.3.1+20261009.zip":       "1.3.1", // +时间戳只取版本本体
		"技能包-v1.3.1-20261009.zip":      "1.3.1",
		"site-map_v10.20.30.zip":       "10.20.30",
		"没有版本的技能包.zip":                 "",
		"OCR 识别-v0.9.zip":              "0.9",
	}
	for in, want := range cases {
		if got := skillVersionFromName(in); got != want {
			t.Errorf("skillVersionFromName(%q) = %q，期待 %q", in, got, want)
		}
	}
}

func TestSkillIDFromName(t *testing.T) {
	cases := map[string]string{
		"微信操作_技能包_v1.3.1_20261009.zip": "微信操作_技能包",
		"wechat-chat-ops-1.2.0.zip":    "wechat-chat-ops",
		"APK":                          "APK",
		"Office 文档/":                   "Office 文档",
		"技能包":                          "技能包",
	}
	for in, want := range cases {
		if got := skillIDFromName(in); got != want {
			t.Errorf("skillIDFromName(%q) = %q，期待 %q", in, got, want)
		}
	}
}

/* ---------- 正常发布 ---------- */

func TestPublishSkillZipPassthrough(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{
		"微信操作/SKILL.md":       okSkillMD,
		"微信操作/scripts/run.py": "print(1)\n",
	})
	code, out := publishReq(t, a, map[string]any{
		"id": "wechat-chat-ops", "name": "微信操作", "version": "1.3.1",
		"description": "描述来自前端", "tags": []string{"办公", "自动化"}, "zip": true,
	}, []pubFile{{name: "微信操作_技能包_v1.3.1_20261009.zip", data: pack}})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if fm.fields["type"] != "skill" || fm.fields["id"] != "wechat-chat-ops" || fm.fields["version"] != "1.3.1" {
		t.Fatalf("转发字段不对: %#v", fm.fields)
	}
	if fm.fields["desc"] != "描述来自前端" || fm.fields["tags"] != "办公,自动化" {
		t.Fatalf("描述/标签转发不对: %#v", fm.fields)
	}
	if !bytes.Equal(fm.zip, pack) {
		t.Fatalf("zip 透传后字节不一致（收到 %d 字节，原 %d 字节）", len(fm.zip), len(pack))
	}
	if out["sha256"] == "" || out["size"].(float64) != float64(len(pack)) {
		t.Fatalf("回执缺 sha256/size: %v", out)
	}
	if out["skillMd"] != "微信操作/SKILL.md" {
		t.Fatalf("包内 SKILL.md 路径识别不对: %v", out["skillMd"])
	}
}

func TestPublishSkillFolderPacking(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	// 前端已剥掉顶层目录：包内相对路径从 SKILL.md 开始
	code, out := publishReq(t, a, map[string]any{
		"id": "wx-ops", "version": "2.0.0", "description": "文件夹投喂", "zip": false,
	}, []pubFile{
		{name: "SKILL.md", data: []byte("---\nname: 微信操作\ndescription: 来自 SKILL.md\n---\n\n把聊天记录导出成 Excel。\n")},
		{name: "scripts/run.py", data: []byte("print(1)\n")},
	})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	names := zipNamesForTest(t, fm.zip)
	if len(names) != 2 || !hasZipEntry(names, "SKILL.md") || !hasZipEntry(names, "scripts/run.py") {
		t.Fatalf("打包结果不对: %v", names)
	}
	if fm.fields["name"] != "微信操作" || fm.fields["desc"] != "文件夹投喂" {
		t.Fatalf("元信息补全不对: %#v", fm.fields)
	}
	if out["skillMd"] != "SKILL.md" {
		t.Fatalf("skillMd 应为根部的 SKILL.md: %v", out["skillMd"])
	}
}

// 版本号/名称/id 都从包本身补全：文件名给版本，包内目录给 id，SKILL.md 给名称与描述。
func TestPublishSkillMetaFallback(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{"微信操作/SKILL.md": okSkillMD})
	code, out := publishReq(t, a, map[string]any{"zip": true},
		[]pubFile{{name: "微信操作_技能包_v1.3.1_20261009.zip", data: pack}})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if out["id"] != "wechat-chat-ops" {
		t.Fatalf("id 应回退到 SKILL.md 的英文名，实得 %v", out["id"])
	}
	if out["version"] != "1.3.1" {
		t.Fatalf("版本号应从文件名解析，实得 %v", out["version"])
	}
	if fm.fields["name"] != "wechat-chat-ops" {
		t.Fatalf("名称应从 SKILL.md frontmatter 补全: %#v", fm.fields)
	}
	if fm.file != "wechat-chat-ops-1.3.1.zip" {
		t.Fatalf("发布包名不对: %s", fm.file)
	}
}

func TestPublishSkillKeepsCacheInvalidated(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	if _, err := a.fetchMarketManifest(a.Cfg.MarketURL, false); err != nil {
		t.Fatal(err)
	}
	if a.marketCache.man == nil {
		t.Fatal("前置：清单缓存应已填充")
	}
	pack := zipBytesForTest(t, map[string]string{"SKILL.md": okSkillMD})
	if code, out := publishReq(t, a, map[string]any{"id": "x", "version": "1.0.0", "zip": true},
		[]pubFile{{name: "x.zip", data: pack}}); code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if a.marketCache.man != nil {
		t.Fatal("发布成功后应清掉清单缓存，否则前端刷新看不到新条目")
	}
}

/* ---------- 拒绝路径 ---------- */

func TestPublishSkillRejectsMissingSkillMD(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{"微信操作/readme.md": "没有 SKILL.md"})
	code, out := publishReq(t, a, map[string]any{"id": "x", "zip": true}, []pubFile{{name: "x.zip", data: pack}})
	if code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "SKILL.md") {
		t.Fatalf("缺 SKILL.md 应 400，实得 %d：%v", code, out)
	}
	if fm.hits != 0 {
		t.Fatal("校验没过就不该打扰市场服务器")
	}
}

func TestPublishSkillRejectsTwoTopDirs(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{
		"技能A/SKILL.md": okSkillMD,
		"技能B/SKILL.md": okSkillMD,
	})
	code, out := publishReq(t, a, map[string]any{"id": "x", "zip": true}, []pubFile{{name: "x.zip", data: pack}})
	if code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "处 SKILL.md") {
		t.Fatalf("一个包装了多个技能应 400，实得 %d：%v", code, out)
	}
}

func TestPublishSkillRejectsBuiltinIDAndBadZip(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{"SKILL.md": okSkillMD})
	if code, out := publishReq(t, a, map[string]any{"id": "pdf", "zip": true},
		[]pubFile{{name: "x.zip", data: pack}}); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "自带技能") {
		t.Fatalf("自带技能 id 应 400，实得 %d：%v", code, out)
	}
	if code, out := publishReq(t, a, map[string]any{"id": "x", "zip": true},
		[]pubFile{{name: "x.zip", data: []byte("这不是 zip")}}); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "zip") {
		t.Fatalf("坏包应 400，实得 %d：%v", code, out)
	}
}

func TestPublishSkillUnconfiguredAndServerReject(t *testing.T) {
	a := newPublishTestApp(t, "")
	pack := zipBytesForTest(t, map[string]string{"SKILL.md": okSkillMD})
	if code, out := publishReq(t, a, map[string]any{"id": "x", "zip": true},
		[]pubFile{{name: "x.zip", data: pack}}); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "未配置市场服务器") {
		t.Fatalf("未配置市场应 400，实得 %d：%v", code, out)
	}

	fm := newFakePublishMarket(t)
	fm.code, fm.errMsg = 409, "同名版本已存在"
	b := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	code, out := publishReq(t, b, map[string]any{"id": "x", "version": "1.0.0", "zip": true},
		[]pubFile{{name: "x.zip", data: pack}})
	if code != 409 || !strings.Contains(fmt.Sprint(out["error"]), "同名版本已存在") {
		t.Fatalf("应原样转达市场服务器的拒绝，实得 %d：%v", code, out)
	}
}

// 真实技能包的形状：外层包目录 + 安装说明.md + 内层技能目录 → 规整成「技能目录在包根」
func TestPublishSkillNestedPack(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	outer := "微信操作_技能包_v1.3.1_20261009/"
	pack := zipBytesForTest(t, map[string]string{
		outer + "安装说明.md":             "# 安装说明\n",
		outer + "微信操作/SKILL.md":       "---\nname: wechat-chat-ops\ndescription: 嵌套包\n---\n\n正文\n",
		outer + "微信操作/manifest.json":  `{"id":"微信操作","name":"微信操作","version":"1.3.1"}`,
		outer + "微信操作/scripts/run.py": "print(1)\n",
	})
	code, out := publishReq(t, a, map[string]any{"zip": true},
		[]pubFile{{name: "微信操作_技能包_v1.3.1_20261009.zip", data: pack}})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if out["normalized"] != true {
		t.Fatalf("嵌套包应被规整: %v", out["normalized"])
	}
	names := zipNamesForTest(t, fm.zip)
	want := []string{"微信操作/SKILL.md", "微信操作/manifest.json", "微信操作/scripts/run.py", "微信操作/安装说明.md"}
	for _, w := range want {
		if !hasZipEntry(names, w) {
			t.Fatalf("规整后的包里缺 %s：%v", w, names)
		}
	}
	for _, n := range names {
		if strings.HasPrefix(n, "微信操作_技能包") {
			t.Fatalf("外层包目录应被剥掉: %v", names)
		}
	}
	if out["id"] != "wechat-chat-ops" || out["version"] != "1.3.1" {
		t.Fatalf("元信息不对: %v", out)
	}
	if fm.fields["name"] != "微信操作" {
		t.Fatalf("名称应从包内 manifest.json 补全: %#v", fm.fields)
	}
}

func TestPublishSkillNormalizesStrayRootFiles(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	// 「技能目录 + 根级散文件」：市场服务器会拒，后端应把散文件并进技能目录再发
	pack := zipBytesForTest(t, map[string]string{
		"微信操作/SKILL.md":     okSkillMD,
		"微信操作/scripts/x.py": "print(1)\n",
		"安装说明.md":           "# 说明\n",
	})
	code, out := publishReq(t, a, map[string]any{"zip": true},
		[]pubFile{{name: "微信操作_技能包_v1.3.1_20261009.zip", data: pack}})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if out["normalized"] != true {
		t.Fatalf("应标记为已规整: %v", out["normalized"])
	}
	names := zipNamesForTest(t, fm.zip)
	if !hasZipEntry(names, "微信操作/安装说明.md") {
		t.Fatalf("根级散文件应并入技能目录: %v", names)
	}
	tops := map[string]bool{}
	for _, n := range names {
		if i := strings.Index(n, "/"); i > 0 {
			tops[n[:i]] = true
		} else {
			tops[n] = true
		}
	}
	if len(tops) != 1 || !tops["微信操作"] {
		t.Fatalf("规整后包根应只剩技能目录: %v", names)
	}
}

func TestPublishSkillNoFiles(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	if code, out := publishReq(t, a, map[string]any{"id": "x"}, nil); code != 400 {
		t.Fatalf("没有文件应 400，实得 %d：%v", code, out)
	}
}
