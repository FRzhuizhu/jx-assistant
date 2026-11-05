//go:build windows

package main

// 右侧栏截图的 Windows 实现：shellEval 往主界面注入一段 JS 读取右栏矩形
// （CSS px）并 POST /api/shell/panel-view 回传；Go 侧换算成屏幕物理坐标
// （客户区原点 + CSS×dpr），GDI 截屏（DIB Section，procs 复用电脑操作组件
// 的 tool_computer_windows.go）。截屏幕像素的好处：右栏里无论是原生
// WebView2 面板（浏览器页）还是任何 HTML 视图都一样成立，不需要按内容
// 类型分路。

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 屏幕截取的共享原语（GDI，纯 syscall，不引 cgo）：右侧栏截图与演示录制器
// 都用它，所以归到截屏职责的本文件，不依附任何业务组件。
var (
	shotUser32 = syscall.NewLazyDLL("user32.dll")
	shotGdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC              = shotUser32.NewProc("GetDC")
	procReleaseDC          = shotUser32.NewProc("ReleaseDC")
	procSetProcessDPIAware = shotUser32.NewProc("SetProcessDPIAware")
	procGetSystemMetrics   = shotUser32.NewProc("GetSystemMetrics")

	procCreateCompatibleDC = shotGdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = shotGdi32.NewProc("DeleteDC")
	procSelectObject       = shotGdi32.NewProc("SelectObject")
	procDeleteObject       = shotGdi32.NewProc("DeleteObject")
	procBitBlt             = shotGdi32.NewProc("BitBlt")
	procCreateDIBSection   = shotGdi32.NewProc("CreateDIBSection")
)

var shotDpiOnce sync.Once

// ensureDPIAware 声明进程 DPI 感知：截屏要的是真实像素，不是系统缩放后的逻辑值。
func ensureDPIAware() {
	shotDpiOnce.Do(func() { _, _, _ = procSetProcessDPIAware.Call() })
}

// screenMetrics 返回主屏物理像素尺寸。
func screenMetrics() (int, int) {
	ensureDPIAware()
	w, _, _ := procGetSystemMetrics.Call(0)
	h, _, _ := procGetSystemMetrics.Call(1)
	return int(int32(w)), int(int32(h))
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func putU16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

var procPanelClientToScreen = windows.NewLazySystemDLL("user32.dll").NewProc("ClientToScreen")

type panelViewPoint struct{ X, Y int32 }

// panelViewProbeJS 注入主界面的探针：读右栏（#rpanel）矩形并回传。
// 右栏收起时 rect 为零 → ok=false。
const panelViewProbeJS = `(function(){` +
	`var p=document.getElementById('rpanel'),ok=false,x=0,y=0,w=0,h=0;` +
	`if(p&&!p.classList.contains('hidden')){var r=p.getBoundingClientRect();` +
	`x=r.x;y=r.y;w=r.width;h=r.height;ok=r.width>8&&r.height>8;}` +
	`fetch('/api/shell/panel-view',{method:'POST',headers:{'Content-Type':'application/json'},` +
	`body:JSON.stringify({x:x,y:y,w:w,h:h,dpr:window.devicePixelRatio||1,ok:ok})` +
	`}).catch(function(){});})()`

// captureRightPanel 截右侧栏当前画面（PNG 字节）。
func (a *App) captureRightPanel() ([]byte, error) {
	if a.shellEval == nil || a.shellWnd == 0 {
		return nil, fmt.Errorf("右侧栏截图仅桌面版支持；网页/手机端没有右侧栏")
	}
	// 丢弃陈旧回传，注入探针，等前端 POST 回来
	select {
	case <-a.panelViewCh:
	default:
	}
	a.shellEval(panelViewProbeJS)
	var rep panelViewReport
	select {
	case rep = <-a.panelViewCh:
	case <-time.After(2500 * time.Millisecond):
		return nil, fmt.Errorf("读取右侧栏状态超时（界面可能正忙，稍后再试）")
	}
	if !rep.OK {
		return nil, fmt.Errorf("右侧栏当前没有打开内容（处于收起状态）")
	}
	// 客户区原点 → 屏幕；CSS×dpr → 物理
	var pt panelViewPoint
	_, _, _ = procPanelClientToScreen.Call(a.shellWnd, uintptr(unsafe.Pointer(&pt)))
	x := int(pt.X) + int(rep.X*rep.Dpr+0.5)
	y := int(pt.Y) + int(rep.Y*rep.Dpr+0.5)
	w := int(rep.W*rep.Dpr + 0.5)
	h := int(rep.H*rep.Dpr + 0.5)
	if w < 8 || h < 8 {
		return nil, fmt.Errorf("右侧栏矩形异常（%dx%d）", w, h)
	}
	return panelCaptureRect(x, y, w, h)
}

// panelCaptureRect 区域截屏：DIB Section 直接拿像素（BGRA 自顶向下），
// SRCCOPY 从屏幕 DC 拷贝（合成桌面内容，WebView2 的 DComp 画面照常截到）。
func panelCaptureRect(x, y, w, h int) ([]byte, error) {
	hDC, _, _ := procGetDC.Call(0)
	if hDC == 0 {
		return nil, fmt.Errorf("获取屏幕 DC 失败")
	}
	defer procReleaseDC.Call(0, hDC)
	hMem, _, _ := procCreateCompatibleDC.Call(hDC)
	if hMem == 0 {
		return nil, fmt.Errorf("创建内存 DC 失败")
	}
	defer procDeleteDC.Call(hMem)

	bmi := make([]byte, 40)
	putU32(bmi[0:4], 40)
	putU32(bmi[4:8], uint32(w))
	putU32(bmi[8:12], uint32(-h)) // 负高度 = 自顶向下
	putU16(bmi[12:14], 1)
	putU16(bmi[14:16], 32)
	var ppv unsafe.Pointer
	hBmp, _, err := procCreateDIBSection.Call(hDC, uintptr(unsafe.Pointer(&bmi[0])), 0, uintptr(unsafe.Pointer(&ppv)), 0, 0)
	if hBmp == 0 {
		return nil, fmt.Errorf("CreateDIBSection 失败: %v", err)
	}
	defer procDeleteObject.Call(hBmp)
	procSelectObject.Call(hMem, hBmp)
	if r, _, err := procBitBlt.Call(hMem, 0, 0, uintptr(w), uintptr(h), hDC, uintptr(x), uintptr(y), uintptr(0x00CC0020)); r == 0 {
		return nil, fmt.Errorf("BitBlt 失败: %v", err)
	}

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
		return nil, err
	}
	return buf.Bytes(), nil
}
