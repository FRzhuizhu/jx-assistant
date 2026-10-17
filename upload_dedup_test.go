package main

// 上传判重与落位的针对性测试（uploadpath.go / server.go handleUpload 共用的那套
// 规矩）：
//   - 统一落 <工作区>/.jxai/uploads，工作区根目录不再进文件；
//   - 内容一致的重传当上传成功处理、不重复落盘（路径型与 multipart 两条路都验）；
//   - 同名不同内容（含同尺寸改一字节的反例——只比大小会误判的场景）顺延 -2；
//   - 用户的三连传场景：A → A′（存成 A-2）→ A′，第三下认出与 A-2 一致；
//   - 目录检索对 .jxai/uploads 开放，对 snapshots/tmp/delivery-backups 与工作区
//     根以外的 .jxai 仍隐藏。

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func uploadDedupFixture(t *testing.T) (*App, *SessionMeta, string) {
	t.Helper()
	a := newCompactTestApp(t)
	ws := t.TempDir()
	a.Cfg.Workspace = ws
	sess := &SessionMeta{ID: "upsess", Title: "上传判重", Mode: "task", Workspace: ws, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	return a, sess, ws
}

func writeFileT(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// listUploads 列出 .jxai/uploads 里的文件名（排序后返回）。
func listUploads(t *testing.T, ws string) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(ws, ".jxai", "uploads"))
	if err != nil {
		t.Fatalf("读上传目录失败: %v", err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// TestUploadDedupAndSuffix 路径型上传的完整判重链（含用户的三连传场景）。
func TestUploadDedupAndSuffix(t *testing.T) {
	a, sess, ws := uploadDedupFixture(t)
	src := filepath.Join(t.TempDir(), "报告.pptx")
	body := strings.Repeat("A", 300)
	writeFileT(t, src, body)

	// 第一次上传：落 .jxai/uploads，工作区根目录不新增文件
	u1, n1, err := a.registerUploadPath(sess, src)
	if err != nil {
		t.Fatalf("首次上传失败: %v", err)
	}
	want1 := filepath.Join(ws, ".jxai", "uploads", "报告.pptx")
	if u1.CopyPath != want1 {
		t.Fatalf("首次上传落位 %s，期望 %s", u1.CopyPath, want1)
	}
	if !strings.Contains(n1, "已复制一份") {
		t.Fatalf("首次注记文案异常: %s", n1)
	}
	if got := listUploads(t, ws); len(got) != 1 {
		t.Fatalf("上传目录应有 1 个文件: %v", got)
	}
	if es, _ := os.ReadDir(ws); len(es) != 1 || es[0].Name() != ".jxai" {
		t.Fatalf("工作区根目录应只有 .jxai，实得 %d 项", len(es))
	}

	// 重传同一文件：当成功处理，不重复落盘
	u2, n2, err := a.registerUploadPath(sess, src)
	if err != nil {
		t.Fatalf("重传同一文件失败: %v", err)
	}
	if u2.CopyPath != want1 {
		t.Fatalf("重传应指向既有副本 %s，实得 %s", want1, u2.CopyPath)
	}
	if !strings.Contains(n2, "内容完全一致") {
		t.Fatalf("重传注记应说明同一文件: %s", n2)
	}
	if got := listUploads(t, ws); len(got) != 1 {
		t.Fatalf("重传不应新增文件: %v", got)
	}

	// 同名不同内容（同尺寸，只改末字节——只比大小会误判的场景）：顺延 -2，原文件不动
	mod := strings.Repeat("A", 299) + "B"
	writeFileT(t, src, mod)
	u3, n3, err := a.registerUploadPath(sess, src)
	if err != nil {
		t.Fatalf("上传修改版失败: %v", err)
	}
	want3 := filepath.Join(ws, ".jxai", "uploads", "报告-2.pptx")
	if u3.CopyPath != want3 {
		t.Fatalf("修改版应顺延为 %s，实得 %s", want3, u3.CopyPath)
	}
	if !strings.Contains(n3, "报告-2.pptx") {
		t.Fatalf("修改版注记应写明顺延名: %s", n3)
	}
	if b, _ := os.ReadFile(want1); string(b) != body {
		t.Fatalf("原有文件被改动")
	}
	if b, _ := os.ReadFile(want3); string(b) != mod {
		t.Fatalf("-2 副本内容不对")
	}

	// 第三次传与第二次相同的文件：认出与 A-2 一致（不是又存一份 -3）
	u4, n4, err := a.registerUploadPath(sess, src)
	if err != nil {
		t.Fatalf("第三次上传失败: %v", err)
	}
	if u4.CopyPath != want3 {
		t.Fatalf("第三次应认出与第二次相同（%s），实得 %s", want3, u4.CopyPath)
	}
	if !strings.Contains(n4, "内容完全一致") {
		t.Fatalf("第三次注记应说明同一文件: %s", n4)
	}
	if got := listUploads(t, ws); len(got) != 2 {
		t.Fatalf("三连传后应只有 2 个文件: %v", got)
	}

	// 登记表口径：4 笔登记、原件路径都在、编号递增
	ups, err := a.Store.UploadsFor(sess.ID)
	if err != nil || len(ups) != 4 {
		t.Fatalf("登记表应有 4 笔: %v %d", err, len(ups))
	}
	for i, u := range ups {
		if u.UID != "u"+string(rune('1'+i)) {
			t.Fatalf("第 %d 笔编号 %s 异常", i+1, u.UID)
		}
		if u.OrigPath != src || u.Name != "报告.pptx" {
			t.Fatalf("第 %d 笔登记异常: %+v", i+1, u)
		}
	}
}

// postMultipart 以 multipart 载荷直调 handleUpload（与浏览器/手机/粘贴同一条路）。
func postMultipart(t *testing.T, a *App, sess *SessionMeta, name, content string) (string, string) {
	t.Helper()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		w, _ := mw.CreateFormFile("file", name)
		_, _ = w.Write([]byte(content))
		_ = mw.Close()
		_ = pw.Close()
	}()
	r := httptest.NewRequest("POST", "/api/sessions/"+sess.ID+"/upload", pr)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetPathValue("id", sess.ID)
	w := httptest.NewRecorder()
	a.handleUpload(w, r)
	if w.Code != 200 {
		t.Fatalf("multipart 上传失败: HTTP %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK   int    `json:"ok"`
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if resp.OK != 1 {
		t.Fatalf("响应 ok!=1: %s", w.Body.String())
	}
	// 注记是 handler 落库的，从库里取最后一条 origin=upload 的行
	rows, err := a.Store.RawMessagesAll(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	note := ""
	for _, m := range rows {
		if m.Origin == "upload" {
			if s, ok := m.Content.(string); ok {
				note = s
			}
		}
	}
	return resp.Path, note
}

// TestUploadMultipartDedupAndRegister multipart 路：判重、顺延、登记与注记口径。
func TestUploadMultipartDedupAndRegister(t *testing.T) {
	a, sess, ws := uploadDedupFixture(t)

	p1, note1 := postMultipart(t, a, sess, "名单.xlsx", strings.Repeat("X", 64))
	if want := ".jxai/uploads/名单.xlsx"; !strings.HasSuffix(filepath.ToSlash(p1), want) {
		t.Fatalf("multipart 首传落位 %s，应落在 %s 下", p1, want)
	}
	if !strings.Contains(note1, "已保存到工作区") {
		t.Fatalf("首传注记异常: %s", note1)
	}

	// 同内容重传（multipart↔multipart）：去重，不新增文件
	p2, note2 := postMultipart(t, a, sess, "名单.xlsx", strings.Repeat("X", 64))
	if p2 != p1 {
		t.Fatalf("同内容重传应复用 %s，实得 %s", p1, p2)
	}
	if !strings.Contains(note2, "内容完全一致") {
		t.Fatalf("重传注记应说明同一文件: %s", note2)
	}

	// 同名不同内容：顺延 -2
	p3, note3 := postMultipart(t, a, sess, "名单.xlsx", strings.Repeat("Y", 64))
	if !strings.HasSuffix(filepath.ToSlash(p3), ".jxai/uploads/名单-2.xlsx") {
		t.Fatalf("不同内容应顺延 -2: %s", p3)
	}
	if !strings.Contains(note3, "名单-2.xlsx") {
		t.Fatalf("顺延注记应写明新名: %s", note3)
	}

	// 路径型 ↔ multipart 跨路判重：同一内容从桌面路径再来一次，也认得出
	src := filepath.Join(t.TempDir(), "名单.xlsx")
	writeFileT(t, src, strings.Repeat("Y", 64))
	u, _, err := a.registerUploadPath(sess, src)
	if err != nil {
		t.Fatalf("路径上传失败: %v", err)
	}
	if filepath.ToSlash(u.CopyPath) != filepath.ToSlash(p3) {
		t.Fatalf("跨路判重应命中 %s，实得 %s", p3, u.CopyPath)
	}

	if got := listUploads(t, ws); len(got) != 2 {
		t.Fatalf("全程应只有 2 个文件: %v", got)
	}
	// multipart 也入登记表（原件路径为空、名字保持用户上传名）
	ups, _ := a.Store.UploadsFor(sess.ID)
	if len(ups) != 4 {
		t.Fatalf("登记表应有 4 笔（3 笔 multipart + 1 笔路径），实得 %d", len(ups))
	}
	for i, up := range ups[:3] {
		if up.OrigPath != "" || up.Name != "名单.xlsx" {
			t.Fatalf("第 %d 笔 multipart 登记异常: %+v", i+1, up)
		}
	}
}

// TestSuffixedName 顺延名的序号位置（扩展名前）与无扩展名情形。
func TestSuffixedName(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"报告.pptx", 2, "报告-2.pptx"},
		{"a.b.c.txt", 3, "a.b.c-3.txt"},
		{"README", 2, "README-2"},
		{"归档.tar.gz", 2, "归档.tar-2.gz"},
	}
	for _, c := range cases {
		if got := suffixedName(c.in, c.n); got != c.want {
			t.Fatalf("suffixedName(%q,%d)=%s，期望 %s", c.in, c.n, got, c.want)
		}
	}
}

// TestJxaiHiddenRelAndWalkVisibility 检索边界：.jxai/uploads 可见，三个系统子目录
// 与工作区根以外的 .jxai 仍隐藏。
func TestJxaiHiddenRelAndWalkVisibility(t *testing.T) {
	if !jxaiHiddenRel(".jxai/tmp") || !jxaiHiddenRel(".jxai/tmp/sub/x.py") ||
		!jxaiHiddenRel(".jxai/snapshots") || !jxaiHiddenRel(".jxai/delivery-backups/1/x") {
		t.Fatal("系统子目录（含嵌套）应判隐藏")
	}
	if jxaiHiddenRel(".jxai/uploads") || jxaiHiddenRel(".jxai/uploads/A.pptx") || jxaiHiddenRel(".jxai/other.txt") {
		t.Fatal("uploads 与 .jxai 其余内容不应隐藏")
	}

	a, sess, ws := uploadDedupFixture(t)
	writeFileT(t, filepath.Join(ws, ".jxai", "uploads", "上传可见.txt"), "v")
	writeFileT(t, filepath.Join(ws, ".jxai", "tmp", "隐藏.txt"), "h")
	writeFileT(t, filepath.Join(ws, ".jxai", "snapshots", "x.bin"), "s")
	writeFileT(t, filepath.Join(ws, ".jxai", "delivery-backups", "y.bin"), "d")
	writeFileT(t, filepath.Join(ws, "普通文件.txt"), "n")
	writeFileT(t, filepath.Join(ws, "sub", ".jxai", "deep.txt"), "deep")

	tctx := &toolContext{app: a, sess: sess}
	var rels []string
	if err := tctx.envWalk(ws, func(rel string, e envEntry) error {
		rels = append(rels, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	has := func(p string) bool {
		for _, r := range rels {
			if r == p {
				return true
			}
		}
		return false
	}
	if !has(".jxai/uploads/上传可见.txt") || !has("普通文件.txt") {
		t.Fatalf("uploads 与根文件应可见: %v", rels)
	}
	if has(".jxai/tmp/隐藏.txt") || has(".jxai/snapshots/x.bin") || has(".jxai/delivery-backups/y.bin") || has("sub/.jxai/deep.txt") {
		t.Fatalf("系统子目录与根外 .jxai 应隐藏: %v", rels)
	}
}
