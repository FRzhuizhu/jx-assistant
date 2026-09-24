package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

const appVersion = "0.1.0"

// App 持有配置与会话存储，是所有 HTTP 处理器的宿主。
type App struct {
	Cfg        *Config
	CfgPath    string
	Store      *Store
	Plugins    *PluginRegistry
	Components *ComponentRegistry
	Todos      *todoScheduler // 待办与定时任务：组件 组件/todo-scheduler 的宿主半边（todo.go）
	static     fs.FS          // 前端静态资源：发布用内嵌副本，开发（-webdir）指向磁盘目录

	mu           sync.Mutex
	sessLocks    map[string]*sync.Mutex
	runMu        sync.Mutex
	runners      map[string]*sessionRun // 正在执行的会话（含后台派发）
	permMu       sync.Mutex
	grants       map[string]*Grant        // 授权凭证（perm.go）
	approvals    map[string]*Approval     // 等待用户决定的越权审批（perm.go）
	questions    map[string]*UserQuestion // 等待用户回答的提问/计划审批（interact.go）
	jobsMu       sync.Mutex
	jobs         map[string]*jobRun // 会话内后台作业（jobs.go）
	protectPaths []string           // 助手自身配置/数据目录，任何模式禁访问
	skillsMu     sync.Mutex
	skillsReady  bool   // 技能母本是否已释放（SkillsDir 首次解析后置位）
	skillsDirOurs string // 已解析的技能目录（App 级缓存，避免跨实例污染）
	remoteMu     sync.Mutex
	remotes      map[string]*remoteClient // SSH 远程连接池（remote.go）
	evtMu        sync.Mutex
	subscribers  map[chan Event]bool // /api/events 的订阅者
	pendMu       sync.Mutex
	pendingSteer map[string][]*PendingMsg // 执行中会话的插话（下一次调用模型前生效）
	pendingQueue map[string][]*PendingMsg // 执行中会话的排队（本轮结束后作为独立一轮发送）
	osOnce       sync.Once
	osDesc       string // 环境快照用的系统描述（Windows 版本 + PowerShell 版本，探测一次缓存）
}

func NewApp(cfg *Config, cfgPath, webDir string) (*App, error) {
	store, err := NewStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	if webDir != "" {
		if info, err := os.Stat(webDir); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("webdir 不可用（需为存在的目录）: %s", webDir)
		}
		static = os.DirFS(webDir)
	}
	plugins := InitPlugins("插件兼容层", "插件", filepath.Join(cfg.DataDir, "plugins.json"))
	status := plugins.List()
	adapted := 0
	for _, s := range status {
		if s.Adapted {
			adapted++
		}
	}
	log.Printf("[插件] 适配器 %d 个（启用 %d），未适配原件 %d 个", adapted, len(plugins.Assets()), len(status)-adapted)
	components := InitComponents("组件", filepath.Join(cfg.DataDir, "components.json"))
	nComp := 0
	for _, c := range components.List() {
		if c.Enabled {
			nComp++
		}
	}
	log.Printf("[组件] 发现 %d 个（启用 %d）", len(components.List()), nComp)
	a := &App{Cfg: cfg, CfgPath: cfgPath, Store: store, Plugins: plugins, Components: components, static: static,
		sessLocks: map[string]*sync.Mutex{},
		grants:    map[string]*Grant{}, approvals: map[string]*Approval{},
		questions: map[string]*UserQuestion{}, jobs: map[string]*jobRun{},
		remotes: map[string]*remoteClient{}}
	// 受保护路径与各运行时注册表的初始化统一在 initRuntime（见 runtime.go）
	a.initRuntime()
	a.recoverInterrupted()
	// 待办与定时任务：载入清单并起调度循环（组件关掉时整拍跳过，见 todo.go）
	a.Todos = newTodoScheduler(a)
	return a, nil
}

// sessLock 保证同一会话同一时刻只有一轮主循环在运行；
// 不同会话（含数字助理派发的子会话）互不阻塞。
func (a *App) sessLock(id string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, ok := a.sessLocks[id]
	if !ok {
		l = &sync.Mutex{}
		a.sessLocks[id] = l
	}
	return l
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.handleStatus)
	mux.HandleFunc("GET /api/search/title", a.handleSearchTitle)
	mux.HandleFunc("GET /api/search/content", a.handleSearchContent)
	mux.HandleFunc("GET /api/config", a.handleGetConfig)
	mux.HandleFunc("POST /api/config", a.handleSetConfig)
	mux.HandleFunc("GET /api/models", a.handleModels)
	mux.HandleFunc("POST /api/providers", a.handleUpsertProvider)
	mux.HandleFunc("POST /api/providers/delete", a.handleDeleteProvider)
	mux.HandleFunc("POST /api/providers/test", a.handleTestProvider)
	mux.HandleFunc("POST /api/providers/fetch", a.handleFetchModels)
	mux.HandleFunc("GET /api/sessions", a.handleListSessions)
	mux.HandleFunc("POST /api/sessions", a.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", a.handleGetSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", a.handlePatchSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", a.handleDeleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/chat", a.handleChat)
	mux.HandleFunc("GET /api/sessions/{id}/stream", a.handleSessionStream)
	mux.HandleFunc("POST /api/sessions/{id}/stop", a.handleStop)
	mux.HandleFunc("POST /api/sessions/{id}/interject", a.handleInterject)
	mux.HandleFunc("POST /api/sessions/{id}/pending", a.handlePending)
	mux.HandleFunc("POST /api/sessions/{id}/compact", a.handleCompact)
	mux.HandleFunc("GET /api/sessions/{id}/todos", a.handleGetTodos)
	mux.HandleFunc("GET /api/sessions/{id}/context", a.handleContextStats)
	mux.HandleFunc("GET /api/sessions/{id}/export", a.handleExport)
	mux.HandleFunc("POST /api/sessions/{id}/upload", a.handleUpload)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("POST /api/open-folder", a.handleOpenFolder)
	mux.HandleFunc("GET /api/pick-folder", a.handlePickFolder)
	mux.HandleFunc("POST /api/sessions/{id}/fork", a.handleFork)
	mux.HandleFunc("GET /api/approvals", a.handleListApprovals)
	mux.HandleFunc("POST /api/approvals/{id}", a.handleResolveApproval)
	mux.HandleFunc("GET /api/questions", a.handleListQuestions)
	mux.HandleFunc("POST /api/questions/{id}", a.handleResolveQuestion)
	mux.HandleFunc("POST /api/workspace/test", a.handleTestWorkspace)
	mux.HandleFunc("POST /api/workspace/ls", a.handleWorkspaceLs)
	mux.HandleFunc("GET /attachments/", a.handleAttachment)
	mux.HandleFunc("GET /api/skills", a.handleListSkills)
	mux.HandleFunc("POST /api/skills/add", a.handleAddSkill)
	mux.HandleFunc("POST /api/skills/toggle", a.handleToggleSkill)
	mux.HandleFunc("POST /api/skills/delete", a.handleDeleteSkill)
	mux.HandleFunc("POST /api/sessions/{id}/remote", a.handleSetSessionRemote)
	mux.HandleFunc("GET /api/plugins", a.handleListPlugins)
	mux.HandleFunc("POST /api/plugins", a.handleSetPlugin)
	mux.Handle("GET /_plugins/", http.HandlerFunc(a.handlePluginAsset))
	mux.HandleFunc("GET /api/components", a.handleListComponents)
	mux.HandleFunc("POST /api/components", a.handleSetComponent)
	mux.Handle("GET /_components/", http.HandlerFunc(a.handleComponentAsset))
	// 待办与定时任务（组件 todo-scheduler 的宿主半边）：组件未启用时这些接口一律 404
	mux.HandleFunc("GET /api/todos", a.handleTodoList)
	mux.HandleFunc("POST /api/todos", a.handleTodoCreate)
	mux.HandleFunc("POST /api/todos/update", a.handleTodoUpdate)
	mux.HandleFunc("POST /api/todos/delete", a.handleTodoDelete)
	mux.HandleFunc("POST /api/todos/run", a.handleTodoRun)
	mux.HandleFunc("POST /api/todos/missed", a.handleTodoMissed)
	mux.HandleFunc("GET /api/todos/workspaces", a.handleTodoWorkspaces)
	a.Plugins.MountRoutes(a, mux)
	mux.HandleFunc("GET /", a.handleStatic)
	return mux
}

// handleStatic 提供前端静态资源；入口页在返回前把已启用插件的
// 浏览器半边资产（css/js）注入 </body> 前。
func (a *App) handleStatic(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" || p == "index.html" {
		data, err := fs.ReadFile(a.static, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		html := string(data)
		if a.Plugins != nil {
			assets := a.Plugins.Assets()
			tags := []string{}
			for _, pl := range assets {
				if pl.CSS != "" {
					tags = append(tags, `<link rel="stylesheet" href="/_plugins/`+pl.ID+`/`+pl.CSS+`">`)
				}
			}
			for _, pl := range assets {
				if pl.JS != "" {
					tags = append(tags, `<script src="/_plugins/`+pl.ID+`/`+pl.JS+`" defer></script>`)
				}
			}
			if len(tags) > 0 && strings.Contains(html, "</body>") {
				html = strings.Replace(html, "</body>", strings.Join(tags, "\n")+"</body>", 1)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(html))
		return
	}
	// 静态子资源（app.js/style.css 等）一律 no-cache：embed FS 不带修改时间戳，
	// 浏览器会对无验证器的响应做启发式缓存，出现"前端改了、刷新不生效"；
	// 强制每次回源校验，本地服务开销可忽略
	w.Header().Set("Cache-Control", "no-cache")
	http.FileServer(http.FS(a.static)).ServeHTTP(w, r)
}

// ---------- 插件兼容层：清单、开关与资产 ----------

func (a *App) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"plugins": a.Plugins.List()})
}

func (a *App) handleSetPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "需要 id 与 enabled")
		return
	}
	if _, ok := a.Plugins.Manifest(body.ID); !ok {
		writeErr(w, 404, "插件不存在或未适配")
		return
	}
	if err := a.Plugins.SetEnabled(body.ID, body.Enabled); err != nil {
		writeErr(w, 500, "保存开关失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": 1, "refreshNeeded": true, "plugins": a.Plugins.List()})
}

// handlePluginAsset 提供适配器浏览器半边资产（仅平级文件，防目录穿越）。
func (a *App) handlePluginAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/_plugins/")
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
	m, ok := a.Plugins.Manifest(id)
	if !ok || !a.Plugins.IsEnabled(id) {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(a.Plugins.Dir(), id, file)
	if full != filepath.Join(filepath.Clean(full)) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, full)
	_ = m
}

// withCORS 允许前端独立部署时跨域调用后端接口，同时输出简要访问日志。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s from %s (%s)", r.Method, r.URL.Path, r.RemoteAddr, r.Header.Get("User-Agent"))
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

// likePattern 把用户输入转成逐字（字面）匹配的 LIKE 模式：
// % _ \ 全部转义，保证搜索词不会被当成通配符。
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// 可搜索的会话范围基座 = 侧栏可见范围：非助理主对话、非子会话。
// 归档与目录过滤由 searchScopeWhere 按 bin/ws 参数装配（都缺省时等价旧行为 archived = 0）。
const searchableSessionsBaseSQL = `mode != 'assistant' AND parent_id = ''`

// searchScopeWhere 按请求参数组装会话范围条件（pfx 为列名前缀，如 "s."）：
//   bin=1 → 回收站：已归档，或其工作目录在回收列表 ws 里；
//   否则  → 正常工作区：未归档，且工作目录不在回收列表 ws 里。
//
// ws 为回收目录列表（JSON 数组，空串代表默认工作区）；keep 为已单独恢复的
// 会话 id 列表：正常侧豁免目录回收（照常可搜），回收站侧则排除（已不属于
// 回收站）。参数缺省或为空时按旧行为处理，两边搜索互不越界。
func searchScopeWhere(r *http.Request, pfx string) (string, []any) {
	bin := r.URL.Query().Get("bin") == "1"
	cond := pfx + "archived = 0"
	if bin {
		cond = pfx + "archived = 1"
	}
	var args []any
	readList := func(key string) []string {
		raw := r.URL.Query().Get(key)
		if raw == "" {
			return nil
		}
		var out []string
		if json.Unmarshal([]byte(raw), &out) != nil {
			return nil
		}
		return out
	}
	placeholders := func(vals []string) string {
		ph := make([]string, len(vals))
		for i, v := range vals {
			ph[i] = "?"
			args = append(args, v)
		}
		return strings.Join(ph, ",")
	}
	wss, keeps := readList("ws"), readList("keep")
	if bin {
		if len(wss) > 0 {
			cond = "(" + pfx + "archived = 1 OR " + pfx + "workspace IN (" + placeholders(wss) + "))"
		}
		if len(keeps) > 0 {
			cond += " AND " + pfx + "id NOT IN (" + placeholders(keeps) + ")"
		}
	} else if len(wss) > 0 {
		inner := pfx + "workspace NOT IN (" + placeholders(wss) + ")"
		if len(keeps) > 0 {
			inner += " OR " + pfx + "id IN (" + placeholders(keeps) + ")"
		}
		cond += " AND (" + inner + ")"
	}
	return cond, args
}

// handleSearchTitle 按标题逐字搜索工作任务。
func (a *App) handleSearchTitle(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 200, map[string]any{"hits": []any{}})
		return
	}
	scope, scopeArgs := searchScopeWhere(r, "")
	rows, err := a.Store.db.Query(
		`SELECT id, title, updated_at FROM sessions
		 WHERE `+searchableSessionsBaseSQL+` AND `+scope+` AND title LIKE ? ESCAPE '\'
		 ORDER BY updated_at DESC LIMIT 50`,
		append(scopeArgs, likePattern(q))...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	hits := []map[string]any{}
	for rows.Next() {
		var id, title, updated string
		if err := rows.Scan(&id, &title, &updated); err != nil {
			continue
		}
		hits = append(hits, map[string]any{"sessionId": id, "title": title, "updatedAt": updated})
	}
	writeJSON(w, 200, map[string]any{"hits": hits})
}

// handleSearchContent 按聊天记录逐字搜索：只搜用户消息与模型最终输出
// （无工具调用的 assistant 消息），忽略思考（本就不入库）、工具调用与
// 系统注入行。每条命中返回定位用 seq 与命中点附近的片段。
func (a *App) handleSearchContent(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 200, map[string]any{"hits": []any{}})
		return
	}
	scope, scopeArgs := searchScopeWhere(r, "s.")
	rows, err := a.Store.db.Query(
		`SELECT m.session_id, s.title, m.seq, m.role, m.content, m.created_at
		 FROM messages m JOIN sessions s ON s.id = m.session_id
		 WHERE s.`+searchableSessionsBaseSQL+` AND `+scope+`
		   AND m.kind = 'chat' AND m.content IS NOT NULL
		   AND (m.role = 'user'
		        OR (m.role = 'assistant' AND (m.tool_calls IS NULL OR m.tool_calls = '')))
		   AND m.content LIKE ? ESCAPE '\'
		 ORDER BY s.updated_at DESC, m.seq ASC LIMIT 200`,
		append(scopeArgs, likePattern(q))...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	hits := []map[string]any{}
	for rows.Next() {
		var sid, title, role, content, created string
		var seq int64
		if err := rows.Scan(&sid, &title, &seq, &role, &content, &created); err != nil {
			continue
		}
		hits = append(hits, map[string]any{
			"sessionId": sid, "title": title, "seq": seq,
			"role": role, "snippet": searchSnippet(content, q), "createdAt": created,
		})
	}
	writeJSON(w, 200, map[string]any{"hits": hits})
}

// searchSnippet 截取首个命中点前后各约 40 字的片段（前端负责高亮）。
func searchSnippet(content, q string) string {
	const radius = 40
	lowC, lowQ := strings.ToLower(content), strings.ToLower(q)
	at := strings.Index(lowC, lowQ)
	if at < 0 {
		at = 0
	}
	runes := []rune(content)
	atRune := len([]rune(content[:at]))
	lo := atRune - radius
	if lo < 0 {
		lo = 0
	}
	hi := atRune + len([]rune(q)) + radius
	if hi > len(runes) {
		hi = len(runes)
	}
	pre, suf := "", ""
	if lo > 0 {
		pre = "…"
	}
	if hi < len(runes) {
		suf = "…"
	}
	return pre + string(runes[lo:hi]) + suf
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	t := a.DefaultTarget()
	writeJSON(w, 200, map[string]any{
		"name":       "坚祥数字助理",
		"version":    appVersion,
		"model":      t.Model,
		"hasKey":     t.APIKey != "",
		"mock":       t.BaseURL == "mock",
		"workspace":  a.Cfg.Workspace,
		"allowShell": a.Cfg.AllowShell,
	})
}

// DefaultProviderID 返回全局默认提供方 id（providers[0]）。
func (a *App) DefaultProviderID() string {
	if len(a.Cfg.Providers) > 0 {
		return a.Cfg.Providers[0].ID
	}
	return ""
}

// handleModels 返回模型选择器用的扁平清单。
func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	type modelItem struct {
		ProviderID      string   `json:"providerId"`
		ProviderName    string   `json:"providerName"`
		ModelID         string   `json:"modelId"`
		ModelName       string   `json:"modelName,omitempty"`
		ContextWindow   int      `json:"contextWindow,omitempty"`
		ReasoningLevels []string `json:"reasoningLevels,omitempty"`
	}
	items := []modelItem{}
	for _, p := range a.Cfg.Providers {
		for _, m := range p.Models {
			levels := m.ReasoningLevels
			if len(levels) == 0 { // 老配置未存档位：按 ID 现场推断
				levels = guessReasoningLevels(m.ID)
			}
			items = append(items, modelItem{p.ID, p.Name, m.ID, m.Name, m.ContextWindow, levels})
		}
	}
	t := a.DefaultTarget()
	writeJSON(w, 200, map[string]any{
		"models":          items,
		"defaultProvider": a.DefaultProviderID(),
		"defaultModel":    t.Model,
	})
}

// resolveProviderDraft 把请求里的 id/baseURL/apiKey 折算成一个可用的提供方：
// 没存过的 id（或新增卡片）直接用请求里带来的地址与密钥，便于未保存先拉取/测试。
func (a *App) resolveProviderDraft(id, baseURL, apiKey string) (*ProviderCfg, error) {
	eff := &ProviderCfg{}
	if p := a.providerByID(strings.TrimSpace(id)); p != nil {
		*eff = *p
	}
	if u := strings.TrimSpace(baseURL); u != "" {
		eff.BaseURL = u
	}
	if k := strings.TrimSpace(apiKey); k != "" {
		eff.APIKey = k
	}
	if strings.TrimSpace(eff.BaseURL) == "" {
		return nil, fmt.Errorf("缺少 API 地址")
	}
	return eff, nil
}

// handleFetchModels 从提供方服务拉取可用模型清单（服务端代理，避开浏览器跨域）。
func (a *App) handleFetchModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	p, err := a.resolveProviderDraft(body.ID, body.BaseURL, body.APIKey)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	models, err := a.FetchModels(p)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// handleUpsertProvider 新增或更新单个提供方（模型页卡片「保存」）。
func (a *App) handleUpsertProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string     `json:"id"`
		Name    string     `json:"name"`
		BaseURL string     `json:"baseURL"`
		APIKey  string     `json:"apiKey"` // 空串 = 沿用已保存的 key
		Models  []ModelCfg `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	id := strings.TrimSpace(in.ID)
	preset, isPreset := PresetByID(id)
	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL == "" {
		if !isPreset {
			writeErr(w, 400, "自定义提供方必须填写 API 地址")
			return
		}
		baseURL = preset.BaseURL
	}
	if id == "" {
		id = fmt.Sprintf("custom-%d", time.Now().UnixMilli())
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		if isPreset {
			name = preset.Name
		} else {
			writeErr(w, 400, "名称不能为空")
			return
		}
	}
	models := []ModelCfg{}
	seen := map[string]bool{}
	for _, m := range in.Models {
		mid := strings.TrimSpace(m.ID)
		if mid == "" || seen[mid] {
			continue
		}
		seen[mid] = true
		m.ID = mid
		// 思考档位未配置时按 ID 自动推断（添加/编辑模型即自动带上）
		if len(m.ReasoningLevels) == 0 {
			m.ReasoningLevels = guessReasoningLevels(mid)
		}
		models = append(models, m)
	}
	pc := ProviderCfg{ID: id, Name: name, BaseURL: baseURL, Models: models}
	if key := strings.TrimSpace(in.APIKey); key != "" {
		pc.APIKey = key
	} else if old := a.providerByID(id); old != nil {
		pc.APIKey = old.APIKey
	}
	replaced := false
	for i := range a.Cfg.Providers {
		if a.Cfg.Providers[i].ID == id {
			a.Cfg.Providers[i] = pc
			replaced = true
			break
		}
	}
	if !replaced {
		a.Cfg.Providers = append(a.Cfg.Providers, pc)
	}
	a.Cfg.syncLegacyFields()
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		writeErr(w, 500, "保存配置失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": 1, "id": id})
}

// handleDeleteProvider 删除单个提供方（模型页卡片「删除提供方」）。
func (a *App) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.ID) == "" {
		writeErr(w, 400, "需要 provider id")
		return
	}
	id := strings.TrimSpace(in.ID)
	kept := make([]ProviderCfg, 0, len(a.Cfg.Providers))
	for _, p := range a.Cfg.Providers {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	a.Cfg.Providers = kept
	a.Cfg.syncLegacyFields()
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		writeErr(w, 500, "保存配置失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": 1})
}

// handleTestProvider 对提供方目录里的所有模型做连通性测试（极小请求，限并发）。
func (a *App) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string   `json:"id"`
		BaseURL string   `json:"baseURL"`
		APIKey  string   `json:"apiKey"`
		Models  []string `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	p, err := a.resolveProviderDraft(in.ID, in.BaseURL, in.APIKey)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"results": TestProviderModels(p, in.Models)})
}

func (a *App) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	type providerView struct {
		ID           string     `json:"id"`
		Name         string     `json:"name"`
		BaseURL      string     `json:"baseURL"`
		APIKeyMasked string     `json:"apiKeyMasked"`
		Preset       bool       `json:"preset"`
		PresetURL    string     `json:"presetURL,omitempty"`
		Models       []ModelCfg `json:"models"`
	}
	providers := []providerView{}
	for _, p := range a.Cfg.Providers {
		pv := providerView{
			ID: p.ID, Name: p.Name, BaseURL: p.BaseURL,
			APIKeyMasked: maskKey(p.APIKey), Models: p.Models,
		}
		if tpl, ok := PresetByID(p.ID); ok {
			pv.Preset = true
			pv.PresetURL = tpl.BaseURL
		}
		providers = append(providers, pv)
	}
	type presetView struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		BaseURL string `json:"baseURL"`
	}
	presets := []presetView{}
	for _, p := range DefaultProviders() {
		presets = append(presets, presetView{ID: p.ID, Name: p.Name, BaseURL: p.BaseURL})
	}
	t := a.DefaultTarget()
	writeJSON(w, 200, map[string]any{
		"baseURL":          t.BaseURL,
		"apiKeyMasked":     maskKey(t.APIKey),
		"model":            t.Model,
		"providers":        providers,
		"presets":          presets,
		"workspace":        a.Cfg.Workspace,
		"allowShell":       a.Cfg.AllowShell,
		"bindHost":         a.Cfg.BindHost,
		"maxSteps":         a.Cfg.MaxSteps,
		"compactMode":      a.Cfg.CompactMode,
		"keepRounds":       a.Cfg.KeepRounds,
		"ruleInterval":     a.Cfg.RuleInterval,
		"compactThreshold": a.Cfg.CompactThreshold,
	})
}

func (a *App) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL    *string `json:"baseURL"`
		APIKey     *string `json:"apiKey"` // 空串表示保持不变
		Model      *string `json:"model"`
		Workspace  *string `json:"workspace"`
		BindHost   *string `json:"bindHost"`
		AllowShell *bool   `json:"allowShell"`
		MaxSteps   *int    `json:"maxSteps"`
		Compact    *struct {
			Mode         string  `json:"mode"`
			KeepRounds   *int    `json:"keepRounds"`
			RuleInterval *int    `json:"ruleInterval"`
			Threshold    float64 `json:"threshold"`
		} `json:"compact"`
		Providers *[]struct {
			ID      string     `json:"id"`
			Name    string     `json:"name"`
			BaseURL string     `json:"baseURL"`
			APIKey  string     `json:"apiKey"` // 空串表示沿用已保存的 key
			Models  []ModelCfg `json:"models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	if body.BaseURL != nil {
		a.Cfg.BaseURL = strings.TrimSpace(*body.BaseURL)
	}
	if body.APIKey != nil && *body.APIKey != "" {
		a.Cfg.APIKey = strings.TrimSpace(*body.APIKey)
	}
	if body.Model != nil {
		a.Cfg.Model = strings.TrimSpace(*body.Model)
	}
	if body.Providers != nil {
		merged := []ProviderCfg{}
		for _, in := range *body.Providers {
			pc := ProviderCfg{
				ID:      strings.TrimSpace(in.ID),
				Name:    strings.TrimSpace(in.Name),
				BaseURL: strings.TrimSpace(in.BaseURL),
				Models:  in.Models,
			}
			if pc.ID == "" || pc.BaseURL == "" {
				continue
			}
			if in.APIKey != "" {
				pc.APIKey = strings.TrimSpace(in.APIKey)
			} else if old := a.providerByID(in.ID); old != nil {
				pc.APIKey = old.APIKey // 前端回传空 key = 保持不变
			}
			merged = append(merged, pc)
		}
		if len(merged) > 0 {
			a.Cfg.Providers = merged
		}
	}
	if body.Workspace != nil {
		a.Cfg.Workspace = strings.TrimSpace(*body.Workspace)
	}
	if body.BindHost != nil {
		a.Cfg.BindHost = strings.TrimSpace(*body.BindHost)
	}
	if body.AllowShell != nil {
		a.Cfg.AllowShell = *body.AllowShell
	}
	if body.MaxSteps != nil && *body.MaxSteps > 0 {
		a.Cfg.MaxSteps = *body.MaxSteps
	}
	if body.Compact != nil {
		if body.Compact.Mode != "" {
			a.Cfg.CompactMode = body.Compact.Mode
		}
		if body.Compact.KeepRounds != nil && *body.Compact.KeepRounds > 0 {
			a.Cfg.KeepRounds = *body.Compact.KeepRounds
		}
		if body.Compact.RuleInterval != nil && *body.Compact.RuleInterval > 0 {
			a.Cfg.RuleInterval = *body.Compact.RuleInterval
		}
		if body.Compact.Threshold > 0.5 && body.Compact.Threshold < 1 {
			a.Cfg.CompactThreshold = body.Compact.Threshold
		}
	}
	a.Cfg.syncLegacyFields()
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		writeErr(w, 500, "保存配置失败: "+err.Error())
		return
	}
	a.handleGetConfig(w, r)
}

func (a *App) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if parent := r.URL.Query().Get("parent"); parent != "" {
		list, err := a.Store.Children(parent)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		for _, m := range list {
			if a.IsRunning(m.ID) {
				m.Status = "running"
			}
		}
		writeJSON(w, 200, map[string]any{"sessions": list})
		return
	}
	list, err := a.Store.List()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 执行状态以内存注册表为准：数据库那列只用于服务重启后的中断恢复
	for _, m := range list {
		if m.Status != "running" && a.IsRunning(m.ID) {
			m.Status = "running"
		}
	}
	writeJSON(w, 200, map[string]any{"sessions": list})
}

func (a *App) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode           string           `json:"mode"`
		Workspace      string           `json:"workspace"`
		Title          string           `json:"title"`
		ParentID       string           `json:"parent"`
		Provider       string           `json:"provider"`
		Model          string           `json:"model"`
		Permission     string           `json:"permission"`
		Thinking       string           `json:"thinking"`
		PlanMode       bool             `json:"planMode"`
		GoalMode       bool             `json:"goalMode"`
		GoalText       string           `json:"goalText"`
		Tools          []string         `json:"tools"`
		Remote         *RemoteWorkspace `json:"remote"`
		InheritContext bool             `json:"inheritContext"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // 允许空请求体
	if body.Mode != "assistant" && body.Mode != "aux" && body.Mode != "office" {
		body.Mode = "task"
	}
	if body.Permission == "" {
		body.Permission = "workspace-write"
	}
	if body.Remote != nil {
		spec, err := body.Remote.normalize()
		if err != nil {
			writeErr(w, 400, "远程工作区配置无效: "+err.Error())
			return
		}
		body.Remote = spec
	}
	// 父会话校验：只能挂在一级会话下。子对话不能再建子对话（与 dispatch_task 的
	// 运行时守卫一起，保证只有一级）。
	var parent *SessionMeta
	if pid := strings.TrimSpace(body.ParentID); pid != "" {
		p, err := a.Store.Get(pid)
		if err != nil {
			writeErr(w, 400, "父会话不存在："+pid)
			return
		}
		if p.ParentID != "" {
			writeErr(w, 400, "子对话不能再建子对话："+pid+" 已经是子对话（只允许一级）")
			return
		}
		parent = p
	}
	m := &SessionMeta{
		ID:         newID(),
		Title:      strings.TrimSpace(body.Title),
		Mode:       body.Mode,
		Workspace:  strings.TrimSpace(body.Workspace),
		Remote:     body.Remote,
		ParentID:   strings.TrimSpace(body.ParentID),
		Provider:   strings.TrimSpace(body.Provider),
		Model:      strings.TrimSpace(body.Model),
		Permission: body.Permission,
		Thinking:   strings.TrimSpace(body.Thinking),
		PlanMode:   body.PlanMode,
		GoalMode:   body.GoalMode,
		GoalText:   truncateRunes(body.GoalText, 2000),
		Tools:      body.Tools,
		CreatedAt:  now(),
		UpdatedAt:  now(),
	}
	// 人工建的子对话＝父对话的分支：模式/工作区/远程/模型/权限/能力全部照抄父会话。
	// 这样系统提示词与工具目录与父完全一致——继承来的历史才能接在同一个前缀上，
	// 命中模型的 KV 前缀缓存（工具目录或系统提示词一旦不同，后面整段都要重算）。
	// 计划/目标模式刻意重置：子对话不该替父会话自动续跑。
	if body.InheritContext {
		if parent == nil {
			writeErr(w, 400, "inheritContext 需要同时指定 parent")
			return
		}
		m.Mode = parent.Mode
		m.Workspace = parent.Workspace
		m.Provider = parent.Provider
		m.Model = parent.Model
		m.Permission = parent.Permission
		m.Thinking = parent.Thinking
		m.Tools = parent.Tools
		if parent.Remote != nil {
			r := *parent.Remote
			m.Remote = &r
		}
		m.Origin = "human"
		if m.Title == "" {
			m.Title = "子对话"
		}
	}
	if permModeOf(m) == permFull {
		m.Tools = bundleFullAccessCaps(m.Tools) // 完全访问：预设捆绑能力一并点亮
	}
	if m.GoalMode {
		m.GoalStatus = "active" // 创建即带目标模式：状态从 active 起步
	}
	if err := a.Store.Create(m); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if body.InheritContext {
		if _, err := a.copyInheritedContext(parent.ID, m.ID); err != nil {
			// 上下文没复制成功就退回：留着半截继承的子对话比没有更糟
			_ = a.Store.Delete(m.ID)
			writeErr(w, 500, "继承父对话上下文失败: "+err.Error())
			return
		}
	}
	writeJSON(w, 200, m)
}

func (a *App) sessionFromPath(w http.ResponseWriter, r *http.Request) *SessionMeta {
	m, err := a.Store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "会话不存在")
		return nil
	}
	return m
}

func (a *App) handleGetSession(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	msgs, err := a.Store.MessagesWithSeq(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// running 与 pending 以内存运行时为准：刷新页面后据此自动接上执行流、
	// 恢复「已插话/已排队」的显示，不依赖刷新前的页面状态
	writeJSON(w, 200, map[string]any{
		"meta": m, "messages": msgs,
		"running": a.IsRunning(m.ID), "pending": a.pendingOf(m.ID),
	})
}

func (a *App) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		Title      *string   `json:"title"`
		Workspace  *string   `json:"workspace"`
		Provider   *string   `json:"provider"`
		Model      *string   `json:"model"`
		Permission *string   `json:"permission"`
		Thinking   *string   `json:"thinking"`
		PlanMode   *bool     `json:"planMode"`
		GoalMode   *bool     `json:"goalMode"`
		GoalText   *string   `json:"goalText"`
		Tools      *[]string `json:"tools"`
		Archived   *bool     `json:"archived"`
		Unread     *bool     `json:"unread"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	if body.Title != nil {
		m.Title = truncateRunes(*body.Title, 40)
	}
	if body.Workspace != nil {
		m.Workspace = strings.TrimSpace(*body.Workspace)
	}
	if body.Provider != nil {
		m.Provider = strings.TrimSpace(*body.Provider)
	}
	if body.Model != nil {
		m.Model = strings.TrimSpace(*body.Model)
	}
	if body.Permission != nil {
		was := permModeOf(m)
		m.Permission = strings.TrimSpace(*body.Permission)
		switch {
		case permModeOf(m) == permFull:
			m.Tools = bundleFullAccessCaps(m.Tools) // 切到完全访问：捆绑能力联动点亮
		case was == permFull && body.Tools == nil:
			m.Tools = stripFullAccessCaps(m.Tools) // 从完全访问切回：捆绑能力随之取消
		}
	}
	if body.Thinking != nil {
		switch t := strings.TrimSpace(*body.Thinking); t {
		case "", "enabled", "disabled", "low", "medium", "high":
			m.Thinking = t
		default:
			writeErr(w, 400, "thinking 档位必须是 enabled/disabled/low/medium/high 或空")
			return
		}
	}
	if body.PlanMode != nil {
		m.PlanMode = *body.PlanMode
	}
	if body.GoalText != nil {
		m.GoalText = truncateRunes(*body.GoalText, 2000)
	}
	if body.GoalMode != nil {
		m.GoalMode = *body.GoalMode
		// 开启/重开目标模式：状态归位 active；关闭时清状态（目标文本保留备查）
		if m.GoalMode {
			m.GoalStatus = "active"
		} else {
			m.GoalStatus = ""
		}
	}
	if body.Archived != nil {
		m.Archived = *body.Archived
	}
	if body.Unread != nil {
		m.Unread = *body.Unread
	}
	if body.Tools != nil {
		m.Tools = *body.Tools
	}
	if err := a.Store.SaveMeta(m); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}

func (a *App) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.closeRemoteSession(id) // 断开该会话的 SSH 连接
	if err := a.Store.Delete(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleCompact 手动压缩：把 [最后摘要边界, 保留轮数之前] 的区间压成一条
// 手动摘要；已有摘要永不重压，多次手动压缩叠加。
func (a *App) handleCompact(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	if a.Cfg.CompactMode == "off" {
		writeErr(w, 409, "上下文压缩已在设置中关闭")
		return
	}
	if a.IsRunning(m.ID) {
		writeErr(w, 409, "该会话正在执行，稍后再压缩")
		return
	}
	summary, err := a.doSummarize(m, "manual", a.Cfg.KeepRounds)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
	writeJSON(w, 200, map[string]any{"ok": 1, "summary": truncateRunes(summary, 300)})
}

// handleExport 导出会话日志。?format=md（默认）出人读的完整 Markdown，
// ?format=json 出机器可存的原始记录；两者都含全部子会话（递归），
// 且都取自 messages 表原始历史——与发给模型的压缩视图无关。
func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	root, err := a.buildExportTree(m.ID, map[string]bool{})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	stamp := time.Now().Format("20060102-150405")
	base := strings.ReplaceAll(truncateRunes(m.Title, 30), " ", "_") + "_" + stamp
	if r.URL.Query().Get("format") == "json" {
		data, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			writeErr(w, 500, "导出序列化失败: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(base+".json"))
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(base+".md"))
	_, _ = w.Write([]byte(a.ExportMarkdown(root)))
}

func wsOr(a *App, m *SessionMeta) string {
	if m.Workspace != "" {
		return m.Workspace
	}
	return a.Cfg.Workspace
}

// handleUpload 接收文件上传：保存进会话工作区，并注入一条上传记录消息
// （模型在后续上下文里能看到，可用只读工具查看文件内容）。
func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "上传解析失败: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer file.Close()
	name := filepath.Base(header.Filename)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/`) {
		writeErr(w, 400, "非法文件名")
		return
	}
	tctx := &toolContext{app: a, sess: m}
	ws, err := tctx.resolve("")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var dst string
	if m.Remote != nil {
		dst = path.Join(ws, name)
	} else {
		dst = filepath.Join(ws, name)
	}
	data, err := io.ReadAll(io.LimitReader(file, 32<<20))
	if err != nil {
		writeErr(w, 500, "读取上传内容失败: "+err.Error())
		return
	}
	// 用户主动上传不走模型权限审批，直接写（本地或远程 SFTP）
	if err := tctx.envWriteFile(dst, data); err != nil {
		writeErr(w, 500, "写入工作区失败: "+err.Error())
		return
	}
	note := fmt.Sprintf("【文件上传】用户上传了文件 %s（%d 字节），已保存到工作区；需要了解内容时可用只读工具查看。",
		filepath.ToSlash(dst), len(data))
	_ = a.Store.AppendRow(m.ID, &StoredMessage{Message: TextMessage("user", note), Kind: "report", CreatedAt: now()})
	m.UpdatedAt = now()
	_ = a.Store.SaveMeta(m)
	a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
	writeJSON(w, 200, map[string]any{"ok": 1, "path": filepath.ToSlash(dst), "size": len(data)})
}

// handleOpenFolder 在系统资源管理器中打开一个目录（工作区行的快捷入口）。
func (a *App) handleOpenFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	path := strings.TrimSpace(body.Path)
	if path == "" {
		writeErr(w, 400, "需要 path")
		return
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		writeErr(w, 400, "目录不存在: "+path)
		return
	}
	if err := exec.Command("explorer", path).Start(); err != nil {
		writeErr(w, 500, "打开失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handlePickFolder 弹出系统原生“选择文件夹”对话框（与浏览器目录选择器同款），
// 阻塞至用户关闭后返回所选目录绝对路径；用户取消时 path 为空串。
// 浏览器端拿不到绝对路径，而本服务运行在用户本机，由后端弹框是唯一可靠途径。
func (a *App) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	start := strings.TrimSpace(r.URL.Query().Get("start"))
	if start == "" {
		start = a.Cfg.Workspace
	}
	path, err := pickFolderSerialized(start, "选择工作目录")
	if err != nil {
		writeErr(w, 500, "无法打开文件夹选择框: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"path": path})
}

// handleInterject 对话中途发消息：不打断当前执行。
// mode=steer（插话）：排进插话队列，主循环在下一次调用模型前注入生效；
// mode=queue（排队）：排进排队队列，本轮结束后作为独立一轮自动发送。
// 会话没在执行时一律按排队处理并顺手把执行拉起来，消息不会卡在队列里。
func (a *App) handleInterject(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Content) == "" {
		writeErr(w, 400, "content 不能为空")
		return
	}
	mode := "queue"
	if body.Mode == "steer" && a.IsRunning(m.ID) {
		mode = "steer"
	}
	pm := a.pushPending(m.ID, mode, strings.TrimSpace(body.Content))
	if !a.IsRunning(m.ID) {
		a.kickQueue(m) // 本轮已经结束：排队消息自己开一轮，不依赖用户再点一次
	}
	writeJSON(w, 200, map[string]any{"accepted": true, "id": pm.ID, "mode": mode})
}

// handlePending 处理排队坞上的操作：删除（remove）或把排队项提前为插话（steer）。
func (a *App) handlePending(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, 400, "id 不能为空")
		return
	}
	action := "remove"
	if body.Action == "steer" {
		action = "steer"
	}
	if !a.updatePending(m.ID, body.ID, action) {
		writeErr(w, 404, "这条消息已经不在队列里了")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "pending": a.pendingOf(m.ID)})
}

// kickQueue 会话空闲且有排队消息时，起一轮把它发出去（排队消息不依赖前端定时器）。
func (a *App) kickQueue(m *SessionMeta) {
	run, ok := a.beginRun(m.ID)
	if !ok {
		return
	}
	a.serveQueue(m, run)
}

// serveQueue 在后台把排队消息逐条发完（无请求订阅者时也要跑）。
func (a *App) serveQueue(m *SessionMeta, run *sessionRun) {
	go a.executeRun(m, run, nil)
}

// handleFork 分支对话：复制出一个新会话。body.upTo 指定分支点时只复制到
// 该 seq（含）为止——消息级分支按钮用；未指定时复制全部，目标正在执行则只
// 复制到最近一轮完成的位置（最后一条带正文且无工具调用的助手消息）。
func (a *App) handleFork(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		UpTo int64 `json:"upTo"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // 允许空请求体（整会话分支）
	all, err := a.Store.RawMessagesAll(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	msgs := []*StoredMessage{}
	for _, x := range all {
		if x.Kind != "summary" && (body.UpTo <= 0 || x.Seq <= body.UpTo) {
			msgs = append(msgs, x)
		}
	}
	if len(msgs) == 0 {
		writeErr(w, 400, "分支点之前没有可复制的历史")
		return
	}
	// 指定分支点时前缀在执行中也稳定（seq 只增不改），无需再截断
	if body.UpTo <= 0 && a.IsRunning(m.ID) {
		last := -1
		for i, x := range msgs {
			if x.Role == "assistant" && len(x.ToolCalls) == 0 && strings.TrimSpace(x.Text()) != "" {
				last = i
			}
		}
		if last >= 0 {
			msgs = msgs[:last+1]
		}
	}
	// 源会话若带继承标记行，分支复制后条目数可能被上面的截断改小，
	// 重写标记里的计数——子对话前端靠它切分继承段，数字不对会错位
	if len(msgs) > 0 && msgs[0].Kind == inheritKind {
		var im inheritMark
		if json.Unmarshal([]byte(msgs[0].Text()), &im) == nil {
			im.Count = len(msgs) - 1
			if b, err := json.Marshal(im); err == nil {
				msgs[0].Message = TextMessage("user", string(b))
			}
		}
	}
	fork := &SessionMeta{
		ID: newID(), Title: m.Title + "（分支）", Mode: m.Mode, Workspace: m.Workspace,
		ParentID: m.ParentID, Provider: m.Provider, Model: m.Model, Permission: m.Permission,
		PlanMode: m.PlanMode, GoalMode: m.GoalMode, Tools: m.Tools,
		CreatedAt: now(), UpdatedAt: now(),
	}
	if err := a.Store.Create(fork); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, x := range msgs {
		_ = a.Store.AppendRow(fork.ID, &StoredMessage{
			Message: x.Message, Kind: x.Kind, CreatedAt: fork.CreatedAt,
		})
	}
	a.broadcast(Event{Type: "session_updated", SessionID: fork.ID})
	writeJSON(w, 200, fork)
}

// handleListApprovals 返回待决定的审批（前端轮询兜底）。
func (a *App) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"approvals": a.pendingApprovals()})
}

// handleResolveApproval 接收用户对越权请求的决定。
func (a *App) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Approved bool `json:"approved"`
		Always   bool `json:"always"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := a.resolveApproval(r.PathValue("id"), body.Approved, body.Always); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleListQuestions 返回待回答的提问/计划审批（前端轮询兜底）。
func (a *App) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"questions": a.pendingQuestions()})
}

// handleResolveQuestion 接收用户对提问的回答（计划批准用 __approve__）。
func (a *App) handleResolveQuestion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answer string `json:"answer"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := a.resolveQuestion(r.PathValue("id"), body.Answer); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleGetTodos 返回会话的待办清单。
func (a *App) handleGetTodos(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"todos": todosOf(a, m.ID)})
}

// handleContextStats 返回会话下一次请求的上下文占用分解与缓存命中率
//（前端输入区「上下文」按钮面板的数据源）。
func (a *App) handleContextStats(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	writeJSON(w, 200, a.contextStats(m))
}

// handleTestWorkspace 测试一个 SSH 远程工作区配置：拨号 → 远程命令 →
// SFTP → 检查目录，返回主机指纹（前端展示，保存时绑定到会话）。
func (a *App) handleTestWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Remote *RemoteWorkspace `json:"remote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Remote == nil {
		writeErr(w, 400, "需要 remote 字段")
		return
	}
	fp, err := a.testRemoteWorkspace(body.Remote)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "hostKey": fp, "message": "连接成功，命令通道与 SFTP 可用，目录存在"})
}

// handleWorkspaceLs 连接远程主机列出一个目录（前端「选择远程工作目录」用）。
// 不依赖会话；连接按规格复用（连续导航不重复拨号）。path 为空时返回
// 远程用户主目录。目录不存在等业务失败以 ok:false + message 回传。
func (a *App) handleWorkspaceLs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Remote *RemoteWorkspace `json:"remote"`
		Path   string           `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Remote == nil {
		writeErr(w, 400, "需要 remote 字段")
		return
	}
	spec, err := body.Remote.normalize()
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	rc, err := a.getBrowseClient(spec)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	home := rc.homeDir()
	p := strings.TrimSpace(body.Path)
	if p == "" {
		p = home
	}
	p = path.Clean(p)
	type dirEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size,omitempty"`
	}
	entries, err := rc.listDir(p)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "path": p, "message": err.Error()})
		return
	}
	out := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, dirEntry{Name: e.Name, IsDir: e.IsDir, Size: e.Size})
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": p, "home": home, "entries": out})
}

// handleAttachment 提供附件库里的图片（read_image 工具的产出），
// 供前端工具卡片直接渲染原图。
func (a *App) handleAttachment(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/attachments/")
	if rel == "" {
		http.NotFound(w, r)
		return
	}
	data, mediaType, err := a.loadAttachment(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

// handleSetSessionRemote 给会话设置 / 解除 SSH 远程工作区。
// body.remote 为空对象或省略 + clear=true 时解除（回到本地工作区）。
func (a *App) handleSetSessionRemote(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		Remote *RemoteWorkspace `json:"remote"`
		Clear  bool             `json:"clear"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	if body.Clear || body.Remote == nil {
		m.Remote = nil
		a.closeRemoteSession(m.ID)
	} else {
		spec, err := body.Remote.normalize()
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		m.Remote = spec
		a.closeRemoteSession(m.ID) // 换目标后旧连接立即回收
	}
	if err := a.Store.SaveMeta(m); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
	writeJSON(w, 200, m)
}

// handleEvents 是全局事件流（SSE）：子任务进度、回报就绪、会话更新。
// 前端常驻订阅，后台派发的进度从这里进（与聊天 SSE 相互独立）。
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "当前连接不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, unsub := a.subscribe()
	defer unsub()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case ev := <-ch:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = w.Write(append([]byte("data: "), append(data, '\n', '\n')...))
			flusher.Flush()
		}
	}
}

// handleChat 接收用户消息：落库 → 启动会话执行 → 以 SSE 跟随输出。
// 执行体独立于本请求（见 runtime.go 的 executeRun）：浏览器刷新/关页只断开
// 这条流，执行照旧跑完并落库；重新打开页面用 GET /api/sessions/{id}/stream 再接上。
func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Content) == "" {
		writeErr(w, 400, "content 不能为空")
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, 500, "当前连接不支持流式响应")
		return
	}
	// 落库用户消息与登记执行放在同一临界区：并发重复发送只会有一条成功
	lock := a.sessLock(m.ID)
	lock.Lock()
	if a.IsRunning(m.ID) {
		lock.Unlock()
		writeErr(w, 409, "该会话正在执行中，请稍候再发")
		return
	}
	run, ok := a.beginRun(m.ID)
	if !ok {
		lock.Unlock()
		writeErr(w, 409, "该会话正在执行中，请稍候再发")
		return
	}
	um := TextMessage("user", body.Content)
	sm := a.appendMessage(m, um)
	opening := []Event{{Type: "message", Message: &sm.Message, Seq: sm.Seq}}
	if m.Title == "" {
		m.Title = truncateRunes(body.Content, 24)
		_ = a.Store.SaveMeta(m)
		opening = append(opening, Event{Type: "title", SessionID: m.ID, Title: m.Title})
	}
	lock.Unlock()
	a.serveRun(w, r, m, run, opening)
}

// handleSessionStream 接上正在执行的会话流（刷新页面/换窗口后继续看实时输出）。
// 未在执行时立刻回一帧 done：前端据此收尾，不会一直挂着。
func (a *App) handleSessionStream(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, 500, "当前连接不支持流式响应")
		return
	}
	run := a.runOf(m.ID)
	if run == nil {
		sseHeaders(w)
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
		w.(http.Flusher).Flush()
		return
	}
	a.serveRun(w, r, m, run, nil)
}

// handleStop 显式停止会话执行（SSE 断开不再等于停止）。
func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	if !a.stopRun(m.ID) {
		writeJSON(w, 200, map[string]any{"stopped": false, "reason": "该会话当前没有在执行"})
		return
	}
	writeJSON(w, 200, map[string]any{"stopped": true})
}

// sseHeaders 切换响应为 SSE。
func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
}

// serveRun 把执行事件以 SSE 写给客户端：先补当前进行中的流式文本（重连不丢半句），
// 再持续转发。函数因客户端断开返回时执行不受影响；opening 为空表示纯重连。
// opening 事件由执行体统一发布（这里只订阅），否则同一条用户消息会发两遍。
func (a *App) serveRun(w http.ResponseWriter, r *http.Request, m *SessionMeta, run *sessionRun, opening []Event) {
	sseHeaders(w)
	flusher := w.(http.Flusher)
	ch, unsub := run.subscribe()
	defer unsub()
	if len(opening) > 0 {
		go a.executeRun(m, run, opening) // 启动执行；事件走订阅通道下发
	} else if text, thinking := run.snapshot(); text != "" || thinking != "" {
		// 纯重连：补上正在进行中的那半句，避免接上时丢内容
		if b, err := json.Marshal(Event{Type: "resume", SessionID: m.ID, Delta: text, Thinking: thinking}); err == nil {
			_, _ = w.Write(append([]byte("data: "), append(b, '\n', '\n')...))
		}
	}
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return // 客户端断开（刷新/关页/主动取消）：执行继续，可随时再接上
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return // 执行结束，订阅通道关闭
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := w.Write(append([]byte("data: "), append(b, '\n', '\n')...)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
