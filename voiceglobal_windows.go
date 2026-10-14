//go:build windows

package main

// 全局语音输入法（胶囊浮窗听写）：把语音组件升级成类输入法体验——
// 在「软件对话框之外」的任意位置（本软件 git 面板/设置页等输入框、以及全电脑
// 任何程序的光标处）按全局快捷键（默认鼠标侧键点按切换；独按 Alt 为副）→
// 屏幕底部弹出胶囊浮窗（voicefloat_windows.go + web/voice-float.html）听写，
// 再按一次结束，文字粘贴进触发时的输入位置；那里不可输入则留在剪贴板
//（浮窗提示「文字已复制」）。
//
// 快捷键分工（与微信输入法一致的两分法）：
//   - 前台窗口属于本进程 → 钩子放行，页面内组件自己处理：焦点在主/子输入框
//     走原有灰字听写；不在则由组件转发 POST /api/voice/global/start（本模块
//     同一入口 toggle）。
//   - 前台是其他程序 → 钩子吞下侧键（防浏览器后退/前进）并 toggle 全局会话。
//
// 钩子纪律（与 recorder_windows.go 同一套）：低级钩子回调跑在系统钩子线程，
// 只做轻量判定（开关状态、会话代数、前台进程归属）与无阻塞投递；建浮窗、
// ExecuteScript、粘贴等重活全在 worker goroutine。自家 SendInput 注入的事件
// 带 LLMHF_INJECTED 标记，一律忽略（防 Ctrl+V 回环）。
//
// 会话代数（epoch）：浮窗页面的收尾 POST 带开始时下发的代数，迟到结果按过期
// 丢弃——快速开关两次时，旧会话的尾段识别不会串进新会话的目标。

import (
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const wmXButtonUp = 0x020C // vkMenu（VK_MENU＝Alt）已在 tool_computer_windows.go 定义

var (
	vgMu        sync.Mutex
	vgInstalled bool
	vgHookTID   uint32
	vgHookDone  chan struct{}
	vgEnabled   atomic.Bool
	vgHoldMode  atomic.Bool
	vgSession   atomic.Bool // 全局会话进行中（听写已开始、未收尾）
	vgEpoch     atomic.Int64
	vgTarget    atomic.Uintptr // 会话开始时的前台窗口（粘贴目标）
	vgApp       *App           // runDesktop 注入：shellEval 停掉页面内听写用
	vgCh        = make(chan vgEvent, 16)
	vgWorkerOnce sync.Once
	vgKbd       atomic.Pointer[vgKbdCfg] // 生效中的键盘快捷键配置（页面同步，apply 换入）
	vgKbdSt     vgKbdState               // 键盘判定状态（只在钩子线程读写）
)

type vgEvent struct{ kind int } // 0=toggle 1=start(hold按下) 2=end(hold松开)

func voiceGlobalSupported() bool { return true }

// voiceGlobalApply 应用配置（装卸钩子）。app 供 shellEval（停页面听写），
// baseURL 是主服务地址（浮窗页面导航用）。桌面壳启动与设置页热更都走这里；
// 浏览器形态从不调用（钩子是桌面壳专属）。
func voiceGlobalApply(app *App, baseURL string) {
	vgApp = app
	if baseURL != "" {
		voiceFloatBaseURL(baseURL)
	}
	enabled := app.Cfg == nil || app.Cfg.VoiceGlobal.Enabled == nil || *app.Cfg.VoiceGlobal.Enabled
	mode := "tap"
	if app.Cfg != nil && app.Cfg.VoiceGlobal.Mode == "hold" {
		mode = "hold"
	}
	vgHoldMode.Store(mode == "hold")
	// 键盘快捷键组合同步自页面（voiceGlobal.kbd；没同步过＝默认独按 Alt）。
	// 长按标志一并烘进配置，钩子状态机免第二次原子读。
	kbdNames := [][]string{{"alt"}}
	if app.Cfg != nil && len(app.Cfg.VoiceGlobal.Kbd) > 0 {
		kbdNames = app.Cfg.VoiceGlobal.Kbd
	}
	vgKbd.Store(vgBuildKbdCfg(kbdNames, vgHoldMode.Load()))
	wasEnabled := vgEnabled.Swap(enabled)
	if enabled == wasEnabled {
		vgEnabled.Store(enabled)
		return
	}
	if enabled {
		voiceGlobalInstall()
	} else {
		voiceGlobalUninstall()
	}
}

func voiceGlobalShutdown() {
	voiceGlobalUninstall()
}

// voiceGlobalReapply 设置页热更配置后的重应用（baseURL 不变，只重算开关与
// 模式；非桌面壳为空操作——voiceglobal_other.go 的桩不定义装卸，浏览器形态
// 从未安装过钩子）。浏览器形态的桩版本在 voiceglobal_other.go。
func voiceGlobalReapply(app *App) { voiceGlobalApply(app, "") }

// ---------- 常驻钩子（WH_MOUSE_LL，只盯侧键） ----------

func vgMouseHookCallback() uintptr {
	var once sync.Once
	var cb uintptr
	once.Do(func() {
		cb = syscall.NewCallback(func(nCode int32, wParam uintptr, lParam unsafe.Pointer) uintptr {
			if nCode >= 0 {
				down := wParam == wmXButtonDown
				up := wParam == wmXButtonUp
				if down || up {
					info := (*msllHookStruct)(lParam)
					if info.Flags&llmhfInjected == 0 {
						xbtn := info.MouseData >> 16 // 1=XBUTTON1 2=XBUTTON2（前后两枚侧键都算）
						if xbtn == 1 || xbtn == 2 {
							if vgHookHandle(down) {
								return 1 // 吞：会话相关或即将开启（防其他程序当后退/前进）
							}
						}
					}
				}
			}
			r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, uintptr(lParam))
			return r
		})
	})
	return cb
}

