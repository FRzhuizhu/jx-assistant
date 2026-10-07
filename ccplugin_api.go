package main

// Claude Code 插件兼容层的 HTTP 端点（设置页「Claude 插件」分区的后端）。
// 路由注册在 server.go 的 Routes：全部挂 /api/cc-plugins 前缀。

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleListCCPlugins 全部已安装插件的状态。
func (a *App) handleListCCPlugins(w http.ResponseWriter, r *http.Request) {
	if a.CCPlugins == nil {
		writeJSON(w, 200, map[string]any{"plugins": []any{}, "marketplaces": []any{}})
		return
	}
	markets := []map[string]any{}
	for id, m := range a.CCPlugins.Marketplaces() {
		markets = append(markets, map[string]any{"id": id, "name": m.Name, "source": m.Source, "url": m.URL, "addedAt": m.AddedAt})
	}
	writeJSON(w, 200, map[string]any{"plugins": a.CCPlugins.List(), "marketplaces": markets})
}

// handleSetCCPlugin 总开关与组件级开关。
func (a *App) handleSetCCPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key       string `json:"key"`
		Enabled   *bool  `json:"enabled"`
		Component string `json:"component"`
		Values    map[string]any `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeErr(w, 400, "需要 key")
		return
	}
	var err error
	switch {
	case body.Values != nil:
		err = a.CCPlugins.SetUserConfig(body.Key, body.Values)
	case body.Component != "":
		if body.Enabled == nil {
			writeErr(w, 400, "组件开关需要 enabled")
			return
		}
		err = a.CCPlugins.SetComponent(body.Key, body.Component, *body.Enabled)
	default:
		if body.Enabled == nil {
			writeErr(w, 400, "需要 enabled 或 component+enabled 或 values")
			return
		}
		err = a.CCPlugins.SetEnabled(body.Key, *body.Enabled)
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "refreshNeeded": true})
}

// handleInstallCCPlugin 安装：{path} 本地目录/zip | {url} git/zip URL |
// {marketplace, plugin} 从已添加市场装。
func (a *App) handleInstallCCPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path        string `json:"path"`
		URL         string `json:"url"`
		Marketplace string `json:"marketplace"`
		Plugin      string `json:"plugin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	var key string
	var err error
	switch {
	case strings.TrimSpace(body.Path) != "":
		key, err = a.CCPlugins.ccInstallFromPath(body.Path, "")
	case strings.TrimSpace(body.URL) != "":
		key, err = a.CCPlugins.ccInstallFromPath(body.URL, "")
	case strings.TrimSpace(body.Marketplace) != "" && strings.TrimSpace(body.Plugin) != "":
		key, err = a.CCPlugins.InstallFromMarketplace(body.Marketplace, body.Plugin)
	default:
		writeErr(w, 400, "需要 path、url 或 marketplace+plugin")
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "key": key})
}

// handleUninstallCCPlugin 卸载。
func (a *App) handleUninstallCCPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeErr(w, 400, "需要 key")
		return
	}
	if err := a.CCPlugins.Uninstall(body.Key); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleCCMarketplaceAdd 添加市场（git URL 或本地目录）。
func (a *App) handleCCMarketplaceAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL  string `json:"url"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	var id string
	var err error
	switch {
	case strings.TrimSpace(body.URL) != "":
		id, err = a.CCPlugins.AddMarketplaceFromURL(body.URL)
	case strings.TrimSpace(body.Path) != "":
		id, err = a.CCPlugins.AddMarketplaceFromPath(body.Path)
	default:
		writeErr(w, 400, "需要 url 或 path")
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": id})
}

// handleCCMarketplaceBrowse 浏览市场条目。
func (a *App) handleCCMarketplaceBrowse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, 400, "缺少市场 id")
		return
	}
	plugins, err := a.CCPlugins.MarketplaceBrowse(id)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"plugins": plugins})
}
