//go:build windows

package main

// 全局语音输入法（胶囊浮窗听写）：把语音组件升级成类输入法体验——
// 在「软件对话框之外」的任意位置（本软件 git 面板/设置页等输入框、以及全电脑
// 任何程序的光标处）按全局快捷键（默认鼠标侧键，点按切换）→ 屏幕底部弹出
// 胶囊浮窗（voicefloat_windows.go + web/voice-float.html）听写，再按一次结束，
// 文字粘贴进触发时的输入位置；那里不可输入则留在剪贴板（浮窗提示「已复制」）。
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

const wmXButtonUp = 0x020C

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
	log.Printf("[语音输入法] 已启用：鼠标侧键 %s（任意位置听写，结束自动粘贴/复制）",
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

// vgHookLoop 专用线程安装低级鼠标钩子并跑消息循环（recorder_windows.go 同款；
// 低级钩子要求安装线程有消息循环且保持存活），WM_QUIT 退出并卸钩。
func vgHookLoop(ready chan<- uint32, errCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hMouse, _, err := procSetWindowsHookEx.Call(whMouseLL, vgMouseHookCallback(), 0, 0)
	if hMouse == 0 {
		errCh <- err
		return
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