// vgHookHandle 钩子回调内的同步判定（必须轻量）。返回 true 表示吞掉该事件。
func vgHookHandle(down bool) bool {
	if !vgEnabled.Load() {
		return false
	}
	inSession := vgSession.Load()
	if vgHoldMode.Load() {
		// 长按：按下开、松开停。会话中的 down/up 都吞（防误触发后退）；
		// 会话外的 down 且前台非本进程 → 开启。
		if inSession {
			if !down {
				vgPost(vgEvent{kind: 2})
			}
			return true
		}
		if down && !vgForegroundIsSelf() {
			vgPost(vgEvent{kind: 1})
			return true
		}
		return false
	}
	// 点按：down 触发 toggle。会话中 → 结束（吞）；前台非本进程 → 开启（吞）。
	if inSession {
		if down {
			vgPost(vgEvent{kind: 0})
		}
		return true
	}
	if down && !vgForegroundIsSelf() {
		vgPost(vgEvent{kind: 0})
		return true
	}
	return false
}

// vgForegroundIsSelf 前台窗口是否属于本进程（是则放行给页面内组件处理）。
func vgForegroundIsSelf() bool {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return false
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
	self, _, _ := procGetCurrentProcessID.Call()
	return pid == uint32(self)
}

func vgPost(ev vgEvent) {
	select {
	case vgCh <- ev:
	default: // 队列满：丢弃（快捷键狂按的冗余事件）
	}
}

// ---------- 键盘钩子（WH_KEYBOARD_LL，监测配置成快捷键的键盘组合） ----------
//
// 快捷键是可配置的（主/副两槽，支持独键与组合键）：页面把槽位里的**键盘组合**
// 经 /api/config（voiceGlobal.kbd）同步过来，这里照配置监测——配置是什么就认
// 什么：组合键（如 Alt+A）只在组合完整按下时触发，单按其中一个键（如单按 A、
// 或单按 Alt）不响应；独按修饰键（如默认的 Alt）点按在松开沿触发，按住期间
// 混入任何其他键（Alt+Tab/Alt+F4…）即解除。只观察、绝不吞键（Alt/Ctrl 等
// 是系统修饰键，吞了会废掉所有程序的菜单与组合键）；注入事件（SendInput）
// 一律忽略。

