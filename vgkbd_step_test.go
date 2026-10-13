//go:build windows

package main

// 键盘快捷键判定状态机（vgKbdStep）的行为测试：真钩子没法用合成输入验证
//（SendInput 事件带 LLKHF_INJECTED 标记、恰好是被判定的排除项），把纯逻辑
// 抽出来在这里覆盖全部关键场景。键名→VK 与 Windows 常量一致：
// alt=0x12 a=0x41 tab=0x09 f9=0x78。

import "testing"

func tapCfg(names ...[]string) *vgKbdCfg { return vgBuildKbdCfg(names, false) }
func holdCfg(names ...[]string) *vgKbdCfg { return vgBuildKbdCfg(names, true) }

func press(s *vgKbdState, cfg *vgKbdCfg, vk uint32) (bool, bool, bool) {
	return vgKbdStep(s, cfg, vk, true, false)
}
func release(s *vgKbdState, cfg *vgKbdCfg, vk uint32) (bool, bool, bool) {
	return vgKbdStep(s, cfg, vk, false, false)
}

// 默认副快捷键＝独按 Alt：点按在松开沿 toggle
func TestVgKbdSoloAltTap(t *testing.T) {
	cfg := tapCfg([]string{"alt"})
	var s vgKbdState
	if tap, st, sp := press(&s, cfg, 0x12); tap || st || sp {
		t.Fatal("独按 Alt 按下沿不应触发")
	}
	if tap, _, _ := release(&s, cfg, 0x12); !tap {
		t.Fatal("独按 Alt 松开沿应 toggle")
	}
	// 已松开后再来的多余 keyup：静默
	if tap, _, _ := release(&s, cfg, 0x12); tap {
		t.Fatal("未按下时的 keyup 不应触发")
	}
}

// 组合键 Alt+A：单按 A 不响应，Alt+A 完整按下才响应
func TestVgKbdComboAltA(t *testing.T) {
	cfg := tapCfg([]string{"alt", "a"})
	var s vgKbdState
	if tap, _, _ := press(&s, cfg, 0x41); tap { // 单按 A
		t.Fatal("单按 A 不应触发")
	}
	if tap, _, _ := release(&s, cfg, 0x41); tap {
		t.Fatal("单按 A 松开也不应触发")
	}
	if tap, _, _ := press(&s, cfg, 0x12); tap { // 按下 Alt（组合未完整）
		t.Fatal("Alt+A 未完整前不应触发")
	}
	if tap, _, _ := press(&s, cfg, 0x41); !tap { // A 补齐 → 触发沿
		t.Fatal("Alt+A 完整按下沿应触发")
	}
	if tap, _, _ := press(&s, cfg, 0x41); tap { // repeat：不重复触发
		t.Fatal("repeat 不应再次触发")
	}
	release(&s, cfg, 0x41)
	if tap, _, _ := release(&s, cfg, 0x12); tap {
		t.Fatal("组合松开沿不应再触发")
	}
	// 组合全部松开后可再次触发
	press(&s, cfg, 0x12)
	if tap, _, _ := press(&s, cfg, 0x41); !tap {
		t.Fatal("组合松干净后应能再次触发")
	}
}

// 组合键先 A 后 Alt（另一种按下顺序）同样只认完整组合
func TestVgKbdComboOrderReversed(t *testing.T) {
	cfg := tapCfg([]string{"alt", "a"})
	var s vgKbdState
	press(&s, cfg, 0x41)
	if tap, _, _ := press(&s, cfg, 0x12); !tap {
		t.Fatal("先 A 后 Alt 的补齐沿应触发")
	}
}

// 单功能键（F9）＝单键组合，按下沿即触发
func TestVgKbdSingleFKey(t *testing.T) {
	cfg := tapCfg([]string{"f9"})
	var s vgKbdState
	if tap, _, _ := press(&s, cfg, 0x78); !tap {
		t.Fatal("F9 按下沿应触发")
	}
	release(&s, cfg, 0x78)
	if tap, _, _ := press(&s, cfg, 0x78); !tap {
		t.Fatal("F9 松开后应能再次触发")
	}
}

