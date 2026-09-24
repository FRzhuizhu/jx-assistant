package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 权限引擎单测：危险命令护栏（组件开关）、工作区边界、模式解析、
// 授权凭证、拒绝驱动的升级审批链路。

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

func TestGrantOneTimeConsume(t *testing.T) {
	a := &App{grants: map[string]*Grant{}}
	g := a.issueGrant("s1", "write_file", "write", "写文件 x", permWorkspace)
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
	// 用测试运行目录当受保护根（不落死绝对路径，仓库改名/换机不受影响）
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
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

// guardOnApp 构造一个带危险命令护栏组件注册表的 App（on=false 时组件被关）。
func guardOnApp(t *testing.T, on bool) *App {
	t.Helper()
	dir := t.TempDir()
	compDir := filepath.Join(dir, "dangerous-cmd-guard")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"dangerous-cmd-guard","name":"危险命令拦截","version":"0.1.0","description":"","client":{}}`
	if err := os.WriteFile(filepath.Join(compDir, "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "components.json")
	comp := InitComponents(dir, state)
	if !on {
		if err := comp.SetEnabled("dangerous-cmd-guard", false); err != nil {
			t.Fatal(err)
		}
	}
	return &App{Components: comp}
}

func TestGuardToggle(t *testing.T) {
	if !guardOnApp(t, true).guardEnabled() {
		t.Fatal("护栏组件开启时 guardEnabled 应为 true")
	}
	if guardOnApp(t, false).guardEnabled() {
		t.Fatal("护栏组件关闭时 guardEnabled 应为 false")
	}
	if !(&App{}).guardEnabled() {
		t.Fatal("无组件注册表时护栏应缺省开启")
	}
	// 组件目录里根本没有该组件（被删掉）也应视为开启
	empty := &App{Components: InitComponents(t.TempDir(), filepath.Join(t.TempDir(), "s.json"))}
	if !empty.guardEnabled() {
		t.Fatal("组件缺失时护栏应视为开启")
	}
}

// escalationApp 构造可跑 authorize 的完整 App（真 Store，供「始终允许」落库）。
func escalationApp(t *testing.T) (*App, *SessionMeta, *toolContext) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "jx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess := &SessionMeta{ID: "sess-t1", Workspace: `D:\w`, Permission: permWorkspace}
	if err := store.Create(sess); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: &Config{AllowShell: true, Workspace: `D:\w`}, Store: store,
		grants: map[string]*Grant{}, approvals: map[string]*Approval{},
		subscribers: map[chan Event]bool{}}
	tc := &toolContext{ctx: context.Background(), app: a, sess: sess}
	return a, sess, tc
}

// TestAuthorizeDenialDriven 覆盖新模型：exec 不预检（护栏除外）、
// 升级需真实拒绝后的 sandbox_permissions+justification、审批决定走向。
func TestAuthorizeDenialDriven(t *testing.T) {
	a, sess, tc := escalationApp(t)

	// 工作区写模式：写工作区内放行，工作区外拒绝（fs 路径沙箱）
	if g, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\w\a.txt`}); err != nil || g.Policy != permWorkspace {
		t.Fatalf("工作区内写应放行且策略为 workspace-write: g=%v err=%v", g, err)
	}
	if _, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\out\a.txt`}); err == nil ||
		!strings.Contains(err.Error(), "fs sandbox") {
		t.Fatalf("工作区外写应带 fs sandbox 拒绝标记: %v", err)
	}

	// exec 不再预检：只读模式下也放行（沙箱内执行，写会被 OS 拒绝）
	sess.Permission = permReadOnly
	if g, err := tc.authorize(permAction{Op: "exec", Tool: "run_command", Command: "echo hi", Cwd: `D:\w`}); err != nil || g.Policy != permReadOnly {
		t.Fatalf("只读模式 exec 应按只读策略放行: g=%v err=%v", g, err)
	}
	sess.Permission = permWorkspace

	// 危险命令护栏：任何模式硬拦截
	if _, err := tc.authorize(permAction{Op: "exec", Tool: "run_command", Command: "format C:", Cwd: `D:\w`}); err == nil {
		t.Fatal("危险命令应被护栏拦截")
	}

	// 升级方向校验：同级/更窄档无效；缺 justification 无效
	if _, err := tc.authorize(permAction{Op: "exec", Command: "x", EscalateTo: permWorkspace}); err == nil {
		t.Fatal("当前已是 workspace-write，再升 workspace-write 应拒绝")
	}
	if _, err := tc.authorize(permAction{Op: "exec", Command: "x", EscalateTo: permFull}); err == nil ||
		!strings.Contains(err.Error(), "justification") {
		t.Fatalf("升级缺 justification 应拒绝: %v", err)
	}

	// 升级 → 审批 → 批准 → 以更宽策略签发
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
	g, err := tc.authorize(permAction{Op: "exec", Command: "git push", EscalateTo: permFull,
		Justification: "需要推送到远端仓库完成交付"})
	if err != nil {
		t.Fatalf("批准后升级应放行: %v", err)
	}
	if g.Policy != permFull {
		t.Fatalf("升级后的凭证策略应为 full，得到 %s", g.Policy)
	}
	if sess.Permission != permWorkspace {
		t.Fatalf("仅点「允许」不应改变会话权限，得到 %s", sess.Permission)
	}

	// 升级 → 审批 → 拒绝 → 该命令终局
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if aps := a.pendingApprovals(); len(aps) == 1 {
				_ = a.resolveApproval(aps[0].ID, false, false)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := tc.authorize(permAction{Op: "exec", Command: "git push", EscalateTo: permFull,
		Justification: "再试一次"}); err == nil || !strings.Contains(err.Error(), "终局") {
		t.Fatalf("被拒绝的升级应终局: %v", err)
	}

	// 「始终允许」：给会话开启「允许工作区外执行命令」能力（勾选点亮）
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if aps := a.pendingApprovals(); len(aps) == 1 {
				_ = a.resolveApproval(aps[0].ID, true, true)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := tc.authorize(permAction{Op: "exec", Command: "git push", EscalateTo: permFull,
		Justification: "这次用始终允许"}); err != nil {
		t.Fatalf("始终允许路径应放行: %v", err)
	}
	if !hasCapability(sess, capUnsandboxed) {
		t.Fatalf("始终允许应点亮 unsandboxed 能力勾选，得到 %v", sess.Tools)
	}
	if sess.Permission != permWorkspace {
		t.Fatalf("始终允许不应改变权限模式（仍是工作区内读写），得到 %s", sess.Permission)
	}

	// 之后升级不再弹审批：直接以请求的更宽策略放行
	g2, err := tc.authorize(permAction{Op: "exec", Command: "git push", EscalateTo: permFull,
		Justification: "开过能力后这次不应再弹审批"})
	if err != nil || g2.Policy != permFull {
		t.Fatalf("能力开启后升级应免审批直接放行: g=%v err=%v", g2, err)
	}
	// 普通无升级 exec 保持当前策略（workspace-write），不受能力影响
	g3, err := tc.authorize(permAction{Op: "exec", Command: "git status", Cwd: `D:\w`})
	if err != nil || g3.Policy != permWorkspace {
		t.Fatalf("普通 exec 应保持 workspace-write 策略: g=%v err=%v", g3, err)
	}
}

// TestFullAccessBundlesCapabilities：完全访问=三项捆绑能力强制视为开启；
// 普通模式下能力仍以勾选为准。
func TestFullAccessBundlesCapabilities(t *testing.T) {
	full := &SessionMeta{Permission: permFull}
	for _, c := range fullAccessCaps {
		if !hasCapability(full, c) {
			t.Errorf("完全访问应视为开启能力 %s", c)
		}
	}
	if hasCapability(full, capSandboxWrite) {
		t.Error("sandbox-write 不在捆绑清单内，完全访问不应点亮它")
	}
	plain := &SessionMeta{Permission: permWorkspace, Tools: []string{capBrowser}}
	if hasCapability(plain, capUnsandboxed) {
		t.Error("未勾选的能力不应开启")
	}
	if !hasCapability(plain, capBrowser) {
		t.Error("勾选的能力应开启")
	}
	if got := bundleFullAccessCaps([]string{capBrowser}); len(got) != 3 {
		t.Errorf("bundleFullAccessCaps 应补齐三项捆绑能力，得到 %v", got)
	}
	// 剥除：切离完全访问时捆绑能力自动取消，其余能力保留
	stripped := stripFullAccessCaps([]string{capUnsandboxed, capSandboxWrite, capBrowser, capComputer})
	if len(stripped) != 1 || stripped[0] != capSandboxWrite {
		t.Errorf("stripFullAccessCaps 应只留下非捆绑能力，得到 %v", stripped)
	}
	plain = &SessionMeta{Permission: permWorkspace, Tools: stripped}
	if hasCapability(plain, capBrowser) || hasCapability(plain, capUnsandboxed) {
		t.Error("切离完全访问后捆绑能力应已取消")
	}
}

// TestWriteEscalationToFullAccess：工作区外写入的升级不再点亮单独能力，
// 「允许」= 本次以完全访问执行；「始终允许」= 会话切到完全访问，
// 捆绑能力一并点亮（持久化），之后工作区外写不再审批。
func TestWriteEscalationToFullAccess(t *testing.T) {
	a, sess, tc := escalationApp(t)

	// 「允许」：本次以完全访问放行，会话仍为工作区内读写
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
	g, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\out\a.txt`,
		EscalateTo: permFull, Justification: "需要写工作区外的配置文件"})
	if err != nil || g.Policy != permFull {
		t.Fatalf("批准后应以完全访问放行本次写入: g=%v err=%v", g, err)
	}
	if sess.Permission != permWorkspace {
		t.Fatalf("仅「允许」不应改变会话权限，得到 %s", sess.Permission)
	}

	// 「始终允许」：会话切到完全访问，捆绑能力一并点亮
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if aps := a.pendingApprovals(); len(aps) == 1 {
				_ = a.resolveApproval(aps[0].ID, true, true)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\out\b.txt`,
		EscalateTo: permFull, Justification: "这次用始终允许"}); err != nil {
		t.Fatalf("始终允许路径应放行: %v", err)
	}
	if sess.Permission != permFull {
		t.Fatalf("始终允许应把会话切到完全访问，得到 %s", sess.Permission)
	}
	for _, c := range fullAccessCaps {
		if !hasCapability(sess, c) {
			t.Errorf("完全访问应捆绑点亮 %s: %v", c, sess.Tools)
		}
	}
	// 切换已持久化：从存储重读仍然生效
	reloaded, err := a.Store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Permission != permFull {
		t.Fatalf("完全访问应已落库，重读到 %s", reloaded.Permission)
	}
	// 完全访问下：工作区外写直接放行（不再弹审批），策略为 full
	g3, err := tc.authorize(permAction{Op: "write", Tool: "write_file", Path: `D:\out\c.txt`})
	if err != nil || g3.Policy != permFull {
		t.Fatalf("完全访问下工作区外写应直接放行: g=%v err=%v", g3, err)
	}
}
