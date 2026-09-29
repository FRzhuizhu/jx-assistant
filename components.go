package main

// 组件管理：本栈原生的轻量外围扩展（区别于 插件兼容层 的 DSH 适配）。
// 组件 = 组件/<名字>/ 下的 manifest.json + 纯浏览器半边 client.js（client.css 可选），
// 没有宿主半边、没有转译——由内嵌的 web/cx.js 运行时负责发现注入与热开关：
// 设置页切换开关时，前端即时加载/卸载组件（卸载依赖组件注册的 dispose），
// 不需要刷新页面，也不需要重启服务。

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ComponentManifest 是 组件/<name>/manifest.json 的结构。
type ComponentManifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Client      struct {
		JS  string `json:"js"`
		CSS string `json:"css"`
	} `json:"client"`
}

// ComponentStatus 是设置页展示的一条组件记录。
type ComponentStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	HasJS       bool   `json:"hasJs"`
	HasCSS      bool   `json:"hasCss"`
}

// ComponentRegistry 持有扫描结果与开关状态；全部方法并发安全。
type ComponentRegistry struct {
	mu        sync.Mutex
	dir       string // 组件/
	statePath string // data/components.json
	items     map[string]ComponentManifest
	enabled   map[string]bool
}

// InitComponents 扫描 组件/ 目录并读开关状态；目录不存在按空处理，绝不阻断启动。
func InitComponents(dir, statePath string) *ComponentRegistry {
	r := &ComponentRegistry{dir: dir, statePath: statePath, items: map[string]ComponentManifest{}, enabled: map[string]bool{}}
	r.rescan()
	return r
}

func (r *ComponentRegistry) rescan() {
	items := map[string]ComponentManifest{}
	if entries, err := os.ReadDir(r.dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			name := e.Name()
			data, err := os.ReadFile(filepath.Join(r.dir, name, "manifest.json"))
			if err != nil {
				continue
			}
			var m ComponentManifest
			if err := json.Unmarshal(data, &m); err != nil {
				log.Printf("[组件] %s/manifest.json 解析失败，已跳过: %v", name, err)
				continue
			}
			if m.ID == "" {
				m.ID = name
			}
			if m.Name == "" {
				m.Name = name
			}
			items[m.ID] = m
		}
	}
	enabled := map[string]bool{}
	if data, err := os.ReadFile(r.statePath); err == nil {
		_ = json.Unmarshal(data, &enabled) // 损坏按空处理（全部缺省启用）
	}
	r.mu.Lock()
	r.items, r.enabled = items, enabled
	r.mu.Unlock()
}

func (r *ComponentRegistry) IsEnabled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return false
	}
	on, rec := r.enabled[id]
	return !rec || on // 未记录的缺省启用
}

func (r *ComponentRegistry) Manifest(id string) (ComponentManifest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.items[id]
	return m, ok
}

// List 按名字排序返回全部组件。
func (r *ComponentRegistry) List() []ComponentStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.items))
	for id := range r.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []ComponentStatus{}
	for _, id := range ids {
		m := r.items[id]
		on, rec := r.enabled[id]
		out = append(out, ComponentStatus{
			ID: id, Name: m.Name, Version: m.Version, Description: m.Description,
			Enabled: !rec || on,
			HasJS:   m.Client.JS != "", HasCSS: m.Client.CSS != "",
		})
	}
	return out
}

// SetEnabled 切换开关并落盘（热生效由前端运行时即时应用，服务端仅记状态）。
func (r *ComponentRegistry) SetEnabled(id string, on bool) error {
	if _, ok := r.Manifest(id); !ok {
		return os.ErrNotExist
	}
	r.mu.Lock()
	r.enabled[id] = on
	r.mu.Unlock()
	data, err := json.MarshalIndent(r.enabled, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(r.statePath, data, 0644)
}

// Dir 返回组件目录（资产服务用）。
func (r *ComponentRegistry) Dir() string { return r.dir }

// ---------- HTTP 处理器 ----------

func (a *App) handleListComponents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"components": a.Components.List()})
}

func (a *App) handleSetComponent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "需要 id 与 enabled")
		return
	}
	if err := a.Components.SetEnabled(body.ID, body.Enabled); err != nil {
		writeErr(w, 404, "组件不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": 1, "components": a.Components.List()})
}

// handleComponentAsset 提供组件的浏览器半边资产（仅平级文件，防目录穿越）。
// 路径 /api/components/{id}/{file}：挂 /api/ 下让远程中继白名单天然覆盖
// （前端经 fetch 拿文本注入，本地直连、远程走隧道，同一条加载路径）。
func (a *App) handleComponentAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/components/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	id, file := parts[0], parts[1]
	if strings.Contains(file, "..") || strings.ContainsAny(file, `\/`) {
		http.NotFound(w, r)
		return
	}
	if _, ok := a.Components.Manifest(id); !ok || !a.Components.IsEnabled(id) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(a.Components.Dir(), id, file))
}
