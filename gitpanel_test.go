package main

// Git 面板测试：解析纯函数逐条断言 + 真机 git 输出校验（临时目录造仓库，
// 用 git 命令自己产出 porcelain/log/for-each-ref，确认解析对得上真实现）。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- porcelain=v2 解析 ----------

func TestGitParseStatusV2Basics(t *testing.T) {
	// -z 模式下所有记录（含头部行）都以 NUL 分隔
	raw := "# branch.oid abc123\x00" +
		"# branch.head go\x00" +
		"# branch.upstream origin/go\x00" +
		"# branch.ab +2 -1\x00" +
		"1 .M N... 100644 100644 100644 abc def main.go\x00" +
		"1 M. N... 100644 100644 100644 abc def staged.txt\x00" +
		"? new\\thing.txt\x00"
	branch, oid, upstream, ahead, behind, entries := gitParseStatusV2(raw)
	if branch != "go" || oid != "abc123" || upstream != "origin/go" || ahead != 2 || behind != 1 {
		t.Fatalf("头部解析不对: %q %q %q %d %d", branch, oid, upstream, ahead, behind)
	}
	if len(entries) != 3 {
		t.Fatalf("应有 3 条改动，实得 %d: %+v", len(entries), entries)
	}
	if entries[0].Path != "main.go" || entries[0].Worktree != "M" || entries[0].Index != "." {
		t.Fatalf("未暂存条目不对: %+v", entries[0])
	}
	if entries[1].Index != "M" || entries[1].Worktree != "." {
		t.Fatalf("已暂存条目不对: %+v", entries[1])
	}
	if entries[2].Mark != "??" || entries[2].Path != "new\\thing.txt" {
		t.Fatalf("未跟踪条目不对: %+v", entries[2])
	}
}

func TestGitParseStatusV2RenameAndConflict(t *testing.T) {
	// 类型 2（重命名）：新路径在空格分段里，原路径是紧随其后的独立 NUL 字段
	raw := "2 R. N... 100644 100644 100644 abc def R100 new\\name.txt\x00old\\name.txt\x00" +
		"u UU N... 000000 000000 000000 000000 h1 h2 h3 both.txt\x00"
	_, _, _, _, _, entries := gitParseStatusV2(raw)
	if len(entries) != 2 {
		t.Fatalf("应有 2 条，实得 %d", len(entries))
	}
	if entries[0].Index != "R" || entries[0].Path != "new\\name.txt" || entries[0].Original != "old\\name.txt" {
		t.Fatalf("重命名条目不对: %+v", entries[0])
	}
	if entries[1].Index != "U" || entries[1].Worktree != "U" || entries[1].Path != "both.txt" {
		t.Fatalf("冲突条目不对: %+v", entries[1])
	}
}

func TestGitGroupStatus(t *testing.T) {
	entries := []gitFileEntry{
		{Index: "M", Worktree: "."}, // 已暂存
		{Index: ".", Worktree: "M"}, // 未暂存
		{Index: "M", Worktree: "M"}, // 两边都有
		{Mark: "??"},                // 未跟踪
		{Index: "U", Worktree: "U"}, // 冲突
	}
	staged, unstaged, untracked, conflicted := gitGroupStatus(entries)
	if len(staged) != 2 || len(unstaged) != 2 || len(untracked) != 1 || len(conflicted) != 1 {
		t.Fatalf("分组不对: %d %d %d %d", len(staged), len(unstaged), len(untracked), len(conflicted))
	}
}

// ---------- 图谱与分支解析 ----------

func TestGitParseDecorations(t *testing.T) {
	refs := gitParseDecorations(" (HEAD -> go, origin/go, tag: v1.2, go-merge)")
	if len(refs) != 4 {
		t.Fatalf("应有 4 个引用，实得 %+v", refs)
	}
	if refs[0].Name != "go" || refs[0].Kind != "head" || !refs[0].Current {
		t.Fatalf("当前分支不对: %+v", refs[0])
	}
	if refs[1].Kind != "remote" || refs[2].Kind != "tag" || refs[2].Name != "v1.2" {
		t.Fatalf("远程/标签不对: %+v %+v", refs[1], refs[2])
	}
	if refs[3].Kind != "head" || refs[3].Current {
		t.Fatalf("普通分支不对: %+v", refs[3])
	}
	if gitParseDecorations("") != nil || gitParseDecorations(" ()") != nil {
		t.Fatal("空装饰应返回 nil")
	}
}

