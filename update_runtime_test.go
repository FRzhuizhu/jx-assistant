package main

// runtime / skills 两块分块更新的测试。核心要锁死两件事：
//  1. 技能区热更只会动 <技能库>\_runtime 与白名单内置技能目录——技能\user 一个
//     字节都不许动（哪怕发布包里混进了 user 目录，也要整包拒绝）；
//  2. 运行时换位失败/新运行时不可用时必须回滚，旧运行时原样还在。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// newBlocksTestApp 造一个只带配置的 App（技能目录与数据目录都指向临时目录）。
func newBlocksTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "技能")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: &Config{DataDir: filepath.Join(dir, "data"), SkillsDir: skillsDir}}
	return a, skillsDir
}

// writeFileTB 写一个文件（自动建父目录）。
func writeFileTB(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// makeZip 用 name→content 生成 zip 字节。
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveZip 起一个只服务给定 zip 的测试服务器，返回 release 资产与命中计数。
func serveZip(t *testing.T, name string, data []byte) (releaseInfo, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	rel := releaseInfo{Assets: []releaseAsset{{Name: name, BrowserDownloadURL: srv.URL + "/" + name}}}
	return rel, &hits
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestApplySkillsBlockReplacesBuiltinKeepsUser(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	// 现场：一个内置技能（旧文件要消失）、一个用户技能（必须原样）
	writeFileTB(t, filepath.Join(skillsDir, "pdf", "SKILL.md"), "old-guide")
	writeFileTB(t, filepath.Join(skillsDir, "pdf", "scripts", "gone.py"), "old")
	writeFileTB(t, filepath.Join(skillsDir, "user", "微信操作", "SKILL.md"), "user-skill")
	writeFileTB(t, filepath.Join(skillsDir, "user", "微信操作", "shots", "a.png"), "user-data")

	zipData := makeZip(t, map[string]string{
		"pdf/SKILL.md":       "new-guide",
		"pdf/scripts/pdf.py": "print(1)",
	})
	rel, hits := serveZip(t, "skills-sk1.zip", zipData)
	b := &updateBlock{Version: "sk-1", File: "skills-sk1.zip"}

	a.applySkillsBlock(b, rel, "")

	if got := readMark(filepath.Join(skillsDir, skillsMarkerName)); got != "sk-1" {
		t.Fatalf("版本标记应写为 sk-1，实际 %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(skillsDir, "pdf", "SKILL.md")); string(got) != "new-guide" {
		t.Fatalf("内置技能未更新，SKILL.md = %q", got)
	}
	if exists(filepath.Join(skillsDir, "pdf", "scripts", "gone.py")) {
		t.Fatal("内置技能整目录替换后，旧文件 gone.py 应当消失（这正是热更删旧文件的能力）")
	}
	// 用户技能一个字节都不能动
	if got, _ := os.ReadFile(filepath.Join(skillsDir, "user", "微信操作", "SKILL.md")); string(got) != "user-skill" {
		t.Fatalf("用户技能被改动: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(skillsDir, "user", "微信操作", "shots", "a.png")); string(got) != "user-data" {
		t.Fatalf("用户技能数据被改动: %q", got)
	}
	// 同版本再来一轮：不该重新下载
	a.applySkillsBlock(b, rel, "")
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Fatalf("同版本应跳过下载，实际请求 %d 次", n)
	}
}

func TestApplySkillsBlockRejectsUserDirInPackage(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	writeFileTB(t, filepath.Join(skillsDir, "pdf", "SKILL.md"), "old-guide")
	writeFileTB(t, filepath.Join(skillsDir, "user", "demo", "SKILL.md"), "user-skill")

	zipData := makeZip(t, map[string]string{
		"pdf/SKILL.md":  "new-guide",
		"user/evil.txt": "should-never-land",
	})
	rel, _ := serveZip(t, "skills-bad.zip", zipData)
	a.applySkillsBlock(&updateBlock{Version: "sk-bad", File: "skills-bad.zip"}, rel, "")

	if got, _ := os.ReadFile(filepath.Join(skillsDir, "pdf", "SKILL.md")); string(got) != "old-guide" {
		t.Fatalf("含用户技能的包应整包拒绝，内置技能不该被改，实际 %q", got)
	}
	if exists(filepath.Join(skillsDir, "user", "evil.txt")) {
		t.Fatal("包内的 user/ 目录绝不能被写入")
	}
	if got := readMark(filepath.Join(skillsDir, skillsMarkerName)); got != "" {
		t.Fatalf("拒绝后不该写版本标记，实际 %q", got)
	}
}

func TestPlanSkillDirsWhitelist(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pdf", "_runtime", "user", "随便什么"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := planSkillDirs(dir); err == nil {
		t.Fatal("含 _runtime / user / 未知目录的包必须被拒绝")
	}
	ok := t.TempDir()
	for _, name := range []string{"pdf", "ocr"} {
		if err := os.MkdirAll(filepath.Join(ok, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	names, err := planSkillDirs(ok)
	if err != nil {
		t.Fatalf("纯内置技能包应当通过: %v", err)
	}
	if strings.Join(names, ",") != "ocr,pdf" {
		t.Fatalf("返回的技能名应为升序 ocr,pdf，实际 %v", names)
	}
}

func TestApplyRuntimeBlockSkipsWhenMarkerMatches(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	rt := filepath.Join(skillsDir, "_runtime")
	writeFileTB(t, filepath.Join(rt, "python", "python314._pth"), "python314.zip\n")
	writeFileTB(t, filepath.Join(rt, runtimeMarkerName), "py3.14.8\n")

	rel, hits := serveZip(t, "runtime.zip", makeZip(t, map[string]string{"python/python.exe": "x"}))
	a.applyRuntimeBlock(&updateBlock{Version: "py3.14.8", File: "runtime.zip"}, rel, "")

	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("版本一致时不该下载，实际请求 %d 次", n)
	}
	if !exists(filepath.Join(rt, "python", "python314._pth")) {
		t.Fatal("未更新时运行时目录必须原样保留")
	}
}

func TestApplyRuntimeBlockRollsBackWhenNewRuntimeBroken(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	rt := filepath.Join(skillsDir, "_runtime")
	// 旧运行时：3.12（故意与 pythonVersion 不同，逼它走更新路径）
	writeFileTB(t, filepath.Join(rt, "python", "python312._pth"), "python312.zip\n")
	writeFileTB(t, filepath.Join(rt, "python", "sentinel.txt"), "old-runtime")

	// 新运行时包结构合法，但 python.exe 是假文件 → 冒烟必失败 → 应回滚
	zipData := makeZip(t, map[string]string{
		"python/python314._pth": "python314.zip\n",
		"python/python.exe":     "not-a-real-exe",
	})
	rel, _ := serveZip(t, "runtime-py3.14.8.zip", zipData)
	a.applyRuntimeBlock(&updateBlock{Version: "py3.14.8", File: "runtime-py3.14.8.zip"}, rel, "")

	if got, _ := os.ReadFile(filepath.Join(rt, "python", "sentinel.txt")); string(got) != "old-runtime" {
		t.Fatal("新运行时冒烟失败后必须回滚到旧运行时")
	}
	if exists(filepath.Join(rt, "python", "python.exe")) {
		t.Fatal("回滚后不该留下新运行时的文件")
	}
	if got := readMark(filepath.Join(rt, runtimeMarkerName)); got != "" {
		t.Fatalf("回滚后不该写版本标记，实际 %q", got)
	}
	// 记账里也不该记成已应用
	st := a.loadUpdateBlockState()
	if st.Runtime != "" {
		t.Fatalf("回滚后不该记 runtime 版本，实际 %q", st.Runtime)
	}
}

func TestFinalizeRuntimeStateCleansStaleAndOldDirs(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	rt := filepath.Join(skillsDir, "_runtime")
	writeFileTB(t, filepath.Join(rt, "python", "python312._pth"), "python312.zip\n")
	oldDir := filepath.Join(skillsDir, runtimeOldPrefix+"1700000000")
	writeFileTB(t, filepath.Join(oldDir, "python", "python314._pth"), "python314.zip\n")
	// 用户技能必须不受影响
	writeFileTB(t, filepath.Join(skillsDir, "user", "demo", "SKILL.md"), "user-skill")

	a.FinalizeRuntimeState()

	if exists(rt) {
		t.Fatal("启动兜底应把与当前版本不符（3.12）的旧运行时整个清掉")
	}
	if exists(oldDir) {
		t.Fatal("换位残留的 _runtime.old-* 应被清理")
	}
	if !exists(filepath.Join(skillsDir, "user", "demo", "SKILL.md")) {
		t.Fatal("清理运行时不得碰到用户技能")
	}
}

func TestUpdateBlocksParsesRuntimeAndSkills(t *testing.T) {
	raw := `{"engine":{"version":"20261020-1","file":"engine.zip"},
	         "runtime":{"version":"py3.14.8","file":"runtime-py3.14.8.zip","sha256":"AB"},
	         "skills":{"version":"sk-1","file":"skills-sk-1.zip"},
	         "components":{"voice":{"version":"1.0.0","file":"voice.zip"}}}`
	var b updateBlocks
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	if b.Runtime == nil || b.Runtime.Version != "py3.14.8" || b.Runtime.File != "runtime-py3.14.8.zip" {
		t.Fatalf("runtime 块解析不对: %+v", b.Runtime)
	}
	if b.Skills == nil || b.Skills.Version != "sk-1" {
		t.Fatalf("skills 块解析不对: %+v", b.Skills)
	}
	// 老客户端只认 engine/components：多出来的字段不该影响它们的解析
	if b.Engine == nil || b.Components["voice"].Version != "1.0.0" {
		t.Fatal("engine/components 解析被破坏")
	}
}
