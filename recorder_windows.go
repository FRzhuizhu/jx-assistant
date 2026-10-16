//go:build windows

package main

// 演示记录器的 Windows 实现：纯 syscall 直调 user32 / winmm，不引入 cgo
//（与 tool_computer_windows.go 同一路线）。
//   全局钩子：SetWindowsHookEx WH_MOUSE_LL / WH_KEYBOARD_LL——低级钩子回调
//   运行在系统钩子线程上，回调里只做「组事件 → 无阻塞投递进 channel」，
//   慢活（PNG 编码、写盘、窗口标题轮询）全部丢给 worker goroutine，避免
//   回调超时被系统摘钩。自家 computer_* 注入的事件带 LLMHF_INJECTED 标记，
//   刻意过滤（AI 的点击不该混进用户的演示）。
//   录音：winmm waveIn 缓冲采集 16kHz 单声道 16bit PCM（一次采集两用：
//   全量落 audio.wav + 喂 VAD 停顿分段），无 cgo、无第三方库；没有麦克风时
//   降级为纯键鼠+截图记录。随录随转：VAD 段定稿（3 秒尾静音）经 recorderASR
//   （voiceasr.go → 语音组件本地模型）识别，逐段落 讲解.srt。

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
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
)

// waveIn 缓冲采集（winmm）：录 16kHz/单声道/16bit PCM 喂 VAD 与 audio.wav。
var (
	winmmDLL                  = syscall.NewLazyDLL("winmm.dll")
	procWaveInOpen            = winmmDLL.NewProc("waveInOpen")
	procWaveInStart           = winmmDLL.NewProc("waveInStart")
	procWaveInReset           = winmmDLL.NewProc("waveInReset")
	procWaveInClose           = winmmDLL.NewProc("waveInClose")
	procWaveInPrepareHeader   = winmmDLL.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHeader = winmmDLL.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer       = winmmDLL.NewProc("waveInAddBuffer")
)

const whdrDone = 0x00000001     // WAVEHDR.dwFlags：缓冲已录满归还
const whdrPrepared = 0x00000002 // WAVEHDR.dwFlags：缓冲已 Prepare（回队列必须保留）

type tWAVEFORMATEX struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

type tWAVEHDR struct {
	LpData        uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

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
	audio     *recAudioSession // 录音+VAD+随录随转会话（audioOK=true 时非 nil）

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
			// 低级钩子回调跑在系统钩子线程的原生栈上，且是全系统鼠标输入的
			// 必经之路：panic 冒出去＝整进程无声死亡且不留堆栈（windowsgui
			// 子系统）。就地兜住落日志，兜住后返回 0＝事件继续传链。
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[recorder] 鼠标钩子回调 panic（已兜住）: %v\n%s", r, debug.Stack())
				}
			}()
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
			// 同鼠标钩子：原生栈上的 panic 必须就地兜住（见上）。
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[recorder] 键盘钩子回调 panic（已兜住）: %v\n%s", r, debug.Stack())
				}
			}()
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
// 【临时护栏】钩子回调（recMouseCb/recKbdCb）由本线程消息泵派发、跑在这条
// goroutine 上，与主窗口同理须置于 stackguard_windows.go 的预增长深栈内
// （Go 回调期间栈拷贝缺陷，详见该文件注释）。
func recHookLoop(ready chan<- uint32, errCh chan<- error) {
	withGuardedStack(func() { recHookLoopInner(ready, errCh) })
}

