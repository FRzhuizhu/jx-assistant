package main

// 组件机制的两组钉子：
//  1. 开关状态的读时解释（components.go）与静默更新补装（update_blocks.go
//     planComponentBlocks）的组合行为——2026-11-06 客户事故的核心语义：
//     开关状态只在用户手动切换时写值（SetEnabled），初始化不写；「未记录」
//     靠读时按默认启用解释（!rec || on）；老客户升级拿到补装的新内置组件
//     目录 → rescan → 无记录 → 默认开；显式关过的保持关。
//  2. 两档归属（内置 组件/<id> 与用户 组件/user/<id>）：同一注册表、同一套
//     开关与资产；内置不可删除、用户可删除；同名时内置优先；清单入口按需
//     重扫（不重启服务也能看见新放进去的组件目录）。

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// writeComp 造一个最小组件目录（manifest + client.js）。
func writeComp(t *testing.T, root, id, name, version string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	m := `{"id":"` + id + `","name":"` + name + `","version":"` + version + `","client":{"js":"client.js"}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.js"), []byte("// "+id), 0644); err != nil {
		t.Fatal(err)
	}
}

// 两档归属：内置在 组件/<id>、用户在 组件/user/<id>，清单靠 builtin 分区；
// 用户组件可删除（目录与开关态一并清），内置组件拒绝删除。
func TestComponentsBuiltinAndUserTiers(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "components.json")
	writeComp(t, root, "git-panel", "Git 面板", "1.0.0")
	writeComp(t, filepath.Join(root, userComponentsSubdir), "my-ext", "我的扩展", "0.1.0")

	r := InitComponents(root, state)
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("两档组件应都进清单，实得 %d 条: %+v", len(list), list)
	}
	byID := map[string]ComponentStatus{}
	for _, c := range list {
		byID[c.ID] = c
	}
	if !byID["git-panel"].BuiltIn || byID["my-ext"].BuiltIn {
		t.Fatalf("归属标记错: git-panel builtin=%v（应 true）、my-ext builtin=%v（应 false）",
			byID["git-panel"].BuiltIn, byID["my-ext"].BuiltIn)
	}
	if dir, _ := r.CompDir("my-ext"); filepath.Base(dir) != "my-ext" || filepath.Base(filepath.Dir(dir)) != userComponentsSubdir {
		t.Fatalf("用户组件目录应锚在 组件/user/ 下，实得 %s", dir)
	}

	// 内置不可删除；用户可删除，且开关态一并清掉
	if err := r.Remove("git-panel"); err == nil {
		t.Fatal("内置组件不应允许删除")
	}
	if err := r.SetEnabled("my-ext", false); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("my-ext"); err != nil {
		t.Fatalf("用户组件应可删除: %v", err)
	}
	if _, ok := r.Manifest("my-ext"); ok {
		t.Fatal("删除后不应还在清单里")
	}
	if _, err := os.Stat(filepath.Join(root, userComponentsSubdir, "my-ext")); !os.IsNotExist(err) {
		t.Fatalf("删除后目录应消失: %v", err)
	}
	if r.IsEnabled("git-panel") != true {
		t.Fatal("内置组件开关态不该被用户组件删除波及")
	}
}

// 同名时内置优先（内置由程序持有，用户侧不能顶替）。
func TestComponentIDCollisionBuiltinWins(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "components.json")
	writeComp(t, root, "voice", "语音（内置）", "1.3.6")
	writeComp(t, filepath.Join(root, userComponentsSubdir), "voice", "语音（用户）", "9.9.9")

	r := InitComponents(root, state)
	m, ok := r.Manifest("voice")
	if !ok || m.Name != "语音（内置）" {
		t.Fatalf("同名应以内置为准，实得 %q", m.Name)
	}
	if !r.IsBuiltin("voice") {
		t.Fatal("同名项归属应为内置")
	}
}

// 发现机制缺口：进程启动后新放进来的组件目录（手工/market 以外的渠道），
// 清单接口按需重扫即能看见——不必重启服务（桌面版单实例常驻，重启窗口
// 不等于重启服务）。节流窗口内的请求不重复扫。
func TestComponentsMaybeRescanPicksUpNewDir(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "components.json")
	r := InitComponents(root, state)
	if len(r.List()) != 0 {
		t.Fatal("初始应为空")
	}
	writeComp(t, root, "user-memory", "AI 记忆与拟人化", "1.1.0")
	// 把上一轮扫描时间推到节流窗口之外（模拟「启动早已扫过」的常驻进程）
	r.mu.Lock()
	r.lastScan = time.Time{}
	r.mu.Unlock()
	r.maybeRescan() // 首次调用在扫描窗口外，立即重扫
	if _, ok := r.Manifest("user-memory"); !ok {
		t.Fatal("运行中新放入的组件目录应被按需重扫发现")
	}
	// 节流：紧接着再放一个，2 秒内不该被扫到
	writeComp(t, root, "late-one", "迟到组件", "0.1.0")
	r.maybeRescan()
	if _, ok := r.Manifest("late-one"); ok {
		t.Fatal("节流窗口内不应重复扫描")
	}
	// 过了窗口即可见
	time.Sleep(2100 * time.Millisecond)
	r.maybeRescan()
	if _, ok := r.Manifest("late-one"); !ok {
		t.Fatal("节流窗口过后应扫到新目录")
	}
}
