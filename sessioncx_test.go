package main

// 会话级组件/插件开关覆盖（sessioncx.go）的行为钉子：
//   1. 覆盖优先于全局：会话把全局开着的关掉、把全局关着的打开，都要成立；
//   2. 与全局拨成一致的值＝覆盖记录删除（恢复跟随全局，存储不膨胀）；
//   3. cxoComponentLive（worker/端点存活口径）：全局关了但还有会话要它 → 活；
//   4. 持久化：覆盖写进会话附属数据，重启（重新 loadSessionCX）仍在；
//      会话删除 → forget 摘除，custom 归假。

import (
	"os"
	"path/filepath"
	"testing"
)

// 造一个带两个组件的测试 App：voice 全局开、git-panel 全局关（写状态文件）。
func newSessionCXTestApp(t *testing.T) (*App, string, string) {
	t.Helper()
	dir := t.TempDir()
	compDir := filepath.Join(dir, "组件")
	for _, id := range []string{"voice", "git-panel"} {
		sub := filepath.Join(compDir, id)
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		m := `{"id":"` + id + `","name":"` + id + `","version":"1.0.0","client":{}}`
		if err := os.WriteFile(filepath.Join(sub, "manifest.json"), []byte(m), 0644); err != nil {
			t.Fatal(err)
		}
	}
	dataDir := t.TempDir()
	// git-panel 显式关（未记录=默认开的读时解释）
	state := `{"git-panel":false}` + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, "components.json"), []byte(state), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a := &App{
		Store:      store,
		Components: InitComponents(compDir, filepath.Join(dataDir, "components.json")),
		Layers:     &PluginLayerRegistry{},
	}
	a.SessionCX = loadSessionCX(store)
	// 两个会话：A、B
	mk := func(id string) *SessionMeta {
		m := &SessionMeta{ID: id, Mode: "task"}
		if err := store.Create(m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	return a, mk("sess-a").ID, mk("sess-b").ID
}

func TestSessionCXOverridePriority(t *testing.T) {
	a, sidA, sidB := newSessionCXTestApp(t)

	// 基线：voice 全局开、git-panel 全局关
	if !a.cxoComponentEnabled(sidA, "voice") || a.cxoComponentEnabled(sidA, "git-panel") {
		t.Fatal("基线：无覆盖时应等于全局开关")
	}

	// A 会话关 voice（全局开着）→ A 关、B 与全局不受影响
	a.cxoSetComponent(sidA, "voice", false)
	if a.cxoComponentEnabled(sidA, "voice") {
		t.Fatal("A 覆盖关 voice 后，A 里应停用")
	}
	if !a.cxoComponentEnabled(sidB, "voice") || !a.Components.IsEnabled("voice") {
		t.Fatal("A 的覆盖不应影响 B 与全局开关")
	}
	if !a.SessionCX.custom(sidA) || a.SessionCX.custom(sidB) {
		t.Fatal("sessionCustom 只应标在 A")
	}

	// A 会话开 git-panel（全局关着）→ A 开
	a.cxoSetComponent(sidA, "git-panel", true)
	if !a.cxoComponentEnabled(sidA, "git-panel") || a.Components.IsEnabled("git-panel") {
		t.Fatal("A 覆盖开 git-panel 后：A 生效、全局仍关")
	}

	// cxoComponentLive：voice 全局仍开 → 活；git-panel 全局关但有会话要 → 活；
	// 拨回去后（无任何会话要）→ 不活
	if !a.cxoComponentLive("voice") || !a.cxoComponentLive("git-panel") {
		t.Fatal("全局开着或有会话要着的组件应视为活")
	}
	a.cxoSetComponent(sidA, "git-panel", false)
	if a.cxoComponentLive("git-panel") {
		t.Fatal("全局关且无会话要的组件应视为不活")
	}
}

func TestSessionCXOverrideNormalizesToGlobal(t *testing.T) {
	a, sidA, _ := newSessionCXTestApp(t)
	a.cxoSetComponent(sidA, "voice", false) // 覆盖：关
	if !a.SessionCX.custom(sidA) {
		t.Fatal("写入覆盖后应标记定制")
	}
	a.cxoSetComponent(sidA, "voice", true) // 拨回与全局一致 → 覆盖删除
	if a.SessionCX.custom(sidA) {
		t.Fatal("拨回全局值后覆盖应删除（恢复跟随全局）")
	}
	if !a.cxoComponentEnabled(sidA, "voice") {
		t.Fatal("跟随全局＝开")
	}
}

func TestSessionCXPersistAndForget(t *testing.T) {
	a, sidA, sidB := newSessionCXTestApp(t)
	a.cxoSetComponent(sidA, "voice", false)
	a.cxoSetComponent(sidA, "git-panel", true)
	a.cxoSetPlugin(sidA, "claude", "demo@market", true)

	// 重启（重新 load）：覆盖仍在
	r2 := loadSessionCX(a.Store)
	if !r2.custom(sidA) || r2.custom(sidB) {
		t.Fatal("重启后 A 的覆盖应仍在、B 应仍无覆盖")
	}
	if r2.of(sidA) == nil || r2.of(sidA).Components["voice"] != false || r2.of(sidA).Components["git-panel"] != true {
		t.Fatal("重启后覆盖内容应完整")
	}
	if r2.of(sidA).Plugins["claude/demo@market"] != true {
		t.Fatal("插件覆盖键应为 层/插件id")
	}

	// 会话删除 → forget + 附属数据行一并清
	a.SessionCX.forget(sidA)
	if a.SessionCX.custom(sidA) {
		t.Fatal("forget 后不应再标记定制")
	}
}