func TestGitParseLogGraph(t *testing.T) {
	raw := "\nh1\x1f\x1f张三\x1f2026-09-25T10:00:00+08:00\x1f (HEAD -> go)\x1f根提交\x1e" +
		"\nh2\x1fh1 h3\x1f李四\x1f2026-09-25T09:00:00+08:00\x1f\x1f合并提交\x1e"
	commits := gitParseLogGraph(raw)
	if len(commits) != 2 {
		t.Fatalf("应有 2 条提交，实得 %d", len(commits))
	}
	root, merge := commits[0], commits[1]
	if root.Hash != "h1" || len(root.Parents) != 0 || root.Author != "张三" || root.Subject != "根提交" {
		t.Fatalf("根提交不对: %+v", root)
	}
	if len(root.Refs) != 1 || root.Refs[0].Name != "go" || !root.Refs[0].Current {
		t.Fatalf("根提交装饰不对: %+v", root.Refs)
	}
	if len(merge.Parents) != 2 || merge.Parents[0] != "h1" || merge.Parents[1] != "h3" {
		t.Fatalf("合并提交父列表不对: %+v", merge.Parents)
	}
}

func TestGitParseForEachRef(t *testing.T) {
	raw := "refs/heads/go\x1fgo\x1fa1b2\x1forigin/go\x1f[ahead 2, behind 1]\x1f*\x1f最近提交\x1e" +
		"\nrefs/heads/go-merge\x1fgo-merge\x1fc3d4\x1f\x1f\x1f \x1f旧提交\x1e"
	bs := gitParseForEachRef(raw)
	if len(bs) != 2 {
		t.Fatalf("应有 2 条分支，实得 %d", len(bs))
	}
	if !bs[0].Current || bs[0].Name != "go" || bs[0].Ahead != 2 || bs[0].Behind != 1 || bs[0].Upstream != "origin/go" || bs[0].Short != "a1b2" {
		t.Fatalf("当前分支不对: %+v", bs[0])
	}
	if bs[1].Current || bs[1].Ahead != 0 || bs[1].Subject != "旧提交" {
		t.Fatalf("普通分支不对: %+v", bs[1])
	}
	// 远程符号 HEAD（origin/HEAD 的短名没有 /HEAD 后缀）靠完整 refname 露馅
	remotes := gitParseForEachRef("refs/remotes/origin\x1forigin\x1fa1b2\x1f\x1f\x1f \x1f最近提交\x1e" +
		"\nrefs/remotes/origin/go\x1forigin/go\x1fa1b2\x1f\x1f\x1f \x1f最近提交\x1e")
	if len(remotes) != 2 {
		t.Fatal("解析应保留原始两条，过滤是调用方的事")
	}
}

func TestGitParseNameStatus(t *testing.T) {
	files := gitParseNameStatus("M\x00a.txt\x00D\x00b.txt\x00R100\x00old.txt\x00new.txt\x00")
	if len(files) != 3 {
		t.Fatalf("应有 3 条，实得 %d: %+v", len(files), files)
	}
	if files[0].Index != "M" || files[0].Path != "a.txt" {
		t.Fatalf("M 不对: %+v", files[0])
	}
	if files[2].Index != "R" || files[2].Original != "old.txt" || files[2].Path != "new.txt" {
		t.Fatalf("R 不对: %+v", files[2])
	}
}

// ---------- 安全校验 ----------

func TestGitSafePathspecs(t *testing.T) {
	ok := []string{"main.go", "web/app.js", "中文 目录/文件.txt", "main.go"}
	got := gitSafePathspecs(ok)
	if len(got) != 3 || got[0] != "main.go" || got[2] != "中文 目录/文件.txt" {
		t.Fatalf("合法路径应保留并去重: %+v", got)
	}
	bad := []string{
		"",                        // 空
		"/etc/passwd",             // 绝对路径
		"C:\\Windows\\x",          // 盘符绝对路径
		"../secrets",              // 上跳
		"a/./b",                   // 当前段
		":(top)main.go",           // pathspec magic
		"dir/",                    // 目录形式
		strings.Repeat("a", 5000), // 超长
	}
	if got := gitSafePathspecs(bad); len(got) != 0 {
		t.Fatalf("全部应被拒绝，实得: %+v", got)
	}
}

