package main

import (
	"strings"
	"testing"
)

// 渲染导出 Markdown：覆盖思考过程、摘要标注、系统记录、工具全量结果与子会话附录。
func TestExportMarkdown(t *testing.T) {
	a := &App{Cfg: DefaultConfig()}
	root := &exportNode{
		Meta: &SessionMeta{
			ID: "sess-main", Title: "导出测试", Mode: "assistant",
			Provider: "deepseek", Model: "deepseek-flash",
			Permission: "workspace-write", PlanMode: true, CreatedAt: "2026-09-24 12:00:00",
		},
		Messages: []*StoredMessage{
			{Message: TextMessage("user", "帮我创建 demo.txt"), CreatedAt: "2026-09-24 12:00:01"},
			{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "c1", Function: ToolCallFunc{Name: "write_file", Arguments: `{"path":"demo.txt","content":"hi"}`},
			}}, Content: ""}, Thinking: "先想一下路径", CreatedAt: "2026-09-24 12:00:02"},
			{Message: Message{Role: "tool", Content: "已写入 demo.txt（2 字节）", ToolCallID: "c1"}, CreatedAt: "2026-09-24 12:00:03"},
			{Message: TextMessage("user", "【派发回报】子任务完成"), CreatedAt: "2026-09-24 12:01:00"},
			{Message: TextMessage("assistant", "全部完成"), CreatedAt: "2026-09-24 12:01:05"},
			{Kind: "summary", Origin: "auto", CoversUpTo: 3, Superseded: true,
				Message: TextMessage("user", "旧自动摘要：创建了文件"), CreatedAt: "2026-09-24 12:02:00"},
			{Kind: "summary", Origin: "manual", CoversUpTo: 5,
				Message: TextMessage("user", "手动摘要：任务完成"), CreatedAt: "2026-09-24 12:03:00"},
		},
		Children: []*exportNode{{
			Meta:     &SessionMeta{ID: "sess-sub", Title: "演示子任务", Mode: "task", CreatedAt: "2026-09-24 12:00:30", ParentID: "sess-main"},
			Messages: []*StoredMessage{{Message: TextMessage("assistant", "子任务正文"), CreatedAt: "2026-09-24 12:00:40"}},
		}},
	}
	md := a.ExportMarkdown(root)

	for _, want := range []string{
		"# 导出测试",
		"会话：sess-main（数字助理）",
		"模型：deepseek/deepseek-flash",
		"计划模式：开",
		"思考过程（完整原文）", "先想一下路径",
		"工具调用 write_file（参数完整原文）",
		`write_file {"path":"demo.txt","content":"hi"}`,
		"工具结果 · 2026-09-24 12:00:03 · 全文", "未截断", "已写入 demo.txt（2 字节）",
		"📎 系统记录 · 2026-09-24 12:01:00", "【派发回报】子任务完成",
		"[摘要 · 自动 · 覆盖至第 3 条 · 已被更新的自动摘要合并]",
		"[摘要 · 手动 · 覆盖至第 5 条]",
		"子会话（1 个",
		"📎 任务：演示子任务",
		"来源：派生自会话 sess-main",
		"子任务正文",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("导出缺少 %q\n--- 渲染结果 ---\n%s", want, md)
		}
	}
	if strings.Contains(md, "（截断）") {
		t.Error("导出不应再出现截断标记")
	}
}

// 工具结果超长也不截断（旧实现 2000 字截断）。
func TestExportNoTruncation(t *testing.T) {
	a := &App{Cfg: DefaultConfig()}
	long := strings.Repeat("字", 5000)
	root := &exportNode{
		Meta: &SessionMeta{ID: "s", Title: "t", Mode: "task", CreatedAt: "2026-09-24 12:00:00"},
		Messages: []*StoredMessage{
			{Message: Message{Role: "tool", Content: long, ToolCallID: "c1"}, CreatedAt: "2026-09-24 12:00:01"},
		},
	}
	md := a.ExportMarkdown(root)
	if !strings.Contains(md, "全文 5000 字（未截断）") {
		t.Errorf("未标注全文字数：%s", md[:min(len(md), 200)])
	}
	if got := strings.Count(md, "字"); got < 5000 {
		t.Errorf("工具结果被截断：只出现 %d 个字，应 5000", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
