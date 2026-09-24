package main

import (
	"os"
	"testing"
	"time"
)

// 快照与交付登记的生命周期测试：落库/按 seq 取数/编辑清理/按天过期/双池限量驱逐。
func TestSnapshotLifecycle(t *testing.T) {
	os.RemoveAll("data-snap-test")
	defer os.RemoveAll("data-snap-test")
	s, err := NewStore("data-snap-test")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer s.db.Close()
	if err := s.Create(&SessionMeta{ID: "s1", Title: "快照测试", CreatedAt: now(), UpdatedAt: now()}); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	old := time.Now().AddDate(0, 0, -30).Format("2006-01-02T15:04:05")
	// 会话消息骨架：user(1) assistant(2) user(3)，编辑重发要校验目标行存在
	for _, mm := range []Message{
		TextMessage("user", "问"), TextMessage("assistant", "答"), TextMessage("user", "再问"),
	} {
		if err := s.Append("s1", mm); err != nil {
			t.Fatalf("写消息失败: %v", err)
		}
	}
	// 四条快照（锚在助手步骤的 seq 上：2/3/5）：a.txt 新旧各一条（文件整体
	// 未超龄，不能删）、b.txt 一条新的、c.txt 一条超龄的独立文件（整体过期，应删）
	cps := []*Checkpoint{
		{SessionID: "s1", Seq: 2, ToolName: "write_file", Path: "a.txt", ExistedBefore: true, Before: []byte("旧a"), AfterHash: "h1", AfterSize: 3, CreatedAt: old},
		{SessionID: "s1", Seq: 3, ToolName: "edit_file", Path: "a.txt", ExistedBefore: true, Before: []byte("旧a"), AfterHash: "h2", AfterSize: 4, CreatedAt: now()},
		{SessionID: "s1", Seq: 4, ToolName: "write_file", Path: "b.txt", Before: nil, AfterHash: "h3", AfterSize: 5, CreatedAt: now()},
		{SessionID: "s1", Seq: 5, ToolName: "write_file", Path: "c.txt", Before: nil, AfterHash: "h4", AfterSize: 5, CreatedAt: old},
	}
	for _, cp := range cps {
		if _, err := s.AppendCheckpoint(cp); err != nil {
			t.Fatalf("落快照失败: %v", err)
		}
	}
	got, err := s.CheckpointsAfter("s1", 1)
	if err != nil || len(got) != 4 {
		t.Fatalf("CheckpointsAfter(1) 应得 4 条，实际 %d err=%v", len(got), err)
	}
	in, err := s.CheckpointsIn("s1", []int64{4})
	if err != nil || len(in) != 1 || in[0].Path != "b.txt" {
		t.Fatalf("CheckpointsIn 应得 b.txt，实际 %+v err=%v", in, err)
	}

	// 按天清理（按文件原子）：只有 c.txt 整体过期，删 1 条；a.txt 有新快照要整文件保留
	if ids, err := s.PruneCheckpointsOlderThan("s1", time.Now().AddDate(0, 0, -7).Format("2006-01-02T15:04:05")); err != nil || len(ids) != 1 {
		t.Fatalf("超龄清理应只删 c.txt 的 1 条，实际 %d err=%v", len(ids), err)
	}
	got, _ = s.CheckpointsAfter("s1", 0)
	if len(got) != 3 {
		t.Fatalf("清理后应剩 3 条，实际 %d", len(got))
	}

	// 编辑重发：seq=2 之后无记录不删；seq=1 之后应清掉两条快照与交付
	if err := s.AddDelivery(&Delivery{SessionID: "s1", Seq: 2, Path: "a.txt", Note: "交付", CreatedAt: now()}); err != nil {
		t.Fatalf("落交付失败: %v", err)
	}
	if _, err := s.EditUserMessage("s1", 3, "改"); err != nil {
		t.Fatalf("编辑 seq=3 失败: %v", err)
	}
	if dvs, _ := s.DeliveriesFor("s1"); len(dvs) != 1 {
		t.Fatalf("编辑 seq=3 不应动 seq=2 的交付")
	}
	if _, err := s.EditUserMessage("s1", 1, "改"); err != nil {
		t.Fatalf("编辑 seq=1 失败: %v", err)
	}
	if got, _ = s.CheckpointsAfter("s1", 0); len(got) != 0 {
		t.Fatalf("编辑 seq=1 后快照应清空，剩 %d", len(got))
	}
	if dvs, _ := s.DeliveriesFor("s1"); len(dvs) != 0 {
		t.Fatalf("编辑 seq=1 后交付应清空，剩 %d", len(dvs))
	}
}