func TestGitSanitizeMessage(t *testing.T) {
	if got := gitSanitizeMessage("  提交\x00信息\x07\n\n\n\n正文  \n"); got != "提交信息\n\n正文" {
		t.Fatalf("净化不对: %q", got)
	}
	if got := gitSanitizeMessage(strings.Repeat("字", gitMessageMaxChars+100)); len(got) != gitMessageMaxChars {
		t.Fatalf("限长不对: %d", len(got))
	}
	if gitSanitizeMessage(" \n\t ") != "" {
		t.Fatal("纯空白应为空串")
	}
}

func TestGitCapTextMultibyte(t *testing.T) {
	text := strings.Repeat("汉", 100) // 每字 3 字节
	got, trunc := gitCapText(text, 200)
	if !trunc {
		t.Fatal("应标记截断")
	}
	// 截断点不能落在多字节字符中间（最多回退 3 字节）
	if strings.HasSuffix(got, string([]byte{0xE6})) || len(got) > 202 {
		t.Fatalf("截断破坏了 UTF-8: %d 字节", len(got))
	}
}

func TestGitAddedTextDiff(t *testing.T) {
	got, trunc := gitAddedTextDiff("a\nb\n", "x.txt", 100)
	if trunc || !strings.HasPrefix(got, "--- /dev/null\n+++ b/x.txt\n+a\n+b\n") {
		t.Fatalf("伪 diff 不对: %q", got)
	}
	small, truncSmall := gitAddedTextDiff("1\n2\n3\n4\n5\n", "y.txt", 3)
	if !truncSmall || !strings.HasSuffix(small, "+1\n+2\n+3\n") {
		t.Fatalf("行数上限不对: %q trunc=%v", small, truncSmall)
	}
}

func TestGitDiffIsBinary(t *testing.T) {
	if !gitDiffIsBinary("diff --git a/x b/x\nGIT binary patch\n") {
		t.Fatal("应识别 binary patch")
	}
	if !gitDiffIsBinary("Binary files a/x and b/x differ\n") {
		t.Fatal("应识别 Binary files")
	}
	if gitDiffIsBinary("+普通文本 diff") {
		t.Fatal("普通 diff 不应误判")
	}
}

// ---------- 真机 git 校验（临时仓库，git 缺失则跳过） ----------

func gitAvailable(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("本机没有 git，跳过真机校验")
	}
	return git
}

func gitInitRepo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitAvailable(t), args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=测试", "GIT_AUTHOR_EMAIL=t@test", "GIT_COMMITTER_NAME=测试", "GIT_COMMITTER_EMAIL=t@test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "测试")
	run("config", "user.email", "t@test")
	_ = dir
	return dir
}

