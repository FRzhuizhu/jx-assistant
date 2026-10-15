package main

// 演示记录器：创建技能模式（skill-create）的配套记录工具，归用户直控——
// 输入框旁的录制按钮经 HTTP 端点（/api/demo-record/*，见 server.go）起录/停录，
// 模型不持有录制工具；停录后材料以【演示记录】系统通知进对话（同文件上传
// 通知机制），模型 read INDEX.md 定位关键步骤、read_image 看截图，即可还原
// 用户做了什么，进而沉淀成 SKILL.md 技能。
// 起录后全局鼠标/键盘钩子在每次点击、回车/Tab、窗口切换时自动截屏（画面没变
// 则跳过），全程记事件日志与麦克风音频；停录收录成材料目录（截图序列 +
// events.jsonl + audio.wav + INDEX.md 对照表）。
// 实现按平台分文件（recorder_windows.go 真实现，recorder_other.go 空桩），
// 与 computer_* 组件同一组织方式。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- 转写接线位 ----------

// recorderASR 把一段 16kHz/单声道/16bit WAV 交给语音组件的本地识别 worker
// （voiceasr.go 在 NewApp 注册；语音组件停用时调用返回错误＝该段字幕丢弃，
// 不影响录制本体）。演示记录随录随转：VAD 按停顿切段（recorder_windows.go），
// 每段经这里识别成文字，逐段落 讲解.srt——时间轴与事件/截图同源。测试环境
// 不注册（nil）＝纯录音、无字幕，行为与接组件前完全一致。
var recorderASR func(wav []byte) (string, error)

// ---------- 材料 目录名约定 ----------

const (
	recorderBaseDir   = "演示记录"         // 工作区下的记录根目录
	recorderEventsFn  = "events.jsonl" // 事件日志（每行一条 JSON）
	recorderAudioFn   = "audio.wav"    // 麦克风录音（16kHz 单声道）
	recorderSrtFn     = "讲解.srt"       // 语音转写字幕（转写服务接入后产出）
	recorderIndexFn   = "INDEX.md"     // 材料对照表（stop 时生成）
	recorderMaxShots  = 400            // 单次记录截图上限（防磁盘失控）
	recorderDirPrefix = "record-"      // 记录目录前缀，后接时间戳
)

// recorderStats 是一次记录收尾时的统计（INDEX 与工具返回共用）。
type recorderStats struct {
	Dir     string
	MS      int
	Events  int
	Shots   int
	AudioOK bool
}