// 双池限量驱逐：自动池超限只驱逐有自动足迹的最旧文件（整文件删）；
// 手动池的大快照不因自动池超限被驱逐，手动池自己超限才动它。
func TestSnapshotDualBudget(t *testing.T) {
	os.RemoveAll("data-snap2-test")
	defer os.RemoveAll("data-snap2-test")
	s, err := NewStore("data-snap2-test")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer s.db.Close()
	if err := s.Create(&SessionMeta{ID: "s2", Title: "双池测试", CreatedAt: now(), UpdatedAt: now()}); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	// 先落：小自动快照 old.txt / new.txt（各 10B），大手动快照 doc.pptx（100B）
	seed := []*Checkpoint{
		{SessionID: "s2", Seq: 1, ToolName: "write_file", Path: "old.txt", ExistedBefore: true, Before: make([]byte, 10), CreatedAt: now()},
		{SessionID: "s2", Seq: 2, ToolName: "write_file", Path: "new.txt", ExistedBefore: true, Before: make([]byte, 10), CreatedAt: now()},
		{SessionID: "s2", Seq: 3, ToolName: manualSnapshotTool, Path: "doc.pptx", ExistedBefore: true, Before: make([]byte, 100), CreatedAt: now()},
	}
	for _, cp := range seed {
		if _, err := s.AppendCheckpoint(cp); err != nil {
			t.Fatalf("落快照失败: %v", err)
		}
	}
	// 自动池预算 15B：总量 20B 超限 → 驱逐最旧的 old.txt（整文件 1 条）；
	// doc.pptx 在自动池无足迹，不受影响
	ids, err := s.EvictCheckpointsOver("s2", 15, -1)
	if err != nil || len(ids) != 1 {
		t.Fatalf("自动池驱逐应删 old.txt 的 1 条，实际 %d err=%v", len(ids), err)
	}
	got, _ := s.CheckpointsAfter("s2", 0)
	paths := map[string]int{}
	for _, c := range got {
		paths[c.Path]++
	}
	if paths["old.txt"] != 0 || paths["new.txt"] != 1 || paths["doc.pptx"] != 1 {
		t.Fatalf("驱逐后应为 new.txt+doc.pptx，实际 %+v", paths)
	}
	// 手动池预算 50B：doc.pptx 100B 超限 → 整文件删除
	ids, err = s.EvictCheckpointsOver("s2", -1, 50)
	if err != nil || len(ids) != 1 {
		t.Fatalf("手动池驱逐应删 doc.pptx 的 1 条，实际 %d err=%v", len(ids), err)
	}
	got, _ = s.CheckpointsAfter("s2", 0)
	if len(got) != 1 || got[0].Path != "new.txt" {
		t.Fatalf("最后应只剩 new.txt，实际 %+v", got)
	}
	// 同文件跨池：old2.txt 一条自动 + 一条手动，自动池超限时整文件一起删
	// （此时 new.txt 也因自动足迹超限被先行驱逐：10+20=30B > 15B）
	_, _ = s.AppendCheckpoint(&Checkpoint{SessionID: "s2", Seq: 4, ToolName: "write_file", Path: "old2.txt", ExistedBefore: true, Before: make([]byte, 20), BeforeSize: 20, CreatedAt: now()})
	_, _ = s.AppendCheckpoint(&Checkpoint{SessionID: "s2", Seq: 5, ToolName: manualSnapshotTool, Path: "old2.txt", ExistedBefore: true, Before: make([]byte, 20), BeforeSize: 20, CreatedAt: now()})
	if ids, err = s.EvictCheckpointsOver("s2", 15, -1); err != nil || len(ids) != 3 {
		t.Fatalf("跨池文件应整文件驱逐（new.txt 1 条 + old2.txt 2 条），实际 %d err=%v", len(ids), err)
	}
	if got, _ = s.CheckpointsAfter("s2", 0); len(got) != 0 {
		t.Fatalf("驱逐后应全部清空，剩 %d", len(got))
	}
}
