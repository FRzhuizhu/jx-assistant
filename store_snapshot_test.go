package main

import (
	"os"
	"testing"
	"time"
)

// 快照与交付登记的生命周期测试：落库/按 seq 取数/编辑清理/按天过期/按量驱逐。
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
	// 三条快照：一条超龄（seq1），两条新（seq2 覆盖同文件、seq3 新文件）
	cps := []*Checkpoint{
		{SessionID: "s1", Seq: 1, ToolName: "write_file", Path: "a.txt", ExistedBefore: true, Before: []byte("旧a"), AfterHash: "h1", AfterSize: 3, CreatedAt: old},
		{SessionID: "s1", Seq: 2, ToolName: "edit_file", Path: "a.txt", ExistedBefore: true, Before: []byte("旧a"), AfterHash: "h2", AfterSize: 4, CreatedAt: now()},
		{SessionID: "s1", Seq: 3, ToolName: "write_file", Path: "b.txt", Before: nil, AfterHash: "h3", AfterSize: 5, CreatedAt: now()},
	}
	for _, cp := range cps {
		if err := s.AppendCheckpoint(cp); err != nil {
			t.Fatalf("落快照失败: %v", err)
		}
	}
	got, err := s.CheckpointsAfter("s1", 1)
	if err != nil || len(got) != 2 {
		t.Fatalf("CheckpointsAfter(1) 应得 2 条，实际 %d err=%v", len(got), err)
	}
	in, err := s.CheckpointsIn("s1", []int64{3})
	if err != nil || len(in) != 1 || in[0].Path != "b.txt" {
		t.Fatalf("CheckpointsIn 应得 b.txt，实际 %+v err=%v", in, err)
	}

	// 按天清理：只删超龄那条
	if n, err := s.PruneCheckpointsOlderThan(time.Now().AddDate(0, 0, -7).Format("2006-01-02T15:04:05")); err != nil || n != 1 {
		t.Fatalf("超龄清理应删 1 条，实际 %d err=%v", n, err)
	}
	got, _ = s.CheckpointsAfter("s1", 0)
	if len(got) != 2 {
		t.Fatalf("清理后应剩 2 条，实际 %d", len(got))
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

	// 按量驱逐：两个 10 字节快照，上限 15 → 从最旧驱逐一条
	for i := 0; i < 2; i++ {
		_ = s.AppendCheckpoint(&Checkpoint{SessionID: "s1", Seq: int64(5 + i), ToolName: "write_file",
			Path: string(rune('a'+i)) + ".txt", ExistedBefore: true, Before: make([]byte, 10), CreatedAt: now()})
	}
	if n, err := s.EvictCheckpointsOver("s1", 15); err != nil || n != 1 {
		t.Fatalf("限量驱逐应删 1 条，实际 %d err=%v", n, err)
	}
	if sz, _ := s.SnapshotBytes("s1"); sz != 10 {
		t.Fatalf("驱逐后总量应为 10，实际 %d", sz)
	}
}
