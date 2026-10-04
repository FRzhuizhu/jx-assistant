//go:build windows

package main

// 演示记录器的 Windows 实现：纯 syscall 直调 user32 / winmm，不引入 cgo
//（与 tool_computer_windows.go 同一路线）。
//   全局钩子：SetWindowsHookEx WH_MOUSE_LL / WH_KEYBOARD_LL——低级钩子回调
//   运行在系统钩子线程上，回调里只做「组事件 → 无阻塞投递进 channel」，
//   慢活（PNG 编码、写盘、窗口标题轮询）全部丢给 worker goroutine，避免
//   回调超时被系统摘钩。自家 computer_* 注入的事件带 LLMHF_INJECTED 标记，
//   刻意过滤（AI 的点击不该混进用户的演示）。
//   录音：winmm MCI waveaudio（open new type waveaudio）采 16kHz 单声道
//   16bit WAV，无 cgo、无第三方库；没有麦克风时降级为纯键鼠+截图记录。

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	procSetWindowsHookEx    = compUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = compUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = compUser32.NewProc("CallNextHookEx")
	procGetMessageW         = compUser32.NewProc("GetMessageW")
	procPostThreadMessageW  = compUser32.NewProc("PostThreadMessageW")
	procGetForegroundWindow = compUser32.NewProc("GetForegroundWindow")
	procGetCurrentThreadID  = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")
	procMciSendStringW      = syscall.NewLazyDLL("winmm.dll").NewProc("mciSendStringW")
)

const (
	whMouseLL     = 14
	whKeyboardLL  = 13
	wmQuit        = 0x0012
	wmKeydown     = 0x0100
	wmKeyup       = 0x0101
	wmSysKeydown  = 0x0104
	wmSysKeyup    = 0x0105
	llkhfUp       = 0x0080 // KBDLLHOOKSTRUCT：按键抬起
	llkhfInjected = 0x0010 // 注入事件（SendInput 产生）
	llmhfInjected = 0x0001 // MSLLHOOKSTRUCT：注入事件
	wmLButtonDown = 0x0201
	wmRButtonDown = 0x0204
	wmMButtonDown = 0x0207
	wmXButtonDown = 0x020B
	wmMouseWheel  = 0x020A
	wmMouseHWheel = 0x020E
)

type recPoint struct{ X, Y int32 }

// msllHookStruct / kbdllHookStruct 对应 Win32 低级钩子参数结构。
type msllHookStruct struct {
	Pt        recPoint
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type kbdllHookStruct struct {
	VkCode    uint32
	ScanCode  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type recMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      recPoint
}

// ---------- 全局唯一记录会话 ----------

// demoRec 是一次演示记录的全部运行态。钩子回调只碰 evCh（无阻塞投递）；
// 其余字段要么创建后只读（dir/start），要么只归 worker 或 stop 串行访问。
type demoRec struct {
	dir       string
	start     time.Time
	events    *os.File
	evCh      chan recEvent
	doneCh    chan struct{} // worker 退出
	hooksDone chan struct{} // 钩子线程退出
	hookTID   uint32
	audioOK   bool

	eventsPath string
	evCount    atomic.Int64
	shots      atomic.Int64
	lastMS     atomic.Int64

	lastHash []byte // 上一张截图内容哈希（worker 专属）
}

var (
	recMu      sync.Mutex // 串行化 start/stop
	activeRecP atomic.Pointer[demoRec]
)

func recorderSupported() bool { return true }

func loadActiveRec() *demoRec { return activeRecP.Load() }

// pushEvent 钩子回调投递事件：channel 满时直接丢弃（低级钩子回调绝不能阻塞）。
func pushEvent(ev recEvent) {
	if r := loadActiveRec(); r != nil {
		select {
		case r.evCh <- ev:
		default:
		}
	}
}

// ---------- 钩子回调（进程生命周期内只创建一次） ----------

var (
	recMouseCbOnce sync.Once
	recMouseCb     uintptr
	recKbdCbOnce   sync.Once
	recKbdCb       uintptr
)

func mouseHookCallback() uintptr {
	recMouseCbOnce.Do(func() {
		// lParam 声明为 unsafe.Pointer（指针大小、ABI 兼容；NewCallback 支持）：
		// 回调参数直接以指针类型接收，免去 uintptr→unsafe.Pointer 的可疑转换
		recMouseCb = syscall.NewCallback(func(nCode int32, wParam uintptr, lParam unsafe.Pointer) uintptr {
			if nCode >= 0 {
				info := (*msllHookStruct)(lParam)
				if info.Flags&llmhfInjected == 0 {
					r := loadActiveRec()
					if r != nil {
						t := int(time.Since(r.start).Milliseconds())
						switch wParam {
						case wmLButtonDown, wmRButtonDown, wmMButtonDown, wmXButtonDown:
							btn := map[uintptr]string{
								wmLButtonDown: "左", wmRButtonDown: "右", wmMButtonDown: "中", wmXButtonDown: "侧",
							}[wParam]
							pushEvent(recEvent{T: t, Type: "click", Button: btn,
								X: int(info.Pt.X), Y: int(info.Pt.Y)})
						case wmMouseWheel, wmMouseHWheel:
							delta := int(int16(info.MouseData >> 16))
							if delta != 0 {
								pushEvent(recEvent{T: t, Type: "wheel", Delta: delta / 120})
							}
						}
					}
				}
			}
			r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, uintptr(lParam))
			return r
		})
	})
	return recMouseCb
}

