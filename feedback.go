// 问题反馈：客户端（本机浏览器 / 局域网 / 远程中继）把使用中遇到的问题
// 一键发回服务端；服务端落盘 data/feedback/ 并经事件流广播给所有在线客户端，
// 桌面端 toast 提示、设置「关于」页可查看与删除。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FeedbackItem 一条反馈：内容来自客户端，版本与时间为服务端落盘时补全。
type FeedbackItem struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"` // bug | idea | other
	Content     string          `json:"content"`
	SessionID   string          `json:"sessionId,omitempty"` // 反馈时正在看的会话
	Diagnostics json.RawMessage `json:"diagnostics,omitempty"`
	Version     string          `json:"version"`
	CreatedAt   string          `json:"createdAt"`
}

var feedbackMu sync.Mutex // 反馈目录的读写串行化（写入罕见，简单加锁即可）

// feedbackDir 数据目录下的反馈子目录。
func (a *App) feedbackDir() string { return filepath.Join(a.Cfg.DataDir, "feedback") }

// feedbackPath 校验 id 后返回对应文件路径（防路径穿越）。
func (a *App) feedbackPath(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("非法反馈 id")
	}
	return filepath.Join(a.feedbackDir(), id+".json"), nil
}

func (a *App) handleFeedbackCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind        string          `json:"kind"`
		Content     string          `json:"content"`
		SessionID   string          `json:"sessionId"`
		Diagnostics json.RawMessage `json:"diagnostics"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" {
		writeErr(w, 400, "反馈内容不能为空")
		return
	}
	kind := body.Kind
	if kind != "bug" && kind != "idea" {
		kind = "other"
	}
	randBytes := make([]byte, 4)
	_, _ = rand.Read(randBytes)
	item := FeedbackItem{
		ID:          time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(randBytes),
		Kind:        kind,
		Content:     content,
		SessionID:   body.SessionID,
		Diagnostics: body.Diagnostics,
		Version:     appVersion,
		CreatedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}
	feedbackMu.Lock()
	if err := os.MkdirAll(a.feedbackDir(), 0755); err != nil {
		feedbackMu.Unlock()
		writeErr(w, 500, "创建反馈目录失败: "+err.Error())
		return
	}
	raw, _ := json.MarshalIndent(item, "", "  ")
	if err := os.WriteFile(filepath.Join(a.feedbackDir(), item.ID+".json"), raw, 0644); err != nil {
		feedbackMu.Unlock()
		writeErr(w, 500, "反馈落盘失败: "+err.Error())
		return
	}
	feedbackMu.Unlock()
	log.Printf("[反馈] 收到一条%s（%s）", feedbackKindLabel(kind), item.ID)
	a.broadcast(Event{Type: "feedback", Title: item.ID, Kind: kind})
	writeJSON(w, 200, map[string]any{"ok": true, "id": item.ID})
}

func feedbackKindLabel(kind string) string {
	switch kind {
	case "bug":
		return "问题反馈"
	case "idea":
		return "功能建议"
	}
	return "反馈"
}

// handleFeedbackList 全量列出（倒序，上限 200 条——反馈是低频事件，不做分页）。
func (a *App) handleFeedbackList(w http.ResponseWriter, r *http.Request) {
	feedbackMu.Lock()
	defer feedbackMu.Unlock()
	entries, err := os.ReadDir(a.feedbackDir())
	if err != nil {
		writeJSON(w, 200, map[string]any{"items": []FeedbackItem{}})
		return
	}
	items := make([]FeedbackItem, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.feedbackDir(), e.Name()))
		if err != nil {
			continue
		}
		var item FeedbackItem
		if json.Unmarshal(raw, &item) != nil || item.ID == "" {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	if len(items) > 200 {
		items = items[:200]
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (a *App) handleFeedbackDelete(w http.ResponseWriter, r *http.Request) {
	p, err := a.feedbackPath(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	feedbackMu.Lock()
	defer feedbackMu.Unlock()
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		writeErr(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