func recHookLoopInner(ready chan<- uint32, errCh chan<- error) {
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

// ---------- 录音（waveIn 缓冲采集，16kHz 单声道 16bit） ----------
//
// 一次采集两用：PCM 全量累积（停录写 audio.wav）＋ 喂 VAD 停顿分段。VAD 分段
// 语义与语音组件的听写线（组件/voice/client.js 的 makeLocalRec）逐常量对齐：
// 350ms 起效 / 3s 尾静音定稿 / 18s 强制切段 / 静音垫 1.5s 上限，能量判据
// rms > max(0.008, 底噪×3.5)、底噪自适应（0.98/0.02，上限 0.02）。定稿段经
// recorderASR（voiceasr.go → 语音组件本地模型）识别成文字，逐段落 讲解.srt
//（时间轴与事件/截图同源：都取 time.Since(r.start)）。

const (
	recFrameBytes   = 3200            // 100ms @ 16kHz/16bit/单声道 —— VAD 帧长=缓冲长
	recAudioBuffers = 6               // 在途缓冲数（≈600ms 队列深度）
	recASRTailWait  = 8 * time.Second // 停录等在途识别的上限（同悬浮窗 TAIL_WAIT_MS）
	recSpeechMinMs  = 350             // 短于此视为噪声毛刺，丢弃
	recFinalPauseMs = 3000            // 尾随静音满 3 秒才定稿（真停顿，非说话时长）
	recSegMaxMs     = 18000           // 连续不停顿超 18 秒强制定稿
	recPadMaxMs     = 1500            // 段内静音垫上限（间隙+尾部，近似停顿中点切分）
)

// recASRJob 一段待识别的定稿音频（cue 时间在入队时就定好——识别只回文本）。
type recASRJob struct {
	wav     []byte
	startMs int
	endMs   int
}

// recAudioSession 一次录制的音频会话：采集循环、VAD 状态、识别队列与字幕段。
// 字幕段（cues）由识别 worker 写、停录收尾读，用 cueMu 串行；VAD 状态与 pcm
// 只归采集循环访问（停录后循环已退出）。
type recAudioSession struct {
	r     *demoRec
	hWave uintptr
	hdrs  []tWAVEHDR
	bufs  [][]byte
	pcm   bytes.Buffer // 全量 PCM（audio.wav 本体）

	loopDone chan struct{}
	stopFlag atomic.Bool
	asrCh    chan recASRJob
	asrDone  chan struct{}
	pending  atomic.Int32
	cueMu    sync.Mutex
	cues     []srtCue

	// VAD 状态（仅采集循环内访问）
	noiseFloor     float64
	sinceFinal     []byte // 定稿段音频（语音帧 + 限量静音垫）
	sinceFinalMs   int    // 段内语音时长
	finalSilenceMs int    // 尾随静音计时
	padMs          int    // 段内已收录的静音垫
	lastSpeech     time.Time
	prevEndMs      int // 上一段 cue 结束（下一段的起点）
}

// recAudioStart 开麦采集：设备打不开（无麦克风/驱动拒绝）返回 false——录音
// 失败不拦记录本身（照旧降级为键鼠+截图）。WAVE_MAPPER（-1）取默认设备，
// CALLBACK_NULL 轮询取缓冲（低频 15ms，省一层事件句柄）。
func recAudioStart(r *demoRec) bool {
	s := &recAudioSession{
		r: r, noiseFloor: 0.004,
		loopDone: make(chan struct{}),
		asrCh:    make(chan recASRJob, 16),
		asrDone:  make(chan struct{}),
	}
	fmtEx := tWAVEFORMATEX{FormatTag: 1, Channels: 1, SamplesPerSec: 16000,
		AvgBytesPerSec: 32000, BlockAlign: 2, BitsPerSample: 16}
	var h uintptr
	if ret, _, _ := procWaveInOpen.Call(uintptr(unsafe.Pointer(&h)), ^uintptr(0),
		uintptr(unsafe.Pointer(&fmtEx)), 0, 0, 0); ret != 0 || h == 0 {
		return false
	}
	s.hWave = h
	s.hdrs = make([]tWAVEHDR, recAudioBuffers)
	s.bufs = make([][]byte, recAudioBuffers)
	sz := unsafe.Sizeof(s.hdrs[0])
	for i := range s.hdrs {
		s.bufs[i] = make([]byte, recFrameBytes)
		s.hdrs[i].LpData = uintptr(unsafe.Pointer(&s.bufs[i][0]))
		s.hdrs[i].BufferLength = recFrameBytes
		if ret, _, _ := procWaveInPrepareHeader.Call(h, uintptr(unsafe.Pointer(&s.hdrs[i])), sz); ret != 0 {
			s.abort()
			return false
		}
	}
	for i := range s.hdrs {
		if ret, _, _ := procWaveInAddBuffer.Call(h, uintptr(unsafe.Pointer(&s.hdrs[i])), sz); ret != 0 {
			s.abort()
			return false
		}
	}
	if ret, _, _ := procWaveInStart.Call(h); ret != 0 {
		s.abort()
		return false
	}
	r.audio = s
	go s.loop()
	go s.asrWorker()
	return true
}

// abort 采集启动失败的回滚：停循环与识别队列（stopFlag/关队列），归还全部
// 在途缓冲后关设备——不留孤儿 goroutine。
func (s *recAudioSession) abort() {
	if s.hWave == 0 {
		return
	}
	s.stopFlag.Store(true)
	procWaveInReset.Call(s.hWave)
	sz := unsafe.Sizeof(s.hdrs[0])
	for i := range s.hdrs {
		procWaveInUnprepareHeader.Call(s.hWave, uintptr(unsafe.Pointer(&s.hdrs[i])), sz)
	}
	procWaveInClose.Call(s.hWave)
	s.hWave = 0
	close(s.asrCh) // 队列里不会有任务（识别尚未排队）：worker 随即退出
	select {
	case <-s.loopDone:
	case <-time.After(2 * time.Second):
	}
	select {
	case <-s.asrDone:
	case <-time.After(2 * time.Second):
	}
}

// loop 采集循环：轮询取回录满的缓冲（100ms 一帧），喂 VAD 后立即回队列；
// stopFlag 置位且 waveInReset 已把余下缓冲标 DONE——全部吃掉后退出。
func (s *recAudioSession) loop() {
	defer close(s.loopDone)
	sz := unsafe.Sizeof(s.hdrs[0])
	for {
		progressed := false
		for i := range s.hdrs {
			if s.hdrs[i].Flags&whdrDone == 0 {
				continue
			}
			s.handleFrame(s.bufs[i][:s.hdrs[i].BytesRecorded], time.Now())
			// 回收只清 DONE 状态、必须保留 WHDR_PREPARED：全清会把 PREPARED
			// 一并抹掉，waveInAddBuffer 立刻拒绝回队列（WAVERR_UNPREPARED=34）
			// ——第一轮在途缓冲（6×100ms=600ms）录完后音频从此静默中断，
			// audio.wav 固定只有 0.6 秒、VAD 无段可定稿、字幕/转写全空。
			// 2026-10-16 本机双盲实测：全清=0.60s 且 6 次全拒；保留=满时长。
			s.hdrs[i].Flags = whdrPrepared
			s.hdrs[i].BytesRecorded = 0
			if !s.stopFlag.Load() {
				if ret, _, _ := procWaveInAddBuffer.Call(s.hWave, uintptr(unsafe.Pointer(&s.hdrs[i])), sz); ret != 0 {
					// 失败的缓冲不会再归还，每缓冲至多记一条，日志量有界
					log.Printf("录音缓冲回队列失败（音频采集将中断）：错误码 %d", ret)
				}
			}
			progressed = true
		}
		if s.stopFlag.Load() && !progressed {
			return
		}
		if !progressed {
			time.Sleep(15 * time.Millisecond)
		}
	}
}

// handleFrame 一个 100ms 缓冲：落全量 PCM + 喂 VAD（语义见节首注释）。
func (s *recAudioSession) handleFrame(data []byte, now time.Time) {
	s.pcm.Write(data)
	rms := recRMS(data)
	const frameMs = 100
	isSpeech := rms > math.Max(0.008, s.noiseFloor*3.5)
	if !isSpeech {
		s.noiseFloor = math.Min(0.02, s.noiseFloor*0.98+rms*0.02)
	}
	if isSpeech {
		s.sinceFinal = append(s.sinceFinal, data...)
		s.sinceFinalMs += frameMs
		s.finalSilenceMs = 0
		s.lastSpeech = now
	} else if s.sinceFinalMs > 0 {
		s.finalSilenceMs += frameMs
		if s.padMs < recPadMaxMs { // 间隙/尾部静音限量入段
			s.sinceFinal = append(s.sinceFinal, data...)
			s.padMs += frameMs
		}
	}
	if s.sinceFinalMs >= recSegMaxMs || (s.sinceFinalMs >= recSpeechMinMs && s.finalSilenceMs >= recFinalPauseMs) {
		s.finalize()
	}
}

// finalize 定稿一段：打包 WAV 交识别队列。cue 终点取本段最后一帧语音+300ms
//（尾静音不计入），起点接上一段结束——与 events/截图同一条时间轴。
func (s *recAudioSession) finalize() {
	frames, spoken := s.sinceFinal, s.sinceFinalMs
	endMs := 300
	if !s.lastSpeech.IsZero() {
		endMs = int(s.lastSpeech.Sub(s.r.start).Milliseconds()) + 300
	}
	startMs := s.prevEndMs
	s.sinceFinal, s.sinceFinalMs, s.finalSilenceMs, s.padMs = nil, 0, 0, 0
	s.prevEndMs = endMs
	if spoken < recSpeechMinMs || len(frames) < recFrameBytes { // 噪声毛刺/音频不足 100ms：丢弃
		return
	}
	if recorderASR == nil {
		return
	}
	s.pending.Add(1)
	select {
	case s.asrCh <- recASRJob{wav: recPcmToWav(frames), startMs: startMs, endMs: endMs}:
	default:
		s.pending.Add(-1) // 队列满（识别严重滞后）：丢段保录制
	}
}

// asrWorker 串行消费识别队列（worker 进程本身也是串行的）：文本到手即追加
// 字幕段并整文件重写 讲解.srt。识别失败/空文本只丢该段，不影响录制。
func (s *recAudioSession) asrWorker() {
	defer close(s.asrDone)
	for job := range s.asrCh {
		text, err := recorderASR(job.wav)
		s.pending.Add(-1)
		if err != nil {
			log.Printf("演示记录语音识别失败（%d-%dms 段丢弃）：%v", job.startMs, job.endMs, err)
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		s.cueMu.Lock()
		s.cues = append(s.cues, srtCue{StartMs: job.startMs, EndMs: job.endMs, Text: strings.TrimSpace(text)})
		cues := append([]srtCue(nil), s.cues...)
		s.cueMu.Unlock()
		if err := writeSRTFile(filepath.Join(s.r.dir, recorderSrtFn), cues); err != nil {
			log.Printf("演示记录字幕落盘失败：%v", err)
		}
	}
}

// recAudioFinish 停录收尾：停采集 → VAD 尾段定稿（不足 3s 静音的余段照转）
// → 等在途识别（≤8s，卡死不拖死停录）→ 写 audio.wav。返回音频是否落盘。
func recAudioFinish(r *demoRec) bool {
	s := r.audio
	if s == nil {
		return false
	}
	s.stopFlag.Store(true)
	procWaveInReset.Call(s.hWave) // 停采并把全部在途缓冲标 DONE（由循环吃掉）
	select {
	case <-s.loopDone:
	case <-time.After(3 * time.Second):
	}
	sz := unsafe.Sizeof(s.hdrs[0])
	for i := range s.hdrs {
		procWaveInUnprepareHeader.Call(s.hWave, uintptr(unsafe.Pointer(&s.hdrs[i])), sz)
	}
	procWaveInClose.Call(s.hWave)
	s.hWave = 0
	s.finalize() // 尾段兜底：余下语音没有 3s 静音也照转
	close(s.asrCh)
	select {
	case <-s.asrDone:
	case <-time.After(recASRTailWait):
	}
	if s.pcm.Len() == 0 {
		return false
	}
	return os.WriteFile(filepath.Join(r.dir, recorderAudioFn), recPcmToWav(s.pcm.Bytes()), 0644) == nil
}

// recRMS 归一化均方根（int16 LE → [0,1)），VAD 能量判据的输入。
func recRMS(b []byte) float64 {
	n := len(b) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(b); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(b[i:])))
		sum += v * v
	}
	return math.Sqrt(sum/float64(n)) / 32768
}

