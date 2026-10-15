package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
)

// genDeviceID 生成远程接入设备号：jx- + 6 位易抄写的字符（去掉 0/o/1/i
// 这类易混字符）。手机入口路径 /a/<设备号> 用它，网关上全网唯一。
func genDeviceID() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	out := make([]byte, 6)
	for i := range out {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		out[i] = alphabet[n.Int64()]
	}
	return "jx-" + string(out)
}

// genSecret 生成 n 个十六进制字符的随机密钥（配对密钥等）。
func genSecret(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// ensureRemoteIdentity 远程接入的身份补全：设备号/配对密钥为空时随机生成，
// 并立即回写配置——不落盘的话每次启动都会换身份，手机存的入口就失效了。
func ensureRemoteIdentity(cfg *Config, path string) {
	generated := false
	if cfg.RemoteAccess.MyID == "" {
		cfg.RemoteAccess.MyID = genDeviceID()
		generated = true
	}
	if cfg.RemoteAccess.PairKey == "" {
		cfg.RemoteAccess.PairKey = genSecret(32)
		generated = true
	}
	if generated {
		if err := cfg.Save(path); err != nil {
			log.Printf("远程接入设备号/密钥生成后回写失败: %v", err)
		}
	}
}

// Config 为全局运行配置，落盘为 JSON，可在前端设置页在线修改。
// BaseURL/APIKey/Model 是单模型时代的旧字段：为兼容老配置保留，
// 加载时若 Providers 为空会折算成第一个 provider，保存时反向同步。
type Config struct {
	Port       int           `json:"port"`
	BindHost   string        `json:"bindHost"`
	BaseURL    string        `json:"baseURL"`
	APIKey     string        `json:"apiKey"`
	Model      string        `json:"model"`
	Providers  []ProviderCfg `json:"providers"`
	Workspace  string        `json:"workspace"`
	MaxSteps   int           `json:"maxSteps"`
	AllowShell bool          `json:"allowShell"`
	DataDir    string        `json:"dataDir"`
	SkillsDir  string        `json:"skillsDir"` // 技能目录；空 = 可执行文件旁的 技能/（不可写时落数据目录）
	// 上下文压缩设置
	CompactMode      string  `json:"compactMode"`      // rule=规则压缩(默认) llm=模型摘要 off=关闭
	KeepChars        int     `json:"keepChars"`        // 自动压缩的保留预算（字）：从最新一轮往前累计模型可见字数，装得下的整轮保留——轮是原子单位，至少保 1 轮
	CompactThreshold float64 `json:"compactThreshold"` // 自动压缩触发阈值（估算 token 占窗口比例，0.8/0.9）
	// 文件快照保留策略：按天超龄清理是主力（旧配置缺省回落默认值；-1 = 该项关闭）。
	// 自动池管 write/edit 的小快照；手动池（snapshot_file，往往是
	// Office 文档等大文件）默认不限量——清理靠 7 天过期；确有磁盘压力时再配
	// 500/1024 这一级别的大预算。SnapshotEnabled 是设置页「文件备份」总开关
	//（指针类型：配置文件里没写这一项 = 开启）。
	SnapshotEnabled     *bool `json:"snapshotEnabled,omitempty"`
	SnapshotDays        int   `json:"snapshotDays"`        // 快照保留天数，默认 7；-1=永不过期
	SnapshotMaxMB       int   `json:"snapshotMaxMB"`       // 每会话自动快照总量上限（MB），默认 32；-1=不限量
	SnapshotManualMaxMB int   `json:"snapshotManualMaxMB"` // 每会话手动快照总量上限（MB），默认 -1 不限量；需要时设 500/1024
	// 子对话专家（专家团）预设：dispatch_sub 按名称选用的命名预设（追加提示词/
	// 默认模型/上下文继承/启用开关）。配置文件没写过这一键时补种演示专家。
	Experts []ExpertCfg `json:"experts"`
	// 远程接入（公网中继，RustDesk 式）：出站长连注册到自建 210 网关的 /hub，
	// 手机等外部设备经网关的加密中继访问本服务；端到端 AES-256-GCM（密钥由
	// pairKey 派生），网关只见密文。与聊天服务的 p2p.js 同协议，共用同一网关。
	RemoteAccess RemoteAccessCfg `json:"remoteAccess"`
	// 市场（技能/组件/插件）服务器清单地址：指向 jxai-server 的
	// /market/manifest.json；空 = 未配置（市场页提示并给出与中继网关同 host 的建议值）。
	MarketURL string `json:"marketURL"`
	// 联网搜索服务端地址（web_search 工具的上游网关，jxai-server 的 /api/search，
	// 见 websearch.go）：空 = 跟随市场地址同源——市场与搜索本就同一台服务端。
	SearchURL string `json:"searchURL,omitempty"`
	// 发布者姓名（市场条目的归属名 owner）：发布技能时随包上报，市场卡片拿它
	// 与条目 owner 比对——只显示自己那些条目的「更新发布 / 删除」（见
	// market_publish.go 的 unpublish）。默认由首启向导里填的姓名写入（wizard.go）；
	// 没跑过向导的机器在 设置 · 网络访问 手动填。空 = 未设置。
	PublisherName string `json:"publisherName"`
	// 公司接入（向导，wizard.go）：wizardDone=已完成/跳过首启向导（不再自动弹）；
	// registryURL=总服务器地址（公司名发现不到时的查询兜底，空=只走局域网广播）；
	// wizardCompany/wizardServer 留给自动同步器（sync.go）：定期拉服务端 /announce
	// 跟随变化（模型清单/元数据、公司名、市场/更新/服务端地址），失联时按公司名
	// 重新局域网发现自动迁移（服务端换端口/换机器）。
	// companyLinkOff=公司接入整体停用（设置·网络访问的开关）：永不在公司使用的
	// 机器关掉后不再连服务端，首启向导也不弹；开启的一切（含已配好的内部模型
	// 通道）原样保留，重新打开即恢复同步与向导。
	WizardDone         bool   `json:"wizardDone"`
	RegistryURL        string `json:"registryURL"`
	WizardCompany      string `json:"wizardCompany,omitempty"`
	WizardServer       string `json:"wizardServer,omitempty"`
	WizardPublicServer string `json:"wizardPublicServer,omitempty"` // 服务端公网地址（仅由服务端公告/provision 下发，本地永不写死）：内网失联时改从它拉公告——公司通道配了外网地址就出公司也能用，否则自动停用
	WizardRev          string `json:"wizardRev,omitempty"`          // 最近应用的服务端公告哈希（仅记录）
	CompanyLinkOff     bool   `json:"companyLinkOff,omitempty"`     // 公司接入停用（设置·网络访问的开关）：给永不在公司、连不上服务端的机器——首启向导不弹（status 接口把它并进 wizardDone）、向导 discover/connect 拒绝、公司同步器（sync.go）停转；再开即恢复
	// 更新检查：定期 GET 该地址（默认 Gitea releases/latest API），响应里的
	// tag_name 比本程序版本新就经事件流给所有客户端推「新版本可用」。空串=关闭。
	UpdateURL string `json:"updateURL"`
	// 更新方式：auto（默认）=发布源支持时走分块静默更新（update_blocks.go）；
	// manual=只弹横幅提醒，由用户手动下载完整安装包更新。
	UpdateMode string `json:"updateMode,omitempty"`
	// 语音转写服务配置（演示记录器的讲解音频 → SRT 字幕，预留字段）：
	// provider（如 microsoft）/ endpoint / apiKey / language。当前版本尚未
	// 接入任何服务商（语音组件的朗读走 Edge 免密钥通道、听写走浏览器
	// SpeechRecognition，都不读这份配置）；接入后 recorder.go 的
	// activeSpeechTranscriber 读这里。
	SpeechAPI map[string]string `json:"speechApi,omitempty"`
	// 主题色（#rrggbb）：设置页换点缀色时同步写进来，独立 WebView 环境
	//（读不到主界面 localStorage）可从 /api/config 取它上色。
	AccentColor string `json:"accentColor,omitempty"`
	// 询问提醒（设置·常规）：提问/审批卡等待达到 AfterSec 仍未答（且用户未
	// 停表）时声光提醒——系统叮一声 + 全屏颜色闪烁 + 语音组件朗读 Text
	//（组件停用时只叮不读）。Enabled=nil 表示未写过该键＝默认开启。
	AskAlert AskAlertCfg `json:"askAlert,omitempty"`
	// 「执行结束后自动续发排队消息」（设置·常规）：本轮回复完成后，排队队列里
	// 的待发消息——真人消息 / 派发回报 / 派发需求 / 系统注入，**不分来源**——
	// 自动逐条作为独立一轮发出。nil 表示未写过该键＝默认开启；显式 false 才关闭：
	// 关掉后本轮结束队列保留，排队坞给「继续发送」入口（点它照发）。
	QueueAutoSend *bool `json:"queueAutoSend,omitempty"`
	// 「关闭窗口驻留托盘」（设置·常规，仅桌面版）：✕ / Alt+F4 关窗时只隐藏窗口、
	// 进程与后端（会话/任务）驻留，双击托盘图标回到界面。nil 表示未写过该键＝
	// 默认开启；显式 false 才关闭：关窗即真正退出程序。网页/手机形态无此语义。
	CloseToTray *bool `json:"closeToTray,omitempty"`
}

// AskAlertCfg 询问提醒配置段：Enabled=nil＝默认开启；AfterSec 触发秒数
//（默认 240=4 分钟，钳 30-600）；Text 朗读文案（空＝默认那句）。
type AskAlertCfg struct {
	Enabled  *bool  `json:"enabled,omitempty"`
	AfterSec int    `json:"afterSec,omitempty"`
	Text     string `json:"text,omitempty"`
}

// 询问提醒的默认朗读文案与触发秒数（含访问器的兜底，测试里 Cfg 为 nil 也安全）。
const (
	askAlertDefaultSec  = 240
	askAlertDefaultText = "领导，在吗？有个问题需要您审批确认一下。"
)

// askAlertOn 询问提醒是否开启（nil 接收器＝关：测试的裸 App 没有 Cfg）。
func (c *Config) askAlertOn() bool {
	if c == nil {
		return false
	}
	return c.AskAlert.Enabled == nil || *c.AskAlert.Enabled
}

// askAlertAfterSec 触发秒数（解析后的生效值，30-600）。
func (c *Config) askAlertAfterSec() int {
	v := askAlertDefaultSec
	if c != nil && c.AskAlert.AfterSec >= 30 && c.AskAlert.AfterSec <= 600 {
		v = c.AskAlert.AfterSec
	}
	return v
}

// askAlertText 朗读文案（空回填默认）。
func (c *Config) askAlertText() string {
	if c != nil {
		if s := strings.TrimSpace(c.AskAlert.Text); s != "" {
			return s
		}
	}
	return askAlertDefaultText
}

// askAlertDurText 提醒秒数的人类读法（240 →「4 分钟」、90 →「90 秒」），
// envPrompt 的提问等待公告用。
func askAlertDurText(sec int) string {
	if sec%60 == 0 {
		return fmt.Sprintf("%d 分钟", sec/60)
	}
	return fmt.Sprintf("%d 秒", sec)
}

// queueAutoSendOn 执行结束后是否自动续发排队消息（nil 键＝默认开；nil 接收器
// ＝开：测试里的裸 App 没有 Cfg）。
func (c *Config) queueAutoSendOn() bool {
	if c == nil {
		return true
	}
	return c.QueueAutoSend == nil || *c.QueueAutoSend
}

// closeToTrayOn 关闭窗口时是否驻留托盘（nil 键＝默认开；nil 接收器＝开：
// 测试里的裸 App 没有 Cfg）。仅桌面形态消费；见 desktop_windows.go 关窗分支。
func (c *Config) closeToTrayOn() bool {
	if c == nil {
		return true
	}
	return c.CloseToTray == nil || *c.CloseToTray
}

// RemoteAccessCfg 的 Host/Port 默认指向已跑通的 210 网关；MyID 与聊天服务的
// ds-main 错开（网关按设备号路由，各应用互不干扰）；RegKey 是网关信令准入 key。
type RemoteAccessCfg struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	MyID    string `json:"myId"`
	PairKey string `json:"pairKey"`
	RegKey  string `json:"regKey"`
}

// 步数上限（一条消息触发的任务里，模型调用＋工具执行的连续步数）的默认值：
// 既是新装的默认，也是升级时「还是旧默认、没人手动改过」的配置被提到的新值。
// 旧默认 30（太紧）、100（稍复杂的任务仍常跑满）都提到过；本次提到 200——
// 手动设过其他值的配置一律保留原值（见 LoadConfig 的兼容分支与
// maxStepsLegacyDefaults）。
const maxStepsDefault = 200

// maxStepsLegacyDefaults 是历次旧默认值：配置里的值正好等于其中之一，说明用户
// 从没手动改过（手动改过的不会是这些整数旧默认）——升级时随新默认一并提升，
// 用户自己设的值（如 300）不受影响。30=首版默认，100=上一次提升后的默认。
var maxStepsLegacyDefaults = []int{30, 100}

// isLegacyMaxSteps 该值是不是历次旧默认之一（升级提值的判定，见上面注释）。
func isLegacyMaxSteps(n int) bool {
	for _, v := range maxStepsLegacyDefaults {
		if n == v {
			return true
		}
	}
	return false
}

func DefaultConfig() *Config {
	wd, _ := os.Getwd()
	// 工作区默认目录：桌面 = 当前目录（安装形态锚到 exe 旁，见 main.go）；
	// 安卓壳经 JX_WORKSPACE_DEFAULT 注入 engineRoot/workspace——应用私有目录
	// 里单独的工作区子目录，引擎配置/数据/运行时不再泡在工作区里（边界清晰）。
	ws := wd
	if v := os.Getenv("JX_WORKSPACE_DEFAULT"); v != "" {
		ws = v
	}
	return &Config{
		Port:                8788,
		BindHost:            "127.0.0.1",
		BaseURL:             "https://api.deepseek.com/v1",
		APIKey:              "",
		Model:               "deepseek-chat",
		Workspace:           ws,
		MaxSteps:            maxStepsDefault,
		AllowShell:          true,
		DataDir:             "data",
		CompactMode:         "rule",
		KeepChars:           100000, // 10 万字 ≈ 7.5 万 token 的近期原文（各工具压缩后常见余量）
		CompactThreshold:    0.8,
		SnapshotDays:        7,
		SnapshotMaxMB:       32,
		SnapshotManualMaxMB: -1,
		SnapshotEnabled:     nil, // 缺省开启；nil 表示"未配置"，按开处理
		Experts:             defaultExperts(),
		RemoteAccess: RemoteAccessCfg{
			Enabled: false,
			Host:    "example.com",
			Port:    2227,
			MyID:    "", // 留空：首次加载时为每台安装生成唯一设备号（多人安装互不冲突）
			RegKey:  "chat_p2p_v1",
		},
		UpdateURL:  "https://example.com:PORT/api/v1/repos/your-org/your-repo/releases/latest",
		UpdateMode: "auto",
		AskAlert:   AskAlertCfg{AfterSec: askAlertDefaultSec, Text: askAlertDefaultText},
	}
}

// LoadConfig 读取配置文件；文件不存在时写入一份默认配置再返回。
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		ensureRemoteIdentity(cfg, path)
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	def := DefaultConfig()
	if cfg.Port == 0 {
		cfg.Port = def.Port
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = def.BaseURL
	}
	if cfg.Model == "" {
		cfg.Model = def.Model
	}
	if cfg.Workspace == "" {
		cfg.Workspace = def.Workspace
	}
	// 步数上限：老配置里还是旧默认（30 / 100）的（说明没人手动改过）随新默认
	// 提到 200，并在下面落盘；手动设过其他值的配置原样保留。缺这一项/写成 0
	// 仍按默认补齐。
	maxStepsBumped := false
	if isLegacyMaxSteps(cfg.MaxSteps) {
		cfg.MaxSteps = maxStepsDefault
		maxStepsBumped = true
	}
	if cfg.DataDir == "" {
		cfg.DataDir = def.DataDir
	}
	if cfg.BindHost == "" {
		cfg.BindHost = def.BindHost
	}
	if cfg.SnapshotDays == 0 {
		cfg.SnapshotDays = def.SnapshotDays
	}
	if cfg.SnapshotMaxMB == 0 {
		cfg.SnapshotMaxMB = def.SnapshotMaxMB
	}
	if cfg.SnapshotManualMaxMB == 0 {
		cfg.SnapshotManualMaxMB = def.SnapshotManualMaxMB
	}
	if cfg.CompactMode != "rule" && cfg.CompactMode != "llm" && cfg.CompactMode != "off" {
		cfg.CompactMode = def.CompactMode
	}
	if cfg.KeepChars <= 0 {
		cfg.KeepChars = def.KeepChars
	}
	if cfg.CompactThreshold <= 0.5 || cfg.CompactThreshold >= 1 {
		cfg.CompactThreshold = def.CompactThreshold
	}
	// 远程接入：老配置没有这一键时补默认网关地址；设备号与配对密钥留空则
	// 为本机随机生成并立即落盘——每台安装天然拿到互不冲突的设备号和独立
	// 密钥，多台电脑共用一个网关也不会互相顶掉（网关按设备号路由注册）。
	if cfg.RemoteAccess.Host == "" {
		cfg.RemoteAccess.Host = def.RemoteAccess.Host
	}
	// 一次性迁移（2026-10）：老部署的远程网关填的是 QNAP DDNS 域名，而 2227 上
	// 的证书只覆盖 example.com/.cn——老域名生成的配对二维码/入口链接手机端必报
	// 证书错误。启动时静默迁移到自有域名（仅精确匹配本家老地址，不动其他公司的网关）。
	remoteHostMigrated := false
	if cfg.RemoteAccess.Host == "nas.example.org" {
		cfg.RemoteAccess.Host = "example.com"
		remoteHostMigrated = true
	}
	if cfg.RemoteAccess.Port == 0 {
		cfg.RemoteAccess.Port = def.RemoteAccess.Port
	}
	if cfg.RemoteAccess.RegKey == "" {
		cfg.RemoteAccess.RegKey = def.RemoteAccess.RegKey
	}
	ensureRemoteIdentity(cfg, path)
	// 更新检查：老配置没有这一键时补默认发布源；显式 "off" 原样保留
	// （转成空的话，下一次保存配置又会被补回默认，用户就关不掉了）
	if cfg.UpdateURL == "" {
		cfg.UpdateURL = def.UpdateURL
	}
	// 更新方式：老配置没有这一键时补默认 auto
	if cfg.UpdateMode == "" {
		cfg.UpdateMode = def.UpdateMode
	}
	// 询问提醒：老配置没有这一键时补默认（触发秒数与朗读文案）；AfterSec
	// 越界的也拉回范围（保存侧已钳，这里兜升级前手改配置文件的情况）。
	if cfg.AskAlert.AfterSec <= 0 {
		cfg.AskAlert.AfterSec = askAlertDefaultSec
	}
	if cfg.AskAlert.AfterSec < 30 {
		cfg.AskAlert.AfterSec = 30
	}
	if cfg.AskAlert.AfterSec > 600 {
		cfg.AskAlert.AfterSec = 600
	}
	if strings.TrimSpace(cfg.AskAlert.Text) == "" {
		cfg.AskAlert.Text = askAlertDefaultText
	}
	// 敏感字段透明解密（dpapi:v1: 密文 → 明文；旧版明文原样）。
	// 首启迁移：明文照常读入，下一次 Save 自动转密文。
	cfg.APIKey = unprotectSecret(cfg.APIKey)
	for i := range cfg.Providers {
		cfg.Providers[i].APIKey = unprotectSecret(cfg.Providers[i].APIKey)
	}
	// 老配置兼容：没有 Providers 时把旧的三元组折算成第一个 provider
	if len(cfg.Providers) == 0 {
		providers := DefaultProviders()
		p := providers[0]
		p.BaseURL = cfg.BaseURL
		p.APIKey = cfg.APIKey
		p.Models = nil
		if cfg.Model != "" {
			p.Models = []ModelCfg{{ID: cfg.Model, ContextWindow: 1048576}}
		}
		cfg.Providers = []ProviderCfg{p}
	}
	// 步数上限从旧默认提升过：立即落盘，让设置页读数、配置文件与运行时三者
	// 一致（免得用户看到配置文件里还是 30，以为没生效；其他补全都留给下一次保存）
	if maxStepsBumped || remoteHostMigrated {
		if err := cfg.Save(path); err != nil {
			log.Printf("回写配置迁移失败: %v", err)
		}
	}
	cfg.syncLegacyFields()
	return cfg, nil
}

// syncLegacyFields 让旧字段与第一个 provider 保持一致，老版本 exe 回退时仍能读。
func (c *Config) syncLegacyFields() {
	if len(c.Providers) == 0 {
		return
	}
	p := c.Providers[0]
	c.BaseURL = p.BaseURL
	c.APIKey = p.APIKey
	c.Model = ""
	if len(p.Models) > 0 {
		c.Model = p.Models[0].ID
	}
}

func (c *Config) Save(path string) error {
	// 敏感字段落盘前 DPAPI 加密（副本上做，内存对象保持明文供运行时使用）：
	// 沙箱读不受限（官方同款语义），config.json 被命令直接读到的只是密文。
	out := *c
	out.Providers = make([]ProviderCfg, len(c.Providers))
	copy(out.Providers, c.Providers)
	if enc, err := protectSecret(out.APIKey); err == nil {
		out.APIKey = enc
	}
	for i := range out.Providers {
		if enc, err := protectSecret(out.Providers[i].APIKey); err == nil {
			out.Providers[i].APIKey = enc
		}
	}
	data, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return err
	}
	// 原子写：先写同目录临时文件再改名覆盖。非原子写曾因并发保存
	// （多实例/诊断脚本与正在运行的实例交错写同一文件）把 config.json
	// 写成交错残片——密文尾部截断后永久无法解密，设置保存表现为随机
	// 失败。改名覆盖是原子的：读者要么看到旧文件、要么看到新文件。
	// 0600：Windows 上密钥已有 DPAPI 加密、权限位无意义；安卓/Linux
	// 上密钥为明文落盘（无系统级密钥服务），配置文件只归当前用户所有。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
