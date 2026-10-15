package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorderRecentDirs：按时间戳名倒序（新→旧）、上限截断、忽略非 record- 前缀、
// 空工作区/不存在/非法参数返回 nil——建技能会话的清单通知靠它列举现成材料。
func TestRecorderRecentDirs(t *testing.T) {
	ws := t.TempDir()
	if got := recorderRecentDirs(ws, 3); got != nil {
		t.Fatalf("不存在的工作区应返回 nil，got %v", got)
	}
	if got := recorderRecentDirs("", 3); got != nil {
		t.Fatalf("空路径应返回 nil，got %v", got)
	}
	if got := recorderRecentDirs(ws, 0); got != nil {
		t.Fatalf("n<=0 应返回 nil，got %v", got)
	}
	base := filepath.Join(ws, recorderBaseDir)
	for _, name := range []string{"record-20260101-090000", "record-20260102-100000", "record-20260103-110000", "not-a-record"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	got := recorderRecentDirs(ws, 3)
	if len(got) != 3 || got[0] != "record-20260103-110000" || got[1] != "record-20260102-100000" || got[2] != "record-20260101-090000" {
		t.Fatalf("应按时间倒序取 3 条且忽略非 record- 前缀，got %v", got)
	}
	if got := recorderRecentDirs(ws, 10); len(got) != 3 {
		t.Fatalf("不足上限时应全返回，got %v", got)
	}
}

// demoRecordListNote：清单通知应含【演示记录】前缀（前端 isSysNote 折叠渲染
// 依赖它）、目录名（带 演示记录/ 前缀）与分析指引（INDEX.md）。
func TestDemoRecordListNote(t *testing.T) {
	note := demoRecordListNote([]string{"record-20260103-110000", "record-20260102-100000"})
	for _, want := range []string{"【演示记录】", "2 份", "演示记录/record-20260103-110000", "演示记录/record-20260102-100000", "INDEX.md"} {
		if !strings.Contains(note, want) {
			t.Fatalf("清单通知缺 %q：%s", want, note)
		}
	}
}

// demoRecordStopNote：停录通知应含【演示记录】前缀、材料目录与 read INDEX.md
// 指引（无字幕文件时如实说明未生成）。
func TestDemoRecordStopNote(t *testing.T) {
	stats := &recorderStats{Dir: filepath.Join(t.TempDir(), recorderBaseDir, "record-20260101-090000"), MS: 90_000, Events: 12, Shots: 5, AudioOK: true}
	note := demoRecordStopNote(stats)
	for _, want := range []string{"【演示记录】", "record-20260101-090000", "1m30s", "事件 12 条", "截图 5 张", "INDEX.md", "字幕：未生成"} {
		if !strings.Contains(note, want) {
			t.Fatalf("停录通知缺 %q：%s", want, note)
		}
	}
	// 有字幕文件时改报段数（随录随转的实况汇报）
	if err := os.MkdirAll(stats.Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeSRTFile(filepath.Join(stats.Dir, recorderSrtFn), []srtCue{{StartMs: 0, EndMs: 2000, Text: "这样点"}}); err != nil {
		t.Fatal(err)
	}
	note = demoRecordStopNote(stats)
	if !strings.Contains(note, "已生成 "+recorderSrtFn+"（1 段，随录随转）") {
		t.Fatalf("有字幕时通知未报段数：%s", note)
	}
}

// writeSRTFile/srtTimecode：标准 SRT 格式，且写出的文件能被 parseSRT 原样
// 读回——随录随转的落盘与 INDEX 的解析是同一对函数，往返一致是硬要求。
func TestWriteSRTFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), recorderSrtFn)
	cues := []srtCue{
		{StartMs: 0, EndMs: 3200, Text: " 先点右上角导出 "},
		{StartMs: 65_000, EndMs: 71_250, Text: "再确认"},
	}
	if err := writeSRTFile(path, cues); err != nil {
		t.Fatalf("writeSRTFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "00:00:00,000 --> 00:00:03,200\n先点右上角导出") ||
		!strings.Contains(string(data), "00:01:05,000 --> 00:01:11,250\n再确认") {
		t.Fatalf("SRT 时间码/文本格式异常:\n%s", data)
	}
	back := parseSRT(string(data))
	if len(back) != 2 || back[0].StartMs != 0 || back[0].EndMs != 3200 || back[1].StartMs != 65_000 || back[1].EndMs != 71_250 {
		t.Fatalf("写读往返不一致: %+v", back)
	}
}

// recPcmToWav：44 字节 RIFF 头 + PCM 原样，格式字段 16kHz/单声道/16bit
// （识别 worker 按头自解析；录制器采集的就是这个格式）。
func TestRecPcmToWav(t *testing.T) {
	pcm := make([]byte, 3200) // 100ms @16k/mono/16bit
	for i := range pcm {
		pcm[i] = byte(i)
	}
	wav := recPcmToWav(pcm)
	if len(wav) != 44+len(pcm) || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("WAV 头异常: %q", wav[:44])
	}
	if binary.LittleEndian.Uint16(wav[20:]) != 1 || binary.LittleEndian.Uint16(wav[22:]) != 1 ||
		binary.LittleEndian.Uint32(wav[24:]) != 16000 || binary.LittleEndian.Uint32(wav[28:]) != 32000 ||
		binary.LittleEndian.Uint16(wav[32:]) != 2 || binary.LittleEndian.Uint16(wav[34:]) != 16 ||
		binary.LittleEndian.Uint32(wav[40:]) != uint32(len(pcm)) {
		t.Fatalf("WAV 格式字段异常")
	}
	if !bytes.Equal(wav[44:], pcm) {
		t.Fatal("PCM 数据被改动")
	}
}
