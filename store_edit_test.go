package main

import (
	"os"
	"testing"
)

// 编辑重发的落库三连测：截断+改写 / 编辑点不存在 / 编辑最后一轮时不留尾巴。
func TestEditUserMessage(t *testing.T) {
	os.RemoveAll("data-edit-test")
	defer os.RemoveAll("data-edit-test")
	s, err := NewStore("data-edit-test")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer s.db.Close()
	if err := s.Create(&SessionMeta{ID: "e1", Title: "编辑测试", CreatedAt: now(), UpdatedAt: now()}); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	// 两轮对话：user(1) assistant(2) user(3) assistant(4) report(5)
	for _, m := range []Message{
		TextMessage("user", "第一问"), TextMessage("assistant", "第一答"),
		TextMessage("user", "第二问"), TextMessage("assistant", "第二答"),
	} {
		if err := s.Append("e1", m); err != nil {
			t.Fatalf("写消息失败: %v", err)
		}
	}
	if err := s.AppendRow("e1", &StoredMessage{Message: TextMessage("user", "【执行中断】测试"), Kind: "report", CreatedAt: now()}); err != nil {
		t.Fatalf("写回报行失败: %v", err)
	}

	// 编辑 seq=3：之后的 assistant(4) 与 report(5) 都应被移除，正文改为新内容
	removed, err := s.EditUserMessage("e1", 3, "第二问（已改）")
	if err != nil || removed != 2 {
		t.Fatalf("编辑失败: removed=%d err=%v", removed, err)
	}
	msgs, _ := s.MessagesWithSeq("e1")
	if len(msgs) != 3 {
		t.Fatalf("编辑后应剩 3 条，实际 %d", len(msgs))
	}
	if got := msgs[2].Text(); got != "第二问（已改）" {
		t.Fatalf("正文未改写: %q", got)
	}
	if msgs[2].Seq != 3 {
		t.Fatalf("编辑后 seq 应保持 3，实际 %d", msgs[2].Seq)
	}

	// 编辑不存在的消息：报错且不动数据
	if _, err := s.EditUserMessage("e1", 99, "x"); err == nil {
		t.Fatalf("编辑不存在的 seq 应报错")
	}
	msgs2, _ := s.MessagesWithSeq("e1")
	if len(msgs2) != 3 {
		t.Fatalf("失败的编辑不应改动数据，实际 %d 条", len(msgs2))
	}

	// 编辑最后一条用户消息（其后无记录）：removed=0，追加重跑后 seq 从 4 续排
	if removed, err := s.EditUserMessage("e1", 3, "再改一次"); err != nil || removed != 0 {
		t.Fatalf("尾部编辑失败: removed=%d err=%v", removed, err)
	}
	if err := s.Append("e1", TextMessage("assistant", "新答")); err != nil {
		t.Fatalf("追加重跑回答失败: %v", err)
	}
	msgs3, _ := s.MessagesWithSeq("e1")
	if last := msgs3[len(msgs3)-1]; last.Seq != 4 || last.Role != "assistant" {
		t.Fatalf("重跑后 seq 应从 4 续排，实际 seq=%d role=%s", last.Seq, last.Role)
	}
}
