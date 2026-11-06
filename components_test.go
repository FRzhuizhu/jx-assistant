package main

// 组件开关状态的读时解释（components.go）与静默更新补装（update_blocks.go
// planComponentBlocks）的组合行为钉子——2026-11-06 客户事故的核心语义：
//   1. 开关状态只在用户手动切换时写值（SetEnabled），初始化不写——
//      「未记录」靠读时按默认启用解释（!rec || on）；
//   2. 老客户升级拿到补装的新内置组件目录 → rescan → 无记录 → 默认开，
//      入口自动可见；
//   3. 显式关过的用户（记录 false）升级补装/重启后保持关。

import (
	"os"
	"path/filepath"
	"testing"
)

// 场景：老客户升级链——本机从未有过该组件（无目录、无记录），静默更新补装
// 目录后（rescan），组件应默认启用；用户显式关闭后重启，应保持关。
func TestComponentEnabledAfterSilentInstall(t *testing.T) {
	dir := t.TempDir() // 模拟 组件/：初始没有 skill-market（老客户）
	state := filepath.Join(t.TempDir(), "components.json")
	r := InitComponents(dir, state)
	if r.IsEnabled("skill-market") {
		t.Fatal("目录都没有时不应启用")
	}
	if _, err := os.Stat(state); err == nil {
		t.Fatal("初始化不应主动写开关文件（未记录=默认开是读时解释，不落盘）")
	}

	// 静默更新补装目录（planComponentBlocks 放行 → applyComponentBlock 落盘
	// → Rescan，这里直接模拟目录落位）
	sub := filepath.Join(dir, "skill-market")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	m := `{"id":"skill-market","name":"市场","version":"1.0.0","client":{}}`
	if err := os.WriteFile(filepath.Join(sub, "manifest.json"), []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
	r.Rescan()
	if !r.IsEnabled("skill-market") {
		t.Fatal("补装后无记录应默认启用（升级即开，客户事故的修复语义）")
	}

	// 用户显式关闭 → 落盘 false → 重启（重新 Init 同一 state 文件）→ 保持关
	if err := r.SetEnabled("skill-market", false); err != nil {
		t.Fatal(err)
	}
	r2 := InitComponents(dir, state)
	if r2.IsEnabled("skill-market") {
		t.Fatal("显式关过的组件重启后应保持关")
	}
}
