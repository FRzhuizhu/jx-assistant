//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 默认工作区改名迁移（安装目录\工作区 → 安装目录\默认工作区）：
// 旧目录原地改名、用户文件原地保留；没有旧目录时不动；新旧并存时以新目录为准、
// 旧目录不覆盖不删除。
func TestMigrateLegacyWorkspaceDir(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, legacyWorkspaceFolderName)
	newDir := filepath.Join(root, workspaceFolderName)
	note := filepath.Join(oldDir, "子目录", "笔记.txt")
	if err := os.MkdirAll(filepath.Dir(note), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("用户文件"), 0644); err != nil {
		t.Fatal(err)
	}

	legacy, migrated := migrateLegacyWorkspaceDir(oldDir, newDir)
	if legacy != oldDir || !migrated {
		t.Fatalf("旧目录应被改名：legacy=%q migrated=%v", legacy, migrated)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("旧目录应已不存在: %v", err)
	}
	// 改名不搬内容：用户的文件在新目录里原地还在
	if b, err := os.ReadFile(filepath.Join(newDir, "子目录", "笔记.txt")); err != nil || string(b) != "用户文件" {
		t.Fatalf("改名后用户文件应完好: %v %q", err, string(b))
	}

	if legacy, migrated := migrateLegacyWorkspaceDir(oldDir, newDir); legacy != "" || migrated {
		t.Fatalf("没有旧目录时不应动作：legacy=%q migrated=%v", legacy, migrated)
	}

	// 新旧并存（用户自己又建了个「工作区」）：新目录为准，旧目录原样留着
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "旧.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if legacy, migrated := migrateLegacyWorkspaceDir(oldDir, newDir); legacy != oldDir || !migrated {
		t.Fatalf("新旧并存应报已落到新目录：legacy=%q migrated=%v", legacy, migrated)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "旧.txt")); err != nil {
		t.Fatalf("新旧并存时旧目录应原样保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "子目录", "笔记.txt")); err != nil {
		t.Fatalf("新目录里已有的内容不该被覆盖: %v", err)
	}
}
