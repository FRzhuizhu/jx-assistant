package main

// voicetype_link.go —— 与独立语音输入法（VoiceTypeGo）的联动配置。
//
// 为什么需要它：输入法是**独立进程**——读不到本页 localStorage，也不该读
// config.json（那里面有 Key）。而「听写快捷键」住在页面 localStorage
// （组件 voice 的 cx:voice:* 经 HK 统一登记），主程序这边唯一能落盘的通道就是
// 这里：前端在快捷键/点缀色变化时 POST /api/voicetype/link，主程序把**热键与
// 颜色**写进 voicetype.json（与 config.json 同目录），输入法每秒看 mtime 热加载。
//
// 文件内容（刻意只有这三样，任何密钥都不进这个文件）：
//
//	{
//	  "hotkeys": {"main": ["alt"], "sub": ["mouse-side"]},
//	  "accentColor": "#4d6bfe",
//	  "textColor": "#1f2328",
//	  "updatedAt": "2026-11-03T21:30:00+08:00"
//	}
//
// 合并策略：请求里没带的字段保持原值——前端两处调用（听写快捷键 / 点缀色）
// 各报各的，互相不覆盖。

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// voicetypeLinkFile 联动文件名（与 config.json 同目录）。
const voicetypeLinkFile = "voicetype.json"

// voicetypeLink 联动文件结构（字段名与输入法侧 vt_link_windows.go 一致）。
type voicetypeLink struct {
	Hotkeys struct {
		Main []string `json:"main"`
		Sub  []string `json:"sub"`
	} `json:"hotkeys"`
	AccentColor string `json:"accentColor,omitempty"`
	TextColor   string `json:"textColor,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

// voicetypeLinkPath 联动文件路径：与 config.json 同目录（安装版在 exe 旁、
// 开发版在仓库根，输入法两处都会探测）。
func (a *App) voicetypeLinkPath() string {
	dir := filepath.Dir(a.CfgPath)
	if dir == "" || dir == "." {
		if exe, err := os.Executable(); err == nil {
			dir = filepath.Dir(exe)
		}
	}
	return filepath.Join(dir, voicetypeLinkFile)
}

// readVoicetypeLink 读现有文件（不存在/损坏都返回零值，不报错）。
func (a *App) readVoicetypeLink() voicetypeLink {
	var l voicetypeLink
	raw, err := os.ReadFile(a.voicetypeLinkPath())
	if err != nil {
		return l
	}
	_ = json.Unmarshal(raw, &l)
	return l
}

// writeVoicetypeLink 合并写：hotkeys 非 nil 才覆盖；颜色非空才覆盖。
//
// 原子写（临时文件 + rename）：输入法每秒轮询这个文件，读到半个 JSON 会被当成
// 损坏并跳过，用户会看到"改了没反应"。
func (a *App) writeVoicetypeLink(main, sub []string, accent, text string) error {
	cur := a.readVoicetypeLink()
	if main != nil || sub != nil {
		if main != nil {
			cur.Hotkeys.Main = main
		}
		if sub != nil {
			cur.Hotkeys.Sub = sub
		}
	}
	if s := strings.TrimSpace(accent); s != "" {
		cur.AccentColor = s
	}
	if s := strings.TrimSpace(text); s != "" {
		cur.TextColor = s
	}
	// 颜色缺省时用配置里的点缀色兜底（用户可能只改过主题色）
	if cur.AccentColor == "" {
		cur.AccentColor = a.Cfg.AccentColor
	}
	cur.UpdatedAt = time.Now().Format(time.RFC3339)

	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	path := a.voicetypeLinkPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	log.Printf("[voicetype-link] 已写 %s（热键 main=%v sub=%v 点缀色=%s 文字色=%s）",
		path, cur.Hotkeys.Main, cur.Hotkeys.Sub, cur.AccentColor, cur.TextColor)
	return nil
}

// handleVoicetypeLink POST /api/voicetype/link
//
// body: {"main":["alt"],"sub":[],"accentColor":"#4d6bfe","textColor":"#1f2328"}
// 全部字段可选；只写进来的那些。响应返回写后的文件内容（前端/排查都能直接看）。
func (a *App) handleVoicetypeLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "只接受 POST")
		return
	}
	var body struct {
		Main        []string `json:"main"`
		Sub         []string `json:"sub"`
		AccentColor string   `json:"accentColor"`
		TextColor   string   `json:"textColor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	// 键名归一：小写、去空、只认合法键名（与前端键名体系一致；不做宽进宽出，
	// 免得一个拼错的键名把输入法的热键整体顶掉）
	main := normalizeVoicetypeKeys(body.Main)
	sub := normalizeVoicetypeKeys(body.Sub)
	if err := a.writeVoicetypeLink(main, sub, body.AccentColor, body.TextColor); err != nil {
		writeErr(w, 500, "写联动配置失败: "+err.Error())
		return
	}
	writeJSON(w, 200, a.readVoicetypeLink())
}

// normalizeVoicetypeKeys 键名归一（nil=本次没带，空数组=明确停用该槽）。
func normalizeVoicetypeKeys(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, k := range in {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	return out
}
