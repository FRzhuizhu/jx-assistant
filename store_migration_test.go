package main

import (
	"os"
	"strings"
	"testing"
)

// 迁移账本三连测：旧库升级 / 全新建库 / 历史篡改拒绝启动。
// 旧库快照由外部准备在 data-mig-test/（跑前用 sqlite backup 拷贝现库）。
func TestMigrationsOnExistingDB(t *testing.T) {
	if _, err := os.Stat("data-mig-test/jx.db"); err != nil {
		t.Skip("没有旧库快照，跳过")
	}
	s, err := NewStore("data-mig-test")
	if err != nil {
		t.Fatalf("旧库升级失败: %v", err)
	}
	defer s.db.Close()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM schema_migration`).Scan(&n); err != nil || n != len(storeMigrations) {
		t.Fatalf("账本条数 %d（应 %d）err=%v", n, len(storeMigrations), err)
	}
	list, err := s.List()
	if err != nil || len(list) == 0 {
		t.Fatalf("升级后列表不可用: %v (%d 条)", err, len(list))
	}
}

func TestMigrationsOnFreshDB(t *testing.T) {
	os.RemoveAll("data-fresh-test")
	defer os.RemoveAll("data-fresh-test")
	s, err := NewStore("data-fresh-test")
	if err != nil {
		t.Fatalf("全新建库失败: %v", err)
	}
	defer s.db.Close()
	m := &SessionMeta{ID: "t1", Title: "迁移测试", CreatedAt: now(), UpdatedAt: now()}
	if err := s.Create(m); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := s.Append("t1", TextMessage("user", "hello")); err != nil {
		t.Fatalf("写消息失败: %v", err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM schema_migration`).Scan(&n)
	if n != len(storeMigrations) {
		t.Fatalf("新库账本条数 %d", n)
	}
}

func TestMigrationChecksumFrozen(t *testing.T) {
	os.RemoveAll("data-tamper-test")
	defer os.RemoveAll("data-tamper-test")
	if _, err := NewStore("data-tamper-test"); err != nil {
		t.Fatalf("首次建库失败: %v", err)
	}
	old := storeMigrations[0].sql[0]
	storeMigrations[0].sql[0] = old + " " // 篡改历史
	defer func() { storeMigrations[0].sql[0] = old }()
	_, err := NewStore("data-tamper-test")
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("篡改历史迁移应拒绝启动，得到: %v", err)
	}
}
