package main

// 文件工具三道防线单测（对齐 DSH）：
//   - writeFenceAllows：进程内路径围栏（canonicalize + 词法 + 身份比对，
//     对齐 fs-sandbox 的 checkedTarget/containment）；
//   - 观察策略：既有文件本会话 read 过才能被 write 覆盖（对齐
//     fs-observation-policy 的写意图），会话间隔离；
//   - writeFileAtomic：同目录临时文件 + rename 原子替换，NTFS 硬链接
//     外部别名不被写穿（对齐 fs-local 的 writeFileAtomic）。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFenceAllows(t *testing.T) {
	ws := t.TempDir()
	tc := localTC(ws)
	// 「外部」必须同时在工作区与系统临时目录之外（t.TempDir 落在 %TEMP% 内，
	// 现在临时目录在围栏白名单里，不能当外部样本）
	outside := filepath.Join(os.Getenv("USERPROFILE"), "jx-fence-outside-probe.txt")

	if !tc.writeFenceAllows(filepath.Join(ws, "a.txt")) {
		t.Errorf("工作区内应放行")
	}
	if !tc.writeFenceAllows(filepath.Join(os.TempDir(), "jx-fence-probe.txt")) {
		t.Errorf("系统临时目录应放行（DSH writableRoots 同款）")
	}
	if tc.writeFenceAllows(outside) {
		t.Errorf("工作区与临时目录之外应拒绝")
	}
	// 链接逃逸：工作区内放指向外部的 junction，写链接下的路径必须拒绝
	// （符号链接需要特权，junction 用 mklink /J 即可；EvalSymlinks 两者都跟随。
	// 目标目录也必须在临时目录之外，否则命中白名单测不出拦截）
	outsideDir, err := os.MkdirTemp(os.Getenv("USERPROFILE"), "jx-fence-out-*")
	if err != nil {
		t.Skipf("建外部目录失败（跳过）: %v", err)
	}
	defer os.RemoveAll(outsideDir)
	link := filepath.Join(ws, "escape.dir")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outsideDir).CombinedOutput(); err != nil {
		t.Skipf("创建 junction 失败（跳过）: %v %s", err, out)
	}
	if tc.writeFenceAllows(filepath.Join(link, "x.txt")) {
		t.Errorf("junction 逃逸未被拦截（canonicalize 失效）")
	}
}

func newObservedApp(t *testing.T, ws string) (*App, *Store, *toolContext) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("建存储失败: %v", err)
	}
	t.Cleanup(func() { store.db.Close() })
	a := &App{Store: store, Cfg: DefaultConfig()}
	a.Cfg.Workspace = ws
	a.grants = map[string]*Grant{}
	a.approvals = map[string]*Approval{}
	sess := &SessionMeta{ID: "obs-test", Mode: "task", Permission: "workspace-write",
		CreatedAt: time.Now().Format(time.RFC3339), Workspace: ws}
	if err := store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	return a, store, &toolContext{app: a, sess: sess, ctx: context.Background()}
}

func TestObservationPolicyCAS(t *testing.T) {
	ws := t.TempDir()
	a, _, tc := newObservedApp(t, ws)
	target := filepath.Join(ws, "existing.txt")
	if err := os.WriteFile(target, []byte("旧内容"), 0644); err != nil {
		t.Fatal(err)
	}

	wargs, _ := json.Marshal(map[string]string{"path": "existing.txt", "content": "新内容"})
	// 未读先覆盖 → 拒绝
	if _, err := runWriteFile(tc, wargs); err == nil || !strings.Contains(err.Error(), "观察策略") {
		t.Fatalf("盲覆盖应被观察策略拒绝，得到: %v", err)
	}
	// read 过后 → 放行
	rargs, _ := json.Marshal(map[string]string{"path": "existing.txt"})
	if _, err := runReadFile(tc, rargs); err != nil {
		t.Fatalf("read 失败: %v", err)
	}
	if _, err := runWriteFile(tc, wargs); err != nil {
		t.Fatalf("读后覆盖应放行: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "新内容" {
		t.Errorf("覆盖内容不符: %q", b)
	}

	// 新会话（观察集按会话隔离，进程内状态）盲覆盖 → 再次拒绝
	sess2 := &SessionMeta{ID: "obs-test-2", Mode: "task", Permission: "workspace-write",
		CreatedAt: time.Now().Format(time.RFC3339), Workspace: ws}
	tc2 := &toolContext{app: a, sess: sess2, ctx: context.Background()}
	if _, err := runWriteFile(tc2, wargs); err == nil || !strings.Contains(err.Error(), "观察策略") {
		t.Fatalf("新会话盲覆盖应被拒，得到: %v", err)
	}

	// 新建文件不受观察策略限制
	nargs, _ := json.Marshal(map[string]string{"path": "fresh.txt", "content": "首个内容"})
	if _, err := runWriteFile(tc, nargs); err != nil {
		t.Fatalf("新建文件应直接放行: %v", err)
	}
}

func TestWriteAtomicHardlinkImmunity(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(main, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias.txt")
	if err := os.Link(main, alias); err != nil {
		t.Skipf("创建硬链接失败（跳过）: %v", err)
	}
	if err := writeFileAtomic(main, []byte("new")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	if b, _ := os.ReadFile(main); string(b) != "new" {
		t.Errorf("主路径内容应为 new: %q", b)
	}
	// 替换语义换指新 inode：外部硬链接别名保持旧内容，不被写穿
	if b, _ := os.ReadFile(alias); string(b) != "old" {
		t.Errorf("硬链接外部别名被写穿（应为 old）: %q", b)
	}
}
