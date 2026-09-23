package main

// 插件兼容层：扫描 插件兼容层/ 下的适配器（manifest + 浏览器半边资产），
// 与编译进主程序的 Go 半边（adapter_<名字>.go 里按名注册）联查；
// 开关状态存 data/plugins.json（扁平映射，缺省启用，对齐 DSH .jx-mods.json 做法）。
// 插件/ 目录放 DSH 原件（不参与编译），与适配器按名对照：有原件无适配的记为未适配。

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

// PluginManifest 是 插件兼容层/<name>/manifest.json 的结构。
type PluginManifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Client      struct {
		JS  string `json:"js"`
		CSS string `json:"css"`
	} `json:"client"`
}

// PluginStatus 是设置页展示的一条插件记录。
type PluginStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Adapted     bool   `json:"adapted"` // false = 插件/ 有原件但尚无适配器
}

// goHalf 是适配器的 Go 半边：向路由表挂载自己的端点、给系统提示词追加段。
// 端点内部自行检查启用状态（停用时返回 404）。
type goHalf struct {
	routes func(a *App, mux *http.ServeMux)
	prompt func(a *App, sess *SessionMeta) string // 返回空串表示无附加
}

var goHalves = map[string]goHalf{} // 编译期经 registerGoHalf 注册

func registerGoHalf(id string, h goHalf) { goHalves[id] = h }

// PluginAssets 是要注入前端页面的一份适配器资产。
type PluginAssets struct {
	ID   string `json:"id"`
	JS   string `json:"js,omitempty"`   // 相对适配器目录的文件名
	CSS  string `json:"css,omitempty"`  // 相对适配器目录的文件名
}

// PluginRegistry 持有扫描结果与开关状态；全部方法并发安全。
type PluginRegistry struct {
	mu         sync.Mutex
	dir        string // 插件兼容层/
	pluginsDir string // 插件/
	statePath  string // data/plugins.json
	manifests  map[string]PluginManifest
	unadapted  []string // 插件/ 里没有适配器的名字
	enabled    map[string]bool
}

// InitPlugins 扫描目录并读开关状态；目录不存在时按空处理，绝不阻断启动。
func InitPlugins(compatDir, pluginsDir, statePath string) *PluginRegistry {
	r := &PluginRegistry{
		dir: compatDir, pluginsDir: pluginsDir, statePath: statePath,
		manifests: map[string]PluginManifest{}, enabled: map[string]bool{},
	}
	r.rescan()
	return r
}

func (r *PluginRegistry) rescan() {
	manifests := map[string]PluginManifest{}
	if entries, err := os.ReadDir(r.dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			data, err := os.ReadFile(filepath.Join(r.dir, name, "manifest.json"))
			if err != nil {
				continue
			}
			var m PluginManifest
			if err := json.Unmarshal(data, &m); err != nil {
				log.Printf("[插件] %s/manifest.json 解析失败，已跳过: %v", name, err)
				continue
			}
			if m.ID == "" {
				m.ID = name
			}
			if m.Name == "" {
				m.Name = name
			}
			manifests[m.ID] = m
		}
	}
	unadapted := []string{}
	if entries, err := os.ReadDir(r.pluginsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if _, ok := manifests[e.Name()]; !ok {
				unadapted = append(unadapted, e.Name())
			}
		}
	}
	sort.Strings(unadapted)

	enabled := map[string]bool{}
	if data, err := os.ReadFile(r.statePath); err == nil {
		_ = json.Unmarshal(data, &enabled) // 损坏时按空处理（全部缺省启用）
	}

	r.mu.Lock()
	r.manifests, r.unadapted, r.enabled = manifests, unadapted, enabled
	r.mu.Unlock()
}

// IsEnabled 报告某适配器是否启用（未记录的缺省启用）。
func (r *PluginRegistry) IsEnabled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.manifests[id]; !ok {
		return false
	}
	return r.enabled[id] || !mapHas(r.enabled, id)
}

func mapHas(m map[string]bool, k string) bool {
	_, ok := m[k]
	return ok
}

// Manifest 返回某适配器的清单。
func (r *PluginRegistry) Manifest(id string) (PluginManifest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.manifests[id]
	return m, ok
}

// List 给设置页：适配器在前（按 id 排序），未适配原件在后灰显。
func (r *PluginRegistry) List() []PluginStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.manifests))
	for id := range r.manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []PluginStatus{}
	for _, id := range ids {
		m := r.manifests[id]
		out = append(out, PluginStatus{
			ID: id, Name: m.Name, Version: m.Version, Description: m.Description,
			Enabled: r.enabled[id] || !mapHas(r.enabled, id),
			Adapted: true,
		})
	}
	for _, name := range r.unadapted {
		out = append(out, PluginStatus{ID: name, Name: name, Adapted: false, Enabled: false})
	}
	return out
}

// SetEnabled 切换开关并落盘。
func (r *PluginRegistry) SetEnabled(id string, on bool) error {
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

// Assets 返回当前启用适配器的浏览器半边资产（供注入 index.html）。
func (r *PluginRegistry) Assets() []PluginAssets {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.manifests))
	for id := range r.manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []PluginAssets{}
	for _, id := range ids {
		if r.enabled[id] || !mapHas(r.enabled, id) {
			m := r.manifests[id]
			if m.Client.JS != "" || m.Client.CSS != "" {
				out = append(out, PluginAssets{ID: id, JS: m.Client.JS, CSS: m.Client.CSS})
			}
		}
	}
	return out
}

// PromptExtra 汇总已启用适配器的系统提示词附加段。
func (r *PluginRegistry) PromptExtra(a *App, sess *SessionMeta) string {
	parts := []string{}
	for id, h := range goHalves {
		if h.prompt == nil || !r.IsEnabled(id) {
			continue
		}
		if s := h.prompt(a, sess); strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// MountRoutes 把所有 Go 半边的端点挂上路由表（端点自检启用状态）。
func (r *PluginRegistry) MountRoutes(a *App, mux *http.ServeMux) {
	for _, h := range goHalves {
		if h.routes != nil {
			h.routes(a, mux)
		}
	}
}

// Dir 返回插件兼容层目录（资产服务用）。
func (r *PluginRegistry) Dir() string { return r.dir }
