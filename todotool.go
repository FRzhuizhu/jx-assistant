package main

// todo_write（对齐官方标准栈的 todo 工具）：模型维护当前任务的待办清单，
// 前端在会话顶部渲染进度。清单整体替换写入（每次都提交全量列表），
// 持久化在 meta 表（会话附属数据），经 /api/sessions/{id}/todos 读取。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TodoItem 是清单中的一项。
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

func todoWriteTool() Tool {
	return Tool{
		Spec: toolSpec("todo_write", "维护当前任务的待办清单（整体替换：每次提交完整列表）。用于多步骤任务：开始前把工作拆成待办；开工一项置 in_progress（同时只能有一项）；做完置 completed 再开下一项。简单到一两步的事不必用。",
			objSchema(map[string]any{
				"todos": objSchema(map[string]any{
					"content": strProp("待办内容，一句话"),
					"status":  strProp("pending | in_progress | completed"),
				}),
			}, "todos")),
		Run: runTodoWrite,
	}
}

func runTodoWrite(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if len(p.Todos) == 0 {
		// 空清单 = 清空（任务取消/不再需要时可用）
		_ = t.app.Store.SetSessionData(t.sess.ID, "todos", "")
		t.app.broadcast(Event{Type: "todos", SessionID: t.sess.ID})
		return "待办清单已清空。", nil
	}
	inProgress := 0
	for i := range p.Todos {
		td := &p.Todos[i]
		td.Content = strings.TrimSpace(td.Content)
		if td.Content == "" {
			return "", fmt.Errorf("第 %d 项 content 为空", i+1)
		}
		switch td.Status {
		case "pending", "":
			td.Status = "pending"
		case "in_progress":
			inProgress++
		case "completed":
		default:
			return "", fmt.Errorf("第 %d 项状态非法：%s（可选 pending / in_progress / completed）", i+1, td.Status)
		}
		if len(td.Content) > 500 {
			td.Content = truncateRunes(td.Content, 500)
		}
	}
	if inProgress > 1 {
		return "", fmt.Errorf("同时只能有一项 in_progress（当前 %d 项），请把其余改为 pending", inProgress)
	}
	data, err := json.Marshal(p.Todos)
	if err != nil {
		return "", err
	}
	if err := t.app.Store.SetSessionData(t.sess.ID, "todos", string(data)); err != nil {
		return "", err
	}
	t.app.broadcast(Event{Type: "todos", SessionID: t.sess.ID})
	done := 0
	for _, td := range p.Todos {
		if td.Status == "completed" {
			done++
		}
	}
	return fmt.Sprintf("待办清单已更新（%d 项，完成 %d）。", len(p.Todos), done), nil
}

// todosOf 读取会话的待办清单（前端 API 用）；空返回 nil。
func todosOf(a *App, sessionID string) []TodoItem {
	raw := a.Store.GetSessionData(sessionID, "todos")
	if raw == "" {
		return nil
	}
	out := []TodoItem{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}