// recEvent 是事件日志（events.jsonl）里的一行。T 为相对记录起点的毫秒数
// （与 audio.wav / SRT 的时间轴同源对齐）。VK/Up 是钩子回调投递键盘原始
// 事件的内部字段，worker 聚合改写成 text/key 事件后不会落盘。
type recEvent struct {
	T      int    `json:"t"`
	Type   string `json:"type"` // click / wheel / text / key / window / shot
	Button string `json:"button,omitempty"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Text   string `json:"text,omitempty"`
	Key    string `json:"key,omitempty"`
	Title  string `json:"title,omitempty"`
	Delta  int    `json:"delta,omitempty"`
	Shot   string `json:"shot,omitempty"`
	VK     uint32 `json:"vk,omitempty"`
	Up     bool   `json:"up,omitempty"`
}

// ---------- 用户侧入口的支撑函数（HTTP 端点见 server.go） ----------

// recorderRecentDirs 列出工作区 演示记录/ 下已收录的记录目录，按目录名倒序
//（名字含时间戳，倒序＝新→旧）取前 n 条。建技能会话时的【演示记录】清单
// 通知用它告诉模型有哪些现成材料可分析——首页「先录后聊」的入口。
func recorderRecentDirs(ws string, n int) []string {
	if ws == "" || n <= 0 {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(ws, recorderBaseDir))
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), recorderDirPrefix) {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	if len(dirs) > n {
		dirs = dirs[:n]
	}
	return dirs
}

// recorderShutdown 进程收尾兜底：还在录就停录（保存 audio.wav）并生成
// INDEX.md，避免中途退出丢掉整段材料。桌面壳退出路径调用；没在录时
// 是空操作。此处不做语音转写
//（可能涉网络与耗时，留给停录端点与将来的补转端点）。
func recorderShutdown() {
	stats, err := recorderStop()
	if err != nil || stats == nil {
		return
	}
	_ = buildRecorderIndex(stats)
}

// demoRecordStopNote 停录后注入会话的【演示记录】通知正文（与文件上传通知
// 同机制：kindReport 用户侧系统行，模型下一轮可见）。字幕行按材料实况描述
//（随录随转：讲解.srt 是录制期间逐段落盘的，这里只如实汇报有没有、几段）。
func demoRecordStopNote(stats *recorderStats) string {
	srtNote := "未生成（语音组件未启用，或本次没识别到有效语音）"
	if data, err := os.ReadFile(filepath.Join(stats.Dir, recorderSrtFn)); err == nil {
		if n := len(parseSRT(string(data))); n > 0 {
			srtNote = fmt.Sprintf("已生成 %s（%d 段，随录随转）", recorderSrtFn, n)
		}
	}
	return fmt.Sprintf("【演示记录】用户点停止按钮完成了一段操作演示，材料已收录：\n"+
		"- 目录：%s\n- 时长 %s，事件 %d 条，截图 %d 张\n- 音频：%s；字幕：%s\n"+
		"下一步：read %s 看时间线对照表，再按编号 read_image 看关键截图；"+
		"用户当时的口头讲解若没进对话，请他口述关键步骤的意图，不要猜。",
		filepath.ToSlash(stats.Dir),
		(time.Duration(stats.MS) * time.Millisecond).Round(time.Second),
		stats.Events, stats.Shots,
		map[bool]string{true: recorderAudioFn + " 已保存", false: "未录到（无可用麦克风）"}[stats.AudioOK],
		srtNote, filepath.ToSlash(filepath.Join(stats.Dir, recorderIndexFn)))
}

// demoRecordListNote 建技能会话时的【演示记录】清单通知正文：工作区已有
// 现成演示材料时告诉模型有哪些可分析（dirs 为 recorderRecentDirs 的结果）。
func demoRecordListNote(dirs []string) string {
	names := make([]string, len(dirs))
	for i, d := range dirs {
		names[i] = recorderBaseDir + "/" + d
	}
	return "【演示记录】工作区已有 " + strconv.Itoa(len(dirs)) + " 份演示材料（按时间倒序）：" +
		strings.Join(names, "、") +
		"。每份含 INDEX.md 时间线对照表、编号截图、events.jsonl 事件日志与 audio.wav 录音（暂无语音转文字，讲解需用户口述或对话里说明）。" +
		"用户要分析某份时先 read 它的 INDEX.md，再按编号 read_image 看关键截图。"
}

// ---------- SRT 解析与 INDEX 生成（跨平台共用） ----------

// srtCue 是一条字幕：毫秒区间 + 文本。
type srtCue struct {
	StartMs int
	EndMs   int
	Text    string
}

// parseSRT 解析标准 SRT 字幕（块间空行分隔，时间行 HH:MM:SS,mmm --> …）。
// 解析宽松：坏块跳过，不整体失败。
func parseSRT(data string) []srtCue {
	var cues []srtCue
	for _, block := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		// 找时间行（有的块第一行是序号，有的直接是时间）
		ti := 0
		if strings.Contains(lines[0], "-->") {
			ti = 0
		} else if len(lines) > 1 && strings.Contains(lines[1], "-->") {
			ti = 1
		} else {
			continue
		}
		segs := strings.SplitN(lines[ti], "-->", 2)
		if len(segs) != 2 {
			continue
		}
		s, ok1 := parseSrtTime(strings.TrimSpace(segs[0]))
		e, ok2 := parseSrtTime(strings.Split(strings.TrimSpace(segs[1]), " ")[0])
		if !ok1 || !ok2 {
			continue
		}
		text := strings.TrimSpace(strings.Join(lines[ti+1:], " "))
		if text == "" {
			continue
		}
		cues = append(cues, srtCue{StartMs: s, EndMs: e, Text: text})
	}
	return cues
}

func parseSrtTime(s string) (int, bool) {
	// HH:MM:SS,mmm（毫秒分隔符逗号/点都认）
	s = strings.Replace(s, ".", ",", 1)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	secParts := strings.Split(parts[2], ",")
	sec, err3 := strconv.Atoi(secParts[0])
	ms := 0
	var err4 error
	if len(secParts) > 1 {
		ms, err4 = strconv.Atoi(secParts[1])
		if err4 == nil && len(secParts[1]) == 1 {
			ms *= 100
		} else if err4 == nil && len(secParts[1]) == 2 {
			ms *= 10
		}
	}
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return 0, false
	}
	return ((h*60+m)*60+sec)*1000 + ms, true
}

// writeSRTFile 把字幕段写成标准 SRT（序号/时间码/文本，块间空行）。随录随转
// 用：每出一段整文件重写（段数小、代价可忽略），中途崩溃已落盘的段仍在。
func writeSRTFile(path string, cues []srtCue) error {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTimecode(c.StartMs), srtTimecode(c.EndMs), strings.TrimSpace(c.Text))
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

// srtTimecode 毫秒 → SRT 时间码 HH:MM:SS,mmm。
func srtTimecode(ms int) string {
	if ms < 0 {
		ms = 0
	}
	h, m, s := ms/3600000, ms/60000%60, ms/1000%60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms%1000)
}

// recPcmToWav 给裸 PCM（16kHz/单声道/16bit，录制器的采集格式）包 RIFF 头，
// 供识别 worker 消费（worker 按头自解析采样率与声道）。
func recPcmToWav(pcm []byte) []byte {
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	recLe32(hdr[4:], uint32(36+len(pcm)))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	recLe32(hdr[16:], 16) // fmt 块长
	hdr[20], hdr[22] = 1, 1
	recLe32(hdr[24:], 16000) // 采样率
	recLe32(hdr[28:], 32000) // 字节率
	recLe16(hdr[32:], 2)     // 块对齐
	recLe16(hdr[34:], 16)    // 位深
	copy(hdr[36:40], "data")
	recLe32(hdr[40:], uint32(len(pcm)))
	return append(hdr, pcm...)
}

func recLe16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }

func recLe32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// buildRecorderIndex 生成 INDEX.md：事件时间线 + 截图编号 + 对应字幕段对照表。
func buildRecorderIndex(stats *recorderStats) error {
	raw, err := os.ReadFile(filepath.Join(stats.Dir, recorderEventsFn))
	if err != nil {
		return err
	}
	var cues []srtCue
	if srtData, err := os.ReadFile(filepath.Join(stats.Dir, recorderSrtFn)); err == nil {
		cues = parseSRT(string(srtData))
	}
	rel, _ := filepath.Rel(filepath.Dir(filepath.Dir(stats.Dir)), stats.Dir)

	var b strings.Builder
	fmt.Fprintf(&b, "# 演示记录对照表\n\n")
	fmt.Fprintf(&b, "- 目录：`%s`（相对工作区）\n", filepath.ToSlash(rel))
	fmt.Fprintf(&b, "- 时长约 %s；事件 %d 条，截图 %d 张\n",
		(time.Duration(stats.MS) * time.Millisecond).Round(time.Second), stats.Events, stats.Shots)
	if stats.AudioOK {
		fmt.Fprintf(&b, "- 音频：`%s`（麦克风全程录音）\n", recorderAudioFn)
	} else {
		fmt.Fprintf(&b, "- 音频：未录到（无可用麦克风）\n")
	}
	if len(cues) > 0 {
		fmt.Fprintf(&b, "- 字幕：`%s`（%d 段，时间与事件轴同源对齐）\n", recorderSrtFn, len(cues))
	} else {
		fmt.Fprintf(&b, "- 字幕：未生成（转写服务未配置）；用户在对话里说的话就是讲解词\n")
	}
	b.WriteString("\n说明：时间轴以记录起点为 0（毫秒）。截屏发生在点击、回车/Tab、窗口切换时；" +
		"连续打字聚合成一条 text 事件（中文输入法的上屏结果以截图为准，事件里只有原始按键）。" +
		"看图用 read_image，路径 = 上面的目录 + 截图文件名。\n\n")
	b.WriteString("| 时间 | 事件 | 截图 | 讲解 |\n|---|---|---|---|\n")

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev recEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		shot := ""
		if ev.Shot != "" {
			shot = ev.Shot
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			fmtMS(ev.T), describeRecEvent(&ev), shot, subtitleNear(cues, ev.T))
	}
	return os.WriteFile(filepath.Join(stats.Dir, recorderIndexFn), []byte(b.String()), 0644)
}

// subtitleNear 找覆盖/紧邻事件时刻的字幕文本（前后 3.5 秒窗口），多段去重拼接。
func subtitleNear(cues []srtCue, tMs int) string {
	var picked []string
	seen := map[string]bool{}
	for _, c := range cues {
		if tMs >= c.StartMs-3500 && tMs <= c.EndMs+3500 {
			txt := strings.ReplaceAll(c.Text, "|", "／")
			if !seen[txt] {
				seen[txt] = true
				picked = append(picked, txt)
			}
		}
		if len(picked) >= 3 {
			break
		}
	}
	return strings.Join(picked, " / ")
}

func describeRecEvent(ev *recEvent) string {
	switch ev.Type {
	case "click":
		return fmt.Sprintf("点击 (%d, %d) %s键", ev.X, ev.Y, ev.Button)
	case "wheel":
		return fmt.Sprintf("滚轮 %s %d 格", map[bool]string{true: "向上", false: "向下"}[ev.Delta > 0], absInt(ev.Delta))
	case "text":
		return "输入 " + strconv.Quote(ev.Text)
	case "key":
		return "按键 " + ev.Key
	case "window":
		return "切换窗口 → " + strings.ReplaceAll(ev.Title, "|", "／")
	case "shot":
		return "截屏（画面变化）"
	}
	return ev.Type
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func fmtMS(ms int) string {
	d := time.Duration(ms) * time.Millisecond
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}

// sortRecEvents 供测试/内部使用的事件排序（按时间稳定排序）。
func sortRecEvents(evs []recEvent) {
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].T < evs[j].T })
}
