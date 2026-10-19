package main

// 「已完成」状态的测试：标记后调度停（tick 不触发、错过不再提示、视图不带
// 下次执行时间）、重开恢复；update 的 done 由系统落完成时刻（用户/模型只给布尔）；
// 模型工具 todo_task_update 同步 done 参数。

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newDoneTestApp 建带临时库的 App 与手动构造的调度器，并挂上真实组件清单
// （todo-scheduler 缺省启用——tick 的组件开关检查才能通过）。
func newDoneTestApp(t *testing.T) (*App, *todoScheduler) {
	t.Helper()
	a := newCompactTestApp(t)
	a.initRuntime()
	a.sessLocks = map[string]*sync.Mutex{}
	a.Cfg.Workspace = t.TempDir()
	a.Cfg.Providers = nil
	compDir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("组件", "todo-scheduler", "manifest.json"))
	if err != nil {
		t.Fatalf("读组件清单失败: %v", err)
	}
	dst := filepath.Join(compDir, "todo-scheduler", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, src, 0644); err != nil {
		t.Fatal(err)
	}
	a.Components = InitComponents(compDir, filepath.Join(t.TempDir(), "components.json"))
	s := &todoScheduler{app: a, busy: map[string]bool{}, tombs: map[string]bool{},
		data: &TodoData{Version: todoDataVersion, Tasks: []*TaskItem{}, Records: []*RunRecord{}}}
	return a, s
}

// 已完成的任务：tick 不触发（槽点已到也不派发）、collectMissed 不再提示、
// 视图不带下次执行时间；重新打开后恢复。
func TestTodoDoneStopsSchedulingAndArchives(t *testing.T) {
	a, s := newDoneTestApp(t)
	if !a.todoEnabled() {
		t.Fatal("测试环境应识别 todo-scheduler 组件（缺省启用）")
	}
	view, err := s.create(todoRequest{
		Title: "过期未跑", Description: "x", Executor: execAssistant,
		Schedule: &Schedule{Kind: schedOnce, At: time.Now().Add(-2 * time.Second).UnixMilli()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.NextRunAt != 0 {
		t.Fatalf("已过期的 once 任务不该有下次执行时间: %d", view.NextRunAt)
	}
	// 先看未完成时：错过会被 detectMissed 找到（对照组，证明过滤是 Done 引起的）
	if miss := s.collectMissed(); len(miss) != 1 || miss[0].TaskID != view.ID {
		t.Fatalf("未完成任务应有一条错过提示: %+v", miss)
	}
	// 标记完成：错过提示消失、视图不带下次执行时间
	if _, err := s.update(todoRequest{ID: view.ID, Done: todoBoolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	done := findTodoTask(t, s, view.ID)
	if !done.Done || done.DoneAt <= 0 {
		t.Fatalf("完成标记应落库并带系统时间: %+v", done)
	}
	if miss := s.collectMissed(); len(miss) != 0 {
		t.Fatalf("已完成任务的错过不该再提示: %+v", miss)
	}
	snap := s.snapshot()
	views, _ := snap["tasks"].([]TaskView)
	for _, v := range views {
		if v.ID == view.ID && v.NextRunAt != 0 {
			t.Fatalf("已完成任务不该带下次执行时间: %d", v.NextRunAt)
		}
	}
	// tick 不触发：把槽点换成「刚到」的 once（新鲜窗口内），Done 的任务不派发
	fresh := findTodoTask(t, s, view.ID)
	fresh.Schedule = Schedule{Kind: schedOnce, At: time.Now().UnixMilli()}
	s.mu.Lock()
	if task, ok := s.find(view.ID); ok {
		task.Schedule = fresh.Schedule
	}
	s.mu.Unlock()
	time.Sleep(50 * time.Millisecond) // 确保仍在新鲜窗口内
	s.tick()
	time.Sleep(150 * time.Millisecond)
	s.mu.Lock()
	records := len(s.data.Records)
	s.mu.Unlock()
	if records != 0 {
		t.Fatalf("已完成的任务不该被 tick 触发执行, got %d 条记录", records)
	}
	// 重新打开：Done 清掉、DoneAt 清零，错过提示回来（调度恢复）
	if _, err := s.update(todoRequest{ID: view.ID, Done: todoBoolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	reopened := findTodoTask(t, s, view.ID)
	if reopened.Done || reopened.DoneAt != 0 {
		t.Fatalf("重开应清完成状态: %+v", reopened)
	}
	if miss := s.collectMissed(); len(miss) != 1 {
		t.Fatalf("重开后错过提示应恢复: %+v", miss)
	}
}

// 模型工具同步：todo_task_update 有 done 参数；详情文本带完成状态。
func TestTodoUpdateToolDoneParam(t *testing.T) {
	props := paramTypes(t, todoTaskUpdateTool())
	if _, ok := props["done"]; !ok {
		t.Fatal("todo_task_update 缺 done 参数")
	}
}

func todoBoolPtr(b bool) *bool { return &b }
