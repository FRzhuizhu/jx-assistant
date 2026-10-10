package main

// 独立 MCP 服务器注册表：不装整个 Claude Code 插件，直接添加一个 MCP
// 服务（stdio 命令或 http/sse 地址），像插件一样开关管理。连接复用
// ccplugin_mcp.go 的 mcpClient（宿主用 mcpPlainHost：仅 ${环境变量} 展开），
// 工具以 mcp__<服务器名>__<工具> 注册进工具目录——服务器名限定字母数字
// 连字符（不含下划线），与插件路径的 mcp__<插件>_<服务器>__<工具> 命名
// 空间天然隔离。
// 信任边界与 CC 插件的 mcp 组件一致：显式添加并启用 = 放行该服务器的
// 工具调用，不再逐工具审批。状态落 <dataDir>/mcp-servers.json；安装器
// （mcpcatalog.go）拉取的桥代码放 <dataDir>/mcp-servers/<id>/，每桥独立
// venv，互不污染。
// 本注册表同时是一个插件兼容层（层 id：mcp），统一插件页按层分组展示，
// 行内动作（删除/测试）走专用 /api/mcp（mcpservers_api.go）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// mcpServerNameRe 服务器名的合法域：字母开头，仅字母数字连字符。
// 不含下划线，避免与插件路径的 mcp__<插件>_<服务器>__<工具> 撞名。
var mcpServerNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)

// mcpServerEntry 是 mcp-servers.json 里的一条服务器定义（自描述，含开关）。
type mcpServerEntry struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	Source      string            `json:"source,omitempty"` // manual | catalog:<id>
	AddedAt     string            `json:"addedAt,omitempty"`
	Type        string            `json:"type,omitempty"` // ""（stdio）| http | sse
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// def 转成 mcpClient 用的服务器定义。
func (e *mcpServerEntry) def() ccMCPServerDef {
	return ccMCPServerDef{
		Type: e.Type, Command: e.Command, Args: e.Args,
		Env: e.Env, URL: e.URL, Headers: e.Headers,
	}
}

// mcpServersFile 是 mcp-servers.json 的整体结构。
type mcpServersFile struct {
	Version int               `json:"version"`
	Servers []*mcpServerEntry `json:"servers"`
}

// mcpServerStatus 是设置页/API 的一条服务器记录（含连通状态）。
type mcpServerStatus struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	Type        string            `json:"type"` // stdio | http | sse
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Connected   bool              `json:"connected"`
	ToolCount   int               `json:"toolCount"`
	Tools       []string          `json:"tools,omitempty"`
	LastError   string            `json:"lastError,omitempty"`
	Source      string            `json:"source,omitempty"`
	AddedAt     string            `json:"addedAt,omitempty"`
}

// MCPServersRegistry 持有独立服务器清单与活动连接；全部方法并发安全。
type MCPServersRegistry struct {
	mu        sync.Mutex
	app       *App
	root      string // <dataDir>/mcp-servers（绝对路径）
	statePath string
	file      mcpServersFile
	clients   map[string]*mcpClient // 已建连接（键：服务器名）
	lastErr   map[string]string     // 最近一次启动/调用失败原因

	installMu sync.Mutex // 目录安装器串行化（同一时间只装一个桥）
}

// InitMCPServers 读状态文件并构建客户端（不连接；连接由 StartAll/Test 触发）。
func InitMCPServers(app *App, dataDir string) *MCPServersRegistry {
	root, err := filepath.Abs(filepath.Join(dataDir, "mcp-servers"))
	if err != nil {
		root = filepath.Join(dataDir, "mcp-servers")
	}
	r := &MCPServersRegistry{
		app:       app,
		root:      root,
		statePath: filepath.Join(dataDir, "mcp-servers.json"),
		file:      mcpServersFile{Version: 1},
		clients:   map[string]*mcpClient{},
		lastErr:   map[string]string{},
	}
	if data, err := os.ReadFile(r.statePath); err == nil {
		_ = jsonUnmarshalLoose(data, &r.file)
	}
	n := 0
	for _, s := range r.file.Servers {
		if s.Enabled {
			n++
		}
	}
	log.Printf("[MCP] 独立服务器 %d 个（启用 %d）", len(r.file.Servers), n)
	return r
}

// jsonUnmarshalLoose JSON 解析的薄封装（与其它状态文件同样的损坏按空处理策略）。
func jsonUnmarshalLoose(data []byte, v any) error { return json.Unmarshal(data, v) }

// save 落盘状态（调用方持锁）。
func (r *MCPServersRegistry) save() error {
	data, err := json.MarshalIndent(&r.file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(r.statePath, data, 0644)
}

// Root 返回 mcp-servers 数据根（安装器放代码的位置）。
func (r *MCPServersRegistry) Root() string { return r.root }

// validate 校验并规范化一条定义（name 域、传输形态、必填项）。
func (e *mcpServerEntry) validate() error {
	e.Name = strings.TrimSpace(e.Name)
	e.Type = strings.TrimSpace(strings.ToLower(e.Type))
	e.Command = strings.TrimSpace(e.Command)
	e.URL = strings.TrimSpace(e.URL)
	if !mcpServerNameRe.MatchString(e.Name) {
		return fmt.Errorf("服务器名须以字母开头、仅含字母数字连字符（≤64）：%q", e.Name)
	}
	switch e.Type {
	case "", "stdio":
		e.Type = ""
		if e.Command == "" {
			return fmt.Errorf("stdio 服务器需要 command（启动命令）")
		}
	case "http", "sse":
		if !strings.HasPrefix(e.URL, "http://") && !strings.HasPrefix(e.URL, "https://") {
			return fmt.Errorf("%s 服务器需要 http(s):// 的 url", e.Type)
		}
	default:
		return fmt.Errorf("未知传输类型 %q（stdio/http/sse）", e.Type)
	}
	return nil
}

// Upsert 新增或更新一条服务器定义并重连。同名覆盖（AddedAt/Source 原值
// 优先保留），成功后立即后台连接，不阻塞返回。
func (r *MCPServersRegistry) Upsert(e mcpServerEntry) error {
	if err := e.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	for _, s := range r.file.Servers {
		if s.Name == e.Name {
			if e.AddedAt == "" {
				e.AddedAt = s.AddedAt
			}
			if e.Source == "" {
				e.Source = s.Source
			}
			break
		}
	}
	r.removeClientLocked(e.Name)
	out := r.file.Servers[:0]
	for _, s := range r.file.Servers {
		if s.Name != e.Name {
			out = append(out, s)
		}
	}
	if e.AddedAt == "" {
		e.AddedAt = time.Now().Format(time.RFC3339)
	}
	out = append(out, &e)
	r.file.Servers = out
	err := r.save()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if e.Enabled {
		r.connectAsync(e.Name)
	}
	return nil
}

// Remove 删除一条服务器定义（连接一并关闭）。
func (r *MCPServersRegistry) Remove(name string) error {
	r.mu.Lock()
	found := false
	out := r.file.Servers[:0]
	for _, s := range r.file.Servers {
		if s.Name == name {
			found = true
			continue
		}
		out = append(out, s)
	}
	r.file.Servers = out
	r.removeClientLocked(name)
	delete(r.lastErr, name)
	err := r.save()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("MCP 服务器 %s 不存在", name)
	}
	return nil
}

// SetEnabled 开关一条服务器并按需连接/断开（统一插件页总开关的落地）。
func (r *MCPServersRegistry) SetEnabled(name string, on bool) error {
	r.mu.Lock()
	var hit *mcpServerEntry
	for _, s := range r.file.Servers {
		if s.Name == name {
			s.Enabled = on
			hit = s
		}
	}
	if !on {
		r.removeClientLocked(name)
	}
	err := r.save()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if hit == nil {
		return fmt.Errorf("MCP 服务器 %s 不存在", name)
	}
	if on {
		r.connectAsync(name)
	}
	return nil
}

// removeClientLocked 关闭并丢弃一条连接（调用方持锁）。
func (r *MCPServersRegistry) removeClientLocked(name string) {
	if c, ok := r.clients[name]; ok {
		c.shutdown()
		delete(r.clients, name)
	}
}

// clientOf 取或建一条服务器的客户端（不启动）。
func (r *MCPServersRegistry) clientOf(e *mcpServerEntry) *mcpClient {
	if c, ok := r.clients[e.Name]; ok {
		return c
	}
	c := &mcpClient{fullName: e.Name, def: e.def(), host: mcpPlainHost{dir: r.root}}
	r.clients[e.Name] = c
	return c
}

// entryOf 按名取定义。
func (r *MCPServersRegistry) entryOf(name string) (*mcpServerEntry, bool) {
	for _, s := range r.file.Servers {
		if s.Name == name {
			return s, true
		}
	}
	return nil, false
}

// connectAsync 后台连接一条服务器，结果记 lastErr（不阻塞调用方）。
func (r *MCPServersRegistry) connectAsync(name string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.connectOne(ctx, name); err != nil {
			r.mu.Lock()
			r.lastErr[name] = err.Error()
			r.mu.Unlock()
			log.Printf("[MCP] %s 启动失败: %v", name, err)
		} else {
			r.mu.Lock()
			delete(r.lastErr, name)
			r.mu.Unlock()
			log.Printf("[MCP] %s 已连接", name)
		}
	}()
}

// connectOne 建立连接并完成握手（锁外执行；重入安全）。
func (r *MCPServersRegistry) connectOne(ctx context.Context, name string) error {
	r.mu.Lock()
	e, ok := r.entryOf(name)
	if !ok || !e.Enabled {
		r.mu.Unlock()
		return fmt.Errorf("MCP 服务器 %s 不存在或未启用", name)
	}
	c := r.clientOf(e)
	r.mu.Unlock()
	if err := c.start(ctx); err != nil {
		return err
	}
	return nil
}

// StartAll 后台连接全部启用的服务器（引擎启动时调用，失败记日志不阻断）。
func (r *MCPServersRegistry) StartAll() {
	r.mu.Lock()
	names := []string{}
	for _, s := range r.file.Servers {
		if s.Enabled {
			names = append(names, s.Name)
		}
	}
	r.mu.Unlock()
	for _, n := range names {
		r.connectAsync(n)
	}
}

// ShutdownAll 关闭全部连接（进程退出前调用）。
func (r *MCPServersRegistry) ShutdownAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.clients {
		r.removeClientLocked(name)
	}
}

// Test 连通性测试：连上并拉工具清单，返回工具名与描述（已连接则直接回）。
func (r *MCPServersRegistry) Test(name string) ([]mcpToolDef, error) {
	r.mu.Lock()
	e, ok := r.entryOf(name)
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("MCP 服务器 %s 不存在", name)
	}
	c := r.clientOf(e)
	started := c.started
	r.mu.Unlock()
	if !started {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := c.start(ctx); err != nil {
			r.mu.Lock()
			r.lastErr[name] = err.Error()
			r.mu.Unlock()
			return nil, err
		}
		r.mu.Lock()
		delete(r.lastErr, name)
		r.mu.Unlock()
	}
	c.mu.Lock()
	tools := append([]mcpToolDef(nil), c.tools...)
	c.mu.Unlock()
	return tools, nil
}

// List 全部服务器的状态（按名排序，含连通信息）。
func (r *MCPServersRegistry) List() []mcpServerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]mcpServerStatus, 0, len(r.file.Servers))
	for _, s := range r.file.Servers {
		st := mcpServerStatus{
			Name: s.Name, Description: s.Description, Enabled: s.Enabled,
			Type: s.Type, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers,
			Source: s.Source, AddedAt: s.AddedAt, LastError: r.lastErr[s.Name],
		}
		if st.Type == "" {
			st.Type = "stdio"
		}
		if c, ok := r.clients[s.Name]; ok {
			c.mu.Lock()
			if c.started {
				st.Connected = true
				st.ToolCount = len(c.tools)
				for _, t := range c.tools {
					st.Tools = append(st.Tools, t.Name)
				}
				st.LastError = ""
			}
			c.mu.Unlock()
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Tools 返回全部已连接服务器的工具（挂进 toolsFor 的动态目录段）。
// 工具集只在增删/开关边界变化，会话中保持稳定（前缀缓存契约）。
func (r *MCPServersRegistry) Tools() []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Tool{}
	for _, s := range r.file.Servers {
		if !s.Enabled {
			continue
		}
		c, ok := r.clients[s.Name]
		if !ok {
			continue
		}
		c.mu.Lock()
		started, tools := c.started, c.tools
		c.mu.Unlock()
		if !started {
			continue
		}
		for _, tl := range tools {
			tl := tl
			full := "mcp__" + s.Name + "__" + tl.Name
			params := tl.InputSchema
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, Tool{
				Spec: ToolDef{Type: "function", Function: ToolSpec{
					Name: full, Description: mcpStandaloneToolDesc(tl, s.Name), Parameters: params,
				}},
				Run: func(t *toolContext, args json.RawMessage) (string, error) {
					return t.app.MCPServers.Call(t.ctx, full, args)
				},
			})
		}
	}
	return out
}

// mcpStandaloneToolDesc 组装独立服务器工具描述（含来源标注）。
func mcpStandaloneToolDesc(t mcpToolDef, server string) string {
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = "MCP 工具（来自服务器 " + server + "）"
	} else {
		desc += "（MCP 工具，来自服务器 " + server + "）"
	}
	return desc
}

// Call 按完整工具名 mcp__<服务器>__<工具> 路由执行。
func (r *MCPServersRegistry) Call(ctx context.Context, full string, args json.RawMessage) (string, error) {
	rest := strings.TrimPrefix(full, "mcp__")
	i := strings.Index(rest, "__")
	if i <= 0 {
		return "", fmt.Errorf("MCP 工具名 %s 不是 mcp__<服务器>__<工具> 形态", full)
	}
	name, tool := rest[:i], rest[i+2:]
	r.mu.Lock()
	e, ok := r.entryOf(name)
	if !ok {
		r.mu.Unlock()
		return "", fmt.Errorf("MCP 服务器 %s 不存在", name)
	}
	c := r.clientOf(e)
	r.mu.Unlock()
	return c.callTool(ctx, tool, args)
}

// Count 服务器条数（层 Info 用）。
func (r *MCPServersRegistry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.file.Servers)
}

// ---------- 层实现：独立 MCP 服务器（pluginlayers.go 的契约） ----------

type mcpLayer struct{ r *MCPServersRegistry }

func (l mcpLayer) Info() LayerInfo {
	return LayerInfo{ID: "mcp", Name: "MCP 服务器", Count: l.r.Count()}
}

func (l mcpLayer) Plugins() []LayerPlugin {
	out := []LayerPlugin{}
	for _, s := range l.r.List() {
		out = append(out, LayerPlugin{
			Layer: "mcp", LayerName: "MCP 服务器",
			ID: s.Name, Name: s.Name, Description: s.Description,
			Enabled: s.Enabled, MCP: 1,
		})
	}
	return out
}

func (l mcpLayer) SetEnabled(id string, on bool) error {
	return l.r.SetEnabled(id, on)
}
