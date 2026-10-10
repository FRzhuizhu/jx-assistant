package main

// 独立 MCP 服务器的 HTTP 端点（设置页「MCP 服务器」分组的后端）。
// 路由注册在 server.go 的 Routes：/api/mcp 前缀。

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// handleListMCPServers 全部独立服务器状态（含连通与工具数）。
func (a *App) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	if a.MCPServers == nil {
		writeJSON(w, 200, map[string]any{"servers": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"servers": a.MCPServers.List()})
}

// handleUpsertMCPServer 新增/更新一条服务器定义。
func (a *App) handleUpsertMCPServer(w http.ResponseWriter, r *http.Request) {
	if a.MCPServers == nil {
		writeErr(w, 500, "MCP 注册表未初始化")
		return
	}
	var body struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Enabled     *bool             `json:"enabled"`
		Type        string            `json:"type"`
		Command     string            `json:"command"`
		Args        []string          `json:"args"`
		Env         map[string]string `json:"env"`
		URL         string            `json:"url"`
		Headers     map[string]string `json:"headers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeErr(w, 400, "需要 name")
		return
	}
	e := mcpServerEntry{
		Name: body.Name, Description: body.Description, Type: body.Type,
		Command: body.Command, Args: body.Args, Env: body.Env,
		URL: body.URL, Headers: body.Headers, Source: "manual",
	}
	if body.Enabled == nil || *body.Enabled {
		e.Enabled = true // 添加即启用（显式添加=信任该服务器）
	}
	if err := a.MCPServers.Upsert(e); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "refreshNeeded": true})
}

// handleDeleteMCPServer 删除一条服务器定义。
func (a *App) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if a.MCPServers == nil {
		writeErr(w, 500, "MCP 注册表未初始化")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeErr(w, 400, "需要 name")
		return
	}
	if err := a.MCPServers.Remove(body.Name); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "refreshNeeded": true})
}

// handleTestMCPServer 连通性测试：启动/复用连接并列出工具清单。
func (a *App) handleTestMCPServer(w http.ResponseWriter, r *http.Request) {
	if a.MCPServers == nil {
		writeErr(w, 500, "MCP 注册表未初始化")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeErr(w, 400, "需要 name")
		return
	}
	tools, err := a.MCPServers.Test(body.Name)
	if err != nil {
		writeErr(w, 400, "连接失败："+err.Error())
		return
	}
	list := []map[string]any{}
	for _, t := range tools {
		list = append(list, map[string]any{"name": t.Name, "description": t.Description})
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(tools), "tools": list})
}

// handleUploadMCPServers 拖拽导入：上传一份 MCP 配置 .json（行业三形态，
// 见 parseMCPServersDoc），全部服务器注册为连接器（来源 manual）。浏览器
// 拖拽拿不到本地路径，multipart 直传是唯一通道。
func (a *App) handleUploadMCPServers(w http.ResponseWriter, r *http.Request) {
	if a.MCPServers == nil {
		writeErr(w, 500, "MCP 注册表未初始化")
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, 400, "上传解析失败: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(header.Filename), ".json") {
		writeErr(w, 400, "只支持 .json 配置文件（{\"mcpServers\":{...}} 或 {\"名\":{...}} 形态；目录/zip 请用目录安装或市场）")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		writeErr(w, 400, "读取上传内容失败: "+err.Error())
		return
	}
	// 单服务器形态用文件名（去扩展名）当服务器名
	fallback := strings.TrimSuffix(filepath.Base(header.Filename), filepath.Ext(header.Filename))
	defs, err := parseMCPServersDoc(data, fallback)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	names, err := a.MCPServers.UpsertFromDefs(defs, "manual", "")
	if err != nil && len(names) == 0 {
		writeErr(w, 400, err.Error())
		return
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "servers": names, "partialError": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "servers": names})
}

// handleMCPCatalog 目录清单（含安装状态）。
func (a *App) handleMCPCatalog(w http.ResponseWriter, r *http.Request) {
	if a.MCPServers == nil {
		writeJSON(w, 200, map[string]any{"items": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"items": a.MCPServers.CatalogView()})
}

// handleMCPCatalogInstall 一键安装目录条目（下载+venv+pip+注册，可能要
// 数分钟；返回注册的服务器名与给用户的后续步骤提示）。
func (a *App) handleMCPCatalogInstall(w http.ResponseWriter, r *http.Request) {
	if a.MCPServers == nil {
		writeErr(w, 500, "MCP 注册表未初始化")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "需要 id")
		return
	}
	name, notes, err := a.MCPServers.InstallCatalog(body.ID)
	if err != nil {
		writeErr(w, 400, "安装失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "name": name, "notes": notes})
}
