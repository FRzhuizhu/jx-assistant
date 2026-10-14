package main

// dsh-speech 插件的 Go 半边适配（对应 DSH 插件 jxSpeech Remote 中的 polish
// 方法）：转写润色走本程序已配置的 LLM。其余能力（TTS、听写、设置区）都在
// 组件 组件/voice 里。原 setVoiceActive（听写激活后 N 分钟窗口内给系统提示词
// 附加"转写瑕疵"段）已随插件退役：语音提示改为会话级粘性标记 SessionMeta.
// VoiceUsed——chat/interject 上报置位、永不撤下（server.go / agent.go / store.go）。

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

const speechPluginID = "dsh-speech"

const speechPolishDefaultPrompt = `你是语音识别转写的润色器。用户给你一段转写文本，请只做保守纠错：
- 修正同音错字、明显的错别字与标点、分词问题
- 不改变句意，不增删内容，不改写口吻，不翻译
直接输出修正后的全文，不要任何解释、前后缀或代码块。`

func init() {
	registerGoHalf(speechPluginID, goHalf{
		routes: speechRoutes,
	})
}

func speechRoutes(a *App, mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugins/dsh-speech/polish", a.handleSpeechPolish)
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
