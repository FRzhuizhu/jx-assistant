package main

// 迁移机制单测（migrations.go）：执行记账后不再执行、失败不记账下次重试、
// 按序执行互不影响；正式清单在「已跑过旧版清理的存量机器」上重跑安全（回归）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunMigrationListRecordsAndSkips 账本语义：成功记账、已记账跳过、失败
// 不记账（下次重试）且不阻断后续任务。
func TestRunMigrationListRecordsAndSkips(t *testing.T) {
	a := newSkillTestApp(t)
	ran := map[string]int{}
	list := []appMigration{
		{id: "t1.ok", desc: "成功任务", run: func(a *App) error { ran["t1.ok"]++; return nil }},
		{id: "t2.fail", desc: "失败任务", run: func(a *App) error { ran["t2.fail"]++; return os.ErrPermission }},
		{id: "t3.ok", desc: "失败后的任务", run: func(a *App) error { ran["t3.ok"]++; return nil }},
	}
	a.runMigrationList(list)
	if ran["t1.ok"] != 1 || ran["t3.ok"] != 1 || ran["t2.fail"] != 1 {
		t.Fatalf("执行次数不对（失败任务不应阻断后续）: %#v", ran)
	}
	recs := a.loadMigrationRecs()
	if _, ok := recs["t1.ok"]; !ok {
		t.Fatal("成功任务应记账")
	}
	if _, ok := recs["t2.fail"]; ok {
		t.Fatal("失败任务不应记账（下次要重试）")
	}

	// 第二轮：已记账的不再执行；上轮失败的这轮成功了才记账
	list[1].run = func(a *App) error { ran["t2.fail"]++; return nil }
	a.runMigrationList(list)
	if ran["t1.ok"] != 1 || ran["t3.ok"] != 1 {
		t.Fatalf("已记账任务不应重复执行: %#v", ran)
	}
	if ran["t2.fail"] != 2 {
		t.Fatalf("失败任务应重试: %#v", ran)
	}
	if _, ok := a.loadMigrationRecs()["t2.fail"]; !ok {
		t.Fatal("重试成功后应记账")
	}
}

// TestMigrationsIdempotentOnMigratedMachines 正式清单的回归：模拟「已跑过
// 20261030-5 旧版每次启动清理的机器」（目录已删）再跑迁移清单——安全无事，
// 且已清理过的泄漏技能不会再出现。
func TestMigrationsIdempotentOnMigratedMachines(t *testing.T) {
	a := newSkillTestApp(t)
	userDir, _ := a.userSkillsDir()
	os.MkdirAll(filepath.Join(userDir, "jxmes-master"), 0755)
	os.WriteFile(filepath.Join(userDir, "jxmes-master", "SKILL.md"),
		[]byte("---\nname: x\ndescription: d\n---\n正文"), 0644)

	a.runMigrations() // 第一次：清掉泄漏技能，两个迁移记账
	if dirExists(filepath.Join(userDir, "jxmes-master")) {
		t.Fatal("泄漏技能应被清理")
	}
	recs := a.loadMigrationRecs()
	for _, id := range []string{"20261030.builtin-skill-from-user", "20261030.purge-leaked-skills"} {
		if _, ok := recs[id]; !ok {
			t.Fatalf("迁移 %s 应记账: %#v", id, recs)
		}
	}

	// 第二次启动（新会话场景）：手工再放一个泄漏目录模拟「拷回误操作」，
	// 已记账的迁移不执行 → 目录保留（账本语义优先于重复清理）
	os.MkdirAll(filepath.Join(userDir, "jxmes-master"), 0755)
	a2 := &App{Cfg: a.Cfg} // 同一 DataDir：账本在；新实例：SkillsDir 缓存清零
	a2.runMigrations()
	if !dirExists(filepath.Join(userDir, "jxmes-master")) {
		t.Fatal("已记账的迁移不应重复执行（账本语义）")
	}
}

// 账本时间可序列化（time.Time ↔ JSON RFC3339），重启读回不丢。
func TestMigrationRecsRoundTrip(t *testing.T) {
	a := newSkillTestApp(t)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	out, err := json.Marshal(map[string]time.Time{"x": at})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(a.migrationsPath(), out); err != nil {
		t.Fatal(err)
	}
	recs := a.loadMigrationRecs()
	got, ok := recs["x"]
	if !ok || got.Format("2006-01-02 15:04") != at.Format("2006-01-02 15:04") {
		t.Fatalf("账本往返不对: %#v", recs)
	}
}
