package main

// 会话浏览器 profile 按天回收的针对性测试（browser_cleanup.go）：
// 超龄被清、未超龄保留、白名单（活实例/在跑会话）不删、0=不清理、
// 共享目录与非法形状目录不在范围。真实浏览器目录用假目录模拟即可——
// 清理只看 mtime 与目录名形状。

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bpTestApp 拿最小 App：cleanupBrowserProfiles 只碰 Cfg、锁字段、Store(nil)
// 与内存注册表，零值 App 即可承载。
func bpTestApp(t *testing.T, days *int) *App {
	t.Helper()
	dir := t.TempDir()
	return &App{Cfg: &Config{DataDir: dir, BrowserProfileDays: days}}
}

// bpMkDir 建一个带旧 mtime 的 profile 目录与几个文件。
func bpMkDir(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(p, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "Default", "Local State"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := time.Now().Add(-age)
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	// 子目录与文件同步做旧：活跃判定看树内最新 mtime，别让新文件污染用例
	_ = os.Chtimes(filepath.Join(p, "Default"), mod, mod)
	_ = os.Chtimes(filepath.Join(p, "Default", "Local State"), mod, mod)
	return p
}

func TestBrowserProfileRetentionDays(t *testing.T) {
	a := &App{}
	if got := a.browserProfileRetentionDays(); got != 3 {
		t.Fatalf("未配置应回落默认 3，实际 %d", got)
	}
	for _, c := range []struct {
		in, want int
	}{{0, 0}, {7, 7}, {-5, 0}} {
		a.Cfg = &Config{BrowserProfileDays: &c.in}
		if got := a.browserProfileRetentionDays(); got != c.want {
			t.Fatalf("配置 %d 应解析为 %d，实际 %d", c.in, c.want, got)
		}
	}
}

func TestCleanupBrowserProfiles(t *testing.T) {
	d3 := 3
	a := bpTestApp(t, &d3)
	now := time.Now()

	old := bpMkDir(t, filepath.Join(a.Cfg.DataDir, "browser-profiles-headed"), "aa11bb22cc33dd44ee", 5*24*time.Hour)   // 超龄
	fresh := bpMkDir(t, filepath.Join(a.Cfg.DataDir, "browser-profiles-headed"), "bb22cc33dd44ee55ff", 1*24*time.Hour) // 未超龄
	wlive := bpMkDir(t, filepath.Join(a.Cfg.DataDir, "webview-browsers"), "cc33dd44ee55ff6600", 5*24*time.Hour)        // 超龄但活实例
	_ = bpMkDir(t, filepath.Join(a.Cfg.DataDir, "browser-profiles"), "dd44ee55ff66007711", 5*24*time.Hour)             // 超龄，应清
	shared := bpMkDir(t, a.Cfg.DataDir, "browser-profile-headed", 90*24*time.Hour)                                     // 共享目录（非会话根）
	weird := bpMkDir(t, filepath.Join(a.Cfg.DataDir, "webview-browsers"), "带空格 的目录", 90*24*time.Hour)                  // 非法形状

	a.browsersHeaded = map[string]*BrowserHost{"cc33dd44ee55ff6600": {}} // 白名单：活实例

	a.cleanupBrowserProfiles(now)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超龄目录应被清理")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("未超龄目录应保留")
	}
	if _, err := os.Stat(wlive); err != nil {
		t.Fatal("白名单（活实例）目录即使超龄也应保留")
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatal("共享目录不在扫描范围，应保留")
	}
	if _, err := os.Stat(weird); err != nil {
		t.Fatal("非法形状目录不在清理范围，应保留")
	}
}

func TestCleanupBrowserProfilesSubtreeFresh(t *testing.T) {
	// 树内最新 mtime 判活跃：顶层目录很旧，但子目录里有新写入 → 整棵保留
	d3 := 3
	a := bpTestApp(t, &d3)
	p := bpMkDir(t, filepath.Join(a.Cfg.DataDir, "browser-profiles-headed"), "ee55ff660077118822", 5*24*time.Hour)
	if err := os.Chtimes(filepath.Join(p, "Default"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	a.cleanupBrowserProfiles(time.Now())
	if _, err := os.Stat(p); err != nil {
		t.Fatal("子目录有新写入（浏览器在用）的 profile 不应被清理")
	}
}

func TestCleanupBrowserProfilesDisabled(t *testing.T) {
	zero := 0
	a := bpTestApp(t, &zero)
	p2 := bpMkDir(t, filepath.Join(a.Cfg.DataDir, "browser-profiles-headed"), "ff6600771188229933", 90*24*time.Hour)
	a.cleanupBrowserProfiles(time.Now())
	if _, err := os.Stat(p2); err != nil {
		t.Fatal("保留天数配 0 = 不清理，超龄目录也应保留")
	}
}
