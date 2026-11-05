//go:build windows

package main

// 可编辑判定（vtEditabilityGate）的回归测试。
//
//   go test -run TestGatePure -v                       纯逻辑各层（不需要真窗口）
//   go test -run TestCaptureForeground -v              打印当前前台的判定现场
//   $env:VT_PROBE_HWND="<hwnd>"; go test -run TestCaptureForeground -v
//                                                      先把指定窗口切前台再判定
//   go test -run "TestNotepad|TestDesktop" -v          真窗口验收（记事本=可输入、桌面=只复制）
//
// 为什么这些用例要存在：四层判据任何一层都会因为第三方应用换 UI 框架而失效，
// 而失效的表现完全一样——"该复制却往空处粘"或"该粘却只进剪贴板"，不报错。
// 2026-11 的 bug 就是"悬浮窗类名 webview 命中乐观放行"，靠日志里那句
// "前台类=webview editable=true" 才定位到。

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// ---------- 纯逻辑：各层判据各自生效、且顺序对 ----------

func TestGatePure(t *testing.T) {
	cases := []struct {
		name string
		tgt  vtTarget
		want vtGate
	}{
		{
			name: "没有前台窗口",
			tgt:  vtTarget{},
			want: vtGateNoForeground,
		},
		{
			name: "自己进程的窗口（悬浮窗 webview 类名）——绝不注入",
			tgt:  vtTarget{Hwnd: 1234, Pid: uint32(os.Getpid()), WindowClass: "webview", ProcName: "voicetype_go.exe"},
			want: vtGateOwnWindow,
		},
		{
			name: "explorer 桌面 Progman——有焦点但不是输入目标",
			tgt:  vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Progman", ProcName: "explorer.exe", HasCaret: true},
			want: vtGateExplorerDesktop,
		},
		{
			name: "explorer 文件夹窗口地址栏（有 caret）",
			tgt:  vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "CabinetWClass", ProcName: "explorer.exe", HasCaret: true},
			want: vtGateCaret,
		},
		{
			name: "native Edit 控件",
			tgt:  vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Notepad", ProcName: "notepad.exe", FocusClass: "Edit"},
			want: vtGateNativeClass,
		},
		{
			name: "UIA 报 Edit 且可写 → 注入（WebView2 输入框就是这种）",
			tgt: vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "webview", ProcName: "msedgewebview2.exe",
				Uia: vtUiaInfo{OK: true, ControlType: uiaCtEdit, Enabled: true, KeyboardFocusable: true,
					HasValuePattern: true, ValueReadOnly: false}},
			want: vtGateUiaEditable,
		},
		{
			name: "UIA 报 Edit 但只读 → 只复制",
			tgt: vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "webview", ProcName: "app.exe",
				Uia: vtUiaInfo{OK: true, ControlType: uiaCtEdit, Enabled: true,
					HasValuePattern: true, ValueReadOnly: true}},
			want: vtGateUiaReadOnly,
		},
		{
			name: "网页正文（Document + 只读 ValuePattern）→ 只复制",
			tgt: vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe",
				Uia: vtUiaInfo{OK: true, ControlType: uiaCtDocument, Enabled: true, KeyboardFocusable: true,
					HasValuePattern: true, ValueReadOnly: true, HasTextPattern: true}},
			want: vtGateUiaReadOnly,
		},
		{
			name: "contenteditable（Document + 可写 ValuePattern）→ 注入",
			tgt: vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe",
				Uia: vtUiaInfo{OK: true, ControlType: uiaCtDocument, Enabled: true, KeyboardFocusable: true,
					HasValuePattern: true, ValueReadOnly: false}},
			want: vtGateUiaRichEditor,
		},
		{
			name: "Chromium 里键盘可聚焦的分组（富文本编辑器）→ 乐观放行",
			tgt: vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "feishu.exe",
				Uia: vtUiaInfo{OK: true, ControlType: uiaCtGroup, Enabled: true, KeyboardFocusable: true}},
			want: vtGateChromiumOptimistic,
		},
		{
			name: "Chromium 里聚焦的是按钮 → 不放行",
			tgt: vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe",
				Uia: vtUiaInfo{OK: true, ControlType: uiaCtButton, Enabled: true, KeyboardFocusable: true}},
			want: vtGateNoSignal,
		},
		{
			name: "UIA 探测成功但只有 Pane 且不可聚焦（网页容器）→ 浏览器不做进程兜底，只复制",
			tgt: vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe",
				Uia: vtUiaInfo{OK: true, ControlType: uiaCtPane, Enabled: true}},
			want: vtGateNoSignal,
		},
		{
			name: "微信 4.x（Qt 自绘、UIA 无信号）→ 进程名兜底注入",
			tgt:  vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "Qt51514QWindowIcon", ProcName: "Weixin.exe"},
			want: vtGateProcessAllowlist,
		},
		{
			name: "未知应用、无任何信号 → 只复制",
			tgt:  vtTarget{Hwnd: 1, Pid: 999999, WindowClass: "SomeUnknownClass", ProcName: "mystery.exe"},
			want: vtGateNoSignal,
		},
	}
	for _, c := range cases {
		got := vtEditabilityGate(c.tgt)
		if got != c.want {
			t.Errorf("%s：判定=%s 期望=%s", c.name, got, c.want)
		}
	}
}

// TestGateEditableMatrix 钉住"哪些结论算可输入"（改 editable() 时别悄悄放宽）。
func TestGateEditableMatrix(t *testing.T) {
	want := map[vtGate]bool{
		vtGateNoForeground: false, vtGateOwnWindow: false, vtGateExplorerDesktop: false,
		vtGateCaret: true, vtGateNativeClass: true, vtGateUiaEditable: true,
		vtGateUiaRichEditor: true, vtGateUiaReadOnly: false, vtGateMenuActive: false,
		vtGateChromiumOptimistic: true, vtGateProcessAllowlist: true, vtGateNoSignal: false,
	}
	for g, w := range want {
		if g.editable() != w {
			t.Errorf("%s：editable=%v 期望=%v", g, g.editable(), w)
		}
	}
}

// ---------- 纯逻辑：注入前现场合成（vtFinishTarget） ----------

// TestFinishTargetPure 钉住判定时机与回退规则。2026-11-05 的 bug 就出在这里：
// 同一窗口"沿用起始结论"，录音开始时在输入框、结束时焦点移到网页正文，
// 仍判可输入注入，Ctrl+V 落空还显示"已输入"。
func TestFinishTargetPure(t *testing.T) {
	const wA, wB = uintptr(0x1111), uintptr(0x2222)
	self := uint32(os.Getpid())
	// start：窗口 A 的输入框（录音开始采集，带 UIA）
	start := vtTarget{Hwnd: wA, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe",
		Uia: vtUiaInfo{OK: true, ControlType: uiaCtEdit, Enabled: true, KeyboardFocusable: true,
			HasValuePattern: true, ValueReadOnly: false}}
	// 同一窗口、焦点移到网页正文（Document 只读）——注入前一刻的真实现场
	nowBody := vtTarget{Hwnd: wA, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe",
		Uia: vtUiaInfo{OK: true, ControlType: uiaCtDocument, Enabled: true, KeyboardFocusable: true,
			HasValuePattern: true, ValueReadOnly: true}}
	cases := []struct {
		name      string
		start     vtTarget
		end       vtTarget
		now       vtTarget
		wantGate  vtGate
		wantMenuR bool
	}{
		{
			name:     "开始输入框、注入前焦点已移到正文 → 只复制（本次 bug）",
			start:    start,
			end:      vtTarget{Hwnd: wA, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe"},
			now:      nowBody,
			wantGate: vtGateUiaReadOnly,
		},
		{
			name:     "录音开始不在输入框、注入前点进了输入框 → 注入（反向变化）",
			start:    nowBody,
			end:      vtTarget{Hwnd: wA, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe"},
			now:      start,
			wantGate: vtGateUiaEditable,
		},
		{
			name:      "同一窗口、注入前焦点被菜单抢走 → 退回录音开始现场 + Esc",
			start:     start,
			end:       vtTarget{Hwnd: wA, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe"},
			now:       vtTarget{Hwnd: wA, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe", Uia: vtUiaInfo{OK: true, ControlType: uiaCtMenu, Enabled: true}},
			wantGate:  vtGateUiaEditable,
			wantMenuR: true,
		},
		{
			name:     "识别期间切到别的窗口输入框 → 以注入前窗口为准",
			start:    start,
			end:      vtTarget{Hwnd: wB, Pid: 999998, WindowClass: "Notepad", ProcName: "notepad.exe", FocusClass: "Edit"},
			now:      vtTarget{Hwnd: wB, Pid: 999998, WindowClass: "Notepad", ProcName: "notepad.exe", FocusClass: "Edit"},
			wantGate: vtGateNativeClass,
		},
		{
			name:     "注入前悬浮窗抢到前台 → 用录音结束时的外部现场",
			start:    start,
			end:      vtTarget{Hwnd: wA, Pid: 999999, WindowClass: "Chrome_WidgetWin_1", ProcName: "msedge.exe", HasCaret: true},
			now:      vtTarget{Hwnd: 0x3333, Pid: self, WindowClass: "webview", ProcName: "voicetype_go.exe"},
			wantGate: vtGateCaret,
		},
		{
			name:     "注入前与结束时都拿不到外部现场 → 退回录音开始现场",
			start:    start,
			end:      vtTarget{Hwnd: 0x3333, Pid: self, WindowClass: "webview", ProcName: "voicetype_go.exe"},
			now:      vtTarget{},
			wantGate: vtGateUiaEditable,
		},
	}
	for _, c := range cases {
		tgt := vtFinishTarget(c.start, c.end, c.now)
		g := vtEditabilityGate(tgt)
		if g != c.wantGate {
			t.Errorf("%s：判定=%s 期望=%s", c.name, g, c.wantGate)
		}
		if tgt.MenuRecovered != c.wantMenuR {
			t.Errorf("%s：MenuRecovered=%v 期望=%v", c.name, tgt.MenuRecovered, c.wantMenuR)
		}
	}
}

// ---------- 真窗口：现场采集 + 判定 ----------

func TestCaptureForeground(t *testing.T) {
	if h := os.Getenv("VT_PROBE_HWND"); h != "" {
		var hwnd uintptr
		for i := 0; i < len(h); i++ {
			hwnd = hwnd*10 + uintptr(h[i]-'0')
		}
		vtFocusWindow(hwnd)
		if ms := os.Getenv("VT_PROBE_DELAY_MS"); ms != "" {
			var d int
			for i := 0; i < len(ms); i++ {
				d = d*10 + int(ms[i]-'0')
			}
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
	}
	tgt := vtCaptureTarget(true, "测试")
	g := vtEditabilityGate(tgt)
	t.Logf("判定=%s 可输入=%v | %s", g, g.editable(), tgt.describe())
}

// TestNotepadIsEditable 记事本 = 原生 Edit，必须判成可输入（否则语音输入法就废了）。
func TestNotepadIsEditable(t *testing.T) {
	cmd := exec.Command("notepad.exe")
	if err := cmd.Start(); err != nil {
		t.Skipf("拉起记事本失败（环境不允许）：%v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	var hwnd uintptr
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		hwnd = vtFindWindowByProcAndClass("notepad", "Notepad")
		if hwnd != 0 {
			break
		}
	}
	if hwnd == 0 {
		t.Skip("没等到记事本窗口（可能被系统重定向到商店应用）")
	}
	vtFocusWindow(hwnd)
	time.Sleep(400 * time.Millisecond)
	tgt := vtCaptureTarget(true, "记事本")
	g := vtEditabilityGate(tgt)
	t.Logf("记事本判定=%s | %s", g, tgt.describe())
	if !g.editable() {
		t.Errorf("记事本应判成可输入，实际=%s", g)
	}
}

// TestDesktopIsNotEditable 桌面（Progman）必须判成只复制——历史 bug 的重灾区。
func TestDesktopIsNotEditable(t *testing.T) {
	hwnd := vtFindWindowByProcAndClass("explorer", "Progman")
	if hwnd == 0 {
		t.Skip("没找到桌面窗口 Progman")
	}
	vtFocusWindow(hwnd)
	time.Sleep(400 * time.Millisecond)
	tgt := vtCaptureTarget(true, "桌面")
	g := vtEditabilityGate(tgt)
	t.Logf("桌面判定=%s | %s", g, tgt.describe())
	if g.editable() {
		t.Errorf("桌面应判成只复制，实际=%s", g)
	}
}

// ---------- 测试用 Win32 小工具 ----------

var (
	tu32 = syscall.NewLazyDLL("user32.dll")

	tProcEnumWindows           = tu32.NewProc("EnumWindows")
	tProcGetWindowThreadProcId = tu32.NewProc("GetWindowThreadProcessId")
	tProcGetClassNameW         = tu32.NewProc("GetClassNameW")
	tProcIsWindowVisible       = tu32.NewProc("IsWindowVisible")
	tProcSetForegroundWindow   = tu32.NewProc("SetForegroundWindow")
	tProcBringWindowToTop      = tu32.NewProc("BringWindowToTop")
	tProcShowWindow            = tu32.NewProc("ShowWindow")
	tProcAttachThreadInput     = tu32.NewProc("AttachThreadInput")
	tProcGetForegroundWindow   = tu32.NewProc("GetForegroundWindow")
	tProcGetWindowTextW        = tu32.NewProc("GetWindowTextW")
	tk32                       = syscall.NewLazyDLL("kernel32.dll")
	tProcGetCurrentThreadId    = tk32.NewProc("GetCurrentThreadId")
)

// vtFocusWindow 把窗口切到前台（AttachThreadInput 绕过前台锁定）。
func vtFocusWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	tProcShowWindow.Call(hwnd, 9 /*SW_RESTORE*/)
	fg, _, _ := tProcGetForegroundWindow.Call()
	var pid uint32
	ftid, _, _ := tProcGetWindowThreadProcId.Call(fg, uintptr(unsafe.Pointer(&pid)))
	ttid, _, _ := tProcGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	myTid, _, _ := tProcGetCurrentThreadId.Call()
	if ftid != 0 {
		tProcAttachThreadInput.Call(myTid, ftid, 1)
	}
	tProcAttachThreadInput.Call(myTid, ttid, 1)
	tProcBringWindowToTop.Call(hwnd)
	tProcSetForegroundWindow.Call(hwnd)
	if ftid != 0 {
		tProcAttachThreadInput.Call(myTid, ftid, 0)
	}
	tProcAttachThreadInput.Call(myTid, ttid, 0)
}

// vtFindWindowByProcAndClass 按「进程名（不含 .exe，子串）+ 窗口类名（子串）」找可见顶层窗口。
func vtFindWindowByProcAndClass(procLike, classLike string) uintptr {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if v, _, _ := tProcIsWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		var pid uint32
		tProcGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		name := vtProcessName(pid)
		buf := make([]uint16, 256)
		n, _, _ := tProcGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 256)
		cls := syscall.UTF16ToString(buf[:n])
		if containsFold(name, procLike) && containsFold(cls, classLike) {
			found = hwnd
			return 0 // 停
		}
		return 1
	})
	tProcEnumWindows.Call(cb, 0)
	return found
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
