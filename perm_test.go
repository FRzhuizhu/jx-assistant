package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 权限引擎纯函数单测：危险命令拦截、工作区边界（本地/远程）、模式解析。

func TestDestructiveCommandHit(t *testing.T) {
	blocked := map[string]string{
		"format C:":                      "格式化磁盘",
		"FORMAT d: /q":                   "格式化磁盘",
		"shutdown /s /t 0":               "关机",
		"sudo shutdown -h now":           "关机",
		"rm -rf /":                       "删除根",
		"rm -rf ~":                       "删除主目录",
		"rm  -rf  /tmp/x/* && rm -rf /*": "删除根",
		"mkfs.ext4 /dev/sda1":            "格式化",
		"dd if=img.iso of=/dev/sdb":      "直写块设备",
		"reg add HKLM\\SOFTWARE\\X /v a": "注册表",
		":(){ :|:& };:":                  "fork炸弹",
		"rd /s /q C:\\":                  "删盘根",
	}
	for cmd := range blocked {
		if why := destructiveCommandHit(cmd); why == "" {
			t.Errorf("应当拦截: %q", cmd)
		}
	}
	allowed := []string{
		"git status", "go build ./...", "rm -rf ./build", "del build\\tmp.js",
		"rmdir /s /q build", "del /f /s /q node_modules\\*.log",
		"echo format > notes.txt", "rm -rf /tmp/work", "dir C:\\",
		"reg add HKCU\\Software\\X /v a /t REG_SZ /d b",
	}
	for _, cmd := range allowed {
		if why := destructiveCommandHit(cmd); why != "" {
			t.Errorf("不应当拦截: %q（命中 %s）", cmd, why)
		}
	}
}

func localTC(ws string) *toolContext {
	return &toolContext{app: &App{Cfg: &Config{Workspace: ws}}, sess: &SessionMeta{Workspace: ws}}
}

func remoteTC(dir string) *toolContext {
	return &toolContext{app: &App{}, sess: &SessionMeta{Remote: &RemoteWorkspace{Dir: dir}}}
}

