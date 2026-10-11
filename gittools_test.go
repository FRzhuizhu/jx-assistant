package main

// git_sync 工具的门控与行为测试：组件开关、只读拦截、推送/拉取/op 归一、
// 仓库候选校验。走真实 git 与宿主通道（与 Git 面板同一执行缝），只落
// t.TempDir()。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitToolFixture(t *testing.T) (*App, *toolContext, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("本机没有 git")
	}
	// 工作区（含仓库本体）与裸远程都放在同一个临时根里；仓库根就是会话工作区
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	bare := filepath.Join(root, "upstream.git")
	for _, d := range []string{repo, bare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range [][]string{
		{"git", "init", "-q", "--bare", bare},
		{"git", "init", "-q", "-b", "main", repo},
	} {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", c, err, out)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("remote", "add", "origin", bare)
	gitCommitFile(t, repo, "a.txt", "hello")
	run("push", "-q", "-u", "origin", "main")

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess := &SessionMeta{ID: "git-tool-sess", Mode: "task", Workspace: repo, Permission: permWorkspace}
	if err := store.SaveMeta(sess); err != nil {
		t.Fatal(err)
	}
	// gitResolve 先读 a.Cfg.Workspace 再按会话覆盖，Cfg 必须在场
	a := &App{Cfg: &Config{Workspace: repo}, Store: store, Components: mkTestRegistry(t, map[string]string{gitComponentID: "1.0"})}
	tc := &toolContext{ctx: context.Background(), app: a, sess: sess}
	return a, tc, repo
}

func gitCommitFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"git", "add", name},
		{"git", "commit", "-qm", "add " + name},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", c, err, out)
		}
	}
}

func callGitSync(t *testing.T, tc *toolContext, op, repo string) (string, error) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"op": op, "repo": repo})
	return runGitSync(tc, args)
}

func TestGitSyncToolRegistration(t *testing.T) {
	a, _, _ := gitToolFixture(t)
	// 组件启用：工具在册
	tools := a.gitTools()
	if len(tools) != 1 || tools[0].Spec.Function.Name != "git_sync" {
		t.Fatalf("组件启用时应注册 git_sync，得到 %#v", tools)
	}
	// 组件关掉：工具不注册
	if err := a.Components.SetEnabled(gitComponentID, false); err != nil {
		t.Fatal(err)
	}
	if ts := a.gitTools(); len(ts) != 0 {
		t.Fatalf("组件停用后不应注册工具: %#v", ts)
	}
}

func TestGitSyncToolReadOnlyDenied(t *testing.T) {
	a, tc, _ := gitToolFixture(t)
	if err := a.Components.SetEnabled(gitComponentID, false); err != nil {
		t.Fatal(err)
	}
	_, err := callGitSync(t, tc, "push", "")
	if err == nil || !strings.Contains(err.Error(), "组件未启用") {
		t.Fatalf("组件停用时应报未启用: %v", err)
	}
	// 只读档：哪怕组件开着也拦
	if err := a.Components.SetEnabled(gitComponentID, true); err != nil {
		t.Fatal(err)
	}
	tc.sess.Permission = permReadOnly
	_, err = callGitSync(t, tc, "fetch", "")
	if err == nil || !strings.Contains(err.Error(), "只读模式") {
		t.Fatalf("只读档应被拦: %v", err)
	}
	tc.sess.Permission = permWorkspace
}

func TestGitSyncToolRemoteSessionDenied(t *testing.T) {
	_, tc, _ := gitToolFixture(t)
	tc.sess.Remote = &RemoteWorkspace{} // 非空即远程
	defer func() { tc.sess.Remote = nil }()
	if _, err := callGitSync(t, tc, "push", ""); err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("远程会话应被拦: %v", err)
	}
}

func TestGitSyncToolPushFlow(t *testing.T) {
	_, tc, repo := gitToolFixture(t)
	// op 缺省 = push，无领先提交
	out, err := callGitSync(t, tc, "", "")
	if err != nil || !strings.Contains(out, "没有领先远程的提交可推送") {
		t.Fatalf("缺省 push 应报无可推送: %v %q", err, out)
	}
	// 新提交后推送成功
	gitCommitFile(t, repo, "b.txt", "world")
	out, err = callGitSync(t, tc, "push", "")
	if err != nil || !strings.Contains(out, "已推送 1 个提交") {
		t.Fatalf("应推送 1 个提交: %v %q", err, out)
	}
	// fetch 只更新状态
	out, err = callGitSync(t, tc, "fetch", "")
	if err != nil || !strings.Contains(out, "已更新远程状态") {
		t.Fatalf("fetch 应成功: %v %q", err, out)
	}
}

func TestGitSyncToolRepoGuard(t *testing.T) {
	_, tc, repo := gitToolFixture(t)
	// 不在候选里的路径：报错列候选，不静默回退
	_, err := callGitSync(t, tc, "push", `D:\不存在的仓库`)
	if err == nil || !strings.Contains(err.Error(), "不在会话工作区扫出的候选") || !strings.Contains(err.Error(), repo) {
		t.Fatalf("未知仓库应报错并列候选: %v", err)
	}
	// 非法 op
	args, _ := json.Marshal(map[string]string{"op": "force"})
	if _, err := runGitSync(tc, args); err == nil || !strings.Contains(err.Error(), "仅支持") {
		t.Fatalf("非法 op 应报错: %v", err)
	}
	// 无上游：needHelp 原样透传
	bare2 := filepath.Join(filepath.Dir(repo), "up2.git")
	if err := os.MkdirAll(bare2, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "--bare", bare2).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "other", bare2).CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v %s", err, out)
	}
	// 把当前分支上游摘掉（推到 other 但不设 -u），制造无上游状态
	if out, err := exec.Command("git", "-C", repo, "push", "-q", "other", "main:other-main").CombinedOutput(); err != nil {
		t.Fatalf("push other: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "branch", "--unset-upstream").CombinedOutput(); err != nil {
		t.Fatalf("unset upstream: %v %s", err, out)
	}
	out, err := callGitSync(t, tc, "push", "")
	if err != nil || !strings.Contains(out, "无法自动完成") || !strings.Contains(out, "上游") {
		t.Fatalf("无上游应 needHelp 透传: %v %q", err, out)
	}
}
