package main

// 用户技能管理单测（skill_manager.go）：displayName 中文名体系、环境快照的
// 显示名优先与描述截断、安装口的内容体检（空 SKILL.md 拦截）、文件夹拖拽
// 安装端点（/api/skills/install-files）。

import (
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSkillTestApp(t *testing.T) *App {
	t.Helper()
	return newMarketTestApp(t, "") // 只用它的 DataDir/SkillsDir 骨架
}

func TestSkillMetaParsesDisplayName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: wechat-chat-ops\ndisplayName: 微信操作\ndescription: 驱动微信\nversion: 1.2.0\n---\n\n正文"), 0644)
	fm := skillMeta(dir)
	if fm.Name != "wechat-chat-ops" || fm.DisplayName != "微信操作" || fm.Description != "驱动微信" || fm.Version != "1.2.0" {
		t.Fatalf("frontmatter 解析不对: %#v", fm)
	}
	if fm.Body != "正文" {
		t.Fatalf("正文不对: %q", fm.Body)
	}
	// 显示名回退：displayName → name → id
	if got := skillDisplayOf("", "fallback-name", "id"); got != "fallback-name" {
		t.Fatalf("回退到 name 不对: %q", got)
	}
	if got := skillDisplayOf(" ", "", "the-id"); got != "the-id" {
		t.Fatalf("回退到 id 不对: %q", got)
	}
}

func TestListSkillsCarriesDisplayNameAndVersion(t *testing.T) {
	a := newSkillTestApp(t)
	userDir, _ := a.userSkillsDir()
	skill := filepath.Join(userDir, "wechat-chat-ops")
	os.MkdirAll(skill, 0755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: wechat-chat-ops\ndisplayName: 微信操作\ndescription: 驱动微信\nversion: 2.0.0\n---\n\n正文"), 0644)
	list, err := a.listSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.User) != 1 {
		t.Fatalf("应识别 1 个用户技能: %#v", list.User)
	}
	s := list.User[0]
	if s.DisplayName != "微信操作" || s.Version != "2.0.0" || s.ID != "wechat-chat-ops" {
		t.Fatalf("清单字段不对: %#v", s)
	}
}

func TestUserSkillsPromptBlockDisplayNameAndTruncation(t *testing.T) {
	a := newSkillTestApp(t)
	userDir, _ := a.userSkillsDir()
	skill := filepath.Join(userDir, "wx")
	os.MkdirAll(skill, 0755)
	long := strings.Repeat("长", 260)
	os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: wx\ndisplayName: 微信操作\ndescription: "+long+"\n---\n\n正文"), 0644)
	block := a.userSkillsPromptBlock()
	if !strings.Contains(block, "微信操作（id=wx）") {
		t.Fatalf("快照应优先 displayName: %q", block)
	}
	if strings.Contains(block, long) {
		t.Fatal("超长描述应被截断，不应整段进环境快照")
	}
	if !strings.Contains(block, "…") {
		t.Fatal("截断应带省略号")
	}
	if r := []rune(strings.TrimSpace(block)); len(r) > 400 { // 快照本身不该膨胀
		t.Fatalf("快照过长: %d runes", len(r))
	}
}

func TestAddUserSkillRejectsEmptyBody(t *testing.T) {
	a := newSkillTestApp(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: empty\n---\n"), 0644)
	if _, err := a.addUserSkill(dir); err == nil || !strings.Contains(err.Error(), "正文") {
		t.Fatalf("只有 frontmatter 的 SKILL.md 应在安装口被拦: %v", err)
	}
	// zip 同理（市场装下来的空包也进不来）
	zipPath := filepath.Join(dir, "..", "empty.zip")
	if err := os.WriteFile(zipPath, zipBytesForTest(t, map[string]string{"SKILL.md": ""}), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.addUserSkill(zipPath); err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Fatalf("空 SKILL.md 的 zip 应被拦: %v", err)
	}
}

func TestInstallSkillFilesEndpoint(t *testing.T) {
	a := newSkillTestApp(t)
	// multipart：一个顶层目录（=拖入的技能文件夹）+ SKILL.md/脚本。
	// files 用有序切片：multipart 的 filename 只剩 basename，与 paths 按下标配对，
	// 用 map 迭代会随机错位（真实前端按 paths 顺序 append）。
	var body = &strings.Builder{}
	mw := multipart.NewWriter(body)
	type tf struct{ path, content string }
	files := []tf{
		{"微信操作/SKILL.md", "---\nname: wx\ndisplayName: 微信操作\ndescription: 测试\n---\n\n正文"},
		{"微信操作/scripts/run.py", "print(1)"},
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.path)
	}
	pb, _ := json.Marshal(paths)
	_ = mw.WriteField("paths", string(pb))
	for _, f := range files {
		fw, _ := mw.CreateFormFile("files", filepath.Base(f.path))
		fw.Write([]byte(f.content))
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/skills/install-files", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	a.handleInstallSkillFiles(rec, req)
	if rec.Code != 200 {
		t.Fatalf("期待 200，收到 %d：%s", rec.Code, rec.Body.String())
	}
	out := userSkillEntry{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "微信操作" || out.DisplayName != "微信操作" {
		t.Fatalf("安装结果不对: %#v", out)
	}
	// 装到 技能/user/ 了
	if _, err := os.Stat(filepath.Join(mustUserDir(t, a), "微信操作", "scripts", "run.py")); err != nil {
		t.Fatal("文件应落进技能目录")
	}

	// 散文件（无公共顶层目录）拒收
	var body2 = &strings.Builder{}
	mw2 := multipart.NewWriter(body2)
	pb2, _ := json.Marshal([]string{"SKILL.md", "run.py"})
	_ = mw2.WriteField("paths", string(pb2))
	for _, p := range []string{"SKILL.md", "run.py"} {
		fw, _ := mw2.CreateFormFile("files", p)
		fw.Write([]byte("x"))
	}
	mw2.Close()
	req2 := httptest.NewRequest(http.MethodPost, "/api/skills/install-files", strings.NewReader(body2.String()))
	req2.Header.Set("Content-Type", mw2.FormDataContentType())
	rec2 := httptest.NewRecorder()
	a.handleInstallSkillFiles(rec2, req2)
	if rec2.Code != 400 || !strings.Contains(rec2.Body.String(), "整个技能文件夹") {
		t.Fatalf("散文件应 400，实得 %d：%s", rec2.Code, rec2.Body.String())
	}

	// 路径穿越拒收
	var body3 = &strings.Builder{}
	mw3 := multipart.NewWriter(body3)
	pb3, _ := json.Marshal([]string{"pkg/SKILL.md", "pkg/../../evil.py"})
	_ = mw3.WriteField("paths", string(pb3))
	for _, p := range []string{"SKILL.md", "evil.py"} {
		fw, _ := mw3.CreateFormFile("files", p)
		fw.Write([]byte("x"))
	}
	mw3.Close()
	req3 := httptest.NewRequest(http.MethodPost, "/api/skills/install-files", strings.NewReader(body3.String()))
	req3.Header.Set("Content-Type", mw3.FormDataContentType())
	rec3 := httptest.NewRecorder()
	a.handleInstallSkillFiles(rec3, req3)
	if rec3.Code != 400 {
		t.Fatalf("路径穿越应 400，实得 %d：%s", rec3.Code, rec3.Body.String())
	}
}

func mustUserDir(t *testing.T, a *App) string {
	t.Helper()
	d, err := a.userSkillsDir()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