func TestInWorkspaceLocal(t *testing.T) {
	tc := localTC(`D:\work\proj`)
	cases := []struct {
		p    string
		want bool
	}{
		{`D:\work\proj\a.go`, true},
		{`D:\work\proj\sub\b.go`, true},
		{`d:\WORK\PROJ\c.go`, true}, // 大小写不敏感
		{`D:\work\proj2\d.go`, false},
		{`D:\work\e.go`, false},
		{`E:\x.go`, false},
	}
	for _, c := range cases {
		if got := tc.inWorkspace(c.p); got != c.want {
			t.Errorf("inWorkspace(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestInWorkspaceRemote(t *testing.T) {
	tc := remoteTC("/home/user/proj")
	cases := []struct {
		p    string
		want bool
	}{
		{"/home/user/proj/a.go", true},
		{"/home/user/proj/sub/b.go", true},
		{"/home/user/proj2/c.go", false},
		{"/home/user/d.go", false},
		{"/etc/passwd", false},
	}
	for _, c := range cases {
		if got := tc.inWorkspace(c.p); got != c.want {
			t.Errorf("inWorkspace(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestResolveRemoteUsesPosixRules(t *testing.T) {
	tc := remoteTC("/home/user/proj")
	p, err := tc.resolve("src/a.go")
	if err != nil || p != "/home/user/proj/src/a.go" {
		t.Errorf("远程相对路径解析错误: %q, err=%v", p, err)
	}
	p, _ = tc.resolve("/etc/hosts")
	if p != "/etc/hosts" {
		t.Errorf("远程绝对路径应原样保留: %q", p)
	}
}

func TestPermModeDefaults(t *testing.T) {
	if m := permModeOf(&SessionMeta{}); m != permWorkspace {
		t.Errorf("默认模式应为 workspace-write，得到 %s", m)
	}
	if m := permModeOf(&SessionMeta{Permission: "full"}); m != permFull {
		t.Errorf("应解析 full，得到 %s", m)
	}
	if m := permModeOf(&SessionMeta{Permission: "乱写"}); m != permWorkspace {
		t.Errorf("非法值应回落默认，得到 %s", m)
	}
}

func TestRememberedSignature(t *testing.T) {
	s := &SessionMeta{}
	if isRemembered(s, "exec", "x") {
		t.Fatal("空会话不应命中")
	}
	s.Allowed = append(s.Allowed, allowSignature("exec", "x"))
	if !isRemembered(s, "exec", "x") {
		t.Error("应命中已记录签名")
	}
	if isRemembered(s, "exec", "y") {
		t.Error("不同摘要不应命中")
	}
}

func TestGrantOneTimeConsume(t *testing.T) {
	a := &App{grants: map[string]*Grant{}}
	g := a.issueGrant("s1", "write_file", "write", "写文件 x", false, false)
	if err := a.consumeGrant(g); err != nil {
		t.Fatalf("首次核销应成功: %v", err)
	}
	if err := a.consumeGrant(g); err == nil || !strings.Contains(err.Error(), "已核销") {
		t.Fatalf("二次核销应被拒绝: %v", err)
	}
	if err := a.consumeGrant(nil); err == nil {
		t.Fatal("空凭证应被拒绝")
	}
}

func TestProtectedPath(t *testing.T) {
	root := `D:\智能体工作区\软著版数字助理`
	a := &App{protectPaths: []string{
		strings.ToLower(root + `\config.json`),
		strings.ToLower(root + `\data`),
	}}
	if !a.isProtectedPath(root+`\config.json`, false) {
		t.Error("配置文件应受保护")
	}
	if !a.isProtectedPath(root+`\data\jx.db`, false) {
		t.Error("数据目录内应受保护")
	}
	if a.isProtectedPath(root+`\dataxx\a.txt`, false) {
		t.Error("前缀相似但不同的目录不应误伤")
	}
	if a.isProtectedPath("/home/u/config.json", true) {
		t.Error("远程会话不做本地保护判断")
	}
}

// 审批链路端到端：能力开关 → 越界执行进入审批 → 用户批准 → 签发沙箱外凭证。
func TestAuthorizeEscalationApprovalFlow(t *testing.T) {
	a := &App{Cfg: &Config{AllowShell: true, Workspace: `D:\w`},
		grants: map[string]*Grant{}, approvals: map[string]*Approval{}, subscribers: map[chan Event]bool{}}
	sess := &SessionMeta{Workspace: `D:\w`}
	tc := &toolContext{ctx: context.Background(), app: a, sess: sess}

	// 只读模式：写与执行都被拒
	sess.Permission = permReadOnly
	if _, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\w\a.txt`}); err == nil {
		t.Fatal("只读模式应拒绝写文件")
	}
	if _, err := tc.authorize(permAction{Op: "exec", Tool: "run_command", Command: "echo hi", Cwd: `D:\w`}); err == nil {
		t.Fatal("只读模式应拒绝执行命令")
	}

	// 工作区写模式：工作区外写被拒
	sess.Permission = ""
	if _, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\w\a.txt`}); err != nil {
		t.Fatalf("工作区内写应放行: %v", err)
	}
	if _, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\out\a.txt`}); err == nil {
		t.Fatal("工作区外写应拒绝")
	}

	// 越界执行：未开启能力 → 直接拒
	if _, err := tc.authorize(permAction{Op: "exec", Tool: "run_command", Command: "git status", Cwd: `D:\other`}); err == nil ||
		!strings.Contains(err.Error(), "工作区之外") {
		t.Fatalf("未开启能力时应拒绝越界执行: %v", err)
	}

	// 开启能力 → 进入审批 → 批准 → 沙箱外凭证
	sess.Tools = []string{capUnsandboxed}
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if aps := a.pendingApprovals(); len(aps) == 1 {
				_ = a.resolveApproval(aps[0].ID, true, false)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	g, err := tc.authorize(permAction{Op: "exec", Tool: "run_command", Command: "git status", Cwd: `D:\other`})
	if err != nil {
		t.Fatalf("批准后应放行: %v", err)
	}
	if !g.Escaped || !g.FullEnv {
		t.Fatal("沙箱外凭证应带 Escaped 与 FullEnv")
	}
	if err := a.consumeGrant(g); err != nil {
		t.Fatalf("核销失败: %v", err)
	}

	// 危险命令硬拦截（即便完全访问）
	sess.Permission = permFull
	if _, err := tc.authorize(permAction{Op: "exec", Tool: "run_command", Command: "format C:", Cwd: `D:\w`}); err == nil {
		t.Fatal("危险命令应被硬拦截")
	}
	// 完全访问下越界执行不弹审批直接放行
	g2, err := tc.authorize(permAction{Op: "exec", Tool: "run_command", Command: "git status", Cwd: `D:\other`})
	if err != nil || !g2.Escaped {
		t.Fatalf("完全访问越界应直接放行: %v", err)
	}
}
