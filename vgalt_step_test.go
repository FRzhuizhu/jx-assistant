//go:build windows

package main

// 独按 Alt 判定状态机（vgAltStep）的行为测试：真钩子没法用合成输入验证
//（SendInput 事件带 LLKHF_INJECTED 标记、恰好是被判定的排除项），把纯逻辑
// 抽出来在这里覆盖全部关键场景。

import "testing"

func altDownEv(vk uint32) (uint32, bool, bool) { return vk, true, false }
func altUpEv(vk uint32) (uint32, bool, bool)   { return vk, false, false }

func TestVgAltStepSoloTap(t *testing.T) {
	var s vgAltState
	// 独按 Alt：按下沿布防（fireDown），松开沿触发（fireUp）
	if fd, fu, ch := vgAltStep(&s, vkMenu, true, false); !fd || fu || ch {
		t.Fatalf("按下沿应只 fireDown，得到 %v/%v/%v", fd, fu, ch)
	}
	if fd, fu, ch := vgAltStep(&s, vkMenu, false, false); fd || !fu || ch {
		t.Fatalf("独按松开应只 fireUp，得到 %v/%v/%v", fd, fu, ch)
	}
	if s.down || s.solo {
		t.Fatalf("松开后状态应复位：down=%v solo=%v", s.down, s.solo)
	}
}

func TestVgAltStepRepeatIgnored(t *testing.T) {
	var s vgAltState
	vgAltStep(&s, vkMenu, true, false)
	// 系统重发的 repeat keydown：不重复布防
	if fd, _, _ := vgAltStep(&s, vkMenu, true, false); fd {
		t.Fatal("repeat 按下沿不应再 fireDown")
	}
	// 正常松开：仍是独按点按（repeat 不影响），应触发
	if _, fu, _ := vgAltStep(&s, vkMenu, false, false); !fu {
		t.Fatal("repeat 后的正常松开应触发")
	}
	// 已松开后再来的多余 keyup：静默
	if _, fu, _ := vgAltStep(&s, vkMenu, false, false); fu {
		t.Fatal("未按下时的 keyup 不应触发")
	}
}

func TestVgAltStepComboCancels(t *testing.T) {
	// Alt+Tab / Alt+F4：按下 Alt 后混入其他键 → 松开 Alt 不触发
	var s vgAltState
	vgAltStep(&s, vkMenu, true, false)
	vgAltStep(&s, 0x09, true, false) // Tab
	if _, fu, ch := vgAltStep(&s, vkMenu, false, false); fu || ch {
		t.Fatalf("混键后松开不应触发（tap 场景）：fireUp=%v cancelHold=%v", fu, ch)
	}
	// 先按其他键再按 Alt（组合键的另一种顺序）：也不触发
	var s2 vgAltState
	vgAltStep(&s2, 0x46, true, false) // F
	vgAltStep(&s2, vkMenu, true, false)
	if _, fu, _ := vgAltStep(&s2, vkMenu, false, false); fu {
		t.Fatal("组合中按下 Alt 后松开不应触发")
	}
}

func TestVgAltStepInjectedNeverFires(t *testing.T) {
	// SendInput 注入的 Alt（测试脚本、自动化工具）：不触发
	var s vgAltState
	if fd, _, _ := vgAltStep(&s, vkMenu, true, true); fd {
		t.Fatal("注入的 Alt 按下不应布防")
	}
	if _, fu, _ := vgAltStep(&s, vkMenu, false, true); fu {
		t.Fatal("注入的 Alt 松开不应触发")
	}
	if s.down {
		t.Fatal("状态应复位")
	}
}

func TestVgAltStepHoldMode(t *testing.T) {
	// 长按：按下沿开会（fireDown），松开沿收（fireUp）
	var s vgAltState
	vgAltStep(&s, vkMenu, true, false)
	s.started = true // vgAltHandle 确认开会后回填
	if _, fu, ch := vgAltStep(&s, vkMenu, false, false); !fu || ch {
		t.Fatalf("长按松开应 fireUp 收会话：fireUp=%v cancelHold=%v", fu, ch)
	}
	// 长按中混键（Alt+Tab）：立即 cancelHold，随后的松开沿不再动作
	var s2 vgAltState
	vgAltStep(&s2, vkMenu, true, false)
	s2.started = true
	if _, _, ch := vgAltStep(&s2, 0x09, true, false); !ch {
		t.Fatal("长按中混键应 cancelHold")
	}
	if _, fu, ch := vgAltStep(&s2, vkMenu, false, false); fu || ch {
		t.Fatalf("cancelHold 后松开沿应静默：fireUp=%v cancelHold=%v", fu, ch)
	}
}

func TestVgAltStepUnrelatedKeys(t *testing.T) {
	// Alt 未按下时，其他键的任意组合都不产生任何动作
	var s vgAltState
	for _, vk := range []uint32{0x41, 0x09, 0x1B, 0x70} {
		if fd, fu, ch := vgAltStep(&s, vk, true, false); fd || fu || ch {
			t.Fatalf("无 Alt 时按 %X 不应动作", vk)
		}
	}
}
