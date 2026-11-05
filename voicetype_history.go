package main

// voicetype_history.go —— 设置页「最近语音记录」的后端：读独立语音输入法
// （VoiceTypeGo）落在自己目录里的 history.json（原始语音 wav + 文字 + 结果）。
//
// 单写者原则：记录由输入法进程单点写（说话/识别/分发都在那边）；「重新识别」
// 优先写 trigger.txt=reid n 让输入法自己做（更新记录 + 走完整分发，还不会有
// 两个进程同时改 history.json 的竞态）；输入法没在跑时才由主程序直调组件的
// ASR worker 改写记录——此时没有并发写者，安全。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// voicetypeHistoryDir 输入法所在目录（候选里第一个真有 exe 的）；找不到返回空。
func voicetypeHistoryDir() string {
	for _, exe := range voiceTypeCandidates() {
		if fileExists(exe) {
			return filepath.Dir(exe)
		}
	}
	return ""
}

// readVoicetypeHistory 读 history.json（不存在/损坏都返回 nil）。
func readVoicetypeHistory() []map[string]any {
	dir := voicetypeHistoryDir()
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "history.json"))
	if err != nil {
		return nil
	}
	var arr []map[string]any
	_ = json.Unmarshal(raw, &arr)
	return arr
}

// handleVoicetypeHistory GET /api/voicetype/history —— 最近语音记录列表
//（time/text/status/audio，audio 为空表示该条没有音频）。
func (a *App) handleVoicetypeHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"items": readVoicetypeHistory()})
}

// handleVoicetypeHistoryWav GET /api/voicetype/history/wav?i=n —— 第 n 条的原始
// 语音（audio/wav），设置页 <audio> 试听用。
func (a *App) handleVoicetypeHistoryWav(w http.ResponseWriter, r *http.Request) {
	items := readVoicetypeHistory()
	i, _ := strconv.Atoi(r.URL.Query().Get("i"))
	if i < 0 || i >= len(items) {
		writeErr(w, 404, "没有这条记录")
		return
	}
	audio, _ := items[i]["audio"].(string)
	if audio == "" {
		writeErr(w, 404, "这条记录没有音频")
		return
	}
	wav, err := os.ReadFile(filepath.Join(voicetypeHistoryDir(), filepath.FromSlash(audio)))
	if err != nil {
		writeErr(w, 404, "音频文件读不出: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(wav)
}

// handleVoicetypeHistoryReidentify POST /api/voicetype/history/reidentify {"i":n}
//
// 输入法在跑 → 写 trigger.txt（reid n），由它重识别并走完整分发，前端稍后刷新
// 列表即可；没在跑 → 主程序直调组件 ASR worker 识别并就地更新该条记录。
func (a *App) handleVoicetypeHistoryReidentify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "只接受 POST")
		return
	}
	var body struct {
		I int `json:"i"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	items := readVoicetypeHistory()
	if body.I < 0 || body.I >= len(items) {
		writeErr(w, 404, "没有这条记录")
		return
	}
	audio, _ := items[body.I]["audio"].(string)
	dir := voicetypeHistoryDir()
	if audio == "" {
		writeErr(w, 400, "这条记录没有音频，无法重新识别")
		return
	}

	if voiceTypeRunning() {
		// 输入法在跑：交给它（单写者 + 完整分发链路）。trigger.txt 处理完即删。
		if err := os.WriteFile(filepath.Join(dir, "trigger.txt"), []byte("reid "+strconv.Itoa(body.I)), 0644); err != nil {
			writeErr(w, 500, "触发失败: "+err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"queued": true})
		return
	}

	// 输入法没跑：主程序直调组件 ASR worker（此刻无并发写者，可安全改写）。
	wav, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(audio)))
	if err != nil {
		writeErr(w, 404, "音频文件读不出: "+err.Error())
		return
	}
	text, err := a.transcribeWavBytes(wav)
	if err != nil {
		writeErr(w, 500, "重新识别失败: "+err.Error())
		return
	}
	items[body.I]["text"] = text
	items[body.I]["status"] = "copied" // 文字已就绪：设置页里可一键复制
	raw, _ := json.MarshalIndent(items, "", "  ")
	tmp := filepath.Join(dir, "history.json.tmp")
	if err := os.WriteFile(tmp, raw, 0644); err == nil {
		_ = os.Rename(tmp, filepath.Join(dir, "history.json"))
	}
	writeJSON(w, 200, map[string]any{"queued": false, "text": text, "items": items})
}