// vgVKName 页面键名 → VK 码（键名与组件 normKey 同一套）。
func vgVKName(s string) (uint32, bool) {
	switch s {
	case "ctrl":
		return 0x11, true
	case "alt":
		return 0x12, true
	case "shift":
		return 0x10, true
	case "meta": // Win 键（左枚举；右 Win 0x5C 也按它认）
		return 0x5B, true
	case "space":
		return 0x20, true
	}
	if len(s) == 1 {
		c := s[0]
		if c >= 'a' && c <= 'z' {
			return uint32(c-'a') + 0x41, true
		}
		if c >= '0' && c <= '9' {
			return uint32(c-'0') + 0x30, true
		}
	}
	if len(s) == 2 && s[0] == 'f' && s[1] >= '1' && s[1] <= '9' { // F1..F9
		return uint32(s[1]-'1') + 0x70, true
	}
	if len(s) == 3 && s[0] == 'f' && s[1] == '1' && s[2] >= '0' && s[2] <= '2' { // F10..F12
		return uint32(s[2]-'0') + 0x78, true
	}
	return 0, false
}

func vgIsModVK(vk uint32) bool {
	return vk == 0x10 || vk == 0x11 || vk == 0x12 || vk == 0x5B
}

// vgKbdCfg 一份生效中的键盘快捷键配置（apply 时构建、原子换入，钩子线程只读）。
type vgKbdCfg struct {
	combos [][]uint32 // 非独按组合（含单功能键如 F9）：组合**完整按下沿**触发
	solos  []uint32   // 独按修饰键（Alt/Ctrl/Shift/Win 单键）：点按松开沿触发
	hold   bool       // 长按模式（构建时从 vgHoldMode 烘进来）
	label  string     // 人类可读描述（安装日志用）
}

func vgVKDisplay(s string) string {
	switch s {
	case "ctrl":
		return "Ctrl"
	case "alt":
		return "Alt"
	case "shift":
		return "Shift"
	case "meta":
		return "Win"
	case "space":
		return "空格"
	}
	return strings.ToUpper(s)
}

func vgBuildKbdCfg(names [][]string, hold bool) *vgKbdCfg {
	c := &vgKbdCfg{hold: hold}
	var parts []string
	for _, combo := range names {
		if len(combo) == 0 || len(combo) > 4 {
			continue
		}
		vks := make([]uint32, 0, len(combo))
		for _, k := range combo {
			vk, ok := vgVKName(k)
			if !ok {
				vks = nil
				break
			}
			vks = append(vks, vk)
		}
		if len(vks) == 0 {
			continue
		}
		disp := make([]string, len(combo))
		for i, k := range combo {
			disp[i] = vgVKDisplay(k)
		}
		parts = append(parts, strings.Join(disp, "+"))
		if len(vks) == 1 && vgIsModVK(vks[0]) {
			c.solos = append(c.solos, vks[0])
		} else {
			c.combos = append(c.combos, vks)
		}
	}
	if len(parts) == 0 {
		c.label = "无键盘快捷键"
	} else {
		c.label = strings.Join(parts, "、")
	}
	return c
}

// vgKbdState 键盘判定状态（只在钩子线程里读写，无需加锁；抽出纯逻辑便于测试）。
type vgKbdState struct {
	held      [256]bool
	armed     uint32 // 布防中的独按修饰键 VK（0=无；布防后混入其他键即解除）
	armedHold bool   // 布防的独按键已在长按模式开会
	kbdHold   bool   // 键盘开启的长按会话进行中
	latch     int    // 点按已触发的组合下标+1（组合键全松开后复位；0=无）
}

// vgKbdStep 喂一个键盘事件，返回动作：fireTap＝点按 toggle；fireStart／
// fireStop＝长按开会／收会。injected（SendInput 注入）事件一律无动作。
func vgKbdStep(s *vgKbdState, cfg *vgKbdCfg, vk uint32, down, injected bool) (fireTap, fireStart, fireStop bool) {
	if cfg == nil || injected || vk == 0 || vk >= 256 {
		return
	}
	if down {
		if s.held[vk] {
			return // 系统重发的 repeat
		}
		// 布防中的独按键遇到别的键：解除（Alt+X 组合保护；长按已开会的立即收）
		if s.armed != 0 && s.armed != vk {
			s.armed = 0
			if s.armedHold {
				s.armedHold = false
				s.kbdHold = false
				fireStop = true
			}
		}
		s.held[vk] = true
		// 组合完整按下沿：本次按下的键属于该组合且组合自此完整
		for i, combo := range cfg.combos {
			hit, complete := false, true
			for _, k := range combo {
				if k == vk {
					hit = true
				}
				if !s.held[k] {
					complete = false
				}
			}
			if !hit || !complete {
				continue
			}
			if cfg.hold {
				if !s.kbdHold {
					s.kbdHold = true
					fireStart = true
				}
			} else if s.latch != i+1 {
				s.latch = i + 1
				fireTap = true
			}
		}
		// 独按修饰键布防
		if s.armed == 0 {
			for _, m := range cfg.solos {
				if m == vk {
					s.armed = m
					if cfg.hold {
						s.armedHold = true
						s.kbdHold = true
						fireStart = true
					}
					break
				}
			}
		}
		return
	}
	// 松开沿
	s.held[vk] = false
	if s.armed == vk {
		s.armed = 0
		if s.armedHold {
			s.armedHold = false
			s.kbdHold = false
			fireStop = true
		} else if !cfg.hold {
			fireTap = true // 独按点按：松开沿 toggle
		}
		return
	}
	if s.kbdHold { // 长按会话还在的判定：组合仍完整或独按仍布防
		active := s.armedHold
		if !active {
			for _, combo := range cfg.combos {
				complete := true
				for _, k := range combo {
					if !s.held[k] {
						complete = false
						break
					}
				}
				if complete {
					active = true
					break
				}
			}
		}
		if !active {
			s.kbdHold = false
			fireStop = true
		}
	}
	if s.latch != 0 { // 组合键全部松开：允许下一次点按
		still := false
		for _, k := range cfg.combos[s.latch-1] {
			if s.held[k] {
				still = true
				break
			}
		}
		if !still {
			s.latch = 0
		}
	}
	return
}

func vgKbdHookCallback() uintptr {
	var once sync.Once
	var cb uintptr
	once.Do(func() {
		cb = syscall.NewCallback(func(nCode int32, wParam uintptr, lParam unsafe.Pointer) uintptr {
			if nCode >= 0 {
				down := wParam == wmKeydown || wParam == wmSysKeydown
				up := wParam == wmKeyup || wParam == wmSysKeyup
				if down || up {
					k := (*kbdllHookStruct)(lParam)
					tap, start, stop := vgKbdStep(&vgKbdSt, vgKbd.Load(), k.VkCode, down, k.Flags&llkhfInjected != 0)
					if tap || start || stop {
						vgKbdHandle(tap, start, stop)
					}
				}
			}
			// 键盘事件一概放行：只观察，触发与否全靠上面投递的事件
			r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, uintptr(lParam))
			return r
		})
	})
	return cb
}

// vgKbdHandle 键盘触发的投递。开始/toggle 只在前台非本进程时发（本进程内
// 由页面组件自己处理，钩子再发会双开）；收会话不挑前台——长按中切回本窗
// 松键也得能收，否则会话悬到 12 秒兜底。
func vgKbdHandle(fireTap, fireStart, fireStop bool) {
	if !vgEnabled.Load() {
		return
	}
	if fireStop {
		vgPost(vgEvent{kind: 2})
	}
	if fireStart || fireTap {
		if vgForegroundIsSelf() {
			return
		}
		if fireStart {
			vgPost(vgEvent{kind: 1})
		}
		if fireTap {
			vgPost(vgEvent{kind: 0})
		}
	}
}

func voiceGlobalInstall() {
	vgMu.Lock()
	defer vgMu.Unlock()
	if vgInstalled {
		return
	}
	hookDone := make(chan struct{})
	ready := make(chan uint32, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(hookDone)
		vgHookLoop(ready, errCh)
	}()
	var tid uint32
	select {
	case tid = <-ready:
	case err := <-errCh:
		log.Printf("[语音输入法] 全局钩子安装失败: %v", err)
		return
	case <-time.After(3 * time.Second):
		log.Printf("[语音输入法] 全局钩子安装超时")
		return
	}
	vgInstalled = true
	vgHookTID = tid
	vgHookDone = hookDone
	// vgWorker 是无限事件循环，必须在独立 goroutine 里跑：sync.Once.Do 会在
	// 调用者线程同步执行 f——此前主线程（runDesktop 的启动路径）在 Do 里直接
	// 进了 for-range 阻塞，w.Run() 永远执行不到，桌面壳整个冻死（浏览器面板
	// 创建排不上队只是最先暴露的症状）。
	go vgWorkerOnce.Do(vgWorker)
	// 2026-10-14 系统级钩子已停用（见 vgHookLoop 注释）：日志如实说明现状
	log.Printf("[语音输入法] 已启用（系统级钩子已停用，仅本软件内快捷键弹浮窗；其他软件按键不再接管）")
}

func voiceGlobalUninstall() {
	vgMu.Lock()
	defer vgMu.Unlock()
	if !vgInstalled {
		return
	}
	vgInstalled = false
	procPostThreadMessageW.Call(uintptr(vgHookTID), wmQuit, 0, 0)
	if vgHookDone != nil {
		select {
		case <-vgHookDone:
		case <-time.After(2 * time.Second):
		}
	}
	vgHookDone = nil
	log.Printf("[语音输入法] 已停用")
}

// vgHookLoop 专用线程安装低级鼠标+键盘钩子并跑消息循环（recorder_windows.go
// 同款；低级钩子要求安装线程有消息循环且保持存活），WM_QUIT 退出并卸钩。
func vgHookLoop(ready chan<- uint32, errCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// 2026-10-14 停用系统级钩子（用户决定）：实机出现「其他软件里按键无响应、
	// 全系统键鼠短暂锁死（关闭本软件后仍持续一会儿、狂点几次才恢复）」——
	// 低级钩子坐在全系统输入路径上做判定，回调偶发超时（LowLevelHooksTimeout）
	// 就会被系统拖慢整条输入链甚至静默移除，复杂度与收益不成比例。
	// 停用后的行为：
	//   · 其他软件里按键恢复原生行为（不再被吞、不再有锁死风险）；
	//   · 本软件内各处（焦点不在主/子输入框）按快捷键仍弹胶囊浮窗——页面
	//     转发路径（POST /api/voice/global/start）不经过系统钩子，照常可用；
	//   · 吞键判定、键盘组合状态机、浮窗与粘贴注入全部原样保留。
	// 恢复方法：取消下面两段注释（连同函数尾部的两行 Unhook）即可。
	//
	// hMouse, _, err := procSetWindowsHookEx.Call(whMouseLL, vgMouseHookCallback(), 0, 0)
	// if hMouse == 0 {
	// 	errCh <- err
	// 	return
	// }
	// hKbd, _, errKbd := procSetWindowsHookEx.Call(whKeyboardLL, vgKbdHookCallback(), 0, 0)
	// if hKbd == 0 { // 键盘钩子失败不致命：鼠标侧键（主快捷键）仍可用
	// 	log.Printf("[语音输入法] 键盘钩子安装失败（独按 Alt 副快捷键不可用）: %v", errKbd)
	// }
	tid, _, _ := procGetCurrentThreadID.Call()
	ready <- uint32(tid)
	var msg recMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) {
			break
		}
	}
	// if hKbd != 0 {
	// 	procUnhookWindowsHookEx.Call(hKbd)
	// }
	// procUnhookWindowsHookEx.Call(hMouse)
}

// ---------- 会话编排（worker goroutine） ----------

func vgWorker() {
	for ev := range vgCh {
		switch ev.kind {
		case 0: // toggle
			if vgSession.Load() {
				vgEnd()
			} else {
				vgBegin()
			}
		case 1:
			if !vgSession.Load() {
				vgBegin()
			}
		case 2:
			if vgSession.Load() {
				vgEnd()
			}
		}
	}
}

func vgBegin() {
	if err := voiceFloatEnsure(); err != nil {
		return // 创建失败：保持无会话（下一次触发再试一次也不行，浮窗已记失败）
	}
	ep := vgEpoch.Add(1)
	if !vgSession.CompareAndSwap(false, true) {
		return
	}
	h, _, _ := procGetForegroundWindow.Call()
	vgTarget.Store(h)
	// 停掉页面内进行中的听写（防双录；焦点本来就不在输入框，留着只是浪费麦克风）
	if vgApp != nil && vgApp.shellEval != nil {
		vgApp.shellEval("window.JXVoiceStopAll&&window.JXVoiceStopAll()")
	}
	voiceFloatShow()
	voiceFloatEval("__vgStart(" + strconv.FormatInt(ep, 10) + ")")
}

func vgEnd() {
	if !vgSession.CompareAndSwap(true, false) {
		return
	}
	voiceFloatEval("__vgStop()")
	// 页面收尾（尾段识别最多 8 秒）后 POST /api/voice/global/finish；这里兜底：
	// 页面若挂了没回包，12 秒后强制隐藏浮窗，别让胶囊赖在屏幕上。
	time.AfterFunc(12*time.Second, voiceFloatHide)
}

// voiceGlobalToggle 页面转发入口（软件内非对话框区按快捷键：组件 POST
// /api/voice/global/start → 这里，与钩子同一 toggle 语义）。
func voiceGlobalToggle() {
	if !vgEnabled.Load() {
		return
	}
	vgPost(vgEvent{kind: 0})
}

// voiceGlobalStopSession 只收不 toggle：进行中的全局会话走结束路径（页面内
// 听写开启前调用，防双录）；没有会话就是空操作。
func voiceGlobalStopSession() {
	if vgSession.Load() {
		vgPost(vgEvent{kind: 2})
	}
}

// voiceGlobalFinish 浮窗页收尾回调（POST /api/voice/global/finish）：
// 校验代数 → 写剪贴板 → 恢复目标窗口 → 模拟 Ctrl+V → 浮窗提示结果并隐藏。
func voiceGlobalFinish(text string, epoch int64) {
	if epoch != vgEpoch.Load() {
		voiceFloatHide() // 迟到的旧会话结果：丢弃并收场
		return
	}
	pasted := false
	if text != "" {
		if err := writeClipboardText(text); err != nil {
			log.Printf("[语音输入法] 写剪贴板失败: %v", err)
		}
		target := vgTarget.Load()
		if ok, _, _ := procVfIsWindow.Call(uintptr(target)); ok != 0 {
			// 浮窗全程 NOACTIVATE，理论上前台从未离开目标；被切走则尝试拉回，
			// 拉不回（前台锁）就退化为仅复制（文字仍在剪贴板）。
			if fg, _, _ := procGetForegroundWindow.Call(); uintptr(fg) == uintptr(target) {
				pasted = vgPaste()
			} else if r, _, _ := procSetForegroundWindow.Call(uintptr(target)); r != 0 {
				time.Sleep(150 * time.Millisecond) // 激活稳定
				pasted = vgPaste()
			}
		}
	}
	// 文案由页面 __vgDone 按 pasted 自行区分（已输入 ✓ / 已复制到剪贴板 / 未识别到语音）
	voiceFloatEval("__vgDone(" + boolQuote(pasted && text != "") + ")")
	time.AfterFunc(1500*time.Millisecond, voiceFloatHide)
}

// vgPaste 模拟 Ctrl+V（tool_computer_windows.go 的 SendInput 通道）。
func vgPaste() bool {
	if err := computerSendKey("v", true, false, false); err != nil {
		log.Printf("[语音输入法] 粘贴注入失败: %v", err)
		return false
	}
	return true
}

func boolQuote(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