func kbdHookCallback() uintptr {
	recKbdCbOnce.Do(func() {
		recKbdCb = syscall.NewCallback(func(nCode int32, wParam uintptr, lParam unsafe.Pointer) uintptr {
			if nCode >= 0 && (wParam == wmKeydown || wParam == wmKeyup || wParam == wmSysKeydown || wParam == wmSysKeyup) {
				k := (*kbdllHookStruct)(lParam)
				if k.Flags&llkhfInjected == 0 {
					if r := loadActiveRec(); r != nil {
						pushEvent(recEvent{T: int(time.Since(r.start).Milliseconds()),
							VK: k.VkCode, Up: k.Flags&llkhfUp != 0})
					}
				}
			}
			r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, uintptr(lParam))
			return r
		})
	})
	return recKbdCb
}

// recHookLoop 在专用线程上安装两个低级钩子并跑消息循环（低级钩子要求
// 安装线程有消息循环且保持存活）；WM_QUIT 退出并卸钩。ready 回传线程 id
// （stop 用它投递 WM_QUIT），errCh 回传安装失败。
func recHookLoop(ready chan<- uint32, errCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hMouse, _, err := procSetWindowsHookEx.Call(whMouseLL, mouseHookCallback(), 0, 0)
	if hMouse == 0 {
		errCh <- fmt.Errorf("安装鼠标钩子失败: %v", err)
		return
	}
	hKbd, _, err := procSetWindowsHookEx.Call(whKeyboardLL, kbdHookCallback(), 0, 0)
	if hKbd == 0 {
		procUnhookWindowsHookEx.Call(hMouse)
		errCh <- fmt.Errorf("安装键盘钩子失败: %v", err)
		return
	}
	tid, _, _ := procGetCurrentThreadID.Call()
	ready <- uint32(tid)
	var msg recMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) { // WM_QUIT 或 GetMessage 出错
			break
		}
	}
	procUnhookWindowsHookEx.Call(hKbd)
	procUnhookWindowsHookEx.Call(hMouse)
}

// ---------- 录音（MCI waveaudio，16kHz 单声道） ----------

const recAudioAlias = "jxrec"

func mciSend(cmd string) error {
	p, err := syscall.UTF16PtrFromString(cmd)
	if err != nil {
		return err
	}
	r, _, _ := procMciSendStringW.Call(uintptr(unsafe.Pointer(p)), 0, 0, 0)
	if r != 0 {
		return fmt.Errorf("MCI 命令失败(%d): %s", r, cmd)
	}
	return nil
}

// recAudioStart 开麦录音：格式设置尽力而为（个别驱动只认默认格式），
// record 起不来才判失败（无麦克风/设备被占）——录音失败不拦记录本身。
func recAudioStart() bool {
	_ = mciSend("close " + recAudioAlias) // 上一次异常残留的别名先关掉
	if mciSend("open new type waveaudio alias "+recAudioAlias) != nil {
		return false
	}
	_ = mciSend("set " + recAudioAlias + " time format ms bitspersample 16 channels 1 samplespersec 16000 bytespersec 32000 alignment 2")
	return mciSend("record "+recAudioAlias) == nil
}

// recAudioStop 停录并落盘 audio.wav（在钩子停掉之后调用，尾部与事件轴对齐）。
func recAudioStop(dir string) bool {
	defer func() { _ = mciSend("close " + recAudioAlias) }()
	if mciSend("stop "+recAudioAlias) != nil {
		return false
	}
	return mciSend(`save `+recAudioAlias+` "`+filepath.Join(dir, recorderAudioFn)+`"`) == nil
}

// ---------- 启停 ----------

