//go:build windows

package main

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

// TestLiveScreenCapture 真实截屏冒烟测试；非 -short 模式下运行（会截取真实桌面）。
// 运行：go test -run TestLiveScreenCapture . （go test -short . 时跳过）
func TestLiveScreenCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("live test")
	}
	raw, w, h, err := computerCaptureScreen()
	if err != nil {
		t.Fatalf("截屏失败: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("截屏结果不是合法 PNG: %v", err)
	}
	if got := img.Bounds().Dx(); got != w {
		t.Fatalf("宽度不符: %d vs %d", got, w)
	}
	_ = h
	t.Logf("截屏成功: %dx%d, %d 字节 PNG", w, h, len(raw))
	out := os.TempDir() + "\\jx-live-screen.png"
	if err := os.WriteFile(out, raw, 0644); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	t.Logf("已保存到 %s", out)
}