// ---------- 启停 ----------

func recorderStart(dir string) error {
	recMu.Lock()
	defer recMu.Unlock()
	if loadActiveRec() != nil {
		return fmt.Errorf("已有在录的演示记录（%s），先点停止按钮收录后再开新的", loadActiveRec().dir)
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
	// start 先置位再开音频：音频/VAD/cue 时间轴全以它为原点，采集协程与
	// 这里不构成数据竞争。
	r.start = time.Now()
	if recAudioStart(r) {
		r.audioOK = true
	}
	ready := make(chan uint32, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(r.hooksDone)
		recHookLoop(ready, errCh)
	}()
	abortAudio := func() {
		if r.audio != nil {
			r.audio.abort()
			r.audio, r.audioOK = nil, false
		}
	}
	select {
	case r.hookTID = <-ready:
	case err := <-errCh:
		f.Close()
		abortAudio()
		return fmt.Errorf("演示记录器启动失败: %w", err)
	case <-time.After(3 * time.Second):
		f.Close()
		abortAudio()
		// 钩子线程可能迟到就绪：放弃后没人再收 tid，两个低级钩子会挂到进程
		// 退出（全系统输入白白多绕一层）。起个 watcher 消费迟到的 ready 并
		// 投 WM_QUIT，让钩子线程正常拆钩退出；迟到的是安装失败则本就自清。
		go func() {
			select {
			case tid := <-ready:
				if tid != 0 {
					procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
				}
			case <-errCh:
			}
		}()
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
	if r.audio != nil {
		audioOK = recAudioFinish(r) // 停采→尾段定稿→等在途识别→写 audio.wav
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

// recorderActiveInfo 结构化状态（/api/demo-record/status 用）：是否在录 + 材料目录。
func recorderActiveInfo() (bool, string) {
	if r := loadActiveRec(); r != nil {
		return true, filepath.ToSlash(r.dir)
	}
	return false, ""
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
	// 小键盘数字（NumLock 开时的 0x60-0x69；关时发的是导航键，已在 vkName）
	for i := 0; i < 10; i++ {
		vkChar[uint32(0x60+i)] = string(rune('0' + i))
	}
}

// recComboUpper 组合键里的单字母统一大写（记成 Ctrl+S 而非 Ctrl+s，符合
// 快捷键书写习惯；多字符键名如 F5、Tab 不动）。
func recComboUpper(s string) string {
	if len(s) == 1 && s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0] - 32)
	}
	return s
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

	// 修饰键状态：Shift/Ctrl 认主码与左右码；Alt 认 0x12 与左右 0xA4/0xA5（低级
	// 钩子实际报的是左右码，只认 0x12 会把 Alt 组合整个丢掉）；Win 认左右 0x5B/0x5C。
	shift, ctrl, alt, win := false, false, false, false
	var typing strings.Builder
	typingT := 0
	lastKeyAt := time.Time{}
	lastWin := ""
	// key 事件的自动重复合并：按住不放会每 ~30ms 重发一次 keydown，同名组合
	// 400ms 内只记一条（持续按住则窗口随最后一条滚动，不会中途漏记松开前的
	// 长按；真正的连续两次快捷键间隔通常大于此窗）。
	lastKeyName, lastKeyT := "", -1<<30
	appendKey := func(ev recEvent) {
		if ev.Key == lastKeyName && ev.T-lastKeyT < 400 {
			lastKeyT = ev.T
			return
		}
		lastKeyName, lastKeyT = ev.Key, ev.T
		r.append(ev)
	}

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
				case 0x12, 0xA4, 0xA5: // Alt（低级钩子报左右码 0xA4/0xA5）
					alt = !ev.Up
				case 0x5B, 0x5C: // Win（左/右）
					win = !ev.Up
				default:
					if ev.Up {
						continue
					}
					lastKeyAt = time.Now()
					if ctrl || alt || win {
						// 组合键：具名整键记录（如 Ctrl+S、Alt+F4、Win+E）
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
							if win {
								m += "Win+"
							}
							appendKey(recEvent{T: ev.T, Type: "key", Key: recComboUpper(m + name)})
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
						// Shift+非打印键是常用操作（Shift+Tab 换栏、Shift+Enter 换行）：
						// 带 Shift 前缀记；Shift+可打印键＝大写字符，走打字聚合不算组合。
						k := name
						if shift {
							k = "Shift+" + name
						}
						appendKey(recEvent{T: ev.T, Type: "key", Key: k})
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
