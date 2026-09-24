package main

import (
	"encoding/json"
	"errors"
	"os"
)

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
	}
}

// LoadConfig 读取配置文件；文件不存在时写入一份默认配置再返回。
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
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
