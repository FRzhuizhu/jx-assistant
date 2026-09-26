// 问题反馈：客户端（本机 / 局域网 / 远程中继）一键上报问题或建议。
// 只做「接收 + 入库」：不广播、不提供查看与删除接口（以后要看得查库）。
//
// 防注入口径：
//   - SQL 一律参数化占位符（项目惯例，modernc.org/sqlite），无任何字符串拼接；
//   - content/kind/session_id 做长度与字符白名单校验，超限直接 400；
//   - diagnostics 只收合法 JSON 且限长，原文入库不解析不执行；
//   - 入库的是纯数据，将来展示层自行转义。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	feedbackMaxContent = 4000      // 反馈正文上限（字符）
	feedbackMaxDiag    = 32 * 1024 // 诊断信息上限（字节）
)

// AddFeedback 落一条反馈（feedback 表，见 store.go 迁移 0012）。
func (s *Store) AddFeedback(id, kind, content, sessionID, diagnostics, version, createdAt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO feedback
		(id, kind, content, session_id, diagnostics, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, kind, content, sessionID, diagnostics, version, createdAt)
	return err
}

func (a *App) handleFeedbackCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind        string          `json:"kind"`
		Content     string          `json:"content"`
		SessionID   string          `json:"sessionId"`
		Diagnostics json.RawMessage `json:"diagnostics"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	// 内容校验：正文必填限长；类型白名单；会话号白名单字符
	content := strings.TrimSpace(body.Content)
	if content == "" {
		writeErr(w, 400, "反馈内容不能为空")
		return
	}
	if len([]rune(content)) > feedbackMaxContent {
		writeErr(w, 400, "反馈内容过长（上限 4000 字）")
		return
	}
	kind := body.Kind
	if kind != "bug" && kind != "idea" {
		kind = "other"
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if len(sessionID) > 64 || strings.ContainsFunc(sessionID, func(rr rune) bool {
		return !(rr >= 'a' && rr <= 'z' || rr >= '0' && rr <= '9')
	}) {
		sessionID = "" // 可疑会话号直接丢弃，不影响反馈本身
	}
	// 诊断信息：只收合法 JSON 且限长；空则入库空串
	diagnostics := ""
	if len(body.Diagnostics) > 0 {
		if !json.Valid(body.Diagnostics) {
			writeErr(w, 400, "diagnostics 不是合法 JSON")
			return
		}
		if len(body.Diagnostics) > feedbackMaxDiag {
			writeErr(w, 400, "诊断信息过大")
			return
		}
		diagnostics = string(body.Diagnostics)
	}

	randBytes := make([]byte, 6)
	_, _ = rand.Read(randBytes)
	id := time.Now().Format("20060102150405") + "-" + hex.EncodeToString(randBytes)
	if err := a.Store.AddFeedback(id, kind, content, sessionID, diagnostics, appVersion,
		time.Now().Format("2006-01-02 15:04:05")); err != nil {
		writeErr(w, 500, "反馈保存失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": id})
}
