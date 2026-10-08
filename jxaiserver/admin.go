// 市场管理页（/admin）：技能/组件/插件的上架（网页上传 zip）与下架管理，
// 供公司 IT 使用。访问控制是「知道网址才能进」——页面没有任何公开入口、
// 也不出现在配对页/客户端里；用户/角色/管理员体系（用户名登录、权限组）
// 是后续计划，接上后在此路由前加一道鉴权即可（见 README 的说明）。
//
//	GET  /admin                管理页本体（内嵌单文件，no-cache）
//	GET  /admin/api/list       当前市场清单（含版本/大小/时间/哈希）
//	POST /admin/api/publish    上传发布：multipart 表单（type + zip 文件 + 元数据），
//	                           走与 CLI publish 同一条校验/落盘链路
//	POST /admin/api/unpublish  下架 {type, id}（同 CLI unpublish）
package main

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed admin.html
var adminFS embed.FS

func (s *marketStore) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	page, err := adminFS.ReadFile("admin.html")
	if err != nil {
		http.Error(w, "admin.html "+err.Error(), 500)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

func (s *marketStore) handleAdminList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	m := s.load()
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(mustJSONIndent(m))
}

// handleAdminPublish 网页上传发布：multipart 里的 zip 先落到临时文件，再走
// marketStore.publish（与 CLI 同一条链路：类型门槛校验、打 zip、算哈希、原子落盘）。
func (s *marketStore) handleAdminPublish(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 内存缓冲 32MB，更大的直接流到临时文件
		writeErrJSON(w, 400, "表单解析失败: "+err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)
	typ := strings.TrimSpace(r.FormValue("type"))
	if !marketTypes[typ] {
		writeErrJSON(w, 400, "类型需为 skill / component / plugin")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErrJSON(w, 400, "缺少 zip 文件")
		return
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(header.Filename), ".zip") {
		writeErrJSON(w, 400, "只接受 .zip 包（技能须含 SKILL.md；组件须含 manifest.json；插件须含 .claude-plugin/plugin.json）")
		return
	}
	tmp, err := os.MkdirTemp("", "jxadm-")
	if err != nil {
		writeErrJSON(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	zipPath := filepath.Join(tmp, "upload.zip")
	out, err := os.Create(zipPath)
	if err != nil {
		writeErrJSON(w, 500, err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		writeErrJSON(w, 500, "保存上传文件失败: "+err.Error())
		return
	}
	out.Close()
	entry, err := s.publish(typ, zipPath, publishOpts{
		ID:      strings.TrimSpace(r.FormValue("id")),
		Name:    strings.TrimSpace(r.FormValue("name")),
		Version: strings.TrimSpace(r.FormValue("version")),
		Desc:    strings.TrimSpace(r.FormValue("desc")),
		Tags:    strings.TrimSpace(r.FormValue("tags")),
	})
	if err != nil {
		writeErrJSON(w, 400, err.Error())
		return
	}
	writeErrJSONOK(w, map[string]any{"entry": entry})
}

func (s *marketStore) handleAdminUnpublish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !marketTypes[body.Type] || strings.TrimSpace(body.ID) == "" {
		writeErrJSON(w, 400, "需要 type（skill/component/plugin）与 id")
		return
	}
	if err := s.unpublish(body.Type, body.ID); err != nil {
		writeErrJSON(w, 400, err.Error())
		return
	}
	writeErrJSONOK(w, map[string]any{"ok": true})
}

func writeErrJSONOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(append(mustJSONIndent(v), '\n'))
}
