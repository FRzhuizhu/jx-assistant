package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 用真实快照（gitignore 构建产物 android/runtime/runtime.tgz）验证装配器：
// 解压 + sha256 校验 + 二次重写（制作机前缀 → 运行时前缀）+ 幂等水位。
// 本机无快照时跳过（干净 clone / CI）。
func TestTermuxEnsureFromSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过重资源测试（-short）")
	}
	snap := filepath.Join("android", "runtime", "runtime.tgz")
	manifest := filepath.Join("android", "runtime", "MANIFEST.json")
	if _, err := os.Stat(snap); err != nil {
		t.Skip("本机无运行时快照（构建产物），跳过")
	}
	dest := filepath.Join(t.TempDir(), "runtime")
	var msgs []string
	got, err := termuxEnsure(manifest, snap, dest, func(s string) { msgs = append(msgs, s) })
	if err != nil {
		t.Fatalf("装配失败: %v\n%s", err, strings.Join(msgs, "\n"))
	}
	if got != dest {
		t.Fatalf("prefix 路径不符: %s", got)
	}
	if _, err := os.Lstat(filepath.Join(dest, "bin", "bash")); err != nil {
		t.Fatalf("bin/bash 缺失: %v", err)
	}

	// python3 是符号链接：当前环境能否创建符号链接，决定是否做该项断言。
	symlinkOK := true
	if probe := filepath.Join(t.TempDir(), "probe"); os.Symlink("x", probe) != nil {
		symlinkOK = false
		t.Log("当前环境无符号链接权限（Windows 未开开发者模式），跳过链接断言")
	}
	if symlinkOK {
		if _, err := os.Lstat(filepath.Join(dest, "bin", "python3")); err != nil {
			t.Fatalf("bin/python3 缺失: %v", err)
		}
	}

	// 二次重写：全树不应再残留制作机前缀（符号链接目标 + bin 下文本）。
	residual := 0
	_ = filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if tgt, rerr := os.Readlink(path); rerr == nil && strings.Contains(tgt, "com.jianxiang.poc") {
				residual++
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dest, path)
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "bin/") && !strings.HasPrefix(rel, "etc/") {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil || fi.Size() > 1<<20 {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if len(data) > 8192 {
			data = data[:8192]
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil // 二进制（如 .pyc 的 co_filename）与装配器同口径：不视为残留
		}
		if strings.Contains(string(data), "com.jianxiang.poc") {
			residual++
			t.Logf("残留候选: %s", rel)
		}
		return nil
	})
	if residual > 0 {
		t.Fatalf("二次重写不完整：仍有 %d 处残留制作机前缀", residual)
	}

	// 幂等：第二次调用走版本水位短路（emit 为空）。
	if got2, err := termuxEnsure(manifest, snap, dest, nil); err != nil || got2 != dest {
		t.Fatalf("二次装配（幂等）失败: %v", err)
	}
}