func recorderStart(dir string) error {
	recMu.Lock()
	defer recMu.Unlock()
	if loadActiveRec() != nil {
		return fmt.Errorf("已有在录的演示记录（%s），先 demo_record_stop 收录后再开新的", loadActiveRec().dir)
	}
	f, err := os.Create(filepath.Join(dir, recorderEventsFn))
	if err != nil {
		return fmt.Errorf("创建事件日志失败: %w", err)
	}
	r := &demoRec{
		dir: dir, events: f,
		evCh:      make(chan recEvent, 512),
		doneCh:    make(chan struct{}),
		hooksDone: make(chan struct{}),
	}
	if recAudioStart() {
		r.audioOK = true
	}
	r.start = time.Now()
	ready := make(chan uint32, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(r.hooksDone)
		recHookLoop(ready, errCh)
	}()
	select {
	case r.hookTID = <-ready:
	case err := <-errCh:
		f.Close()
		if r.audioOK {
			_ = mciSend("close " + recAudioAlias)
		}
		return fmt.Errorf("演示记录器启动失败: %w", err)
	case <-time.After(3 * time.Second):
		f.Close()
		return fmt.Errorf("演示记录器启动超时（钩子线程未就绪）")
	}
	activeRecP.Store(r)
	go r.work()
	return nil
}

func recorderStop() (*recorderStats, error) {
	recMu.Lock()
	defer recMu.Unlock()
	r := loadActiveRec()
	if r == nil {
		return nil, fmt.Errorf("当前没有在录的演示记录")
	}
	activeRecP.Store(nil) // 先摘牌：钩子回调立即停止投递新事件
	procPostThreadMessageW.Call(uintptr(r.hookTID), wmQuit, 0, 0)
	select {
	case <-r.hooksDone:
	case <-time.After(3 * time.Second):
	}
	audioOK := false
	if r.audioOK {
		audioOK = recAudioStop(r.dir)
	}
	close(r.evCh)
	select {
	case <-r.doneCh:
	case <-time.After(5 * time.Second):
	}
	return &recorderStats{
		Dir: r.dir, MS: int(r.lastMS.Load()), Events: int(r.evCount.Load()),
		Shots: int(r.shots.Load()), AudioOK: audioOK,
	}, nil
}

func recorderStatusText() string {
	r := loadActiveRec()
	if r == nil {
		return "当前没有在录的演示记录。"
	}
	return fmt.Sprintf("记录中：%s（已 %s；事件 %d 条，截图 %d 张，音频%s）",
		filepath.ToSlash(r.dir), time.Since(r.start).Round(time.Second),
		r.evCount.Load(), r.shots.Load(), map[bool]string{true: "在录", false: "未开启"}[r.audioOK])
}

// ---------- worker：慢活全在这（写盘 / 截图 / 打字聚合 / 窗口轮询） ----------

// vkName 特殊键的可读名（打印字符不在此表，走打字聚合）。
var vkName = map[uint32]string{
	0x08: "Backspace", 0x09: "Tab", 0x0D: "Enter", 0x13: "Pause", 0x1B: "Esc",
	0x21: "PageUp", 0x22: "PageDown", 0x23: "End", 0x24: "Home",
	0x25: "Left", 0x26: "Up", 0x27: "Right", 0x28: "Down",
	0x2C: "PrintScreen", 0x2D: "Insert", 0x2E: "Delete", 0x5D: "Menu",
}

// vkChar 不按 Shift 时的字符表（OEM 标点 + 数字 + 空格；字母单算）。
var vkChar = map[uint32]string{
	0x20: " ", 0x30: "0", 0x31: "1", 0x32: "2", 0x33: "3", 0x34: "4",
	0x35: "5", 0x36: "6", 0x37: "7", 0x38: "8", 0x39: "9",
	0xBA: ";", 0xBB: "=", 0xBC: ",", 0xBD: "-", 0xBE: ".", 0xBF: "/", 0xC0: "`",
	0xDB: "[", 0xDC: "\\", 0xDD: "]", 0xDE: "'",
}

// vkShiftChar 按 Shift 的字符表（数字上档符号 + OEM 标点上档）。
var vkShiftChar = map[uint32]string{
	0x30: ")", 0x31: "!", 0x32: "@", 0x33: "#", 0x34: "$", 0x35: "%",
	0x36: "^", 0x37: "&", 0x38: "*", 0x39: "(",
	0xBA: ":", 0xBB: "+", 0xBC: "<", 0xBD: "_", 0xBE: ">", 0xBF: "?", 0xC0: "~",
	0xDB: "{", 0xDC: "|", 0xDD: "}", 0xDE: "\"",
}

func init() {
	for i := 0; i < 12; i++ {
		vkName[uint32(0x70+i)] = fmt.Sprintf("F%d", i+1)
	}
}

