package main

// Claude Code 插件兼容层（ccplugins）：加载并管理 Anthropic Claude Code 插件
// 格式的插件目录，把各组件桥接进本引擎的对应机制——
//   skills/<名>/SKILL.md   → read_skill 按需读取 + 环境快照清单（ccplugin_skills.go）
//   commands/<名>.md       → 斜杠命令注册表 + chat 前置展开（ccplugin_commands.go）
//   agents/<名>.md         → 动态子对话专家（ccplugin_agents.go）
//   hooks/hooks.json       → settings hooks 事件桥（ccplugin_hooks.go）
//   hooks.json 的 modules  → mods JS 运行时（ccplugin_mod.go）
//   .mcp.json              → MCP 客户端工具桥（ccplugin_mcp.go）
// 插件结构（对齐 Claude Code 官方规范）：清单在 .claude-plugin/plugin.json，
// 唯一必需字段 name；skills/commands/agents 目录被同名 manifest 键声明时替换
// 默认扫描位置（skills 键是追加、其余替换）；lspServers/outputStyles/themes/
// workflows/monitors/channels 等组件本引擎忽略，加载时记日志。
//
// 目录布局（数据都在受保护的数据区内，技能工具天然够不着）：
//   <dataDir>/claude-plugins/cache/<市场>/<插件>/<版本>/   插件本体（${CLAUDE_PLUGIN_ROOT}）
//   <dataDir>/claude-plugins/data/<插件>@<市场>/           运行时数据（${CLAUDE_PLUGIN_DATA}）
//   <dataDir>/claude-plugins/marketplaces/<市场id>/        市场克隆或元数据
//   <dataDir>/claude-plugins/plugins.json                  安装记录与开关
//
// 权限策略：skills/commands/agents 静态组件随插件开关生效；hooks/mods/mcp
// 三类执行型组件缺省关闭（Claude 生态对它们不做沙箱，本引擎逐类放行）。

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ccPluginManifest 是 .claude-plugin/plugin.json 的本引擎视图。组件路径键
// 保留原始 JSON（Claude 允许字符串/数组/对象多形态），由各组件桥自行解释。
type ccPluginManifest struct {
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Author        struct {
		Name string `json:"name"`
	} `json:"author"`
	Homepage       string          `json:"homepage"`
	DefaultEnabled *bool           `json:"defaultEnabled"`
	Skills         json.RawMessage `json:"skills"`
	Commands       json.RawMessage `json:"commands"`
	Agents         json.RawMessage `json:"agents"`
	Hooks          json.RawMessage `json:"hooks"`
	MCPServers     json.RawMessage `json:"mcpServers"`
	UserConfig     map[string]ccUserConfigOpt `json:"userConfig"`
	Dependencies   json.RawMessage `json:"dependencies"`
}

// ccUserConfigOpt 是 userConfig 的一个声明（对齐 Claude 规范；sensitive 值
// 同样落 plugins.json，本引擎没有独立的安全存储）。
type ccUserConfigOpt struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Default     any      `json:"default"`
	Options     []string `json:"options"`
	Multiple    bool     `json:"multiple"`
	Sensitive   bool     `json:"sensitive"`
}

// ccComponentIDs 是组件级开关的取值域（也用作能力徽章顺序）。
var ccComponentIDs = []string{"skills", "commands", "agents", "hooks", "mods", "mcp"}

// ccExecComponents 是缺省关闭的执行型组件（静态组件随插件开关直接生效）。
var ccExecComponents = map[string]bool{"hooks": true, "mods": true, "mcp": true}

// ccPluginState 是一个已安装插件的持久状态（plugins.json 的一条）。
type ccPluginState struct {
	Marketplace       string         `json:"marketplace"` // 来源市场 id（本地安装记 "local"）
	Version           string         `json:"version"`
	Path              string         `json:"path"` // 插件根，相对 claude-plugins/ 根
	Enabled           *bool          `json:"enabled,omitempty"` // 缺省：静态组件开、执行组件看清单
	DisabledComps     map[string]bool `json:"disabledComponents,omitempty"`
	UserConfig        map[string]any `json:"userConfig,omitempty"`
}

// ccMarketplaceState 是一个已添加市场的持久状态。
type ccMarketplaceState struct {
	Source    string `json:"source"` // git | url | local
	URL       string `json:"url,omitempty"`
	LocalPath string `json:"localPath,omitempty"`
	Name      string `json:"name"`
	AddedAt   string `json:"addedAt"`
}

// ccStoreState 是 plugins.json 的整体结构。
type ccStoreState struct {
	Version     int                            `json:"version"`
	Marketplaces map[string]*ccMarketplaceState `json:"marketplaces"`
	Plugins     map[string]*ccPluginState      `json:"plugins"` // 键：<插件名>@<市场>
}

// CCPlugin 是一个加载完成的插件的运行时视图。
type CCPlugin struct {
	Key         string // <插件名>@<市场>
	Marketplace string
	Name        string // manifest.name（命名空间用）
	Root        string // 插件根目录（${CLAUDE_PLUGIN_ROOT}）
	DataDir     string // 运行时数据目录（${CLAUDE_PLUGIN_DATA}）
	Manifest    ccPluginManifest
	Skills      []ccSkillEntry
	Commands    []ccCommandEntry
	Agents      []ccAgentEntry
	HooksCfg    []ccHookRule // settings hooks（已按事件名分组前的扁平规则）
	ModModules  []string     // mods 的 JS 模块（相对 Root 路径）
	MCPDefs     map[string]mcpServerDef

	state *ccPluginState // 安装记录（Enabled/DisabledComps/UserConfig 的读写都经注册表锁）
}

// componentEnabled 报告某组件是否放行。优先级：显式组件开关
// （DisabledComps 记录 true=显式关 / false=显式开）> 显式总开关 > 缺省
// （静态组件跟 defaultEnabled，执行型组件一律关）。
func (p *CCPlugin) componentEnabled(comp string) bool {
	if v, ok := p.state.DisabledComps[comp]; ok {
		return !v
	}
	if p.state.Enabled != nil {
		return *p.state.Enabled
	}
	if ccExecComponents[comp] {
		return false
	}
	return p.Manifest.DefaultEnabled == nil || *p.Manifest.DefaultEnabled
}

// Enabled 报告插件整体是否启用（总开关；缺省尊重 defaultEnabled）。
func (p *CCPlugin) Enabled() bool {
	if p.state.Enabled != nil {
		return *p.state.Enabled
	}
	return p.Manifest.DefaultEnabled == nil || *p.Manifest.DefaultEnabled
}

// CCPluginStatus 是设置页/API 的一条插件记录。
type CCPluginStatus struct {
	Key          string            `json:"key"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"displayName"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	Author       string            `json:"author"`
	Marketplace  string            `json:"marketplace"`
	Enabled      bool              `json:"enabled"`
	Components   map[string]bool   `json:"components"` // 各组件当前生效状态
	HasSkills    int               `json:"skills"`     // 数量（徽章展示用）
	HasCommands  int               `json:"commands"`
	HasAgents    int               `json:"agents"`
	HasHooks     int               `json:"hooks"`
	HasMods      int               `json:"mods"`
	HasMCP       int               `json:"mcp"`
	UserConfig   map[string]ccUserConfigOpt `json:"userConfig,omitempty"`
	ConfigValues map[string]any    `json:"configValues,omitempty"`
	LoadError    string            `json:"loadError,omitempty"`
}

// CCPluginRegistry 持有安装记录与加载结果；全部方法并发安全。
// 重载（Reload）在安装/开关变更后调用：重新扫描组件并触发技能变更公告。
type CCPluginRegistry struct {
	mu        sync.RWMutex
	app       *App
	root      string // <dataDir>/claude-plugins
	statePath string
	state     ccStoreState
	plugins   []*CCPlugin // 加载成功的（按 Key 排序）；加载失败的只进日志与状态文件

	modMu    sync.RWMutex
	modHosts map[string]*modHost // 已装载的 mods 运行时（mods 组件放行的插件）
}

// InitCCPlugins 读安装记录并加载全部插件；目录缺失按空处理，绝不阻断启动。
func InitCCPlugins(app *App, dataDir string) *CCPluginRegistry {
	r := &CCPluginRegistry{
		app:  app,
		root: filepath.Join(dataDir, "claude-plugins"),
		statePath: filepath.Join(dataDir, "claude-plugins", "plugins.json"),
		state: ccStoreState{Version: 1, Marketplaces: map[string]*ccMarketplaceState{}, Plugins: map[string]*ccPluginState{}},
	}
	if data, err := os.ReadFile(r.statePath); err == nil {
		_ = json.Unmarshal(data, &r.state) // 损坏按空处理
	}
	if r.state.Marketplaces == nil {
		r.state.Marketplaces = map[string]*ccMarketplaceState{}
	}
	if r.state.Plugins == nil {
		r.state.Plugins = map[string]*ccPluginState{}
	}
	r.loadAll()
	n := 0
	for _, p := range r.plugins {
		if p.Enabled() {
			n++
		}
	}
	log.Printf("[CC插件] 已加载 %d 个（启用 %d）", len(r.plugins), n)
	return r
}

// loadAll 逐条加载安装记录里的插件（读清单 → 收集组件）。
func (r *CCPluginRegistry) loadAll() {
	out := []*CCPlugin{}
	keys := make([]string, 0, len(r.state.Plugins))
	for k := range r.state.Plugins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p, err := r.loadOne(key, r.state.Plugins[key])
		if err != nil {
			log.Printf("[CC插件] %s 加载失败，已跳过: %v", key, err)
			continue
		}
		out = append(out, p)
	}
	r.plugins = out
}

// loadOne 读清单并收集一个插件的全部组件。
func (r *CCPluginRegistry) loadOne(key string, st *ccPluginState) (*CCPlugin, error) {
	root := st.Path
	if !filepath.IsAbs(root) {
		root = filepath.Join(r.root, root)
	}
	// 清单位置：.claude-plugin/plugin.json（Claude 规范），兼容根目录 plugin.json
	manPath := filepath.Join(root, ".claude-plugin", "plugin.json")
	if _, err := os.Stat(manPath); err != nil {
		manPath = filepath.Join(root, "plugin.json")
	}
	data, err := os.ReadFile(manPath)
	if err != nil {
		return nil, fmt.Errorf("读清单失败: %w", err)
	}
	var m ccPluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("清单不是合法 JSON: %w", err)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("清单缺少 name 字段")
	}
	p := &CCPlugin{
		Key: key, Marketplace: st.Marketplace, Name: m.Name,
		Root: root, DataDir: filepath.Join(r.root, "data", key),
		Manifest: m, state: st,
		MCPDefs: map[string]mcpServerDef{},
	}
	if err := os.MkdirAll(p.DataDir, 0755); err != nil {
		return nil, err
	}
	p.loadSkills()
	p.loadCommands()
	p.loadAgents()
	p.loadHooks()
	p.loadMCP()
	return p, nil
}

// saveState 落盘安装记录（调用方持写锁或单线程安装路径）。
func (r *CCPluginRegistry) saveState() error {
	data, err := json.MarshalIndent(&r.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(r.statePath, data, 0644)
}

// Reload 重新加载全部插件并重启执行型组件（MCP 连接与 mods 宿主）。
// 技能/命令/专家清单都走环境快照与按需查询，清单变化由环境快照的差分机制
// 自然公告；MCP/mod 工具集在此边界变化，会话中保持稳定（前缀缓存契约）。
func (r *CCPluginRegistry) Reload() {
	r.mu.Lock()
	r.loadAll()
	r.mu.Unlock()
	r.MCPStartAll()
	r.ModsStartAll()
}

// List 给设置页：全部已安装插件的状态（按 Key 排序）。
func (r *CCPluginRegistry) List() []CCPluginStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CCPluginStatus, 0, len(r.plugins))
	for _, p := range r.plugins {
		comps := map[string]bool{}
		for _, c := range ccComponentIDs {
			comps[c] = p.componentEnabled(c) && p.hasComponent(c)
		}
		st := CCPluginStatus{
			Key: p.Key, Name: p.Name, DisplayName: p.Manifest.DisplayName, Version: p.Manifest.Version,
			Description: p.Manifest.Description, Author: p.Manifest.Author.Name,
			Marketplace: p.Marketplace, Enabled: p.Enabled(), Components: comps,
			HasSkills: len(p.Skills), HasCommands: len(p.Commands), HasAgents: len(p.Agents),
			HasHooks: len(p.HooksCfg), HasMods: len(p.ModModules), HasMCP: len(p.MCPDefs),
			UserConfig: p.Manifest.UserConfig, ConfigValues: p.state.UserConfig,
		}
		out = append(out, st)
	}
	return out
}

// hasComponent 报告插件是否实际携带某组件。
func (p *CCPlugin) hasComponent(comp string) bool {
	switch comp {
	case "skills":
		return len(p.Skills) > 0
	case "commands":
		return len(p.Commands) > 0
	case "agents":
		return len(p.Agents) > 0
	case "hooks":
		return len(p.HooksCfg) > 0
	case "mods":
		return len(p.ModModules) > 0
	case "mcp":
		return len(p.MCPDefs) > 0
	}
	return false
}

// enabled 返回全部已启用插件（组件桥的取数入口）。
func (r *CCPluginRegistry) enabled() []*CCPlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*CCPlugin{}
	for _, p := range r.plugins {
		if p.Enabled() {
			out = append(out, p)
		}
	}
	return out
}

// SetEnabled 切换总开关并落盘（显式组件停用优先于总开关）。
func (r *CCPluginRegistry) SetEnabled(key string, on bool) error {
	r.mu.Lock()
	st, ok := r.state.Plugins[key]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("插件 %s 未安装", key)
	}
	v := on
	st.Enabled = &v
	err := r.saveState()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.Reload() // 状态在锁外重载：MCP/mods 宿主同步换血
	return nil
}

// SetComponent 切换组件级开关并落盘（显式组件开关优先于总开关）。
func (r *CCPluginRegistry) SetComponent(key, comp string, on bool) error {
	if !ccComponentSet[comp] {
		return fmt.Errorf("未知组件: %s", comp)
	}
	r.mu.Lock()
	st, ok := r.state.Plugins[key]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("插件 %s 未安装", key)
	}
	if st.DisabledComps == nil {
		st.DisabledComps = map[string]bool{}
	}
	st.DisabledComps[comp] = !on
	err := r.saveState()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.Reload()
	return nil
}

// ccComponentSet 是合法组件名的集合视图。
var ccComponentSet = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range ccComponentIDs {
		m[c] = true
	}
	return m
}()

// SetUserConfig 更新一个配置值并落盘。
func (r *CCPluginRegistry) SetUserConfig(key string, values map[string]any) error {
	r.mu.Lock()
	st, ok := r.state.Plugins[key]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("插件 %s 未安装", key)
	}
	if st.UserConfig == nil {
		st.UserConfig = map[string]any{}
	}
	for k, v := range values {
		st.UserConfig[k] = v
	}
	err := r.saveState()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.loadAll() // 配置只影响变量替换，不必重启 MCP/mods
	r.mu.Unlock()
	return nil
}

// UserConfigOf 返回某插件当前的配置值（变量替换 ${user_config.x} 用）。
func (r *CCPluginRegistry) UserConfigOf(p *CCPlugin) map[string]any {
	if p.state.UserConfig != nil {
		return p.state.UserConfig
	}
	return map[string]any{}
}

// Marketplaces 返回已添加市场的清单（API 用）。
func (r *CCPluginRegistry) Marketplaces() map[string]*ccMarketplaceState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*ccMarketplaceState, len(r.state.Marketplaces))
	for k, v := range r.state.Marketplaces {
		out[k] = v
	}
	return out
}

// Root 返回 claude-plugins 数据根（安装器用）。
func (r *CCPluginRegistry) Root() string { return r.root }
