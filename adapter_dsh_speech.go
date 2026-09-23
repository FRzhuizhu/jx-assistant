package main

// dsh-speech 插件的 Go 半边适配（对应 DSH 插件 jxSpeech Remote 中的
// polish / setVoiceActive 两个方法）：转写润色走本程序已配置的 LLM，
// 听写激活状态用于给会话系统提示词临时附加"转写瑕疵"提示段。
// 其余能力（TTS、浏览器听写、设置区）都在浏览器半边 client.js 里转译。

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const speechPluginID = "dsh-speech"

const speechPolishDefaultPrompt = `你是语音识别转写的润色器。用户给你一段转写文本，请只做保守纠错：
- 修正同音错字、明显的错别字与标点、分词问题
- 不改变句意，不增删内容，不改写口吻，不翻译
直接输出修正后的全文，不要任何解释、前后缀或代码块。`

const speechVoiceHint = `（提示：用户刚才可能在使用语音输入，消息中若存在同音错字，请按上下文理解其本意并在回复中自然使用正确写法，不必特意指出。）`

// speechVoiceActiveUntil 记录最近一次听写激活的失效时刻（unix 秒）。
var speechVoiceActiveUntil atomic.Int64

func init() {
	registerGoHalf(speechPluginID, goHalf{
		routes: speechRoutes,
		prompt: speechPromptExtra,
	})
}

func speechRoutes(a *App, mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugins/dsh-speech/polish", a.handleSpeechPolish)
	mux.HandleFunc("POST /api/plugins/dsh-speech/voice-active", a.handleVoiceActive)
}

// handleSpeechPolish：保守纠错一段转写文本（复用主配置的 LLM，非流式收集）。
func (a *App) handleSpeechPolish(w http.ResponseWriter, r *http.Request) {
	if a.Plugins == nil || !a.Plugins.IsEnabled(speechPluginID) {
		writeErr(w, 404, "插件已停用或不存在")
		return
	}
	var body struct {
		Text  string `json:"text"`
		Prompt string `json:"prompt"` // 用户自定义润色提示词，空则用默认
		Words string `json:"words"`   // 常用词表（顿号/换行分隔），纠正时优先
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "text 不能为空")
		return
	}
	if len([]rune(text)) > 12000 {
		writeErr(w, 400, "文本过长（上限 1.2 万字）")
		return
	}
	sys := speechPolishDefaultPrompt
	if strings.TrimSpace(body.Prompt) != "" {
		sys = strings.TrimSpace(body.Prompt)
	}
	user := text
	if words := normalizeWordList(body.Words); words != "" {
		user += "\n\n（用户常用词表，纠正时优先按这些写法：\n" + words + "）"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var sb strings.Builder
	err := StreamChat(ctx, a.DefaultTarget(), ChatRequest{
		Messages: []Message{
			TextMessage("system", sys),
			TextMessage("user", user),
		},
	}, StreamCallbacks{OnText: func(s string) { sb.WriteString(s) }})
	if err != nil {
		log.Printf("[dsh-speech] 润色失败: %v", err)
		writeErr(w, 500, "润色失败: "+err.Error())
		return
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		out = text // 失败保留原文（对齐 DSH 行为）
	}
	writeJSON(w, 200, map[string]string{"text": out})
}

func normalizeWordList(s string) string {
	lines := []string{}
	for _, ln := range strings.Split(strings.ReplaceAll(s, "、", "\n"), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n")
}

// handleVoiceActive：听写开始时浏览器上报，之后 N 分钟内会话提示词带转写提示。
func (a *App) handleVoiceActive(w http.ResponseWriter, r *http.Request) {
	if a.Plugins == nil || !a.Plugins.IsEnabled(speechPluginID) {
		writeErr(w, 404, "插件已停用或不存在")
		return
	}
	var body struct {
		Minutes float64 `json:"minutes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Minutes <= 0 || body.Minutes > 30 {
		body.Minutes = 5
	}
	speechVoiceActiveUntil.Store(time.Now().Add(time.Duration(body.Minutes * float64(time.Minute))).Unix())
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// speechPromptExtra：听写激活窗口内给所有会话附加提示段。
func speechPromptExtra(a *App, sess *SessionMeta) string {
	if time.Now().Unix() < speechVoiceActiveUntil.Load() {
		return speechVoiceHint
	}
	return ""
}
