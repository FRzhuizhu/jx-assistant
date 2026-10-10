package main

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
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

const appVersion = "20261010V1" // 版本号体系：日期+V序号（同日发多版用 V2 V3），恒新于旧点分号（见 update.go 的 versionCompare）

// App 持有配置与会话存储，是所有 HTTP 处理器的宿主。
type App struct {
	Cfg        *Config
	CfgPath    string
	Store      *Store
	Ctx        *ContextEngine // 上下文引擎：组装请求材料 + 上下文事件总线（contextengine.go）
	Plugins    *PluginRegistry      // DSH 插件兼容层实现（adapters.go：插件兼容层/ + adapter_*.go）
	Layers     *PluginLayerRegistry // 插件兼容层注册表：主体发现的全部插件体系（pluginlayers.go）
	Components *ComponentRegistry
	CCPlugins  *CCPluginRegistry // Claude Code 插件兼容层实现（ccplugin*.go）
	Todos      *todoScheduler    // 待办与定时任务：组件 组件/todo-scheduler 的宿主半边（todo.go）
	asr        *asrManager       // 本地语音识别测试通道（voiceasr.go，语音组件设置页用）
	static     fs.FS             // 前端静态资源：发布用内嵌副本，开发（-webdir）指向磁盘目录

	mu              sync.Mutex
	sessLocks       map[string]*sync.Mutex
	runMu           sync.Mutex
	runners         map[string]*sessionRun // 正在执行的会话（含后台派发）
	permMu          sync.Mutex
	grants          map[string]*Grant        // 授权凭证（perm.go）
	approvals       map[string]*Approval     // 等待用户决定的越权审批（perm.go）
	permNotes       map[string]string        // 待落快照的「权限变更」注记（perm.go，env 通道）
	observedFiles   sync.Map                 // 会话内已观察文件集（观察策略，perm.go）
	questions       map[string]*UserQuestion // 等待用户回答的提问/计划审批（interact.go）
	jobsMu          sync.Mutex
	jobs            map[string]*jobRun // 会话内后台作业（jobs.go）
	protectPaths    []string           // 助手自身配置/数据目录，任何模式禁访问
	skillsMu        sync.Mutex
	skillsReady     bool   // 技能母本是否已释放（SkillsDir 首次解析后置位）
	skillsDirOurs   string // 已解析的技能目录（App 级缓存，避免跨实例污染）
	marketMu        sync.Mutex
	marketCache     marketCacheEntry // 市场清单拉取缓存（60s，见 market.go）
	remoteMu        sync.Mutex
	remotes         map[string]*remoteClient // SSH 远程连接池（remote.go）
	listenPort      int                      // 本机监听端口（远程中继的桥接目标，热重启连接用）
	remoteCtlMu     sync.Mutex
	remoteCtlCancel context.CancelFunc // 中继连接存活句柄（nil=未运行，remoteaccess.go）
	shellEpochMu    sync.Mutex
	shellEpoch      int64 // 远程外壳版本代数：「关于」页一键清缓存按钮递增（remoteaccess.go，落盘 data/shell-epoch.json）
	evtMu           sync.Mutex
	subscribers     map[chan Event]bool // /api/events 的订阅者
	pendMu          sync.Mutex
	pendingSteer    map[string][]*PendingMsg // 执行中会话的插话（下一次调用模型前生效）
	pendingQueue    map[string][]*PendingMsg // 执行中会话的排队（本轮结束后作为独立一轮发送）
	queuePaused     map[string]bool          // 会话队列是否暂停（暂停后本轮结束不自动续发；恢复时空闲即接着发）
	midrunPref      string                   // 执行中发消息的偏好（queue/steer）：前端设置项 jx.midrun 随消息上报，派发注入按它处理（见 runtime.go）
	errMu           sync.Mutex
	runErrors       map[string]string // 会话最近一次失败原因（前端输入框上方报错条 + 重试）
	osOnce          sync.Once
	osDesc          string // 环境快照用的系统描述（Windows 版本 + PowerShell 版本，探测一次缓存）
	browserMu       sync.Mutex
	browserEmbedded *BrowserHost  // 无头 chromedp 宿主：网页形态的内嵌目标（browser_host.go，惰性创建）
	browserHeaded   *BrowserHost  // 有头独立窗口宿主（旧版行为，惰性创建）
	nativePanel     browserTarget // 桌面右栏原生 WebView2 面板（browser_panel_windows.go；网页模式为 nil）
	browserActive   browserTarget // 工具作用目标：最近一次 browser_open 选定的实例

	// 桌面壳（runDesktop）注入的右侧栏截图通道（panelshot_windows.go）：
	// shellEval 往主界面注入 JS、panelViewCh 收回右栏矩形回传，shellWnd 是
	// 主窗口 HWND（客户区原点换算屏幕坐标用）。非桌面形态全为零值。
	shellEval   func(string)
	shellWnd    uintptr
	panelViewCh chan panelViewReport
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
		permNotes: map[string]string{},
		questions: map[string]*UserQuestion{}, jobs: map[string]*jobRun{},
		remotes: map[string]*remoteClient{}}
	a.Ctx = &ContextEngine{app: a} // 上下文引擎：所有会话经它组装与记事件
	// Claude Code 插件兼容层：加载已安装插件，后台连接 MCP 服务器、装载 mods
	// （失败记日志不阻断）
	a.CCPlugins = InitCCPlugins(a, cfg.DataDir)
	a.CCPlugins.MCPStartAll()
	a.CCPlugins.ModsStartAll()
	// 主体发现插件兼容层：各层（DSH、Claude Code…）经 registerPluginLayer
	// 自注册，主体只按 PluginLayer 契约调用——统一清单与总开关走 /api/plugins，
	// 主体不认识任何具体插件格式
	a.Layers = discoverPluginLayers(a)
	layerNames := []string{}
	for _, l := range a.Layers.Layers() {
		layerNames = append(layerNames, l.Info().Name)
	}
	log.Printf("[插件] 主体发现 %d 个插件兼容层：%s", len(layerNames), strings.Join(layerNames, "、"))
	// 受保护路径与各运行时注册表的初始化统一在 initRuntime（见 runtime.go）
	a.initRuntime()
	// 技能可见性全模式契约自检：任何模式丢了技能清单注入或 read_skill，
	// 直接拒绝启动（见 skill_manager.go 的 assertSkillVisibility）
	a.assertSkillVisibility()
	a.recoverInterrupted()
	// 交付队列崩溃残留兜底：上一进程若死在执行中（running），复位回 pending
	// 并统一重放——登记过的交付不能因为重启就丢
	if n := a.Store.ResetStuckJobs(); n > 0 {
		log.Printf("[交付] 启动恢复：%d 条中断的交付作业已复位待执行", n)
	}
	go a.flushDeliveries("")
	// 待办与定时任务：载入清单并起调度循环（组件关掉时整拍跳过，见 todo.go）
	a.Todos = newTodoScheduler(a)
	// 本地语音识别测试通道（voiceasr.go）：worker 惰性拉起，无模型时端点仍可列清单
	a.asr = &asrManager{app: a}
	// 文件快照保留策略：按天超龄清理 + 按会话限量驱逐（见 snapshot.go）
	go a.snapshotJanitor()
	// 远程外壳版本代数恢复（一键清缓存的计数，remoteaccess.go）
	a.loadShellEpoch()
	return a, nil
}

// sessionModes 是会话模式全集（单一事实源）：建会话校验（handleCreateSession）
// 与启动自检（assertSkillVisibility）共用。新增模式必须加进本列表——自检会
// 逐模式强制「技能清单注入 + read_skill 在场」的全模式契约，漏加即启动炸响。
var sessionModes = []string{"assistant", "aux", "office", "coding", "task", "skill-create"}

// modeKnown 判断模式是否在 sessionModes 全集里。
func modeKnown(m string) bool {
	for _, x := range sessionModes {
		if x == m {
			return true
		}
	}
	return false
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
	mux.HandleFunc("GET /api/display-mode", a.handleGetDisplayMode)
	mux.HandleFunc("POST /api/display-mode", a.handleSetDisplayMode)
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
	mux.HandleFunc("POST /api/sessions/{id}/retry", a.handleRetry)
	mux.HandleFunc("POST /api/sessions/{id}/interject", a.handleInterject)
	mux.HandleFunc("POST /api/sessions/{id}/pending", a.handlePending)
	mux.HandleFunc("POST /api/sessions/{id}/compact", a.handleCompact)
	mux.HandleFunc("GET /api/sessions/{id}/progress", a.handleGetProgress)
	mux.HandleFunc("GET /api/sessions/{id}/plans", a.handleListPlans)
	mux.HandleFunc("POST /api/sessions/{id}/plans/export", a.handleExportPlan)
	mux.HandleFunc("GET /api/sessions/{id}/context", a.handleContextStats)
	mux.HandleFunc("GET /api/sessions/{id}/export", a.handleExport)
	mux.HandleFunc("POST /api/sessions/{id}/upload", a.handleUpload)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("GET /api/p2p-shell", a.handleP2PShell)
	mux.HandleFunc("GET /api/shell-hash", a.handleShellHash)
	mux.HandleFunc("POST /api/shell-bump", a.handleShellBump)     // 「关于」页一键清远程端缓存：外壳代数 +1
	mux.HandleFunc("POST /api/webrtc-offer", a.handleWebRTCOffer) // P2P 直连信令：SDP 换 answer（p2pdirect.go，DataChannel 鉴权另过 auth 帧）
	mux.HandleFunc("POST /api/feedback", a.handleFeedbackCreate)
	mux.HandleFunc("POST /api/open-folder", a.handleOpenFolder)
	mux.HandleFunc("GET /api/pick-folder", a.handlePickFolder)
	mux.HandleFunc("GET /api/pick-file", a.handlePickFile)
	mux.HandleFunc("POST /api/sessions/{id}/fork", a.handleFork)
	mux.HandleFunc("POST /api/sessions/{id}/edit", a.handleEdit)
	mux.HandleFunc("POST /api/sessions/{id}/file-rewind", a.handleFileRewind)
	mux.HandleFunc("GET /api/sessions/{id}/file-versions", a.handleFileVersions)
	mux.HandleFunc("GET /api/sessions/{id}/file-version", a.handleFileVersion)
	mux.HandleFunc("POST /api/sessions/{id}/delivery/open", a.handleDeliveryOpenFile)
	mux.HandleFunc("POST /api/sessions/{id}/delivery/reveal", a.handleDeliveryReveal)
	mux.HandleFunc("GET /api/sessions/{id}/delivery/download", a.handleDeliveryDownload)
	// 桌面壳右键菜单的「在系统中打开/在文件夹中定位/在系统浏览器中打开」：
	// 用户主动操作（同交付卡片口径），open-path/reveal-path 只放行会话工作区
	// 内已存在的路径，open-url 只放行 http(s)
	mux.HandleFunc("POST /api/open-url", a.handleOpenURL)
	mux.HandleFunc("POST /api/sessions/{id}/open-path", a.handleSessionOpenPath)
	mux.HandleFunc("POST /api/sessions/{id}/reveal-path", a.handleSessionRevealPath)
	// 读系统剪贴板（右键「粘贴」在桌面壳里的兜底路径：WebView2 里
	// readText 会永久挂起）。剪贴板是敏感数据：仅回环可达
	mux.HandleFunc("GET /api/clipboard", a.handleClipboardRead)
	// 前端诊断打点（见 handleDiag）：只写日志不回数据
	mux.HandleFunc("GET /api/diag", a.handleDiag)
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
	mux.HandleFunc("GET /api/experts", a.handleListExperts)
	mux.HandleFunc("POST /api/experts", a.handleUpsertExpert)
	mux.HandleFunc("POST /api/experts/delete", a.handleDeleteExpert)
	mux.HandleFunc("POST /api/sessions/{id}/remote", a.handleSetSessionRemote)
	mux.HandleFunc("GET /api/plugins", a.handleListPlugins)
	mux.HandleFunc("POST /api/plugins", a.handleSetPlugin)
	mux.Handle("GET /_plugins/", http.HandlerFunc(a.handlePluginAsset))
	// Claude Code 插件兼容层（ccplugin*.go）：安装/开关/市场/命令清单
	mux.HandleFunc("GET /api/cc-plugins", a.handleListCCPlugins)
	mux.HandleFunc("POST /api/cc-plugins/toggle", a.handleSetCCPlugin)
	mux.HandleFunc("POST /api/cc-plugins/install", a.handleInstallCCPlugin)
	mux.HandleFunc("POST /api/cc-plugins/uninstall", a.handleUninstallCCPlugin)
	mux.HandleFunc("POST /api/cc-plugins/marketplace/add", a.handleCCMarketplaceAdd)
	mux.HandleFunc("GET /api/cc-plugins/marketplace/{id}", a.handleCCMarketplaceBrowse)
	mux.HandleFunc("GET /api/cc-plugins/commands", a.handleCCPluginCommands)
	mux.HandleFunc("GET /api/components", a.handleListComponents)
	mux.HandleFunc("POST /api/components", a.handleSetComponent)
	// 组件浏览器半边资产挂在 /api/ 下（而非独立 /_components/ 前缀）：前端 cx.js
	// 统一用 fetch 拿资产，远程模式下 /api/ 前缀经 remote-boot 的 fetch 劫持自动
	// 走中继隧道——旧 /_components/ 标签相对网关 origin 解析必 404，远程组件全体消失
	mux.Handle("GET /api/components/", http.HandlerFunc(a.handleComponentAsset))
	// 市场（技能/组件/插件，见 market.go）：清单视图 + 一键安装/卸载；
	// 同样挂 /api/ 下让远程中继天然覆盖
	mux.HandleFunc("GET /api/market", a.handleMarketView)
	mux.HandleFunc("POST /api/market/install", a.handleMarketInstall)
	mux.HandleFunc("POST /api/market/uninstall", a.handleMarketUninstall)
	mux.HandleFunc("POST /api/market/publish", a.handleMarketPublish)     // 上传技能：校验打包后转投市场服务器（market_publish.go）
	mux.HandleFunc("POST /api/market/unpublish", a.handleMarketUnpublish) // 删除自己发布的市场条目（同文件）
	// 公司接入向导（wizard.go）：局域网发现 + 总服务器查询 + 一键应用配置
	mux.HandleFunc("POST /api/wizard/discover", a.handleWizardDiscover)
	mux.HandleFunc("POST /api/wizard/connect", a.handleWizardConnect)
	// 语音组件（voice）宿主半边（voiceapi.go）：朗读走微软 Edge「大声朗读」
	// 免密钥通道；组件关掉时一并 404；同样挂 /api/ 下让远程中继天然覆盖
	mux.HandleFunc("GET /api/voice/voices", a.handleVoiceVoices)
	mux.HandleFunc("POST /api/voice/tts", a.handleVoiceTTS)
	// 本地语音识别通道（voiceasr.go）：听写组件的本地引擎用（16k mono WAV base64）
	mux.HandleFunc("POST /api/voice/asr/transcribe", a.handleASRTranscribe)
	// 流式听写通道（voiceasr.go）：WebSocket 一行一答哑桥——partial=实时灰字、
	// final=端点（停顿）后离线精修的黑字。本机/桌面形态直连；远程 https 页面
	// 开不了 ws，前端自动回退整段模式
	mux.HandleFunc("GET /api/voice/asr/stream", a.handleASRStream)
	// 设备桥（device_bridge.go）：Kotlin 壳长轮询拿命令/回传结果；自检端点供
	// 本机 curl 一键诊断三件套链路。三端点仅回环可达（loopbackOnly 闸）
	mux.HandleFunc("GET /api/device/poll", a.handleDevicePoll)
	mux.HandleFunc("POST /api/device/result", a.handleDeviceResult)
	mux.HandleFunc("GET /api/device/selftest", a.handleDeviceSelftest)
	// 浏览器（browser_host.go / browser_panel_windows.go）：桌面右栏是原生
	// WebView2 面板（前端经 panel-rect 上报占位区矩形），navigate/viewport/
	// state 面向内嵌形态目标；open-view/view 是聊天链接点开的入口与远程
	// 交付文件的流式渲染。旧 MJPEG 帧流与输入镜像端点已随镜像方案移除
	mux.HandleFunc("POST /api/browser/panel-rect", a.handleBrowserPanelRect)
	mux.HandleFunc("POST /api/browser/navigate", a.handleBrowserNavigate)
	mux.HandleFunc("POST /api/browser/viewport", a.handleBrowserViewport)
	mux.HandleFunc("GET /api/browser/state", a.handleBrowserState)
	mux.HandleFunc("POST /api/browser/open-view", a.handleBrowserOpenView)
	mux.HandleFunc("GET /api/browser/view", a.handleBrowserView)
	// Electron 壳面板桥的导航回报（panel-bridge.js 在 did-navigate 推送）：
	// 更新内嵌目标状态并广播 browser_nav（旧壳由 onNavCompleted 触发同一事件）
	mux.HandleFunc("POST /api/browser/electron-report", a.handleBrowserElectronReport)
	// 文件查看页（fileview.go）：代码/文本类交付与文件链接的右栏「文件」视图，
	// 自包含 HTML，桌面/网页/远程三端一致；fileraw 供图片/PDF 内嵌
	mux.HandleFunc("GET /api/fileview", a.handleFileView)
	mux.HandleFunc("GET /api/fileraw", a.handleFileRaw)
	// 右侧栏截图（panelshot_windows.go）：panel_screenshot 工具注入主界面的
	// 探针 JS 把右栏矩形回传到这里
	mux.HandleFunc("POST /api/shell/panel-view", a.handleShellPanelView)
	// 待办与定时任务（组件 todo-scheduler 的宿主半边）：组件未启用时这些接口一律 404
	mux.HandleFunc("GET /api/todos", a.handleTodoList)
	mux.HandleFunc("POST /api/todos", a.handleTodoCreate)
	mux.HandleFunc("POST /api/todos/update", a.handleTodoUpdate)
	mux.HandleFunc("POST /api/todos/delete", a.handleTodoDelete)
	mux.HandleFunc("POST /api/todos/run", a.handleTodoRun)
	mux.HandleFunc("POST /api/todos/missed", a.handleTodoMissed)
	mux.HandleFunc("GET /api/todos/workspaces", a.handleTodoWorkspaces)
	// Git 面板（组件 git-panel 的宿主半边）：组件未启用时这些接口一律 404。
	// 读 status/diff/graph/commit/branches，写有 stage/unstage/untrack（移出
	// 跟踪变未跟踪）/commit/summarize（只生成提交信息不提交）/checkout（切
	// 本地分支）/sync（fetch + 快进或无冲突自动合并 + push 当前分支；任何
	// 需要决策的情形原样退回）。不做 reset/revert/discard/stash/rebase。
	mux.HandleFunc("GET /api/git/status", a.handleGitStatus)
	mux.HandleFunc("GET /api/git/diff", a.handleGitDiff)
	mux.HandleFunc("GET /api/git/graph", a.handleGitGraph)
	mux.HandleFunc("GET /api/git/commit", a.handleGitCommitDetail)
	mux.HandleFunc("GET /api/git/branches", a.handleGitBranches)
	mux.HandleFunc("POST /api/git/stage", a.handleGitStage)
	mux.HandleFunc("POST /api/git/unstage", a.handleGitUnstage)
	mux.HandleFunc("POST /api/git/untrack", a.handleGitUntrack)
	mux.HandleFunc("POST /api/git/commit", a.handleGitCommitMsg)
	mux.HandleFunc("POST /api/git/summarize", a.handleGitSummarize)
	mux.HandleFunc("POST /api/git/checkout", a.handleGitCheckout)
	mux.HandleFunc("POST /api/git/sync", a.handleGitSync)
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

// ---------- 插件兼容层：统一清单与总开关（各层的注册表见 pluginlayers.go） ----------

func (a *App) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	if a.Layers == nil {
		writeJSON(w, 200, map[string]any{"layers": []LayerInfo{}, "plugins": []LayerPlugin{}})
		return
	}
	writeJSON(w, 200, map[string]any{"layers": a.Layers.Infos(), "plugins": a.Layers.AllPlugins()})
}

func (a *App) handleSetPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Layer   string `json:"layer"` // 来源体系（层 id；缺省=全部层里按 id 唯一匹配）
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "需要 id 与 enabled")
		return
	}
	if a.Layers == nil {
		writeErr(w, 500, "插件兼容层未初始化")
		return
	}
	if err := a.Layers.SetEnabled(body.Layer, body.ID, body.Enabled); err != nil {
		if errors.Is(err, errLayerNotFound) || errors.Is(err, errPluginNotFound) {
			writeErr(w, 404, err.Error())
			return
		}
		writeErr(w, 500, "保存开关失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": 1, "refreshNeeded": true, "plugins": a.Layers.AllPlugins()})
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

// withCORS 同源校验 + 简要访问日志（P1-9）：去掉 Access-Control-Allow-Origin: *，
// Origin 与 Host 不符一律 403——堵死本机恶意网页跨域调用 /api/approvals（替用户
// 点「允许」）与 PATCH 会话权限（切 full 绕过整个沙箱体系）的 CSRF 面。
// 浏览器同源请求（本地 GUI）与无 Origin 的非浏览器客户端（curl/测试）不受影响。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s from %s (%s)", r.Method, r.URL.Path, r.RemoteAddr, r.Header.Get("User-Agent"))
		origin := r.Header.Get("Origin")
		if origin != "" && !sameOrigin(origin, r.Host) {
			writeErr(w, 403, "跨域请求被拒绝（同源策略）")
			return
		}
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin 判断 Origin 头（scheme://host[:port]）与请求 Host 是否同源。
// Origin 缺省端口（http 80 / https 443）与显式端口等价。
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	o := strings.ToLower(u.Host)
	h := strings.ToLower(host)
	if o == h {
		return true
	}
	// 端口缺省等价：http://x ≡ http://x:80，https://x ≡ https://x:443
	oh, hp := splitHostPort(o)
	hh, hpc := splitHostPort(h)
	if oh != hh {
		return false
	}
	if hp == "" {
		if u.Scheme == "https" {
			hp = "443"
		} else {
			hp = "80"
		}
	}
	if hpc == "" {
		hpc = hp
	}
	return hp == hpc
}

// splitHostPort 拆 host[:port]；IPv6 字面量 [::1]:8788 也覆盖。
func splitHostPort(hp string) (string, string) {
	if strings.HasPrefix(hp, "[") {
		if i := strings.LastIndex(hp, "]:"); i >= 0 {
			return hp[:i+1], hp[i+2:]
		}
		return hp, ""
	}
	if i := strings.LastIndex(hp, ":"); i >= 0 {
		return hp[:i], hp[i+1:]
	}
	return hp, ""
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
//
//	bin=1 → 回收站：已归档，或其工作目录在回收列表 ws 里；
//	否则  → 正常工作区：未归档，且工作目录不在回收列表 ws 里。
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

// handleGetDisplayMode 设置页「界面渲染」区块的数据源：当前渲染模式与来由
// （desktop_windows.go 实现；网页/手机模式 desktop=false，前端隐藏区块）。
func (a *App) handleGetDisplayMode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.displayMode())
}

// handleSetDisplayMode 切换 GPU 渲染 / 兼容模式：写删兼容标记 + 前端热重启
// （后端不动，窗口约 1 秒后原位重开）。
func (a *App) handleSetDisplayMode(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Software bool `json:"software"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	if err := a.setDisplayMode(p.Software); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	t := a.DefaultTarget()
	ra := a.Cfg.RemoteAccess
	writeJSON(w, 200, map[string]any{
		"name":       "坚祥数字助理",
		"version":    appVersion,
		"android":    isAndroidRuntime, // 前端按平台隐藏「操作手机」权限芯片
		"model":      t.Model,
		"hasKey":     t.APIKey != "",
		"wizardDone": a.Cfg.WizardDone, // 首启向导（app-wizard.js 据此自动弹出）
		"mock":       t.BaseURL == "mock",
		"workspace":  a.Cfg.Workspace,
		"allowShell": a.Cfg.AllowShell,
		// 左下角手机图标的状态来源：正常态显示「可远程访问」，异常态回退告警胶囊
		"remote": map[string]any{
			"enabled":   ra.Enabled,
			"connected": remoteConnected.Load(),
		},
		// 更新横幅的数据源：客户端打开时先查这里（SSE 无回放，在线推送靠事件流）
		"update": updateSnapshot(),
	})
}

// updateSnapshot 当前更新检查状态（线程安全读）。
func updateSnapshot() UpdateState {
	updateMu.Lock()
	defer updateMu.Unlock()
	return updateState
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
	// 预置提供方未带模型目录时回落到模板清单：套餐端点（智谱 Coding 等）
	// 不一定支持 /models 拉取，模板自带 glm-5.3 系才能开箱即用
	if isPreset && len(models) == 0 && len(preset.Models) > 0 {
		models = append(models, preset.Models...)
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
		Note    string `json:"note,omitempty"`
	}
	presets := []presetView{}
	for _, p := range DefaultProviders() {
		presets = append(presets, presetView{ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Note: p.Note})
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
		"lanIPs":           lanIPv4s(),
		"remoteAccess":     remoteStatus(a.Cfg.RemoteAccess),
		"marketURL":        a.Cfg.MarketURL,
		"publisherName":    a.Cfg.PublisherName,
		"wizardDone":       a.Cfg.WizardDone,
		"registryURL":      a.Cfg.RegistryURL,
		"updateURL":        a.Cfg.UpdateURL,
		"maxSteps":         a.Cfg.MaxSteps,
		"compactMode":      a.Cfg.CompactMode,
		"keepRounds":       a.Cfg.KeepRounds,
		"snapshotEnabled":  a.snapshotsEnabled(),
		"snapshotDays":     a.Cfg.SnapshotDays,
		"ruleInterval":     a.Cfg.RuleInterval,
		"compactThreshold": a.Cfg.CompactThreshold,
	})
}

func (a *App) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL         *string `json:"baseURL"`
		APIKey          *string `json:"apiKey"` // 空串表示保持不变
		Model           *string `json:"model"`
		Workspace       *string `json:"workspace"`
		BindHost        *string `json:"bindHost"`
		RemoteEnabled   *bool   `json:"remoteEnabled"` // 远程接入（公网中继）总开关，即时生效（保存即热启停）
		RemoteMyID      *string `json:"remoteMyId"`    // 远程接入设备号（手机入口 /a/<设备号>）
		RemotePairKey   *string `json:"remotePairKey"` // 远程接入配对密钥（端到端加密密钥的种子）
		RemoteHost      *string `json:"remoteHost"`    // 中继网关地址（域名或 IP，不带协议与端口）
		RemotePort      *int    `json:"remotePort"`    // 中继网关端口
		MarketURL       *string `json:"marketURL"`     // 市场（技能/组件/插件）服务器清单地址
		PublisherName   *string `json:"publisherName"` // 发布者姓名（市场条目归属名；发布/删除自己条目时用）
		WizardDone      *bool   `json:"wizardDone"`    // 首启向导：完成/跳过（不再自动弹）
		RegistryURL     *string `json:"registryURL"`   // 总服务器地址（向导发现不到公司时的查询兜底）
		UpdateURL       *string `json:"updateURL"`     // 更新检查发布源（"off"/空串关闭）
		AllowShell      *bool   `json:"allowShell"`
		MaxSteps        *int    `json:"maxSteps"`
		SnapshotEnabled *bool   `json:"snapshotEnabled"` // 文件备份总开关（常规页）
		SnapshotDays    *int    `json:"snapshotDays"`    // 快照自动清理天数（-1=永不过期）
		Compact         *struct {
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
		bh := strings.TrimSpace(*body.BindHost)
		// 只收 localhost 或 IP 字面量（含 0.0.0.0 / :: 通配），防设置页
		// 误传垃圾值把下次启动的监听地址写坏。
		if bh != "localhost" && bh != "0.0.0.0" && bh != "::" && net.ParseIP(bh) == nil {
			writeErr(w, 400, "bindHost 只能是 localhost 或 IP 地址")
			return
		}
		a.Cfg.BindHost = bh
	}
	oldRA := a.Cfg.RemoteAccess // 保存后比对：远程接入字段有变则热生效
	if body.RemoteEnabled != nil {
		a.Cfg.RemoteAccess.Enabled = *body.RemoteEnabled
	}
	if v := body.RemoteMyID; v != nil {
		id := strings.TrimSpace(*v)
		if !validRemoteID(id) {
			writeErr(w, 400, "设备号需为 2~32 位字母/数字/中划线/下划线")
			return
		}
		a.Cfg.RemoteAccess.MyID = id
	}
	if v := body.RemotePairKey; v != nil {
		pk := strings.TrimSpace(*v)
		if len(pk) < 8 || len(pk) > 64 || strings.ContainsFunc(pk, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			writeErr(w, 400, "配对密钥需为 8~64 位可打印字符")
			return
		}
		a.Cfg.RemoteAccess.PairKey = pk
	}
	if v := body.RemoteHost; v != nil {
		h := strings.TrimSpace(*v)
		if h != "" && !validRemoteHost(h) {
			writeErr(w, 400, "网关地址需为域名或 IP（不带 http:// 前缀与端口）")
			return
		}
		a.Cfg.RemoteAccess.Host = h
	}
	if v := body.RemotePort; v != nil {
		if *v < 1 || *v > 65535 {
			writeErr(w, 400, "网关端口需在 1~65535 之间")
			return
		}
		a.Cfg.RemoteAccess.Port = *v
	}
	if v := body.MarketURL; v != nil {
		u := strings.TrimSpace(*v)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeErr(w, 400, "市场地址需为 http(s) 的清单地址（jxai-server 的 /market/manifest.json）")
			return
		}
		a.Cfg.MarketURL = u
		a.marketMu.Lock()
		a.marketCache = marketCacheEntry{} // 换市场源：缓存作废
		a.marketMu.Unlock()
	}
	if v := body.PublisherName; v != nil {
		name := strings.TrimSpace(*v)
		if len([]rune(name)) > 32 {
			writeErr(w, 400, "发布者姓名最长 32 字")
			return
		}
		a.Cfg.PublisherName = name
	}
	if v := body.WizardDone; v != nil {
		a.Cfg.WizardDone = *v
	}
	if v := body.RegistryURL; v != nil {
		u := strings.TrimSpace(*v)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeErr(w, 400, "总服务器地址需为 http(s)（jxai-server 的地址）")
			return
		}
		a.Cfg.RegistryURL = u
	}
	if v := body.UpdateURL; v != nil {
		u := strings.TrimSpace(*v)
		if u != "" && u != "off" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeErr(w, 400, "发布源需为 http(s) 地址，或 off 关闭检查")
			return
		}
		a.Cfg.UpdateURL = u // "off" 落盘成字面量，加载时转成关闭
	}
	if body.AllowShell != nil {
		a.Cfg.AllowShell = *body.AllowShell
	}
	if body.MaxSteps != nil && *body.MaxSteps > 0 {
		a.Cfg.MaxSteps = *body.MaxSteps
	}
	if body.SnapshotEnabled != nil {
		a.Cfg.SnapshotEnabled = body.SnapshotEnabled
	}
	if body.SnapshotDays != nil && (*body.SnapshotDays > 0 || *body.SnapshotDays == -1) {
		a.Cfg.SnapshotDays = *body.SnapshotDays
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
	// 远程接入字段有变（开关/设备号/密钥/网关）：立即热启停，无需重启进程
	if a.Cfg.RemoteAccess != oldRA {
		a.syncRemoteAccess()
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
		stops := a.Store.LastStops() // 异常中断标记（空=正常收口，见 Store.SetLastStop）
		for _, m := range list {
			m.LastStop = stops[m.ID]
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
	// 执行状态以内存注册表为准：数据库那列只用于服务重启后的中断恢复；
	// 异常中断标记（侧栏「⚠ 已中断」）同理不读会话表，一次取齐后按 id 分发
	stops := a.Store.LastStops()
	for _, m := range list {
		m.LastStop = stops[m.ID]
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
	if !modeKnown(body.Mode) {
		body.Mode = "task"
	}
	if body.Permission == "" {
		body.Permission = "workspace-write"
		// 创建技能模式默认完全访问：要看懂用户演示（截屏/键鼠记录）并亲自
		// 验证技能关键步骤，操作电脑/浏览器是刚需；用户显式传权限则尊重。
		if body.Mode == "skill-create" {
			body.Permission = permFull
		}
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
	} else if body.Tools == nil {
		// 请求没带能力开关（tools 字段缺席）：按「工作区内读写默认开启「操作浏览器」」
		// 补默认（capDefaultsForMode）。显式传空数组 = 用户主动取消勾选，尊重用户。
		m.Tools = mergeCapDefaults(m.Tools, m.Mode, permModeOf(m))
	}
	if m.GoalMode {
		m.GoalStatus = "active" // 创建即带目标模式：状态从 active 起步
	}
	if err := a.Store.Create(m); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 选定工作目录后即建 .jxai/tmp：模型临时文件的约定去处（见系统提示词）
	if m.Remote == nil {
		(&toolContext{app: a, sess: m}).ensureJxAI()
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

// promptBrief 下发瘦身的比对基元：prompt 快照里只取与缓存相关的 sys/tools。
// env 逐轮必变（时间/环境），且其变化由独立的 env 行驱动前端展示，比较时忽略。
type promptBrief struct {
	Sys   string          `json:"sys"`
	Tools json.RawMessage `json:"tools"`
}

// trimMessagesForUI 对下发前端的消息副本做瘦身（只改本次响应的内存对象，库内原样）：
//   - request/audit 是审计工件：前端不展示（导出走 /api/sessions/{id}/export
//     服务端直读），正文不下发——与 checkpoint 的 Before 不下发同一思路；
//   - prompt 拼装快照与上一条的 sys+tools 相同 → 只发空壳 {}，前端跳过空壳、
//     只见变化行（规范 §4「只讲变化」的对应瘦身）；有变化才完整下发。
//
// 比对基于原始正文（上一条 prompt 的原始 sys/tools），空壳只是替换下发副本、
// 不参与比较，否则「A→A→B」会因中间行被掏空而误判为变化。
func trimMessagesForUI(msgs []*StoredMessage) {
	var last *promptBrief
	for _, m := range msgs {
		switch m.Kind {
		case kindRequest, kindAudit:
			m.Content = ""
		case kindPrompt:
			var cur promptBrief
			if json.Unmarshal([]byte(m.Text()), &cur) != nil {
				continue // 坏行原样下发（前端同样忽略坏行）
			}
			if last != nil && cur.Sys == last.Sys && bytes.Equal(cur.Tools, last.Tools) {
				m.Content = "{}"
				continue
			}
			cur.Tools = append(json.RawMessage(nil), cur.Tools...) // 自持副本，别引用解码缓冲
			last = &cur
		}
	}
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
	trimMessagesForUI(msgs) // 下发瘦身：库内原样，只削响应副本（见函数注释）
	// running 与 pending 以内存运行时为准：刷新页面后据此自动接上执行流、
	// 恢复「已插话/已排队」的显示，不依赖刷新前的页面状态
	cps, err := a.Store.CheckpointsAfter(m.ID, 0) // seq 从 1 起，>0 即全部
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, cp := range cps {
		cp.Before = nil // 正文不下发，前端只做清单展示
	}
	dvs, err := a.Store.DeliveriesFor(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"meta": m, "messages": msgs,
		"running": a.IsRunning(m.ID), "pending": a.pendingOf(m.ID),
		"checkpoints": cps, "deliveries": dvs,
		"error": a.runError(m.ID), // 最近一次失败原因（输入框上方报错条 + 重试）
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
	// 上下文事件收集：字段有实际变化才记，落库成功后经总线 Notify（导出时间线呈现）
	type ctxNote struct{ kind, detail string }
	var notes []ctxNote
	if body.Title != nil {
		m.Title = truncateRunes(*body.Title, 40)
	}
	if body.Workspace != nil {
		oldWS := m.Workspace
		m.Workspace = strings.TrimSpace(*body.Workspace)
		if oldWS != m.Workspace {
			notes = append(notes, ctxNote{ctxWorkspace, "工作目录：" + oldWS + " → " + m.Workspace})
			defer a.scheduleResidualCleanup(oldWS) // 工作区切换：清旧工作区残留
		}
	}
	if body.Provider != nil {
		old := m.Provider
		m.Provider = strings.TrimSpace(*body.Provider)
		if old != m.Provider {
			notes = append(notes, ctxNote{ctxModel, "提供方：" + old + " → " + m.Provider})
		}
	}
	if body.Model != nil {
		old := m.Model
		m.Model = strings.TrimSpace(*body.Model)
		if old != m.Model {
			notes = append(notes, ctxNote{ctxModel, "模型：" + old + " → " + m.Model})
		}
	}
	permWas := permModeOf(m)                          // 权限旧值：SaveMeta 后生成【权限变更】注入用
	permWasTools := append([]string(nil), m.Tools...) //（须在 Permission/Tools 两个分支改动之前捕获）
	if body.Permission != nil {
		was := permModeOf(m)
		wasTools := m.Tools
		m.Permission = strings.TrimSpace(*body.Permission)
		switch {
		case permModeOf(m) == permFull:
			m.Tools = bundleFullAccessCaps(m.Tools) // 切到完全访问：捆绑能力联动点亮
		case was == permFull && body.Tools == nil:
			// 从完全访问切回：捆绑能力随之取消，但默认能力要补回来
			//（工作区内读写默认开启「操作浏览器」——切回时同时打开它）
			m.Tools = mergeCapDefaults(stripFullAccessCaps(m.Tools), m.Mode, permModeOf(m))
		}
		if was != permModeOf(m) {
			notes = append(notes, ctxNote{ctxPerm, "权限模式：" + was + " → " + permModeOf(m)})
		} else if fmt.Sprint(wasTools) != fmt.Sprint(m.Tools) && body.Tools == nil {
			notes = append(notes, ctxNote{ctxPerm, "能力开关随权限联动：" + toolIDsOf(wasTools) + " → " + toolIDsOf(m.Tools)})
		}
	}
	if body.Thinking != nil {
		switch t := strings.TrimSpace(*body.Thinking); t {
		case "", "enabled", "disabled", "low", "medium", "high":
			old := m.Thinking
			m.Thinking = t
			if old != t {
				notes = append(notes, ctxNote{ctxThinking, "思考档位：" + old + " → " + t})
			}
		default:
			writeErr(w, 400, "thinking 档位必须是 enabled/disabled/low/medium/high 或空")
			return
		}
	}
	if body.PlanMode != nil {
		old := m.PlanMode
		m.PlanMode = *body.PlanMode
		if old != m.PlanMode {
			notes = append(notes, ctxNote{ctxMode, "计划模式：" + boolZh(old) + " → " + boolZh(m.PlanMode)})
		}
	}
	if body.GoalText != nil {
		m.GoalText = truncateRunes(*body.GoalText, 2000)
	}
	if body.GoalMode != nil {
		old := m.GoalMode
		m.GoalMode = *body.GoalMode
		if old != m.GoalMode {
			notes = append(notes, ctxNote{ctxMode, "目标模式：" + boolZh(old) + " → " + boolZh(m.GoalMode)})
		}
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
		old := m.Tools
		m.Tools = *body.Tools
		if fmt.Sprint(old) != fmt.Sprint(m.Tools) {
			notes = append(notes, ctxNote{ctxPerm, "能力开关：" + toolIDsOf(old) + " → " + toolIDsOf(m.Tools)})
		}
	}
	if err := a.Store.SaveMeta(m); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, n := range notes {
		a.ctxEngine().Notify(m, n.kind, n.detail)
	}
	// 环境快照正管的条目（权限/能力/计划目标/工作目录）在 PATCH 落库后立即
	// 重算并按需落新快照——变更瞬间即进时间线与下一次组装的上下文，不等下一
	// 次模型调用；下一次组装做等值比较，不会重复落。env「变化驱动」约束的是
	// 发给模型的差分重发，不约束落库时机。权限变更注记随这份快照落库；
	// emit 广播带 SessionID：空闲会话的前端也能实时翻新「运行环境更新」。
	envTouched := false
	for _, n := range notes {
		if n.kind == ctxPerm || n.kind == ctxMode || n.kind == ctxWorkspace {
			envTouched = true
		}
	}
	if envTouched {
		if note := permChangeNote(permSrcPanel, permWas, permWasTools, m); note != "" {
			a.stashPermNote(m.ID, note)
		}
		a.maybeAppendEnvSnapshot(m, a.envPrompt(m), func(ev Event) { ev.SessionID = m.ID; a.broadcast(ev) })
	}
	writeJSON(w, 200, m)
}

// toolIDsOf / boolZh 是上下文事件说明的小格式化辅助（人读一句话进时间线）。
func toolIDsOf(ids []string) string {
	if len(ids) == 0 {
		return "（无）"
	}
	return strings.Join(ids, ",")
}
func boolZh(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func (a *App) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	oldWS := ""
	if m, err := a.Store.Get(id); err == nil {
		oldWS = m.Workspace
	}
	a.closeRemoteSession(id) // 断开该会话的 SSH 连接
	if err := a.Store.Delete(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 上传登记与待执行交付一并清掉：用户删会话即放弃这些（done/failed 的
	// 历史作业行留着无妨，量小且可追溯）
	_ = a.Store.DeleteUploadsForSession(id)
	_ = a.Store.DeletePendingJobsForSession(id)
	a.scheduleResidualCleanup(oldWS) // 会话结束：清本产品的常驻授权残留
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// scheduleResidualCleanup 异步清理旧工作区上本产品的沙箱常驻授权残留
// （S-1-80-* 合成 ACE 与 Low 标签）。仅当没有任何会话仍引用该工作区时执行
// （常驻授权是跨会话复用缓存，有人用就留着）；DSH 官方 S-1-4-* 旧残留属
// 用户确认范畴（sandbox acl-audit --revoke）。失败不影响主流程。
func (a *App) scheduleResidualCleanup(workspace string) {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return
	}
	go func() {
		sessions, err := a.Store.List()
		if err != nil {
			return
		}
		for _, s := range sessions {
			if s.Workspace == ws {
				return // 仍有会话引用，保留常驻授权
			}
		}
		if n, err := cleanupResidualGrants([]string{ws}); err == nil && n > 0 {
			log.Printf("已清理工作区 %s 的 %d 项沙箱残留授权", ws, n)
		}
	}()
}

// startupIntegrityLabelRestore 启动期把历史密级沙箱（机制已撤除）打在
// 工作区上的常驻 Low 完整性标签还原为 Medium。收集配置工作区与全部会话
// 工作区，异步执行、失败只记日志（WO 引导三步见 restoreStaleIntegrityLabels）。
func (a *App) startupIntegrityLabelRestore(primary string) {
	roots := map[string]bool{}
	if p := strings.TrimSpace(primary); p != "" {
		roots[p] = true
	}
	if a.Store != nil {
		if ss, err := a.Store.List(); err == nil {
			for _, s := range ss {
				if p := strings.TrimSpace(s.Workspace); p != "" {
					roots[p] = true
				}
			}
		}
	}
	if len(roots) == 0 {
		return
	}
	list := make([]string, 0, len(roots))
	for p := range roots {
		list = append(list, p)
	}
	n, err := restoreStaleIntegrityLabels(list)
	switch {
	case err != nil:
		log.Printf("还原历史 Low 完整性标签：完成 %d 个工作区后部分失败: %v", n, err)
	case n > 0:
		log.Printf("已还原历史密级沙箱残留的 Low 完整性标签（%d 个工作区: %v）", n, list)
	}
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
	// Claude Code 插件：PreCompact hook（阻断即取消本次压缩）
	if a.CCPlugins != nil && a.CCPlugins.hasAnyHooks() {
		if oc := a.ccEmitHookEvent(r.Context(), m, "PreCompact", "", nil, nil); oc.Block {
			writeErr(w, 403, "压缩被插件 hook 拒绝："+oc.Reason)
			return
		}
	}
	summary, err := a.doSummarize(m, "manual", a.Cfg.KeepRounds)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
	writeJSON(w, 200, map[string]any{"ok": 1, "summary": truncateRunes(summary, 300)})
}

// handleExport 导出两类审计材料，统一向上下文引擎申请（引擎是记录者）：
//
//	?format=md（默认）——运行日志：从会话创建起的完整时间线（上下文组装、
//	  环境快照更新、用户消息、每次模型调用的前缀指纹/用量/缓存命中、模型逐步
//	  输出、报错/停止/压缩/上下文事件等未发给模型的事件）；
//	?format=json ——原始数据：引擎缓存的最近三轮真正发给模型的完整请求体
//	  （与线上发送逐字节同源）+ 相邻两两对比（消息条数/token 估算/工具目录
//	  指纹/首条变化消息位置），用于核对到底给模型发了什么、缓存为何变化。
//
// 两者都取自 messages 表原始历史，只导出当前会话（子会话各自单独导出）。
func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	stamp := time.Now().Format("20060102-150405")
	base := strings.ReplaceAll(truncateRunes(m.Title, 30), " ", "_") + "_" + stamp
	if r.URL.Query().Get("format") == "json" {
		data, err := a.ctxEngine().ExportRawRequests(m)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(base+"_模型请求.json"))
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(base+"_运行日志.md"))
	_, _ = w.Write([]byte(a.ctxEngine().ExportTimeline(m)))
}

func wsOr(a *App, m *SessionMeta) string {
	if m.Workspace != "" {
		return m.Workspace
	}
	return a.Cfg.Workspace
}

// handleUpload 接收文件上传：保存进会话工作区，并注入一条上传记录消息
// （模型在后续上下文里能看到，可用只读工具查看文件内容）。
// 两种载荷：multipart（浏览器/手机，只复制字节，原件路径拿不到）与
// JSON {path}（桌面端经 /api/pick-file 采集到的本机真实路径，会登记
// uploads 表并告知模型该路径可用作交付目标，见 handleUploadByPath）。
func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		a.handleUploadByPath(w, r, m)
		return
	}
	// 256MB：办公 PPT 常带大媒体轻松超 32MB，旧上限会把大文件静默截断成坏文件
	// （LimitReader 截到 32MB 照写不报错）。回环/局域网传 256MB 是用户自己的选择。
	if err := r.ParseMultipartForm(64 << 20); err != nil {
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
	data, err := io.ReadAll(io.LimitReader(file, 256<<20))
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
	_ = a.Store.AppendRow(m.ID, &StoredMessage{Message: TextMessage("user", note), Kind: kindReport, Origin: "upload", CreatedAt: now()})
	m.UpdatedAt = now()
	_ = a.Store.SaveMeta(m)
	a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
	writeJSON(w, 200, map[string]any{"ok": 1, "path": filepath.ToSlash(dst), "size": len(data)})
}

// handleUploadByPath 桌面端路径上传：前端经 /api/pick-file 拿到本机真实
// 路径后送到这里。与 multipart 不同的是原件路径被登记进 uploads 表——
// 它是 deliver_file 的交付目标来源，注记里会向模型说明这一点。
// 原件就在工作区内时不做复制（避免自己覆盖自己），只登记。
func (a *App) handleUploadByPath(w http.ResponseWriter, r *http.Request, m *SessionMeta) {
	if m.Remote != nil {
		writeErr(w, 400, "远程会话不支持按本机路径上传，请用网页文件上传")
		return
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
		writeErr(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	src := strings.TrimSpace(p.Path)
	if src == "" {
		writeErr(w, 400, "缺少 path 字段")
		return
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		writeErr(w, 400, "路径解析失败: "+err.Error())
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 400, "文件不存在或不可访问: "+abs)
		return
	}
	if fi.IsDir() {
		writeErr(w, 400, "路径是目录不是文件: "+abs)
		return
	}
	name := filepath.Base(abs)
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
	dst := filepath.Join(ws, name)
	inWorkspace := strings.EqualFold(canonicalFor(abs), canonicalFor(dst))
	if !inWorkspace {
		// 用户主动上传不走模型权限审批，直接复制进工作区（流式，不受
		// multipart 的 32MB 上限约束）
		if err := copyFileAtomic(dst, abs); err != nil {
			writeErr(w, 500, "复制进工作区失败: "+err.Error())
			return
		}
	}
	u := &Upload{SessionID: m.ID, OrigPath: abs, CopyPath: dst, Name: name, Size: fi.Size(), CreatedAt: now()}
	if err := a.Store.AddUpload(u); err != nil {
		writeErr(w, 500, "上传登记落库失败: "+err.Error())
		return
	}
	var note string
	if inWorkspace {
		note = fmt.Sprintf("【文件上传】%s《%s》（%d 字节）：文件位于工作区内 %s，该路径现在可用作交付工具（deliver_file）的交付目标。",
			u.UID, name, fi.Size(), filepath.ToSlash(dst))
	} else {
		note = fmt.Sprintf("【文件上传】%s《%s》（%d 字节）：原件位于 %s，该路径现在可用作交付工具（deliver_file）的交付目标；已复制一份到工作区 %s。",
			u.UID, name, fi.Size(), filepath.ToSlash(abs), filepath.ToSlash(dst))
	}
	_ = a.Store.AppendRow(m.ID, &StoredMessage{Message: TextMessage("user", note), Kind: kindReport, Origin: "upload", CreatedAt: now()})
	m.UpdatedAt = now()
	_ = a.Store.SaveMeta(m)
	a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
	writeJSON(w, 200, map[string]any{"ok": 1, "path": filepath.ToSlash(dst), "orig": filepath.ToSlash(abs), "uid": u.UID, "size": fi.Size()})
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
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		title = "选择工作目录" // 兼容旧调用方；同时也是找对话框窗口用的标题
	}
	path, err := pickFolderSerialized(start, title)
	if err != nil {
		writeErr(w, 500, "无法打开文件夹选择框: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"path": path})
}

// handlePickFile 同款系统原生对话框，但选择文件。默认过滤器限定 *.zip
// （技能 ZIP 安装包，旧调用方）；query 加 filter=all 时不设过滤器（任意
// 文件，聊天上传采集原件真实路径用），multi=1 允许多选。用户取消时
// paths 为空数组；返回体保留 path 字段（首个）兼容旧调用方。
func (a *App) handlePickFile(w http.ResponseWriter, r *http.Request) {
	start := strings.TrimSpace(r.URL.Query().Get("start"))
	if start == "" {
		start = a.Cfg.Workspace
	}
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		title = "选择文件"
	}
	allFilter := r.URL.Query().Get("filter") == "all"
	multi := r.URL.Query().Get("multi") == "1" || r.URL.Query().Get("multi") == "true"
	log.Printf("[pick-file] 打开原生文件选择框（filter=all:%v multi:%v，阻塞至用户关闭）", allFilter, multi)
	paths, err := pickFileSerialized(start, title, allFilter, multi)
	if err != nil {
		log.Printf("[pick-file] 失败: %v", err)
		writeErr(w, 500, "无法打开文件选择框: "+err.Error())
		return
	}
	log.Printf("[pick-file] 返回 %d 个路径", len(paths))
	first := ""
	if len(paths) > 0 {
		first = paths[0]
	}
	writeJSON(w, 200, map[string]any{"path": first, "paths": paths})
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
		Midrun  string `json:"midrun"` // 前端设置项 jx.midrun（queue/interject）的原值，记下来供派发注入按同一偏好处理
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Content) == "" {
		writeErr(w, 400, "content 不能为空")
		return
	}
	a.noteMidrunPref(body.Midrun)
	mode := "queue"
	if body.Mode == "steer" && a.IsRunning(m.ID) {
		mode = "steer"
	}
	pm := a.pushPending(m.ID, mode, strings.TrimSpace(body.Content), kindChat, "")
	if !a.IsRunning(m.ID) && !a.queuePausedOf(m.ID) {
		a.kickQueue(m) // 本轮已经结束：排队消息自己开一轮，不依赖用户再点一次（暂停时只排队不拉起）
	}
	writeJSON(w, 200, map[string]any{"accepted": true, "id": pm.ID, "mode": mode})
}

// handlePending 处理排队坞上的操作：删除（remove）、提前为插话（steer）、
// 拖拽排序（move，before=锚点项 id，空=队尾）与暂停/恢复队列（pause/resume，
// 这两个是会话级开关，不需要 id）。恢复时若会话空闲且有排队消息，顺手拉起执行。
func (a *App) handlePending(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Before string `json:"before"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	switch body.Action {
	case "pause":
		a.setQueuePaused(m.ID, true)
	case "resume":
		a.setQueuePaused(m.ID, false)
		if !a.IsRunning(m.ID) && len(a.pendingOf(m.ID).Queue) > 0 {
			a.kickQueue(m) // 空闲且有保留的排队消息：接着把它们发完
		}
	case "move":
		if body.ID == "" {
			writeErr(w, 400, "id 不能为空")
			return
		}
		if !a.movePending(m.ID, body.ID, body.Before) {
			writeErr(w, 404, "这条消息已经不在队列里了")
			return
		}
	default: // remove / steer
		if body.ID == "" {
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
		if x.Kind != "summary" && x.Kind != "request" && x.Kind != "audit" && (body.UpTo <= 0 || x.Seq <= body.UpTo) {
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
	if permModeOf(fork) == permFull {
		fork.Tools = bundleFullAccessCaps(fork.Tools) // 分支照抄父会话；完全访问照捆绑点亮
	} else {
		// 分支也是"新建的会话"：按默认策略补回默认能力（与创建路径同一函数）
		fork.Tools = mergeCapDefaults(fork.Tools, fork.Mode, permModeOf(fork))
	}
	if err := a.Store.Create(fork); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	seqMap := map[int64]int64{} // 源 seq → 新会话 seq（AppendRow 顺序分配），供快照/交付按图复制
	for _, x := range msgs {
		row := &StoredMessage{Message: x.Message, Kind: x.Kind, CreatedAt: fork.CreatedAt}
		if err := a.Store.AppendRow(fork.ID, row); err == nil {
			seqMap[x.Seq] = row.Seq
		}
	}
	// 文件快照与交付登记跟着消息一起搬过去：分支里同样能回退文件、看交付
	if pairs, err := a.Store.CopySnapshots(m.ID, fork.ID, seqMap); err != nil {
		log.Printf("[快照] 分支 %s 复制快照失败（分支仍可用，只是缺文件历史）: %v", fork.ID, err)
	} else {
		a.copySnapshotFiles(m, fork, pairs)
	}
	// 上传登记同迁（uid 原样保留）：分支历史注记里的 u 编号继续可解析
	if err := a.Store.CopyUploadsOnBranch(m.ID, fork.ID); err != nil {
		log.Printf("[上传] 分支 %s 复制上传登记失败: %v", fork.ID, err)
	}
	(&toolContext{app: a, sess: fork}).ensureJxAI()
	a.broadcast(Event{Type: "session_updated", SessionID: fork.ID})
	writeJSON(w, 200, fork)
}

// handleEdit 编辑历史用户消息并重发：截断该消息之后的全部记录、改写正文、
// 立即续跑一轮。语义对齐 ZCode 的 editUserQuery——同会话内截断重发；文件
// 回退是可选档（workspaceMode=rewind，对齐其"对话+文件重置"提交按钮）：
// 先做安全预检（fail-closed：有外部修改过的文件就整体拒绝并回清单），
// 文件全部恢复成功才截断对话——文件与对话要么都退、要么都不退。
// 仅空闲会话可编辑；系统注入行（回报/上传/目标续跑/继承标记）不可编辑。
func (a *App) handleEdit(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		Seq           int64  `json:"seq"`
		Content       string `json:"content"`
		WorkspaceMode string `json:"workspaceMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Seq <= 0 || strings.TrimSpace(body.Content) == "" {
		writeErr(w, 400, "需要 seq 与非空 content")
		return
	}
	if body.WorkspaceMode != "preserve" && body.WorkspaceMode != "" && body.WorkspaceMode != "rewind" {
		writeErr(w, 400, "workspaceMode 只能是 preserve 或 rewind")
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, 500, "当前连接不支持流式响应")
		return
	}
	lock := a.sessLock(m.ID)
	lock.Lock()
	if a.IsRunning(m.ID) {
		lock.Unlock()
		writeErr(w, 409, "该会话正在执行中，等本轮结束后再编辑")
		return
	}
	rows, err := a.Store.RawMessagesAll(m.ID)
	if err != nil {
		lock.Unlock()
		writeErr(w, 500, err.Error())
		return
	}
	valid := false
	for _, x := range rows {
		if x.Seq == body.Seq {
			valid = x.Role == "user" && x.Kind == "chat"
			break
		}
	}
	if !valid {
		lock.Unlock()
		writeErr(w, 400, "只有真实的用户消息可以编辑")
		return
	}
	// 文件回退：预检不安全就整体拒绝（对话不裁剪），把清单回给前端选"仅重发对话"
	var restored []*rewindPlan
	if body.WorkspaceMode == "rewind" {
		tctx := &toolContext{app: a, sess: m}
		safe, unsafe, _, perr := a.planRewind(tctx, m.ID, body.Seq, nil, "")
		if perr != nil {
			lock.Unlock()
			writeErr(w, 500, "文件回退预检失败: "+perr.Error())
			return
		}
		if len(unsafe) > 0 {
			lock.Unlock()
			writeJSON(w, 409, map[string]any{
				"error":   "有文件在快照之后被外部修改，无法安全回退；可改为仅重发对话（不动文件）",
				"blocked": true, "unsafe": unsafe, "safe": safe,
			})
			return
		}
		restored = safe
	}
	content := strings.TrimSpace(body.Content)
	doomed, _ := a.Store.CheckpointsAfter(m.ID, body.Seq) // 截断将删掉的快照（拿 id 删正文文件）
	if _, err := a.Store.EditUserMessage(m.ID, body.Seq, content); err != nil {
		lock.Unlock()
		writeErr(w, 500, "编辑落库失败: "+err.Error())
		return
	}
	if len(doomed) > 0 {
		ids := make([]int64, 0, len(doomed))
		for _, cp := range doomed {
			ids = append(ids, cp.ID)
		}
		(&toolContext{app: a, sess: m}).deleteSnapshotFiles(ids)
	}
	if restored != nil {
		tctx := &toolContext{app: a, sess: m}
		if aerr := a.applyRewind(tctx, restored); aerr != nil {
			lock.Unlock()
			log.Printf("[快照] 会话 %s 编辑回退文件失败（已尝试补偿）: %v", m.ID, aerr)
			writeErr(w, 500, "文件回退失败（已自动恢复原状）: "+aerr.Error())
			return
		}
	}
	run, ok := a.beginRun(m.ID)
	if !ok {
		lock.Unlock()
		writeErr(w, 409, "该会话正在执行中，等本轮结束后再编辑")
		return
	}
	lock.Unlock()
	// opening 先发 edited 确认帧：前端据此把本地记录截断对齐，再跟随续跑输出
	opening := []Event{{Type: "edited", Seq: body.Seq, Delta: content}}
	a.serveRun(w, r, m, run, opening)
}

// handleFileRewind 撤销文件改动：不裁剪对话，只把范围内的文件改动恢复到
// 该范围之前（对齐 ZCode 文件摘要面板上的「撤销」）。三种取数口径：
//   - seqs：按轮撤销（文件改动面板的「全部撤回」）；
//   - path：只撤销这一个文件（折叠行内每行文件的「撤回」），仍限定在 seqs 内；
//   - checkpointId：只恢复到某一条快照之前（历史版本列表里的「恢复到此版本前」）。
//
// 同样 fail-closed：有不安全文件就整体拒绝；成功后落一条可见交代，并只清掉
// 真正用掉的这批快照（其余文件的快照继续留在库里，仍能再次撤回）。
func (a *App) handleFileRewind(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		Seqs         []int64 `json:"seqs"`
		Path         string  `json:"path"`
		CheckpointID int64   `json:"checkpointId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		(len(body.Seqs) == 0 && body.CheckpointID == 0) {
		writeErr(w, 400, "需要非空 seqs 或 checkpointId")
		return
	}
	if a.IsRunning(m.ID) {
		writeErr(w, 409, "该会话正在执行中，等本轮结束后再撤销")
		return
	}
	tctx := &toolContext{app: a, sess: m}
	path := strings.TrimSpace(body.Path)
	var cps []*Checkpoint
	var err error
	if body.CheckpointID != 0 {
		// 单条快照（历史版本）：只回退该文件到这次改动之前，不看消息序号
		cps, err = a.Store.CheckpointsByIDs(m.ID, []int64{body.CheckpointID})
		if err == nil && len(cps) == 0 {
			writeErr(w, 404, "快照不存在（可能已被清理）")
			return
		}
		if path == "" && len(cps) > 0 {
			path = cps[0].Path
		}
	} else {
		cps, err = a.Store.CheckpointsIn(m.ID, body.Seqs)
		if err == nil && path != "" {
			kept := cps[:0:0]
			for _, cp := range cps {
				if cp.Path == path {
					kept = append(kept, cp)
				}
			}
			cps = kept
		}
	}
	if err != nil {
		writeErr(w, 500, "取快照失败: "+err.Error())
		return
	}
	safe, unsafe, consumed := a.planRewindFor(tctx, cps)
	if len(unsafe) > 0 {
		writeJSON(w, 409, map[string]any{
			"error":   "有文件在快照之后被外部修改，无法安全回退",
			"blocked": true, "unsafe": unsafe, "safe": safe,
		})
		return
	}
	if len(safe) == 0 {
		// 没有可执行项（如"撤回新建"但文件已不在）：目标状态已达成，照样清快照
		if len(consumed) > 0 {
			_ = a.Store.DeleteCheckpoints(m.ID, consumed)
			tctx.deleteSnapshotFiles(consumed)
			a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
		}
		writeJSON(w, 200, map[string]any{"restored": 0, "deleted": 0})
		return
	}
	if err := a.applyRewind(tctx, safe); err != nil {
		log.Printf("[快照] 会话 %s 撤销文件改动失败（已尝试补偿）: %v", m.ID, err)
		writeErr(w, 500, "文件回退失败（已自动恢复原状）: "+err.Error())
		return
	}
	// 这批快照已完成使命：状态已还原，留着会让下次回退拿到错误基线
	if len(consumed) > 0 {
		_ = a.Store.DeleteCheckpoints(m.ID, consumed)
		tctx.deleteSnapshotFiles(consumed)
	}
	names := []string{}
	restored, deleted := 0, 0
	for _, p := range safe {
		if p.Action == "delete" {
			deleted++
		} else {
			restored++
		}
		names = append(names, p.Path)
	}
	note := fmt.Sprintf("【文件回退】用户撤销了文件改动：恢复 %d 个文件、撤回 %d 个新建文件（%s）。",
		restored, deleted, strings.Join(names, "、"))
	_ = a.Store.AppendRow(m.ID, &StoredMessage{Message: TextMessage("user", note), Kind: kindReport, Origin: "rollback", CreatedAt: now()})
	m.UpdatedAt = now()
	_ = a.Store.SaveMeta(m)
	a.broadcast(Event{Type: "session_updated", SessionID: m.ID})
	writeJSON(w, 200, map[string]any{"restored": restored, "deleted": deleted, "paths": names})
}

// handleFileVersions 列出某文件的历史版本（快照）清单——文件改动面板每行
// 的「历史版本」按钮取数。path 用工作区相对路径（与快照同一口径）。同时回
// 当前文件是否存在与体积，供前端标注"当前"与"已删除"。
func (a *App) handleFileVersions(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeErr(w, 400, "需要 path")
		return
	}
	tctx := &toolContext{app: a, sess: m}
	vers, err := a.fileVersionList(tctx, path)
	if err != nil {
		writeErr(w, 500, "取历史版本失败: "+err.Error())
		return
	}
	payload := map[string]any{"path": path, "versions": vers, "exists": false, "size": 0}
	if data, exists, _ := tctx.workspaceFileBody(path); exists {
		payload["exists"] = true
		payload["size"] = len(data)
	}
	writeJSON(w, 200, payload)
}

// handleFileVersion 取一个历史版本的正文，并附带文件当前内容（供前端就地
// 对比）。版本正文缺失（.jxai/snapshots 正文文件被清掉）时 hasBody=false，
// 前端只展示元数据、不给"恢复"入口。
func (a *App) handleFileVersion(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	id := int64(0)
	if s := strings.TrimSpace(r.URL.Query().Get("id")); s != "" {
		if _, err := fmt.Sscanf(s, "%d", &id); err != nil || id == 0 {
			writeErr(w, 400, "id 不是合法快照 id")
			return
		}
	}
	if path == "" || id == 0 {
		writeErr(w, 400, "需要 path 与 id")
		return
	}
	tctx := &toolContext{app: a, sess: m}
	cps, err := a.Store.CheckpointsByIDs(m.ID, []int64{id})
	if err != nil {
		writeErr(w, 500, "取快照失败: "+err.Error())
		return
	}
	if len(cps) == 0 || cps[0].Path != path {
		writeErr(w, 404, "该文件的这条历史版本不存在（可能已被清理）")
		return
	}
	cp := cps[0]
	data, truncated, ok := tctx.fileVersionBody(cp)
	payload := map[string]any{
		"path": cp.Path, "id": cp.ID, "seq": cp.Seq, "toolName": cp.ToolName,
		"existedBefore": cp.ExistedBefore, "afterSize": cp.AfterSize,
		"createdAt": cp.CreatedAt, "manual": cp.ToolName == manualSnapshotTool,
		"hasBody": ok,
	}
	if ok {
		appendVersionBody(payload, data, truncated)
	}
	cur, exists, curTrunc := tctx.workspaceFileBody(path)
	curPayload := map[string]any{"exists": exists}
	if exists {
		appendVersionBody(curPayload, cur, curTrunc)
	}
	payload["current"] = curPayload
	writeJSON(w, 200, payload)
}

// handleDeliveryOpenFile 用系统默认程序打开一个已登记的交付文件（交付卡片
// 整行点击/「打开」按钮）。只放行 deliveries 里登记过的路径——用户主动操作，
// 不经模型权限审批；未登记的任意路径一律 404，防越权打开。
func (a *App) handleDeliveryOpenFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	want := strings.TrimSpace(body.Path)
	if want == "" {
		writeErr(w, 400, "需要 path")
		return
	}
	if !a.deliveryListed(m.ID, want) {
		http.NotFound(w, r)
		return
	}
	tctx := &toolContext{app: a, sess: m}
	abs, err := tctx.resolve(want)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if isDir, _, serr := tctx.envStat(abs); serr != nil || isDir {
		writeErr(w, 404, "交付文件不存在: "+want)
		return
	}
	if m.Remote != nil {
		writeErr(w, 400, "这是 SSH 远程工作区里的文件，本机没有对应路径，无法用系统程序打开；请在右侧浏览器中查看内容")
		return
	}
	if err := openPathWithDefaultApp(abs); err != nil {
		writeErr(w, 500, "打开失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleDeliveryReveal 在系统文件资源管理器里定位一个已登记的交付文件
// （交付卡片的「在文件夹中查看」）。远程会话没有本机路径，明确拒绝。
func (a *App) handleDeliveryReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	want := strings.TrimSpace(body.Path)
	if want == "" {
		writeErr(w, 400, "需要 path")
		return
	}
	if !a.deliveryListed(m.ID, want) {
		http.NotFound(w, r)
		return
	}
	if m.Remote != nil {
		writeErr(w, 400, "这是 SSH 远程工作区里的文件，本机没有对应路径，无法在资源管理器中定位")
		return
	}
	tctx := &toolContext{app: a, sess: m}
	abs, err := tctx.resolve(want)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if isDir, _, serr := tctx.envStat(abs); serr != nil || isDir {
		writeErr(w, 404, "交付文件不存在: "+want)
		return
	}
	if err := revealInExplorer(abs); err != nil {
		writeErr(w, 500, "打开所在文件夹失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// deliveryListed 判定 path 是否在本会话的交付登记清单里（交付卡片的打开/
// 定位入口共用；未登记路径不动作，防越权）。
func (a *App) deliveryListed(sessionID, path string) bool {
	dvs, err := a.Store.DeliveriesFor(sessionID)
	if err != nil {
		return false
	}
	for _, d := range dvs {
		if d.Path == path {
			return true
		}
	}
	return false
}

// handleOpenURL 用系统默认浏览器打开一个 http(s) 链接（桌面壳右键菜单的
// 「在系统浏览器中打开」）。只放行 http/https——file://、javascript: 等
// 一律拒绝；rundll32 FileProtocolHandler 对 URL 的处理即交给默认浏览器
// （非 Windows 平台 xdg-open/open 同理）。用户主动操作，不经模型权限门控。
func (a *App) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	raw := strings.TrimSpace(body.URL)
	if raw == "" {
		writeErr(w, 400, "需要 url")
		return
	}
	if len(raw) > 2048 {
		writeErr(w, 400, "url 过长")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, 400, "仅支持打开 http/https 链接")
		return
	}
	if err := openPathWithDefaultApp(raw); err != nil {
		writeErr(w, 500, "打开失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleSessionOpenPath 用系统默认程序打开会话工作区里的一个文件或目录
// （右键菜单「在系统中打开」：文件走默认程序，目录等于在资源管理器里打开）。
// 边界与 /api/browser/open-view 同口径：tctx.resolve 把路径限制在会话工作区
// 内、envStat 确认存在；远程会话没有本机路径，明确拒绝（文案同交付卡片）。
func (a *App) handleSessionOpenPath(w http.ResponseWriter, r *http.Request) {
	a.handleSessionOpenOrReveal(w, r, true)
}

// handleSessionRevealPath 在资源管理器里定位会话工作区里的一个文件或目录
// （右键菜单「在文件夹中定位」），校验口径与 open-path 完全一致。
func (a *App) handleSessionRevealPath(w http.ResponseWriter, r *http.Request) {
	a.handleSessionOpenOrReveal(w, r, false)
}

func (a *App) handleSessionOpenOrReveal(w http.ResponseWriter, r *http.Request, open bool) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	want := strings.TrimSpace(body.Path)
	if want == "" {
		writeErr(w, 400, "需要 path")
		return
	}
	if m.Remote != nil {
		writeErr(w, 400, "这是 SSH 远程工作区里的路径，本机没有对应文件，无法用系统程序打开")
		return
	}
	tctx := &toolContext{app: a, sess: m}
	abs, err := tctx.resolve(want)
	if err != nil || !insideWorkspace(tctx, abs) {
		http.NotFound(w, r)
		return
	}
	if _, _, serr := tctx.envStat(abs); serr != nil {
		writeErr(w, 404, "路径不存在: "+want)
		return
	}
	var opErr error
	if open {
		opErr = openPathWithDefaultApp(abs)
	} else {
		opErr = revealInExplorer(abs)
	}
	if opErr != nil {
		writeErr(w, 500, "打开失败: "+opErr.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleClipboardRead 读系统剪贴板（桌面壳右键「粘贴」的兜底：WebView2 里
// navigator.clipboard.readText 会永久挂起，权限态停在 prompt 且没有授权 UI，
// 见 clipboard_windows.go 头注）。返回 {text} 与可选 {png}（截图，base64）。
// 剪贴板属敏感数据：仅回环可达（loopbackOnly，与设备桥同闸），LAN/远程一律 403。
func (a *App) handleClipboardRead(w http.ResponseWriter, r *http.Request) {
	if !loopbackOnly(w, r) {
		return
	}
	text, png, err := readClipboardWindows()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp := map[string]string{"text": text}
	if len(png) > 0 {
		resp["png"] = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, 200, resp)
}

// handleDiag 前端诊断打点：页面探针（web/app-chat.js 的 jxDiag）把关键分支的
// 执行结果经 1px GIF beacon 送到这里落一行日志。桌面壳 WebView2 封了远程调试
// 端口、也没有 console 可看，「点了没反应 / 请求没发出去」这类前端问题只能靠
// 它取证。只写日志不回数据（GIF 空体即可），消息截断防刷屏；走 withCORS 的
// 同源校验与访问日志，不留额外攻击面。
func (a *App) handleDiag(w http.ResponseWriter, r *http.Request) {
	m := r.URL.Query().Get("m")
	if len(m) > 500 {
		m = m[:500] + "…"
	}
	log.Printf("[diag] %s", m)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/gif")
	// 1x1 透明 GIF：Image beacon 只需要一个能解码的图
	_, _ = w.Write([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
}

// handleDeliveryDownload 提供交付文件的下载。只放行已登记进 deliveries 的
// 路径（用户主动下载，不走模型权限审批；未登记的任意路径一律 404，防越权读）。
func (a *App) handleDeliveryDownload(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	want := strings.TrimSpace(r.URL.Query().Get("path"))
	if want == "" {
		writeErr(w, 400, "需要 path")
		return
	}
	dvs, err := a.Store.DeliveriesFor(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	listed := false
	for _, d := range dvs {
		if d.Path == want {
			listed = true
			break
		}
	}
	if !listed {
		http.NotFound(w, r)
		return
	}
	tctx := &toolContext{app: a, sess: m}
	abs, err := tctx.resolve(want)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := tctx.envReadFile(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := path.Base(filepath.ToSlash(want))
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
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

// handleGetProgress 返回会话的进度清单（progress_write 工具的持久化结果）。
func (a *App) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	writeJSON(w, 200, map[string]any{
		"progress": progressListOf(a, m.ID),
		"running":  runningChildrenOf(a, m.ID), // 「运行中的程序」：本会话在跑的子进程（child.go）
	})
}

// handleContextStats 返回会话下一次请求的上下文占用分解与缓存命中率
// （前端输入区「上下文」按钮面板的数据源）。
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
	if m.Remote != nil {
		a.ctxEngine().Notify(m, ctxRemote, "挂载远程工作区："+m.Remote.Display())
	} else {
		a.ctxEngine().Notify(m, ctxRemote, "解除远程工作区，回到本地工作区")
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
		Midrun  string `json:"midrun"` // 前端设置项 jx.midrun 的原值（queue/interject）：记下来供派发注入按同一偏好处理
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Content) == "" {
		writeErr(w, 400, "content 不能为空")
		return
	}
	a.noteMidrunPref(body.Midrun)
	// Claude Code 插件：斜杠命令展开（插件命令正文或 mod 命令执行；未命中原样
	// 发送，不影响普通输入）
	if a.CCPlugins != nil {
		body.Content = a.ccExpandSlashMessage(r.Context(), m, body.Content)
	}
	// Claude Code 插件：UserPromptSubmit hook（可拒绝消息或附加上下文）
	var ccExtraContext string
	if a.CCPlugins != nil && a.CCPlugins.hasAnyHooks() {
		outcome := a.ccEmitHookEvent(r.Context(), m, "UserPromptSubmit", "", nil,
			map[string]any{"prompt": body.Content})
		if outcome.Block {
			writeErr(w, 403, "消息被插件 hook 拒绝："+outcome.Reason)
			return
		}
		if outcome.SystemMsg != "" {
			a.broadcast(Event{Type: "report", SessionID: m.ID, Output: outcome.SystemMsg})
		}
		ccExtraContext = outcome.ExtraContext
	}
	// Claude Code 插件 mods：prompt.submit（可改写文本或丢弃）
	if a.CCPlugins != nil && a.CCPlugins.hasMods() {
		oc := a.ccEmitModEvent(r.Context(), m, "prompt.submit", map[string]any{"text": body.Content})
		if oc.Answer != nil {
			if reason, ok := oc.Answer["drop"].(string); ok {
				writeErr(w, 403, "消息被 mod 丢弃："+reason)
				return
			}
		}
		if t, ok := oc.Rewrite["text"].(string); ok && strings.TrimSpace(t) != "" {
			body.Content = t
		}
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
	opening := []Event{{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: kindChat}}
	// UserPromptSubmit 的 additionalContext：用户消息后落一条系统注入行（发给模型）
	if ccExtraContext != "" {
		rm := a.landUserSide(m, ccExtraContext, kindReport, "plugin-hook", func(ev Event) {
			opening = append(opening, ev)
		})
		_ = rm
	}
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

// handleRetry 失败后重试：不重复落库用户消息，直接带着当前上下文再跑一轮
// （上一轮报错时用户消息已经在库里，重发只会多出一条重复的用户消息）。
func (a *App) handleRetry(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	lock := a.sessLock(m.ID)
	lock.Lock()
	if a.IsRunning(m.ID) {
		lock.Unlock()
		writeJSON(w, 200, map[string]any{"started": false, "reason": "该会话正在执行中"})
		return
	}
	run, ok := a.beginRun(m.ID)
	lock.Unlock()
	if !ok {
		writeJSON(w, 200, map[string]any{"started": false, "reason": "该会话正在执行中"})
		return
	}
	a.clearRunError(m.ID)
	a.serveRun(w, r, m, run, []Event{{Type: "retry"}})
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