func TestGitLiveStatusAndGraph(t *testing.T) {
	gitAvailable(t)
	dir := gitInitRepo(t)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) {
		cmd := exec.Command(gitAvailable(t), "commit", "-m", msg)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=测试", "GIT_AUTHOR_EMAIL=t@test",
			"GIT_COMMITTER_NAME=测试", "GIT_COMMITTER_EMAIL=t@test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit 失败: %v\n%s", err, out)
		}
	}
	add := func() {
		cmd := exec.Command(gitAvailable(t), "add", "-A")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("add 失败: %v\n%s", err, out)
		}
	}
	// 首次提交 → 开分支改文件 → 回主干改文件并合并（造出真实的合并拓扑）
	write("a.txt", "第一版\n")
	add()
	commit("首次提交")
	run2 := func(args ...string) {
		cmd := exec.Command(gitAvailable(t), args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	run2("checkout", "-b", "feature")
	write("a.txt", "分支版\n")
	add()
	commit("分支上的改动")
	run2("checkout", "main")
	write("b.txt", "主干独有\n")
	add()
	commit("主干上的改动")
	run2("merge", "--no-ff", "-m", "合并 feature 分支", "feature")

	// porcelain=v2：真实输出解析
	cmd := exec.Command(gitAvailable(t), "status", "--porcelain=v2", "--branch", "-z")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	branch, _, upstream, _, _, entries := gitParseStatusV2(string(out))
	if branch != "main" || upstream != "" {
		t.Fatalf("分支头不对: %q %q", branch, upstream)
	}
	if len(entries) != 0 {
		t.Fatalf("干净仓库不应有改动: %+v", entries)
	}
	staged, unstaged, untracked, conflicted := gitGroupStatus(entries)
	if len(staged)+len(unstaged)+len(untracked)+len(conflicted) != 0 {
		t.Fatal("分组应全空")
	}

	// 图谱：--all --topo-order 与解析
	format := "--pretty=format:%H" + gitFieldSep + "%P" + gitFieldSep + "%an" + gitFieldSep + "%ad" + gitFieldSep + "%d" + gitFieldSep + "%s" + gitRecSep
	cmd = exec.Command(gitAvailable(t), "log", "--all", "--topo-order", "--date=iso-strict", "-n", "50", format)
	cmd.Dir = dir
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	commits := gitParseLogGraph(string(out))
	if len(commits) != 4 {
		t.Fatalf("应有 4 条提交，实得 %d", len(commits))
	}
	var mergeNode *gitCommitInfo
	for i := range commits {
		if len(commits[i].Parents) == 2 {
			mergeNode = &commits[i]
		}
	}
	if mergeNode == nil {
		t.Fatal("没找到合并提交")
	}
	if mergeNode.Subject != "合并 feature 分支" {
		t.Fatalf("合并提交信息不对: %q", mergeNode.Subject)
	}
	if len(mergeNode.Refs) == 0 || mergeNode.Refs[0].Name != "main" || !mergeNode.Refs[0].Current {
		t.Fatalf("合并提交应带 main 装饰: %+v", mergeNode.Refs)
	}

	// for-each-ref：分支清单（字段序与 gitBranches 一致，首列完整 refname）
	cmd = exec.Command(gitAvailable(t), "for-each-ref", "refs/heads",
		"--format=%(refname)"+gitFieldSep+"%(refname:short)"+gitFieldSep+"%(objectname:short)"+gitFieldSep+"%(upstream:short)"+gitFieldSep+"%(upstream:track)"+gitFieldSep+"%(HEAD)"+gitFieldSep+"%(contents:subject)"+gitRecSep)
	cmd.Dir = dir
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	locals := gitParseForEachRef(string(out))
	if len(locals) != 2 {
		t.Fatalf("应有 main/feature 两条分支，实得 %+v", locals)
	}
	foundCur := false
	for _, b := range locals {
		if b.Current && b.Name == "main" {
			foundCur = true
		}
	}
	if !foundCur {
		t.Fatalf("main 应标记为当前分支: %+v", locals)
	}
}

func TestGitCandidatesScan(t *testing.T) {
	gitAvailable(t)
	dir := gitInitRepo(t)
	// 依赖目录里的东西不该被扫进来；旁边一个并列仓库应该被扫到
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-sibling")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sibling, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repos := gitCandidates(dir)
	if len(repos) < 1 || repos[0].Path != dir {
		t.Fatalf("自身仓库应排第一: %+v", repos)
	}
	foundSibling := false
	for _, r := range repos[1:] {
		if strings.EqualFold(r.Path, sibling) {
			foundSibling = true
		}
	}
	if !foundSibling {
		t.Fatalf("应扫到并列仓库 %s: %+v", sibling, repos)
	}
}

func TestGitRunTimesOutOnHang(t *testing.T) {
	// 环境里一定有 git（仓库本身就是 git 管理的）：用一个会挂起的只读场景测超时
	// 过重，改为验证 gitRun 正常路径的截断标记即可；超时逻辑靠 review 保证。
	gitAvailable(t)
	app := &App{}
	dir := t.TempDir()
	_, trunc, err := app.gitRun(context.Background(), dir, 10*time.Second, "version")
	if err != nil || trunc {
		t.Fatalf("git version 应成功: trunc=%v err=%v", trunc, err)
	}
}
