package main

// 一次性实测矩阵（Git 同步工具改动说明的配套证据，跑完即删）：
// 验证 workspace-write 沙箱内 git 各操作的真实行为。
// 产物全部落 data/（已 gitignore）。判定依据是输出文本（spawnConfinedWindows
// 不回传退出码），成功标记 = 命令链尾的 echo OK-XXX。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitSbxStep(t *testing.T, ws, cmd string, timeout time.Duration) string {
	t.Helper()
	out, err := spawnConfinedLocal(context.Background(), sandboxRun{
		Dir: ws, Command: cmd, Shell: "cmd", Timeout: timeout,
		Policy: permWorkspace, Workspace: ws, Label: "sbx-git-matrix", Model: true,
	})
	if err != nil {
		t.Logf("沙箱执行器报错（命令未跑）：%v", err)
		return "[sandbox-error] " + err.Error()
	}
	return out
}

func TestSbxGitMatrix(t *testing.T) {
	if !isWindowsRuntime {
		t.Skip("仅 Windows 沙箱有意义")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("本机没有 git")
	}
	base := filepath.Join("data", "sbxgit")
	ws := filepath.Join(base, "ws")
	remoteIn := filepath.Join(ws, "remote-in.git")   // 工作区内裸仓库（远程替身）
	remoteOut := filepath.Join(base, "remote-out.git") // 工作区外裸仓库
	os.RemoveAll(base)
	for _, d := range []string{ws, remoteIn, remoteOut} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defer os.RemoveAll(base)
	defer os.Remove(filepath.Join(os.Getenv("USERPROFILE"), "jx-sbx-matrix.tmp"))
	// 宿主侧 fixture：两个裸仓库 + remote 配置
	for _, d := range []string{remoteIn, remoteOut} {
		if out, err := exec.Command("git", "init", "-q", "--bare", d).CombinedOutput(); err != nil {
			t.Fatalf("init 裸仓库失败: %v %s", err, out)
		}
	}
	for _, r := range []struct{ name, url string }{{"rin", remoteIn}, {"rout", remoteOut}} {
		if out, err := exec.Command("git", "-C", ws, "remote", "add", r.name, r.url).CombinedOutput(); err != nil {
			t.Fatalf("remote add 失败: %v %s", err, out)
		}
	}

	// ① 本地全套：init + 本地身份 + add + commit（期望成功）
	out := gitSbxStep(t, ws,
		`git init -q -b main . && git config user.email jx@test.local && git config user.name jxtest && echo hello > a.txt && git add a.txt && git commit -qm init && echo OK-COMMIT`, 60*time.Second)
	t.Logf("① 本地 commit：\n%s", out)
	if !strings.Contains(out, "OK-COMMIT") {
		t.Errorf("① 沙箱内本地提交应成功，实际失败")
	}

	// ② 只读操作（期望成功）
	out = gitSbxStep(t, ws, `git status --short && git log --oneline -1 && echo OK-READ`, 60*time.Second)
	t.Logf("② status/log：\n%s", out)
	if !strings.Contains(out, "OK-READ") {
		t.Errorf("② 沙箱内 git status/log 应成功")
	}

	// ③ 读全局配置与 ~/.gitconfig（期望成功：读不受限）
	out = gitSbxStep(t, ws, `git config --global --list --show-origin & echo ---END---`, 60*time.Second)
	t.Logf("③ 全局配置读取：\n%s", out)

	// ④ 写用户主目录（期望失败：工作区外写入）
	out = gitSbxStep(t, ws, `echo x > "%USERPROFILE%\jx-sbx-matrix.tmp" && echo BAD-WROTE`, 60*time.Second)
	t.Logf("④ 主目录写入：\n%s", out)
	if strings.Contains(out, "BAD-WROTE") {
		t.Errorf("④ 工作区外写入竟成功——沙箱写边界失效")
	}

	// ⑤ push 到工作区内裸仓库（期望成功：对象传输+写全在工作区内）
	out = gitSbxStep(t, ws, `git push -q rin main && echo OK-PUSH-IN`, 90*time.Second)
	t.Logf("⑤ push 工作区内远程：\n%s", out)
	if !strings.Contains(out, "OK-PUSH-IN") {
		t.Errorf("⑤ 沙箱内 push 到工作区内远程应成功")
	}

	// ⑥ push 到工作区外裸仓库（期望失败：要写远程对象库）
	out = gitSbxStep(t, ws, `git push rout main && echo BAD-PUSH-OUT`, 90*time.Second)
	t.Logf("⑥ push 工作区外远程：\n%s", out)
	if strings.Contains(out, "BAD-PUSH-OUT") {
		t.Errorf("⑥ push 工作区外远程竟成功——写边界失效")
	}

	// ⑦ fetch 工作区内远程（期望成功）
	out = gitSbxStep(t, ws, `git fetch rin && echo OK-FETCH`, 90*time.Second)
	t.Logf("⑦ fetch：\n%s", out)
	if !strings.Contains(out, "OK-FETCH") {
		t.Errorf("⑦ 沙箱内 fetch 应成功")
	}

	// ⑧ 凭据助手配置（读；日志用——说明本机 HTTPS 走哪条凭据链）
	out = gitSbxStep(t, ws, `git config --show-origin --get-all credential.helper & echo ---END---`, 60*time.Second)
	t.Logf("⑧ 凭据助手配置：\n%s", out)

	// ⑨ cmdkey 枚举（CredRead 代理：受限令牌能否读凭据库）
	out = gitSbxStep(t, ws, `cmdkey /list & echo ---END---`, 60*time.Second)
	t.Logf("⑨ cmdkey（凭据库读代理）：\n%.600s", out)

	// ⑩ ssh-agent 管道（宿主先探，agent 没跑就跳过——多数用户也没跑）
	if ho, he := exec.Command("ssh-add", "-l").CombinedOutput(); he != nil && strings.Contains(strings.ToLower(he.Error()+string(ho)), "agent") {
		t.Logf("⑩ 宿主侧 ssh-agent 未运行，跳过管道连通测试（宿主输出: %v %s）", he, strings.TrimSpace(string(ho)))
	} else {
		out = gitSbxStep(t, ws, `ssh-add -l & echo ---END---`, 60*time.Second)
		t.Logf("⑩ 沙箱内 ssh-add：\n%s", out)
	}

	// ⑪ 网络只读 ls-remote 公共仓库（无凭据需求，验证联网+对象解包）
	netURL := "https://gitee.com/mirrors/git.git"
	if ho, err := exec.Command("git", "ls-remote", netURL, "HEAD").CombinedOutput(); err != nil {
		t.Logf("⑪ 宿主自身都连不上 %s（%v），网络步骤作废：%s", netURL, err, strings.TrimSpace(string(ho)))
	} else {
		out = gitSbxStep(t, ws, `git ls-remote `+netURL+` HEAD && echo OK-NET`, 120*time.Second)
		t.Logf("⑪ 沙箱内 ls-remote 公共仓库：\n%s", out)
		if !strings.Contains(out, "OK-NET") {
			t.Logf("⑪ 注：沙箱内 ls-remote 失败（联网或解包被挡？），记