func (r *demoRec) append(ev recEvent) {
	if ev.T > int(r.lastMS.Load()) {
		r.lastMS.Store(int64(ev.T))
	}
	if ev.Shot == "" {
		ev.VK, ev.Up = 0, false // 内部字段不落盘
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if _, err := r.events.Write(append(line, '\n')); err == nil {
		r.evCount.Add(1)
	}
}

// shoot 截屏存盘：与上一张逐字节相同则跳过（画面没变），到上限停截不拦事件。
func (r *demoRec) shoot(t int) {
	if int(r.shots.Load()) >= recorderMaxShots {
		return
	}
	raw, _, _, err := computerCaptureScreen()
	if err != nil {
		return
	}
	sum := sha256.Sum256(raw)
	if r.lastHash != nil && bytes.Equal(sum[:], r.lastHash) {
		return
	}
	r.lastHash = sum[:]
	name := fmt.Sprintf("%03d.png", r.shots.Add(1))
	if err := os.WriteFile(filepath.Join(r.dir, name), raw, 0644); err != nil {
		return
	}
	r.append(recEvent{T: t, Type: "shot", Shot: name})
}

func (r *demoRec) work() {
	defer func() {
		_ = r.events.Close()
		close(r.doneCh)
	}()

	shift, ctrl, alt := false, false, false
	var typing strings.Builder
	typingT := 0
	lastKeyAt := time.Time{}
	lastWin := ""

	flushTyping := func() {
		if typing.Len() > 0 {
			r.append(recEvent{T: typingT, Type: "text", Text: typing.String()})
			typing.Reset()
		}
	}

	// 起始窗口 + 首帧截图：给「演示前长什么样」留个底
	if w := activeWindowTitle(); w != "" {
		lastWin = w
		r.append(recEvent{T: 0, Type: "window", Title: w})
	}
	r.shoot(0)

	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ev, ok := <-r.evCh:
			if !ok {
				flushTyping()
				return
			}
			switch {
			case ev.Type == "click" || ev.Type == "wheel":
				flushTyping()
				r.append(ev)
				if ev.Type == "click" {
					r.shoot(ev.T)
				}
			case ev.VK != 0: // 键盘原始事件 → 修饰键状态 / 打字聚合 / 具名按键
				vk := ev.VK
				switch vk {
				case 0x10, 0xA0, 0xA1: // Shift
					shift = !ev.Up
				case 0x11, 0xA2, 0xA3: // Ctrl
					ctrl = !ev.Up
				case 0x12: // Alt
					alt = !ev.Up
				default:
					if ev.Up {
						continue
					}
					lastKeyAt = time.Now()
					if ctrl || alt {
						// 组合键：具名整键记录（如 Ctrl+S）
						if name := keyDisplayName(vk, shift); name != "" {
							flushTyping()
							m := ""
							if ctrl {
								m += "Ctrl+"
							}
							if alt {
								m += "Alt+"
							}
							if shift {
								m += "Shift+"
							}
							r.append(recEvent{T: ev.T, Type: "key", Key: m + name})
						}
						continue
					}
					if c, printable := keyChar(vk, shift); printable {
						if typing.Len() == 0 {
							typingT = ev.T
						}
						typing.WriteString(c)
						continue
					}
					if name := keyDisplayName(vk, shift); name != "" {
						flushTyping()
						r.append(recEvent{T: ev.T, Type: "key", Key: name})
						if vk == 0x0D || vk == 0x09 { // Enter / Tab 常意味着提交或换栏：补一张截图
							r.shoot(ev.T)
						}
					}
				}
			default:
				r.append(ev)
			}
		case <-ticker.C:
			// 打字停顿 >1.2s：聚合落盘（不打断长输入的中途状态）
			if typing.Len() > 0 && time.Since(lastKeyAt) > 1200*time.Millisecond {
				flushTyping()
			}
			// 前台窗口轮询：切换窗口本身就是关键事件
			if w := activeWindowTitle(); w != "" && w != lastWin {
				lastWin = w
				t := int(time.Since(r.start).Milliseconds())
				flushTyping()
				r.append(recEvent{T: t, Type: "window", Title: w})
				r.shoot(t)
			}
		}
	}
}

// keyChar 打印键 → 字符（含 Shift 上档）；字母按 Shift 决定大小写。
func keyChar(vk uint32, shift bool) (string, bool) {
	if c, ok := vkChar[vk]; ok {
		if shift {
			if s, ok2 := vkShiftChar[vk]; ok2 {
				return s, true
			}
		}
		return c, true
	}
	if vk >= 0x41 && vk <= 0x5A { // A-Z
		if shift {
			return string(rune('A' + vk - 0x41)), true
		}
		return string(rune('a' + vk - 0x41)), true
	}
	return "", false
}

// keyDisplayName 具名按键的展示名（打印字符不在此列）。
func keyDisplayName(vk uint32, shift bool) string {
	if name, ok := vkName[vk]; ok {
		return name
	}
	if c, ok := keyChar(vk, shift); ok {
		return c
	}
	return fmt.Sprintf("VK(0x%02X)", vk)
}

func activeWindowTitle() string {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ""
	}
	n, _, _ := procGetWindowTextW.Call(hwnd, 0, 0)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return strings.TrimSpace(syscall.UTF16ToString(buf))
}
