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

// ComponentWorkerDef 是组件自包含后端的声明（manifest 顶层 "workers" 数组项）：
// 主程序的组件桥（componentbridge.go）按声明拉起子进程并把端点桥到它的
// stdin/stdout——后端逻辑零主程序代码，组件可免编译安装（市场 zip 直装）。
type ComponentWorkerDef struct {
	Name   string `json:"name"`   // worker 标识（同组件内唯一；端点按它归属进程）
	Command []string `json:"command"` // argv 模板：{python}=共享便携运行时 {dir}=组件目录，其余原样
	Endpoints []ComponentWorkerEndpoint `json:"endpoints"`
}

// ComponentWorkerEndpoint 声明一条对外端点（挂在 /api/c/<组件id>/<path>）。
type ComponentWorkerEndpoint struct {
	Method string `json:"method"` // GET|POST
	Path   string `json:"path"`   // 组件内相对路径（不含 /api/c/<id>/ 前缀）
	Type   string `json:"type"`   // call=HTTP 一行一答哑管；ws=WebSocket 双向哑转发
}

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
	// Workers 可选：自包含后端（C 形态组件）。不用 "host" 字段名——市场服务端
	// 旧校验把 host 解析为 bool，嵌套对象会被误判非法 JSON；未知字段 workers
	// 静默放行，新组件包无需服务端先升级即可发布。
	Workers []ComponentWorkerDef `json:"workers,omitempty"`
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
	// 残留清理：组件目录已删除（手动删/卸载）的开关态一并剔除并回写——
	// 否则重装同名组件时旧记录会把「缺省启用」短路成停用（实测踩过：
	// input-counter 删目录重装后默认关）。清掉后重装即视为新组件。
	pruned := false
	for id := range enabled {
		if _, ok := items[id]; !ok {
			delete(enabled, id)
			pruned = true
			log.Printf("[组件] 清理残留开关态：%s（组件目录已不存在）", id)
		}
	}
	r.mu.Lock()
	r.items, r.enabled = items, enabled
	r.mu.Unlock()
	if pruned {
		if data, err := json.MarshalIndent(enabled, "", "  "); err == nil {
			_ = os.MkdirAll(filepath.Dir(r.statePath), 0755)
			_ = os.WriteFile(r.statePath, data, 0644)
		}
	}
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

// Rescan 重扫组件目录（市场安装/卸载后调用：新组件即时装载、删除的组件即时效）。
func (r *ComponentRegistry) Rescan() { r.rescan() }

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
	// 组件关闭即杀它声明的全部 worker 进程（热卸载做真；重开会懒启复活）
	a.Bridge.KillComponent(body.ID)
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
