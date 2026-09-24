package main

// 待办与定时任务（todo.go / todotools.go）单测：调度纯函数的时间边界、
// 持久化往返、增删改与清单排序、工具参数（schedule）转换。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// localMs 构造本地时区的 epoch 毫秒（测试里所有时刻都按本地时间写）。
func localMs(y int, mo time.Month, d, h, mi int) int64 {
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local).UnixMilli()
}

// todoTestApp 造一个带 store 与「组件已启用」注册表的 App（不跑调度循环）。
func todoTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	compDir := filepath.Join(dir, "todo-scheduler")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"todo-scheduler","name":"待办与定时任务","version":"1.0.0","description":"","client":{}}`
	if err := os.WriteFile(filepath.Join(compDir, "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	a := &App{Cfg: &Config{Workspace: dir, AllowShell: true}, Store: store,
		Components: InitComponents(dir, filepath.Join(dir, "components.json"))}
	a.initRuntime()
	return a
}

// todoSchedulerFor 造一个调度器（不起循环，测试直接调方法）。
func todoSchedulerFor(t *testing.T, a *App) *todoScheduler {
	t.Helper()
	s := &todoScheduler{app: a, busy: map[string]bool{}, tombs: map[string]bool{}}
	s.data = s.load()
	a.Todos = s
	return s
}

func TestParseTimeToMinutes(t *testing.T) {
	ok := map[string]int{"09:00": 540, "0:05": 5, "23:59": 1439, " 21:30 ": 1290}
	for in, want := range ok {
		got, valid := parseTimeToMinutes(in)
		if !valid || got != want {
			t.Fatalf("parseTimeToMinutes(%q) = %d,%v；期望 %d,true", in, got, valid, want)
		}
	}
	for _, bad := range []string{"", "24:00", "9:60", "9", "09:0", "aa:bb", "-1:00", "9:5:5"} {
		if _, valid := parseTimeToMinutes(bad); valid {
			t.Fatalf("parseTimeToMinutes(%q) 应当无效", bad)
		}
	}
}

func TestComputeNextRunAt(t *testing.T) {
	now := localMs(2026, time.March, 10, 12, 0) // 2026-03-10 是周二
	cases := []struct {
		name string
		s    Schedule
		last int64
		want int64
		ok   bool
	}{
		{"不定时没有下次", Schedule{Kind: schedManual}, 0, 0, false},
		{"每天未到点算今天", Schedule{Kind: schedDaily, Time: "21:00"}, 0, localMs(2026, time.March, 10, 21, 0), true},
		{"每天已过点算明天", Schedule{Kind: schedDaily, Time: "08:00"}, 0, localMs(2026, time.March, 11, 8, 0), true},
		{"工作日跳过周末", Schedule{Kind: schedWeekdays, Time: "08:00"}, 0, localMs(2026, time.March, 11, 8, 0), true},
		{"定时一次未到", Schedule{Kind: schedOnce, At: localMs(2026, time.March, 10, 13, 0)}, 0, localMs(2026, time.March, 10, 13, 0), true},
		{"定时一次已过无下次", Schedule{Kind: schedOnce, At: localMs(2026, time.March, 10, 11, 0)}, 0, 0, false},
		{"每周只算勾选的那天", Schedule{Kind: schedWeekly, Days: []int{5}, Time: "09:00"}, 0, localMs(2026, time.March, 13, 9, 0), true},
		{"每周空天集无下次", Schedule{Kind: schedWeekly, Days: nil, Time: "09:00"}, 0, 0, false},
		{"时刻非法无下次", Schedule{Kind: schedDaily, Time: "99:99"}, 0, 0, false},
		{"间隔按上次执行推算", Schedule{Kind: schedInterval, EveryMinutes: 30}, now, now + 30*60_000, true},
	}
	for _, c := range cases {
		got, ok := computeNextRunAt(c.s, now, c.last)
		if ok != c.ok || got != c.want {
			t.Fatalf("%s：computeNextRunAt = %d,%v；期望 %d,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestComputeDueSlot(t *testing.T) {
	day := localMs(2026, time.March, 10, 0, 0)
	created := day // 任务创建于当天零点

	// 每天 09:00：刚到点触发，槽点就是今天的 09:00
	nine := localMs(2026, time.March, 10, 9, 0)
	if slot, ok := computeDueSlot(Schedule{Kind: schedDaily, Time: "09:00"}, nine+30_000, 0, created); !ok || slot != nine {
		t.Fatalf("刚到点应触发，得到 %d,%v", slot, ok)
	}
	// 同一个槽点不重复触发
	if _, ok := computeDueSlot(Schedule{Kind: schedDaily, Time: "09:00"}, nine+50_000, nine, created); ok {
		t.Fatal("已跑过的槽点不应再次触发")
	}
	// 时间未到不提前跑
	if _, ok := computeDueSlot(Schedule{Kind: schedDaily, Time: "09:00"}, nine-60_000, 0, created); ok {
		t.Fatal("时间未到不应触发")
	}
	// 过期超过新鲜窗口（服务中途才起来）不自动追，转错过
	if _, ok := computeDueSlot(Schedule{Kind: schedDaily, Time: "09:00"}, nine+11*60_000, 0, created); ok {
		t.Fatal("过期超过新鲜窗口不应自动追")
	} else if _, missed := detectMissed(Schedule{Kind: schedDaily, Time: "09:00"}, nine+11*60_000, 0, created); !missed {
		t.Fatal("过期超过新鲜窗口应判为错过")
	}
	// 只工作日：周六不触发
	sat := localMs(2026, time.March, 14, 9, 0)
	if _, ok := computeDueSlot(Schedule{Kind: schedWeekdays, Time: "09:00"}, sat+10_000, 0, created); ok {
		t.Fatal("工作日任务周六不应触发")
	}
	// 每周只在勾选的天触发
	if _, ok := computeDueSlot(Schedule{Kind: schedWeekly, Days: []int{2}, Time: "09:00"}, sat+10_000, 0, created); ok {
		t.Fatal("每周任务未勾选周六不应触发")
	}
	tue := localMs(2026, time.March, 10, 9, 0)
	if slot, ok := computeDueSlot(Schedule{Kind: schedWeekly, Days: []int{2}, Time: "09:00"}, tue+10_000, 0, created); !ok || slot != tue {
		t.Fatalf("每周任务在勾选日应触发，得到 %d,%v", slot, ok)
	}
	// 定时一次：到点触发，过期不追
	once := nine
	if slot, ok := computeDueSlot(Schedule{Kind: schedOnce, At: once}, once+5_000, 0, created); !ok || slot != once {
		t.Fatalf("一次任务到点应触发，得到 %d,%v", slot, ok)
	}
	if _, ok := computeDueSlot(Schedule{Kind: schedOnce, At: once}, once+11*60_000, 0, created); ok {
		t.Fatal("一次任务过期超过新鲜窗口不应自动追")
	}
	// 间隔：浅落后自动追，深落后不追（转错过）
	now := localMs(2026, time.March, 10, 12, 0)
	last := now - 31*60_000
	if _, ok := computeDueSlot(Schedule{Kind: schedInterval, EveryMinutes: 30}, now, last, created); !ok {
		t.Fatal("间隔任务浅落后（一个节拍内）应自动追")
	}
	deep := now - 3*60*60_000
	if _, ok := computeDueSlot(Schedule{Kind: schedInterval, EveryMinutes: 30}, now, deep, created); ok {
		t.Fatal("间隔任务深落后不应自动追")
	} else if _, missed := detectMissed(Schedule{Kind: schedInterval, EveryMinutes: 30}, now, deep, created); !missed {
		t.Fatal("间隔任务深落后应判为错过")
	}
	// 从没跑过且从没配过：不立刻触发（避免启用即爆）
	if _, ok := computeDueSlot(Schedule{Kind: schedInterval, EveryMinutes: 30}, created+1_000, 0, created); ok {
		t.Fatal("间隔任务刚建好不应立刻触发")
	}
}

func TestDetectMissedOnceAndDaily(t *testing.T) {
	// 一次任务：错过就是那个点本身
	at := localMs(2026, time.March, 10, 9, 0)
	if slot, ok := detectMissed(Schedule{Kind: schedOnce, At: at}, at+3*60*60_000, 0, 0); !ok || slot != at {
		t.Fatalf("一次任务错过应返回该时刻，得到 %d,%v", slot, ok)
	}
	// 每天：只回最近一个，不连环
	now := localMs(2026, time.March, 12, 12, 0)
	if slot, ok := detectMissed(Schedule{Kind: schedDaily, Time: "08:00"}, now, 0, 0); !ok || slot != localMs(2026, time.March, 12, 8, 0) {
		t.Fatalf("每天任务错过应只回最近一个，得到 %d,%v", slot, ok)
	}
	// 已经跑过就不算错过
	if _, ok := detectMissed(Schedule{Kind: schedDaily, Time: "08:00"}, now, localMs(2026, time.March, 12, 8, 0), 0); ok {
		t.Fatal("已执行过的槽点不应算错过")
	}
	// 不定时永不自动跑，也永不「错过」
	if _, ok := detectMissed(Schedule{Kind: schedManual}, now, 0, 0); ok {
		t.Fatal("不定时任务不应有错过")
	}
}

func TestValidateSchedule(t *testing.T) {
	valid := []Schedule{
		{Kind: schedManual},
		{Kind: schedOnce, At: time.Now().UnixMilli()},
		{Kind: schedDaily, Time: "09:00"},
		{Kind: schedWeekdays, Time: "23:59"},
		{Kind: schedWeekly, Time: "09:00", Days: []int{1, 3, 5}},
		{Kind: schedInterval, EveryMinutes: 30},
	}
	for _, s := range valid {
		if err := validateSchedule(s); err != nil {
			t.Fatalf("%+v 应合法：%v", s, err)
		}
	}
	invalid := []Schedule{
		{Kind: "everyday"},
		{Kind: schedOnce},
		{Kind: schedDaily, Time: "9:0"},
		{Kind: schedWeekly, Time: "09:00"},
		{Kind: schedWeekly, Time: "09:00", Days: []int{7}},
		{Kind: schedInterval, EveryMinutes: 0},
		{Kind: schedInterval, EveryMinutes: 60 * 24 * 31},
	}
	for _, s := range invalid {
		if err := validateSchedule(s); err == nil {
			t.Fatalf("%+v 应判为非法", s)
		}
	}
}

func TestScheduleArgsToSchedule(t *testing.T) {
	s, err := scheduleArgs{Kind: "once", At: "2026-03-10 21:00"}.toSchedule()
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != schedOnce || s.At != localMs(2026, time.March, 10, 21, 0) {
		t.Fatalf("once 转换错误：%+v", s)
	}
	s, err = scheduleArgs{Kind: "weekly", Time: "09:30", Days: []int{1, 5}}.toSchedule()
	if err != nil || s.Kind != schedWeekly || len(s.Days) != 2 {
		t.Fatalf("weekly 转换错误：%+v %v", s, err)
	}
	if _, err := (scheduleArgs{Kind: "once", At: "明天"}).toSchedule(); err == nil {
		t.Fatal("无法解析的 at 应报错")
	}
	if _, err := (scheduleArgs{Kind: "weekly", Time: "09:00"}).toSchedule(); err == nil {
		t.Fatal("weekly 缺 days 应报错")
	}
	if _, err := (scheduleArgs{Kind: "interval", EveryMinutes: 0}).toSchedule(); err == nil {
		t.Fatal("interval 间隔为 0 应报错")
	}
}

func TestTodoPersistenceRoundTrip(t *testing.T) {
	a := todoTestApp(t)
	s := todoSchedulerFor(t, a)

	// 首启种一条示例任务
	if len(s.data.Tasks) != 1 || s.data.Tasks[0].ID != "task-sample-dispatch" {
		t.Fatalf("首启应有示例任务，实得 %d 条", len(s.data.Tasks))
	}

	dir := t.TempDir()
	created, err := s.create(todoRequest{
		Title: "每天备份", Description: "把项目目录打包到 backups/ 下", Executor: execCommand,
		Schedule: &Schedule{Kind: schedDaily, Time: "23:00"}, Dir: dir, Command: "echo backup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Enabled != true || created.Executor != execCommand {
		t.Fatalf("新建结果不符合预期：%+v", created)
	}

	// 换一个实例重新载入（模拟重启）：数据必须还在
	reloaded := &todoScheduler{app: a, busy: map[string]bool{}, tombs: map[string]bool{}}
	reloaded.data = reloaded.load()
	if len(reloaded.data.Tasks) != 2 {
		t.Fatalf("重载后应有 2 条任务，实得 %d 条", len(reloaded.data.Tasks))
	}
	var found *TaskItem
	for _, task := range reloaded.data.Tasks {
		if task.ID == created.ID {
			found = task
		}
	}
	if found == nil || found.Title != "每天备份" || found.Command != "echo backup" || found.Dir != dir {
		t.Fatalf("重载后的任务与写入不一致：%+v", found)
	}

	// 更新：改执行方式会重置调度基准
	if _, err := reloaded.update(todoRequest{ID: created.ID, Enabled: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	views := reloaded.snapshot()["tasks"].([]TaskView)
	for _, v := range views {
		if v.ID == created.ID && v.Enabled {
			t.Fatal("停用未生效")
		}
	}

	// 删除：任务没了，但墓碑记住它（防止合并把删除复活）
	if err := reloaded.delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if !reloaded.tombs[created.ID] {
		t.Fatal("删除应记入墓碑")
	}
	if _, ok := reloaded.find(created.ID); ok {
		t.Fatal("删除后不应还能找到任务")
	}
}

// boolPtr 取 bool 的地址（请求体里的可选字段用指针区分「没给」与「给了 false」）。
func boolPtr(v bool) *bool { return &v }

func TestTodoSnapshotOrderAndMissed(t *testing.T) {
	a := todoTestApp(t)
	s := todoSchedulerFor(t, a)
	s.data.Tasks = nil
	// 一条不定时（排最后）、一条每天已过期（排前面：启用且有下次执行）
	if _, err := s.create(todoRequest{Title: "不定时待办", Description: "只记着", Schedule: &Schedule{Kind: schedManual}}); err != nil {
		t.Fatal(err)
	}
	soon, err := s.create(todoRequest{Title: "每天的活", Description: "干活", Schedule: &Schedule{Kind: schedDaily, Time: "00:01"}})
	if err != nil {
		t.Fatal(err)
	}
	views := s.snapshot()["tasks"].([]TaskView)
	if len(views) != 2 {
		t.Fatalf("应有 2 条任务，实得 %d", len(views))
	}
	if views[0].ID != soon.ID {
		t.Fatalf("有下次执行的任务应排前面，实得 %s", views[0].Title)
	}
	if views[0].NextRunAt == 0 {
		t.Fatal("启用的定时任务应算出下次执行时间")
	}
	// 当天 00:01 已过（若测试恰在 00:01 之前运行则不算错过）
	if time.Now().After(time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 2, 0, 0, time.Local)) {
		missed := s.snapshot()["missed"].([]MissedItem)
		if len(missed) != 1 || missed[0].TaskID != soon.ID {
			t.Fatalf("过期任务应出现在错过列表：%+v", missed)
		}
	}
}

func TestTodoUpdateResetsBaseOnScheduleChange(t *testing.T) {
	a := todoTestApp(t)
	s := todoSchedulerFor(t, a)
	v, err := s.create(todoRequest{Title: "任务", Description: "描述", Schedule: &Schedule{Kind: schedDaily, Time: "09:00"}})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	first, _ := s.find(v.ID)
	first.LastRunAt = 12345
	s.mu.Unlock()
	if _, err := s.update(todoRequest{ID: v.ID, Schedule: &Schedule{Kind: schedDaily, Time: "10:00"}}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	task, _ := s.find(v.ID)
	base := task.LastRunAt
	s.mu.Unlock()
	if base != 0 {
		t.Fatalf("换执行方式应重置调度基准，实得 %d", base)
	}
}

func TestTodoCreateValidation(t *testing.T) {
	a := todoTestApp(t)
	s := todoSchedulerFor(t, a)
	// 缺标题
	if _, err := s.create(todoRequest{Description: "d", Schedule: &Schedule{Kind: schedManual}}); err == nil {
		t.Fatal("缺标题应报错")
	}
	// 缺描述
	if _, err := s.create(todoRequest{Title: "t", Schedule: &Schedule{Kind: schedManual}}); err == nil {
		t.Fatal("缺描述应报错")
	}
	// 程序任务缺命令
	if _, err := s.create(todoRequest{Title: "t", Description: "d", Executor: execCommand,
		Schedule: &Schedule{Kind: schedManual}, Dir: t.TempDir()}); err == nil {
		t.Fatal("程序任务缺命令应报错")
	}
	// 程序任务目录不存在
	if _, err := s.create(todoRequest{Title: "t", Description: "d", Executor: execCommand,
		Schedule: &Schedule{Kind: schedManual}, Dir: filepath.Join(t.TempDir(), "nope"), Command: "echo"}); err == nil {
		t.Fatal("工作目录不存在应报错")
	}
	// AI 任务不保留命令字段
	ok, err := s.create(todoRequest{Title: "t", Description: "d", Schedule: &Schedule{Kind: schedManual},
		Command: "echo", Dir: "C:\\", Executor: execAssistant})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Command != "" || ok.Dir != "" || ok.Permission != "" {
		t.Fatalf("AI 任务不应带命令/目录/权限：%+v", ok)
	}
}

func TestTodoToolsRegisteredOnlyWhenEnabled(t *testing.T) {
	a := todoTestApp(t)
	if got := len(a.todoTools()); got != 7 {
		t.Fatalf("组件启用时应注册 7 个工具，实得 %d", got)
	}
	if err := a.Components.SetEnabled(todoComponentID, false); err != nil {
		t.Fatal(err)
	}
	if got := len(a.todoTools()); got != 0 {
		t.Fatalf("组件停用后不应注册工具，实得 %d", got)
	}
	// 工具名与预期一致（模型白名单按名字走）
	if err := a.Components.SetEnabled(todoComponentID, true); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"todo_task_list": true, "todo_task_get": true, "todo_task_create": true,
		"todo_task_update": true, "todo_task_delete": true, "todo_task_run": true,
		"todo_workspace_list": true,
	}
	for _, tool := range a.todoTools() {
		if !want[tool.Spec.Function.Name] {
			t.Fatalf("出现未预期的工具名：%s", tool.Spec.Function.Name)
		}
		delete(want, tool.Spec.Function.Name)
	}
	if len(want) != 0 {
		t.Fatalf("缺少工具：%v", want)
	}
}

func TestTodoTaskListToolOutput(t *testing.T) {
	a := todoTestApp(t)
	s := todoSchedulerFor(t, a)
	if _, err := s.create(todoRequest{Title: "每周整理", Description: "写周报",
		Schedule: &Schedule{Kind: schedWeekly, Time: "18:00", Days: []int{5}}}); err != nil {
		t.Fatal(err)
	}
	tc := &toolContext{app: a, sess: &SessionMeta{ID: "s1", Mode: "task"}}
	out, err := runTodoTaskList(tc, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"共 2 条任务", "每周整理", "每周周五 18:00", "AI 任务"} {
		if !strings.Contains(out, want) {
			t.Fatalf("列表输出缺少 %q：\n%s", want, out)
		}
	}
	// 按 id 取详情
	id := ""
	s.mu.Lock()
	for _, task := range s.data.Tasks {
		if task.Title == "每周整理" {
			id = task.ID
		}
	}
	s.mu.Unlock()
	detail, err := runTodoTaskGet(tc, json.RawMessage(`{"task_id":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "写周报") || !strings.Contains(detail, "还没有执行记录") {
		t.Fatalf("详情输出不符合预期：\n%s", detail)
	}
	missing, err := runTodoTaskGet(tc, json.RawMessage(`{"task_id":"task-nope"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(missing, "找不到任务") {
		t.Fatalf("找不到任务时应给出可读提示：%s", missing)
	}
}