// 独按 Alt 期间混入其他键（Alt+Tab）：解除，松开不触发
func TestVgKbdSoloCancelledByForeign(t *testing.T) {
	cfg := tapCfg([]string{"alt"})
	var s vgKbdState
	press(&s, cfg, 0x12)
	press(&s, cfg, 0x09) // Tab
	release(&s, cfg, 0x09)
	if tap, _, _ := release(&s, cfg, 0x12); tap {
		t.Fatal("混键后的 Alt 松开不应触发")
	}
}

// 配置 Alt+A 后独按 Alt 不触发（组合键不认独立按键）
func TestVgKbdSoloAltNotWhenComboConfigured(t *testing.T) {
	cfg := tapCfg([]string{"alt", "a"})
	var s vgKbdState
	press(&s, cfg, 0x12) // Alt（独按，但配置里没有独按 Alt）
	if tap, _, _ := release(&s, cfg, 0x12); tap {
		t.Fatal("配置 Alt+A 后独按 Alt 不应触发")
	}
}

// 注入事件（SendInput）一律不触发，也不污染按下状态
func TestVgKbdInjectedIgnored(t *testing.T) {
	cfg := tapCfg([]string{"alt"})
	var s vgKbdState
	if tap, _, _ := vgKbdStep(&s, cfg, 0x12, true, true); tap {
		t.Fatal("注入的按下沿不应触发")
	}
	if tap, _, _ := vgKbdStep(&s, cfg, 0x12, false, true); tap {
		t.Fatal("注入的松开沿不应触发")
	}
	if s.held[0x12] {
		t.Fatal("注入事件不应计入按下状态")
	}
}

// 长按模式：独按 Alt 按下沿开会、松开沿收
func TestVgKbdHoldSoloAlt(t *testing.T) {
	cfg := holdCfg([]string{"alt"})
	var s vgKbdState
	if _, st, _ := press(&s, cfg, 0x12); !st {
		t.Fatal("长按独按 Alt 按下沿应开会")
	}
	if _, _, sp := release(&s, cfg, 0x12); !sp {
		t.Fatal("长按独按 Alt 松开沿应收会")
	}
}

// 长按模式组合键：补齐沿开会、任一键松开即收
func TestVgKbdHoldCombo(t *testing.T) {
	cfg := holdCfg([]string{"ctrl", "shift", "9"})
	var s vgKbdState
	press(&s, cfg, 0x11)
	press(&s, cfg, 0x10)
	if _, st, _ := press(&s, cfg, 0x39); !st { // '9'
		t.Fatal("组合补齐沿应开会")
	}
	if _, _, sp := release(&s, cfg, 0x10); !sp { // 松 Shift 即收
		t.Fatal("组合任一键松开应收会")
	}
}

// 长按独按中混键：立即收会（Alt+Tab 别拖着会话）
func TestVgKbdHoldSoloCancelledByForeign(t *testing.T) {
	cfg := holdCfg([]string{"alt"})
	var s vgKbdState
	press(&s, cfg, 0x12)
	if _, _, sp := press(&s, cfg, 0x09); !sp {
		t.Fatal("长按独按中混键应立即收会")
	}
	release(&s, cfg, 0x09)
	if _, _, sp := release(&s, cfg, 0x12); sp {
		t.Fatal("已收过的会话不应重复收")
	}
}

// 空配置（页面把快捷键全清了/全是鼠标键）：无动作
func TestVgKbdEmptyCfg(t *testing.T) {
	cfg := tapCfg()
	var s vgKbdState
	if tap, st, sp := press(&s, cfg, 0x12); tap || st || sp {
		t.Fatal("空配置不应有任何动作")
	}
	if tap, st, sp := press(&s, cfg, 0x41); tap || st || sp {
		t.Fatal("空配置不应有任何动作")
	}
}
