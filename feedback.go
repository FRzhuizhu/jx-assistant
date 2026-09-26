// 问题反馈：客户端（本机 / 局域网 / 远程中继）一键上报问题或建议，
// 结构对齐工单：标题 + 正文 + 截图（0~4 张）。只做「接收 + 入库/存图」，
// 不广播、不提供查看接口（以后要看得查库）。
//
// 防注入与安全口径：
//   - SQL 一律参数化占位符（项目惯例，modernc.org/sqlite），无任何字符串拼接；
//   - 入库的是纯数据，正文里的引号、尖括号、代码、SQL 片段一律原样保存，
//     不解释不执行——将来展示层自行转义；
//   - 字段限长：标题 120 字、正文 4000 字；诊断信息须为合法 JSON 且限长；
//   - 图片按文件头（magic bytes）识别类型，只收 png/jpeg/gif/webp，
//     不信任扩展名与 Content-Type；每张限 10MB、每条反馈限 4 张；
//   - 图片存文件系统 data/feedback-files/<反馈id>-<序号>.<ext>，与库记录按 id 关联。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	feedbackMaxTitle   = 120              // 标题上限（字符）
	feedbackMaxContent = 4000             // 正文上限（字符）
	feedbackMaxImages  = 4                // 单条反馈图片上限
	feedbackMaxImgSize = 10 << 20         // 单张图片上限 10MB
	feedbackImgDir     = "feedback-files" // 数据目录下的图片子目录
)

// AddFeedback 落一条反馈（feedback 表，见 store.go 迁移 0012/0013）。
func (s *Store) AddFeedback(id, kind, title, content, sessionID, diagnostics, version, createdAt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO feedback
		(id, kind, title, content, session_id, diagnostics, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, kind, title, content, sessionID, diagnostics, version, createdAt)
	return err
}

// sniffImageExt 按文件头识别图片类型（不信任扩展名与 Content-Type）。
// 返回扩展名（带点）；识别不出返回空串。
func sniffImageExt(head []byte) string {
	switch {
	case len(head) >= 8 && head[0] == 0x89 && head[1] == 'P' && head[2] == 'N' && head[3] == 'G':
		return ".png"
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return ".jpg"
	case len(head) >= 4 && string(head[:4]) == "GIF8":
		return ".gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return ".webp"
	}
	return ""
}

func (a *App) feedbackImgDir() string { return filepath.Join(a.Cfg.DataDir, feedbackImgDir) }

func (a *App) handleFeedbackCreate(w http.ResponseWriter, r *http.Request) {
	// multipart：32MB 内存阈值，超出落临时文件；总上限由各字段校验兜底
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "请用 multipart 表单提交反馈")
		return
	}
	kind := r.FormValue("kind")
	if kind != "bug" && kind != "idea" {
		kind = "other"
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		writeErr(w, 400, "标题不能为空")
		return
	}
	if len([]rune(title)) > feedbackMaxTitle {
		writeErr(w, 400, "标题过长（上限 120 字）")
		return
	}
	content := strings.TrimSpace(r.FormValue("content"))
	if content == "" {
		writeErr(w, 400, "反馈内容不能为空")
		return
	}
	if len([]rune(content)) > feedbackMaxContent {
		writeErr(w, 400, "反馈内容过长（上限 4000 字）")
		return
	}
	sessionID := strings.TrimSpace(r.FormValue("sessionId"))
	if len(sessionID) > 64 || strings.ContainsFunc(sessionID, func(rr rune) bool {
		return !(rr >= 'a' && rr <= 'z' || rr >= '0' && rr <= '9')
	}) {
		sessionID = "" // 可疑会话号直接丢弃，不影响反馈本身
	}
	diagnostics := strings.TrimSpace(r.FormValue("diagnostics"))
	if len(diagnostics) > 32*1024 {
		writeErr(w, 400, "诊断信息过大")
		return
	}

	// 图片：逐张读头识别类型、限大小；任何一张不合规整条拒绝（客户端会提示重选）
	files := r.MultipartForm.File["images"]
	if len(files) > feedbackMaxImages {
		writeErr(w, 400, fmt.Sprintf("图片最多 %d 张", feedbackMaxImages))
		return
	}
	randBytes := make([]byte, 6)
	_, _ = rand.Read(randBytes)
	id := time.Now().Format("20060102150405") + "-" + hex.EncodeToString(randBytes)

	type savedImg struct{ path, ext string }
	saved := make([]savedImg, 0, len(files))
	defer func() { // 失败时清掉已落盘的图片，不留孤儿文件
		if len(saved) < len(files) {
			for _, s := range saved {
				_ = os.Remove(s.path)
			}
		}
	}()
	if len(files) > 0 {
		if err := os.MkdirAll(a.feedbackImgDir(), 0755); err != nil {
			writeErr(w, 500, "创建图片目录失败: "+err.Error())
			return
		}
	}
	for i, fh := range files {
		if fh.Size > feedbackMaxImgSize {
			writeErr(w, 400, fmt.Sprintf("第 %d 张图片超过 10MB", i+1))
			return
		}
		f, err := fh.Open()
		if err != nil {
			writeErr(w, 400, fmt.Sprintf("第 %d 张图片读取失败", i+1))
			return
		}
		head := make([]byte, 12)
		n, _ := io.ReadFull(f, head)
		ext := sniffImageExt(head[:n])
		if ext == "" {
			f.Close()
			writeErr(w, 400, fmt.Sprintf("第 %d 张图片格式不支持（仅 png/jpg/gif/webp）", i+1))
			return
		}
		rest, err := io.ReadAll(io.LimitReader(f, feedbackMaxImgSize))
		f.Close()
		if err != nil {
			writeErr(w, 400, fmt.Sprintf("第 %d 张图片读取失败", i+1))
			return
		}
		path := filepath.Join(a.feedbackImgDir(), id+"-"+strconv.Itoa(i+1)+ext)
		if err := os.WriteFile(path, append(head[:n], rest...), 0644); err != nil {
			writeErr(w, 500, "图片保存失败: "+err.Error())
			return
		}
		saved = append(saved, savedImg{path: path, ext: ext})
	}

	if err := a.Store.AddFeedback(id, kind, title, content, sessionID, diagnostics,
		appVersion, time.Now().Format("2006-01-02 15:04:05")); err != nil {
		for _, s := range saved {
			_ = os.Remove(s.path)
		}
		writeErr(w, 500, "反馈保存失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": id, "images": len(saved)})
}
