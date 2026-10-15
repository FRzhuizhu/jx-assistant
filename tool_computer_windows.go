//go:build windows

package main

// 电脑操作组件的 Windows 实现：纯 syscall 直调 user32 / gdi32，
// 不引入 cgo 与第三方库（与 pickfolder_windows.go 同一路线）。

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"log"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	// user32 / procSetForegroundWindow 复用 pickfolder_windows.go 的声明
	compUser32                  = syscall.NewLazyDLL("user32.dll")
	compGdi32                   = syscall.NewLazyDLL("gdi32.dll")
	procSetProcessDPIAware      = compUser32.NewProc("SetProcessDPIAware")
	procGetSystemMetrics        = compUser32.NewProc("GetSystemMetrics")
	procSendInput               = compUser32.NewProc("SendInput")
	procSetCursorPos            = compUser32.NewProc("SetCursorPos")
	procGetDesktopWindow        = compUser32.NewProc("GetDesktopWindow")
	procGetDC                   = compUser32.NewProc("GetDC")
	procReleaseDC               = compUser32.NewProc("ReleaseDC")
	procEnumWindows             = compUser32.NewProc("EnumWindows")
	procIsWindowVisible         = compUser32.NewProc("IsWindowVisible")
	procGetWindowTextW          = compUser32.NewProc("GetWindowTextW")
	procCreateCompatibleDC      = compGdi32.NewProc("CreateCompatibleDC")
	procDeleteDC                = compGdi32.NewProc("DeleteDC")
	procSelectObject            = compGdi32.NewProc("SelectObject")
	procDeleteObject            = compGdi32.NewProc("DeleteObject")
	procBitBlt                  = compGdi32.NewProc("BitBlt")
	procCreateDIBSection        = compGdi32.NewProc("CreateDIBSection")
)

var dpiOnce sync.Once

// ensureDPIAware 声明进程 DPI 感知，保证截屏与坐标是真实像素而非缩放值。
func ensureDPIAware() {
	dpiOnce.Do(func() { _, _, _ = procSetProcessDPIAware.Call() })
}

func screenMetrics() (int, int) {
	ensureDPIAware()
	w, _, _ := procGetSystemMetrics.Call(0)
	h, _, _ := procGetSystemMetrics.Call(1)
	return int(int32(w)), int(int32(h))
}

// ---------- 键鼠注入 ----------

const (
	inputMouse    = 0
	inputKeyboard = 1
	keyEventfKeyUP   = 0x0002
	keyEventfUnicode = 0x0004
	mouseEventfLeftDown  = 0x0002
	mouseEventfLeftUp    = 0x0004
	mouseEventfRightDown = 0x0008
	mouseEventfRightUp   = 0x0010
	mouseEventfMiddleDown = 0x0020
	mouseEventfMiddleUp   = 0x0040
	mouseEventfWheel      = 0x0800
	wheelDelta            = 120
)

// sendInput 的输入结构体：联合体按最大成员（MOUSEINPUT，x64 下 32 字节）对齐。
type inputUnion struct{ data [32]byte }

func makeMouseInput(flags uint32, dx, dy, mouseData int32) inputUnion {
	var u inputUnion
	u.data[0] = byte(dx)
	u.data[1] = byte(dx >> 8)
	u.data[2] = byte(dx >> 16)
	u.data[3] = byte(dx >> 24)
	u.data[4] = byte(dy)
	u.data[5] = byte(dy >> 8)
	u.data[6] = byte(dy >> 16)
	u.data[7] = byte(dy >> 24)
	putU32(u.data[8:12], uint32(mouseData))
	putU32(u.data[12:16], flags)
	return u
}

func makeKeyInput(wVk, wScan uint16, flags uint32) inputUnion {
	var u inputUnion
	u.data[0] = byte(wVk)
	u.data[1] = byte(wVk >> 8)
	u.data[2] = byte(wScan)
	u.data[3] = byte(wScan >> 8)
	putU32(u.data[4:8], flags)
	return u
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func sendInputs(inputs []inputUnion) error {
	if len(inputs) == 0 {
		return nil
	}
	r, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		uintptr(unsafe.Sizeof(inputUnion{}))+8, // INPUT = type(4)+pad(4)+union(32)
	)
	if r == 0 {
		return fmt.Errorf("SendInput 失败: %v", err)
	}
	return nil
}

func computerClickAt(x, y int, button string, double bool) error {
	sw, sh := screenMetrics()
	if sw <= 0 || sh <= 0 {
		return fmt.Errorf("无法获取屏幕尺寸")
	}
	if x < 0 || y < 0 || x >= sw || y >= sh {
		return fmt.Errorf("坐标 (%d, %d) 超出屏幕范围 %dx%d", x, y, sw, sh)
	}
	if r, _, _ := procSetCursorPos.Call(uintptr(x), uintptr(y)); r == 0 {
		return fmt.Errorf("移动鼠标到 (%d, %d) 失败", x, y)
	}
	var down, up uint32
	switch button {
	case "right":
		down, up = mouseEventfRightDown, mouseEventfRightUp
	case "middle":
		down, up = mouseEventfMiddleDown, mouseEventfMiddleUp
	default:
		down, up = mouseEventfLeftDown, mouseEventfLeftUp
	}
	clicks := 1
	if double {
		clicks = 2
	}
	for i := 0; i < clicks; i++ {
		if err := sendInputs([]inputUnion{makeMouseInput(down, 0, 0, 0)}); err != nil {
			return err
		}
		if err := sendInputs([]inputUnion{makeMouseInput(up, 0, 0, 0)}); err != nil {
			return err
		}
	}
	return nil
}

func computerTypeText(text string) error {
	var inputs []inputUnion
	for _, r := range text {
		inputs = append(inputs, makeKeyInput(0, uint16(r), keyEventfUnicode))
		inputs = append(inputs, makeKeyInput(0, uint16(r), keyEventfUnicode|keyEventfKeyUP))
	}
	return sendInputs(inputs)
}

var namedKeys = map[string]uint16{
	"enter": 0x0D, "return": 0x0D, "esc": 0x1B, "escape": 0x1B, "tab": 0x09,
	"backspace": 0x08, "delete": 0x2E, "del": 0x2E, "space": 0x20,
	"home": 0x24, "end": 0x23, "pageup": 0x21, "pagedown": 0x22,
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
}

func init() {
	for i := 0; i < 12; i++ {
		namedKeys[fmt.Sprintf("f%d", i+1)] = uint16(0x70 + i)
	}
}

const (
	vkControl = 0x11
	vkMenu    = 0x12
	vkShift   = 0x10
)

func computerSendKey(key string, ctrl, alt, shift bool) error {
	name := strings.ToLower(strings.TrimSpace(key))
	var vk uint16
	if code, ok := namedKeys[name]; ok {
		vk = code
	} else if len([]rune(name)) == 1 {
		r := []rune(strings.ToUpper(name))[0]
		vk = uint16(r) // 字母与数字的 ASCII 与 VK 码一致
	} else {
		return fmt.Errorf("不认识的按键: %s", key)
	}
	var inputs []inputUnion
	if ctrl {
		inputs = append(inputs, makeKeyInput(vkControl, 0, 0))
	}
	if alt {
		inputs = append(inputs, makeKeyInput(vkMenu, 0, 0))
	}
	if shift {
		inputs = append(inputs, makeKeyInput(vkShift, 0, 0))
	}
	inputs = append(inputs, makeKeyInput(vk, 0, 0))
	inputs = append(inputs, makeKeyInput(vk, 0, keyEventfKeyUP))
	if shift {
		inputs = append(inputs, makeKeyInput(vkShift, 0, keyEventfKeyUP))
	}
	if alt {
		inputs = append(inputs, makeKeyInput(vkMenu, 0, keyEventfKeyUP))
	}
	if ctrl {
		inputs = append(inputs, makeKeyInput(vkControl, 0, keyEventfKeyUP))
	}
	return sendInputs(inputs)
}

func computerScroll(notches int) error {
	return sendInputs([]inputUnion{makeMouseInput(mouseEventfWheel, 0, 0, int32(notches*wheelDelta))})
}

// ---------- 截屏 ----------

func computerCaptureScreen() ([]byte, int, int, error) {
	ensureDPIAware()
	w, h := screenMetrics()
	if w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("无法获取屏幕尺寸")
	}
	hDesktop, _, _ := procGetDesktopWindow.Call()
	hDC, _, _ := procGetDC.Call(hDesktop)
	if hDC == 0 {
		return nil, 0, 0, fmt.Errorf("获取屏幕 DC 失败")
	}
	defer procReleaseDC.Call(hDesktop, hDC)
	hMem, _, _ := procCreateCompatibleDC.Call(hDC)
	if hMem == 0 {
		return nil, 0, 0, fmt.Errorf("创建内存 DC 失败")
	}
	defer procDeleteDC.Call(hMem)

	// 用 DIB Section 建位图：像素内存直接可读，免去 GetDIBits 的繁琐条件
	bmi := make([]byte, 40)
	putU32(bmi[0:4], 40)
	putU32(bmi[4:8], uint32(w))
	putU32(bmi[8:12], uint32(-h)) // 负高度 = 自顶向下
	putU16(bmi[12:14], 1)
	putU16(bmi[14:16], 32)
	var ppv unsafe.Pointer
	hBmp, _, err := procCreateDIBSection.Call(hDC, uintptr(unsafe.Pointer(&bmi[0])), 0, uintptr(unsafe.Pointer(&ppv)), 0, 0)
	if hBmp == 0 {
		return nil, 0, 0, fmt.Errorf("CreateDIBSection 失败: %v", err)
	}
	defer procDeleteObject.Call(hBmp)
	procSelectObject.Call(hMem, hBmp)
	// SRCCOPY
	if r, _, err := procBitBlt.Call(hMem, 0, 0, uintptr(w), uintptr(h), hDC, 0, 0, uintptr(0x00CC0020)); r == 0 {
		return nil, 0, 0, fmt.Errorf("BitBlt 失败: %v", err)
	}

	// 直接读 DIB 像素内存（BGRA，自顶向下），转 RGBA 后编码 PNG
	pixels := unsafe.Slice((*byte)(ppv), w*h*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = pixels[i*4+2]
		img.Pix[i*4+1] = pixels[i*4+1]
		img.Pix[i*4+2] = pixels[i*4+0]
		img.Pix[i*4+3] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), w, h, nil
}

func putU16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

// ---------- 窗口枚举与激活 ----------

// EnumWindows 的回调跑在原生栈上（panic 冒出去＝整进程无声死亡），且
// syscall.NewCallback 的回调槽位进程级有限（约 2000 个，用尽后再创建会
// panic）：回调只建一次，状态经互斥的包变量传递。enumNeedle 空串 = 收集
// 全部可见窗口标题（computerListWindows），非空 = 找首个标题含子串的窗口
// （computerActivateWindow，命中即停）。
var (
	enumMu     sync.Mutex
	enumTitles []string // 收集模式的累积结果
	enumNeedle string   // 匹配子串（空 = 收集模式）
	enumTarget uintptr  // 匹配到的窗口句柄
	enumWndCb  = syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		// 就地兜住 panic：兜住后返回 0 终止本轮枚举（结果残缺，按本轮
		// 失败/空结果论），进程保住（与窗口过程同一纪律）。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[computer] 枚举窗口回调 panic（已兜住）: %v\n%s", r, debug.Stack())
			}
		}()
		if r1, _, _ := procIsWindowVisible.Call(hwnd); r1 == 0 {
			return 1
		}
		n, _, _ := procGetWindowTextW.Call(hwnd, 0, 0)
		if n == 0 {
			return 1
		}
		buf := make([]uint16, n+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
		title := strings.TrimSpace(syscall.UTF16ToString(buf))
		enumMu.Lock()
		defer enumMu.Unlock()
		if enumNeedle == "" {
			if title != "" {
				enumTitles = append(enumTitles, title)
			}
			return 1
		}
		if strings.Contains(title, enumNeedle) {
			enumTarget = hwnd
			return 0 // 命中即停
		}
		return 1
	})
)

func computerListWindows() ([]string, error) {
	enumMu.Lock()
	enumTitles, enumNeedle, enumTarget = nil, "", 0
	enumMu.Unlock()
	procEnumWindows.Call(enumWndCb, 0)
	enumMu.Lock()
	titles := append([]string(nil), enumTitles...)
	enumTitles = nil
	enumMu.Unlock()
	sort.Strings(titles)
	return titles, nil
}

func computerActivateWindow(substring string) error {
	enumMu.Lock()
	enumNeedle, enumTarget, enumTitles = substring, 0, nil
	enumMu.Unlock()
	procEnumWindows.Call(enumWndCb, 0)
	enumMu.Lock()
	hwnd := enumTarget
	enumNeedle, enumTarget = "", 0
	enumMu.Unlock()
	if hwnd == 0 {
		return fmt.Errorf("没有找到标题包含「%s」的可见窗口", substring)
	}
	procSetForegroundWindow.Call(hwnd)
	return nil
}
