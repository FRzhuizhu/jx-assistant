package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 回退全流程集成测试：带快照写入 → 覆盖 → 编辑回退恢复旧内容 →
// 外部修改后回退被拒（fail-closed）→ 手动快照不做哈希校验。
func TestRewindFlow(t *testing.T) {
	os.RemoveAll("data-rewind-test")
	defer os.RemoveAll("data-rewind-test")
	store, err := NewStore("data-rewind-test")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.db.Close()
	ws := t.TempDir()
	cfg := DefaultConfig()
	cfg.Workspace = ws
	a := &App{Cfg: cfg, Store: store}
	a.initRuntime()
	sess := &SessionMeta{ID: "r1", Mode: "task", Workspace: ws, Permission: "workspace-write",
		CreatedAt: now(), UpdatedAt: now()}
	if err := store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	target := filepath.Join(ws, "demo.txt")

	write := func(seq int64, content string) {
		tctx := &toolContext{app: a, sess: sess, seq: seq, callID: "c" + content, toolName: "write_file"}
		grant, err := tctx.authorize(permAction{Op: "write", Tool: "write_file", Path: target})
		if err != nil {
			t.Fatalf("授权失败: %v", err)
		}
		if err := tctx.snapshotWrite(target, grant, []byte(content)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	readBack := func() string {
		b, err := os.ReadFile(target)
		if err != nil {
			return "<缺失>"
		}
		return string(b)
	}

	// seq1 新建（无旧内容）→ seq2 覆盖为 v2
	write(1, "v1")
	write(2, "v2")
	if got := readBack(); got != "v2" {
		t.Fatalf("前置失败：当前应为 v2，实际 %q", got)
	}

	// 回退到 seq1 之后（即撤回 seq2 的覆盖）：应恢复成 v1
	tctx := &toolContext{app: a, sess: sess}
	safe, unsafe, err := a.planRewind(tctx, "r1", 1, nil)
	if err != nil || len(unsafe) != 0 || len(safe) != 1 {
		t.Fatalf("预检失败: safe=%d unsafe=%d err=%v", len(safe), len(unsafe), err)
	}
	if safe[0].Action != "restore" || string(safe[0].Before) != "v1" {
		t.Fatalf("计划不符: %+v", safe[0])
	}
	if err := a.applyRewind(tctx, safe); err != nil {
		t.Fatalf("回退失败: %v", err)
	}
	if got := readBack(); got != "v1" {
		t.Fatalf("回退后应为 v1，实际 %q", got)
	}

	// 外部修改后再回退：fail-closed 拒绝
	os.WriteFile(target, []byte("外部改过"), 0644)
	_, unsafe, err = a.planRewind(tctx, "r1", 1, nil)
	if err != nil || len(unsafe) != 1 {
		t.Fatalf("外部修改后应判 unsafe: %d err=%v", len(unsafe), err)
	}
	if unsafe[0].Reason == "" {
		t.Fatalf("unsafe 应带原因")
	}

	// 手动快照（snapshot_file 口径）：外部改过也照常按快照恢复
	mtctx := &toolContext{app: a, sess: sess, seq: 3, callID: "cm", toolName: manualSnapshotTool}
	os.WriteFile(target, []byte("手动快照点"), 0644)
	before, _ := mtctx.envReadFile(target)
	if _, err := store.AppendCheckpoint(&Checkpoint{SessionID: "r1", Seq: 3, ToolName: manualSnapshotTool,
		Path: "demo.txt", ExistedBefore: true, Before: before, AfterHash: hashBytes(before), CreatedAt: now()}); err != nil {
		t.Fatalf("手动快照落库失败: %v", err)
	}
	os.WriteFile(target, []byte("shell 改的"), 0644)
	safe, unsafe, err = a.planRewind(tctx, "r1", 0, []int64{3})
	if err != nil || len(unsafe) != 0 || len(safe) != 1 || !safe[0].Manual {
		t.Fatalf("手动快照应免校验放行: safe=%+v unsafe=%d err=%v", safe, len(unsafe), err)
	}
	if err := a.applyRewind(tctx, safe); err != nil {
		t.Fatalf("手动快照回退失败: %v", err)
	}
	if got := readBack(); got != "手动快照点" {
		t.Fatalf("手动快照回退后内容不符: %q", got)
	}
}
