package main

// browser_open 预授权（grants → writeProfileGrants/ApplyGrants）的针对性测试：
// 注入只动指定键、不丢既有设置；无头实例在跑时 ApplyGrants 停实例落盘后可重开。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteProfileGrants(t *testing.T) {
	dir := t.TempDir()
	// 预置一份带其它设置的 Preferences：注入必须只合并、不冲掉既有内容
	prefs := filepath.Join(dir, "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(prefs), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := map[string]any{"browser": map[string]any{"enabled_labs_experiments": []string{"x"}}}
	sb, _ := json.Marshal(seed)
	if err := os.WriteFile(prefs, sb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeProfileGrants(dir, []string{
		"automatic_downloads@https://mail.qq.com",
		"notifications@https://[*.]qq.com",
	}); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	raw, err := os.ReadFile(prefs)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("注入后 Preferences 解析失败: %v\n%s", err, raw)
	}
	// 既有键仍在
	if _, ok := got["browser"]; !ok {
		t.Fatal("注入冲掉了既有的顶层设置（browser 键丢失）")
	}
	// 新键就位
	prof := got["profile"].(map[string]any)
	cs := prof["content_settings"].(map[string]any)
	exc := cs["exceptions"].(map[string]any)
	dl := exc["automatic_downloads"].(map[string]any)
	e := dl["https://mail.qq.com,*"].(map[string]any)
	if e["setting"].(float64) != 1 {
		t.Fatalf("automatic_downloads 授权值不对: %v", e)
	}
	nt := exc["notifications"].(map[string]any)
	if _, ok := nt["https://[*.]qq.com,*"]; !ok {
		t.Fatal("notifications 授权键缺失")
	}
	// 重复注入幂等（同键覆写不报错不膨胀结构）
	if err := writeProfileGrants(dir, []string{"automatic_downloads@https://mail.qq.com"}); err != nil {
		t.Fatalf("重复注入失败: %v", err)
	}
	// 坏格式报错
	if err := writeProfileGrants(dir, []string{"没有at符号"}); err == nil {
		t.Fatal("坏格式未报错")
	}
}

// TestBrowserHostApplyGrants 端到端：无头实例在跑 → ApplyGrants 停实例并注入 →
// 再导航重开成功（浏览器重读 profile 正常启动，授权项在盘上）。
func TestBrowserHostApplyGrants(t *testing.T) {
	if findBrowserExe() == "" {
		t.Skip("系统未找到 Edge/Chrome")
	}
	dir := t.TempDir()
	h := newBrowserHost(&App{Cfg: &Config{DataDir: dir}}, true, filepath.Join(dir, "profile"))
	defer h.Close()
	if _, _, err := h.Navigate("data:text/html,<h1>first</h1>"); err != nil {
		t.Fatalf("首次导航失败: %v", err)
	}
	if !h.Started() {
		t.Fatal("实例未在运行")
	}
	if err := h.ApplyGrants([]string{"automatic_downloads@https://mail.qq.com"}); err != nil {
		t.Fatalf("ApplyGrants 失败: %v", err)
	}
	if h.Started() {
		t.Fatal("ApplyGrants 后实例应已停止（Preferences 需在未运行时注入）")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "profile", "Default", "Preferences"))
	if err != nil {
		t.Fatalf("读注入后的 Preferences 失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Preferences 解析失败: %v", err)
	}
	dl := got["profile"].(map[string]any)["content_settings"].(map[string]any)["exceptions"].(map[string]any)["automatic_downloads"]
	if dl == nil {
		t.Fatal("automatic_downloads 授权未落盘")
	}
	// 重开：授权后的 profile 能正常启动并导航
	if _, _, err := h.Navigate("data:text/html,<h1>second</h1>"); err != nil {
		t.Fatalf("授权后重开导航失败: %v", err)
	}
	if !h.Started() {
		t.Fatal("重开后实例未在运行")
	}
	_ = time.Second
}
