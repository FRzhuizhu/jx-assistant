package main

// 演示记录器：创建技能模式（skill-create）的配套记录工具。
// 用户演示操作时，demo_record_start 起录——全局鼠标/键盘钩子在每次点击、
// 回车/Tab、窗口切换时自动截屏（画面没变则跳过），全程记事件日志与麦克风
// 音频；demo_record_stop 收录成材料目录（截图序列 + events.jsonl + audio.wav
// + INDEX.md 对照表）。模型 read INDEX.md 定位关键步骤、read_image 看截图，
// 即可还原用户做了什么，进而沉淀成 SKILL.md 技能。
// 工具定义跨平台，实现按平台分文件（recorder_windows.go 真实现，
// recorder_other.go 空桩），与 computer_* 组件同一组织方式；全部工具要求
// 会话开启「操作电脑」能力（perm.go 门控），fail closed。

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

// ---------- 转写接口位（预留，当前空着） ----------

// activeSpeechTranscriber 把演示录音转成 SRT 字幕的服务商实现位。
// 当前版本未接入任何服务商（恒为 nil）：demo_record_stop 只保留 audio.wav，
// 理解演示以截图 + 事件日志 + 用户在对话里的讲解为准。微软语音 API 调通后，
// 在这里注册一个 func（读 Cfg.SpeechAPI 的端点/密钥），stop / transcribe
// 即自动开始产出 讲解.srt——历史录音都在，可随时补转。
var activeSpeechTranscriber func(app *App, wavPath, srtPath string) error

func transcriberActive() bool { return activeSpeechTranscriber != nil }

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

// ---------- 工具定义 ----------

func demoRecordTools() []Tool {
	return []Tool{demoRecordStartTool(), demoRecordStopTool(), demoRecordStatusTool(), demoRecordTranscribeTool()}
}

func demoRecordStartTool() Tool {
	return Tool{
		Spec: toolSpec("demo_record_start",
			"开始演示记录：此后用户在电脑上的每次鼠标点击、回车/Tab 按键、窗口切换都会自动截屏，全程记录事件日志与麦克风音频（用户可以边操作边口头讲解）。用户演示完毕后调用 demo_record_stop 收录材料。请在用户明确表示要开始演示时才调用。",
			objSchema(map[string]any{}),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑"); err != nil {
				return "", err
			}
			if t.isRemote() {
				return "", fmt.Errorf("演示记录器仅支持本地会话（当前挂载的是 SSH 远程工作区）")
			}
			dir := filepath.Join(t.workspace(), recorderBaseDir,
				recorderDirPrefix+time.Now().Format("20060102-150405"))
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", err
			}
			if err := recorderStart(dir); err != nil {
				return "", err
			}
			return fmt.Sprintf("已开始演示记录，材料落盘：%s\n"+
				"请用户现在直接去电脑上操作并口头讲解（这里不用再发消息）；演示完回来喊停即可。\n"+
				"提醒用户：记录包含键鼠操作与屏幕截图，仅保存在本机工作区，用完可删。",
				filepath.ToSlash(dir)), nil
		},
	}
}

func demoRecordStopTool() Tool {
	return Tool{
		Spec: toolSpec("demo_record_stop",
			"停止演示记录并收录材料：停全局钩子与录音，生成截图序列、事件日志、INDEX.md 对照表（以及语音字幕——若转写服务已配置）。返回材料概要与目录。",
			objSchema(map[string]any{}),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑"); err != nil {
				return "", err
			}
			stats, err := recorderStop()
			if err != nil {
				return "", err
			}
			srtNote := "未生成（转写服务未配置，理解演示以截图+事件+用户讲解为准）"
			if stats.AudioOK {
				if transcriberActive() {
					wav := filepath.Join(stats.Dir, recorderAudioFn)
					srtPath := filepath.Join(stats.Dir, recorderSrtFn)
					if err := activeSpeechTranscriber(t.app, wav, srtPath); err != nil {
						srtNote = "转写失败：" + err.Error() + "（audio.wav 已保留，可稍后用 demo_record_transcribe 补转）"
					} else {
						srtNote = "已生成 " + recorderSrtFn
					}
				}
			} else if !stats.AudioOK {
				srtNote = "未生成（本次没有可用的麦克风录音）"
			}
			indexNote := ""
			if err := buildRecorderIndex(stats); err != nil {
				indexNote = "\nINDEX.md 生成失败：" + err.Error() + "（可直接读 events.jsonl）"
			}
			dur := time.Duration(stats.MS) * time.Millisecond
			return fmt.Sprintf("演示记录已收录：\n"+
				"- 目录：%s\n- 时长 %s，事件 %d 条，截图 %d 张\n"+
				"- 音频：%s\n- 字幕：%s%s\n\n"+
				"下一步：read %s 看时间线对照表，再按编号 read_image 看关键截图。\n"+
				"材料含用户的键鼠记录与截图，仅存本机；技能写完后提醒用户可删除该目录。",
				filepath.ToSlash(stats.Dir), dur.Round(time.Second), stats.Events, stats.Shots,
				map[bool]string{true: recorderAudioFn + " 已保存", false: "未录到（无可用麦克风）"}[stats.AudioOK],
				srtNote, indexNote,
				filepath.ToSlash(filepath.Join(stats.Dir, recorderIndexFn))), nil
		},
	}
}

func demoRecordStatusTool() Tool {
	return Tool{
		Spec: toolSpec("demo_record_status",
			"查看演示记录器当前状态：是否在录、已产生的事件数与截图数。",
			objSchema(map[string]any{}),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑"); err != nil {
				return "", err
			}
			return recorderStatusText(), nil
		},
	}
}

func demoRecordTranscribeTool() Tool {
	return Tool{
		Spec: toolSpec("demo_record_transcribe",
			"对一次已收录的演示记录补跑语音转写（生成 讲解.srt 并刷新 INDEX.md）。用于转写服务刚配置好、要给历史录音补字幕的场景。",
			objSchema(map[string]any{
				"dir": strProp("演示记录目录（相对或绝对路径，目录里需有 audio.wav）"),
			}, "dir"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑"); err != nil {
				return "", err
			}
			var p struct {
				Dir string `json:"dir"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			dir, err := t.resolve(p.Dir)
			if err != nil {
				return "", err
			}
			if isDir, _, serr := t.envStat(dir); serr != nil || !isDir {
				return "", fmt.Errorf("目录不存在: %s", dir)
			}
			wav := filepath.Join(dir, recorderAudioFn)
			if _, serr := os.Stat(wav); serr != nil {
				return "", fmt.Errorf("目录里没有 %s，无法转写（%s）", recorderAudioFn, dir)
			}
			if !transcriberActive() {
				return "转写服务未配置：当前版本尚未接入语音转写服务商（配置结构已预留）。" +
					"请先用截图 + 事件日志 + 用户在对话里的讲解理解演示；录音已保留，接入后可用本工具补转。", nil
			}
			srtPath := filepath.Join(dir, recorderSrtFn)
			if err := activeSpeechTranscriber(t.app, wav, srtPath); err != nil {
				return "", fmt.Errorf("转写失败: %w", err)
			}
			// 补出字幕后刷新对照表（stats 按 events.jsonl 行数与截图数重建）
			if err := rebuildRecorderIndex(dir); err != nil {
				return fmt.Sprintf("字幕已生成：%s（INDEX.md 刷新失败：%v）", filepath.ToSlash(srtPath), err), nil
			}
			return fmt.Sprintf("字幕已生成并刷新对照表：%s", filepath.ToSlash(srtPath)), nil
		},
	}
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

// rebuildRecorderIndex 按目录里的 events.jsonl / 截图 / 字幕重建 stats 并生成 INDEX。
func rebuildRecorderIndex(dir string) error {
	events, err := os.ReadFile(filepath.Join(dir, recorderEventsFn))
	if err != nil {
		return fmt.Errorf("读 %s 失败: %w", recorderEventsFn, err)
	}
	evCount, shotCount, ms := 0, 0, 0
	for _, line := range strings.Split(string(events), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		evCount++
		var ev recEvent
		if json.Unmarshal([]byte(line), &ev) == nil {
			if ev.T > ms {
				ms = ev.T
			}
			if ev.Shot != "" {
				shotCount++
			}
		}
	}
	audioOK := false
	if fi, err := os.Stat(filepath.Join(dir, recorderAudioFn)); err == nil && fi.Size() > 44 {
		audioOK = true
	}
	return buildRecorderIndex(&recorderStats{Dir: dir, MS: ms, Events: evCount, Shots: shotCount, AudioOK: audioOK})
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
