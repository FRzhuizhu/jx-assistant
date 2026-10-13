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
	vgAltKey    atomic.Bool // 独按 Alt 的全局触发开关（页面副快捷键含 Alt 时开；默认开）
	vgHoldMode  atomic.Bool
	vgSession   atomic.Bool // 全局会话进行中（听写已开始、未收尾）
	vgEpoch     atomic.Int64
	vgTarget    atomic.Uintptr // 会话开始时的前台窗口（粘贴目标）
	vgApp       *App           // runDesktop 注入：shellEval 停掉页面内听写用
	vgCh        = make(chan vgEvent, 16)
	vgWorkerOnce sync.Once
	vgAltSt     vgAltState // 独按 Alt 判定状态（只在钩子线程读写）
	vgAltStarted bool      // 长按模式：Alt 按下沿已开启会话（松开/混键时收掉）
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
	vgAltKey.Store(app.Cfg == nil || app.Cfg.VoiceGlobal.AltKey == nil || *app.Cfg.VoiceGlobal.AltKey)
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

// ---------- 键盘钩子（WH_KEYBOARD_LL，只认「独按 Alt」＝副快捷键） ----------
//
// 副快捷键默认是 Alt：别的程序在前台时页面收不到按键，必须由系统级键盘钩子
// 代办。只观察、绝不吞（返回值永远放行）——Alt 是系统修饰键，吞了会废掉所有
// 程序的菜单与组合键。独按判定与页面组件同款语义：按下后到松开前没混入任何
// 其他键（Alt+Tab、Alt+F4、AltGr 打字都算组合）才触发；点按模式在**松开沿**
// toggle，长按模式按下沿开、松开沿收。注入事件（SendInput）一律不算。

// 独按 Alt 的判定状态（只在钩子线程里读写，无需加锁；抽出纯逻辑便于测试）。
type vgAltState struct {
	down    bool     // Alt 当前按着
	solo    bool     // 本次 Alt 按下后尚未混入其他键（独按候选）
	started bool     // 长按模式：本次按下已开会（松开/混键时收掉）
	held    [256]bool // 其他键的按下状态（识别"先按住别的键再点 Alt"的组合）
}

func (s *vgAltState) anyHeld() bool {
	for i := range s.held {
		if s.held[i] { // held 里只记非 Alt 键（见 vgAltStep）
			return true
		}
	}
	return false
}

// vgAltStep 喂一个键盘事件，返回该沿的动作：fireDown＝按下沿（长按模式的开
// 始沿，是否真的开会由调用方按 enabled/前台再判）、fireUp＝独按松开沿（点按
// 模式的 toggle 沿 / 长按模式的结束沿）、cancelHold＝长按中混入其他键（立即
// 收会话）。injected（SendInput 注入）事件只维护物理按下状态，绝不触发。
func vgAltStep(s *vgAltState, vk uint32, down, injected bool) (fireDown, fireUp, cancelHold bool) {
	if vk != vkMenu {
		if vk < 256 {
			s.held[vk] = down
		}
		if s.down && down && !injected {
			s.solo = false // Alt+其他键：组合（Alt+Tab/Alt+F4/AltGr…），解除独按
			if s.started {
				s.started = false
				cancelHold = true
			}
		}
		return
	}
	if down {
		if !s.down { // 首次按下；系统重发的 repeat 忽略
			s.down = true
			// 独按＝按下前没有别的键按着（先按 F 再点 Alt 也是 Alt+F 组合）
			// 且本事件不是注入的
			s.solo = !injected && !s.anyHeld()
			if s.solo {
				fireDown = true
			}
		}
		return
	}
	if !s.down {
		return
	}
	s.down = false
	solo := s.solo && !injected
	s.solo = false
	if solo {
		fireUp = true
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
					fd, fu, ch := vgAltStep(&vgAltSt, k.VkCode, down, k.Flags&llkhfInjected != 0)
					if fd {
						vgAltHandle(true)
					} else if fu {
						vgAltHandle(false)
					} else if ch {
						vgPost(vgEvent{kind: 2}) // 长按中混键：别拖着会话（Alt+Tab 场景）
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

// vgAltHandle 独按 Alt 的按下沿（down=true）/松开沿。前台是本进程时让页面内
// 组件自己处理（WebView2 若把 Alt 传进了页面，那里是同一套语义；钩子再触发
// 会双开）。维护 vgAltStarted（长按模式是否已由 Alt 开会）。
func vgAltHandle(down bool) {
	if !vgEnabled.Load() || !vgAltKey.Load() || vgForegroundIsSelf() {
		return
	}
	if vgHoldMode.Load() {
		if down {
			if !vgSession.Load() {
				vgAltStarted = true
				vgPost(vgEvent{kind: 1})
			}
		} else if vgAltStarted {
			vgAltStarted = false
			vgPost(vgEvent{kind: 2})
		}
		return
	}
	// 点按：独按在松开沿 toggle（按下沿不动，避免与 Alt 组合键抢跑）
	if !down {
		vgPost(vgEvent{kind: 0})
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
	log.Printf("[语音输入法] 已启用：鼠标侧键 %s + 独按 Alt（任意位置听写，结束自动粘贴/复制）",
		map[bool]string{true: "长按", false: "点按"}[vgHoldMode.Load()])
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
	hMouse, _, err := procSetWindowsHookEx.Call(whMouseLL, vgMouseHookCallback(), 0, 0)
	if hMouse == 0 {
		errCh <- err
		return
	}
	hKbd, _, errKbd := procSetWindowsHookEx.Call(whKeyboardLL, vgKbdHookCallback(), 0, 0)
	if hKbd == 0 { // 键盘钩子失败不致命：鼠标侧键（主快捷键）仍可用
		log.Printf("[语音输入法] 键盘钩子安装失败（独按 Alt 副快捷键不可用）: %v", errKbd)
	}
	tid, _, _ := procGetCurrentThreadID.Call()
	ready <- uint32(tid)
	var msg recMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) {
			break
		}
	}
	if hKbd != 0 {
		procUnhookWindowsHookEx.Call(hKbd)
	}
	procUnhookWindowsHookEx.Call(hMouse)
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
