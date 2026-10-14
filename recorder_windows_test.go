//go:build windows

package main

// 演示记录器 Windows 实现的实机测试：起录（装钩子+开录音+worker）→ 让窗口
// 轮询与首帧截屏自然发生 → 停录 → 验证材料产物与格式。真实点击不便在测试里
// 合成（钩子会过滤注入事件），靠自动事件（首帧截图/窗口轮询）覆盖链路主干。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecorderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := recorderStart(dir); err != nil {
		t.Fatalf("recorderStart: %v", err)
	}
	// 起录后立即再开一次：应被拒绝（互斥）
	if err := recorderStart(t.TempDir()); err == nil {
		t.Fatal("重复起录未被拒绝")
	}
	if got := recorderStatusText(); !strings.Contains(got, "记录中") {
		t.Fatalf("状态文本异常: %s", got)
	}
	// 等 ticker（400ms）跑几轮：首帧截图 + 窗口事件落盘
	time.Sleep(1500 * time.Millisecond)
	stats, err := recorderStop()
	if err != nil {
		t.Fatalf("recorderStop: %v", err)
	}
	if stats.Dir != dir {
		t.Fatalf("目录不一致: %s != %s", stats.Dir, dir)
	}
	if stats.Shots < 1 {
		t.Fatalf("没有产出截图（shots=%d）", stats.Shots)
	}
	if stats.Events < 1 {
		t.Fatalf("没有事件（events=%d）", stats.Events)
	}
	// 二次停录应报「没有在录」
	if _, err := recorderStop(); err == nil {
		t.Fatal("重复停录未被拒绝")
	}

	// 事件日志：每行可解析、内部字段不落盘
	raw, err := os.ReadFile(filepath.Join(dir, recorderEventsFn))
	if err != nil {
		t.Fatalf("读事件日志: %v", err)
	}
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var ev recEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("事件行不是合法 JSON: %q (%v)", line, err)
		}
		if ev.VK != 0 || ev.Up {
			t.Fatalf("内部字段泄漏到事件日志: %q", line)
		}
		lines++
	}
	if lines == 0 {
		t.Fatal("事件日志为空")
	}

	// 截图文件存在且是 PNG
	name := "001.png"
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		// 编号从 1 起；若有窗口轮询首帧编号应为 001
		ents, _ := os.ReadDir(dir)
		pngs := []string{}
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".png") {
				pngs = append(pngs, e.Name())
			}
		}
		if len(pngs) == 0 {
			t.Fatal("目录里没有 PNG")
		}
		name = pngs[0]
	}
	png, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || len(png) < 8 || string(png[:4]) != "\x89PNG" {
		t.Fatalf("截图不是合法 PNG: %v (len=%d)", err, len(png))
	}

	// INDEX 生成 + SRT 对照解析
	if err := buildRecorderIndex(stats); err != nil {
		t.Fatalf("buildRecorderIndex: %v", err)
	}
	idx, err := os.ReadFile(filepath.Join(dir, recorderIndexFn))
	if err != nil {
		t.Fatalf("读 INDEX: %v", err)
	}
	if !strings.Contains(string(idx), "演示记录对照表") || !strings.Contains(string(idx), "| 时间 |") {
		t.Fatalf("INDEX 内容异常:\n%s", idx)
	}
	cues := parseSRT("1\n00:00:01,000 --> 00:00:03,500\n先点右上角导出\n\n2\n00:00:05,000 --> 00:00:06,000\n再确认\n")
	if len(cues) != 2 || cues[0].StartMs != 1000 || cues[0].EndMs != 3500 || cues[1].Text != "再确认" {
		t.Fatalf("SRT 解析异常: %+v", cues)
	}
}

// TestRecorderASRHookNil：默认不接线转写（voiceasr.go 的 NewApp 注册才有），
// 未接线时录制照常、只是不出字幕——测试环境保持这个前提。
func TestRecorderASRHookNil(t *testing.T) {
	if recorderASR != nil {
		t.Fatal("测试环境不应注册转写钩子")
	}
}
