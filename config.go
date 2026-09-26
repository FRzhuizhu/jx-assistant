package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"os"
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
	// 沙箱密级关卡：low=受限子进程降 Low 完整性（默认，路线无关封堵写/删）
	// off=回退官方同款（仅 WRITE_RESTRICTED 双检）
	SandboxIntegrityLevel string `json:"sandboxIntegrityLevel"`
	DataDir               string `json:"dataDir"`
	SkillsDir             string `json:"skillsDir"` // 技能目录；空 = 可执行文件旁的 技能/（不可写时落数据目录）
	// 上下文压缩设置
	CompactMode      string  `json:"compactMode"`      // rule=规则压缩(默认) llm=模型摘要 off=关闭
	KeepRounds       int     `json:"keepRounds"`       // 保留最近 N 轮不压缩
	RuleInterval     int     `json:"ruleInterval"`     // 规则压缩的间隔轮数
	CompactThreshold float64 `json:"compactThreshold"` // 模型摘要自动触发阈值（0.8/0.9）
	// 文件快照保留策略：按天超龄清理是主力（旧配置缺省回落默认值；-1 = 该项关闭）。
	// 自动池管 write_file/edit_file 的小快照；手动池（snapshot_file，往往是
	// Office 文档等大文件）默认不限量——清理靠 7 天过期；确有磁盘压力时再配
	// 500/1024 这一级别的大预算。SnapshotEnabled 是设置页「文件备份」总开关
	//（指针类型：配置文件里没写这一项 = 开启）。
	SnapshotEnabled     *bool `json:"snapshotEnabled,omitempty"`
	SnapshotDays        int   `json:"snapshotDays"`        // 快照保留天数，默认 7；-1=永不过期
	SnapshotMaxMB       int   `json:"snapshotMaxMB"`       // 每会话自动快照总量上限（MB），默认 32；-1=不限量
	SnapshotManualMaxMB int   `json:"snapshotManualMaxMB"` // 每会话手动快照总量上限（MB），默认 -1 不限量；需要时设 500/1024
	// 子对话专家（专家团）预设：dispatch_sub 按名称选用的命名预设（追加提示词/
	Experts []ExpertCfg `json:"experts"`
	// 远程接入（公网中继，RustDesk 式）：出站长连注册到自建 210 网关的 /hub，
	// 手机等外部设备经网关的加密中继访问本服务；端到端 AES-256-GCM（密钥由
	// pairKey 派生），网关只见密文。与聊天服务的 p2p.js 同协议，共用同一网关。
	RemoteAccess RemoteAccessCfg `json:"remoteAccess"`
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

func DefaultConfig() *Config {
	wd, _ := os.Getwd()
	return &Config{
		Port:                  8788,
		BindHost:              "127.0.0.1",
		BaseURL:               "https://api.deepseek.com/v1",
		APIKey:                "",
		Model:                 "deepseek-chat",
		Workspace:             wd,
		MaxSteps:              30,
		AllowShell:            true,
		SandboxIntegrityLevel: "low",
		DataDir:               "data",
		CompactMode:           "rule",
		KeepRounds:            5,
		RuleInterval:          5,
		CompactThreshold:      0.8,
		SnapshotDays:          7,
		SnapshotMaxMB:         32,
		SnapshotManualMaxMB:   -1,
		SnapshotEnabled:       nil, // 缺省开启；nil 表示"未配置"，按开处理
		Experts:               defaultExperts(),
		RemoteAccess: RemoteAccessCfg{
			Enabled: false,
			Host:    "nas.example.org",
			Port:    2227,
			MyID:    "", // 留空：首次加载时为每台安装生成唯一设备号（多人安装互不冲突）
			RegKey:  "chat_p2p_v1",
		},
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
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = def.MaxSteps
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
	if cfg.KeepRounds <= 0 {
		cfg.KeepRounds = def.KeepRounds
	}
	if cfg.RuleInterval <= 0 {
		cfg.RuleInterval = def.RuleInterval
	}
	if cfg.CompactThreshold <= 0.5 || cfg.CompactThreshold >= 1 {
		cfg.CompactThreshold = def.CompactThreshold
	}
	if cfg.SandboxIntegrityLevel != "off" {
		cfg.SandboxIntegrityLevel = "low"
	}
	// 远程接入：老配置没有这一键时补默认网关地址；设备号与配对密钥留空则
	// 为本机随机生成并立即落盘——每台安装天然拿到互不冲突的设备号和独立
	// 密钥，多台电脑共用一个网关也不会互相顶掉（网关按设备号路由注册）。
	if cfg.RemoteAccess.Host == "" {
		cfg.RemoteAccess.Host = def.RemoteAccess.Host
	}
	if cfg.RemoteAccess.Port == 0 {
		cfg.RemoteAccess.Port = def.RemoteAccess.Port
	}
	if cfg.RemoteAccess.RegKey == "" {
		cfg.RemoteAccess.RegKey = def.RemoteAccess.RegKey
	}
	ensureRemoteIdentity(cfg, path)
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
	return os.WriteFile(path, data, 0644)
}
