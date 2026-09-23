package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 多模型接入：providers 预置清单 + 会话级模型解析。
// 只支持 OpenAI 兼容协议（chat/completions）；各家（DeepSeek/智谱/Kimi/通义/
// OpenAI/Ollama 本地）都有兼容端点。Anthropic 原生协议暂不支持。

// ModelCfg 是一个可选模型的定义；ContextWindow 为 0 表示未知，按默认处理。
type ModelCfg struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

// ProviderCfg 是一个模型服务提供方。
type ProviderCfg struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	BaseURL string     `json:"baseURL"`
	APIKey  string     `json:"apiKey,omitempty"`
	Models  []ModelCfg `json:"models"`
}

// DefaultContextWindow 是模型未声明上下文窗口时的保守默认值。
const DefaultContextWindow = 262144

// DefaultProviders 预置常用提供方：DeepSeek 自带模型清单（其余拿到 Key 后
// 用「获取模型列表」一键拉取），URL 均为 OpenAI 兼容端点。
func DefaultProviders() []ProviderCfg {
	return []ProviderCfg{
		{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Models: []ModelCfg{
			{ID: "deepseek-flash", Name: "DeepSeek-V4.1-Flash", ContextWindow: 1048576},
			{ID: "deepseek-v4-pro", Name: "DeepSeek-V4-Pro", ContextWindow: 1048576},
		}},
		{ID: "zhipu", Name: "智谱 GLM", BaseURL: "https://open.bigmodel.cn/api/paas/v4"},
		{ID: "moonshot", Name: "Kimi / 月之暗面", BaseURL: "https://api.moonshot.cn/v1"},
		{ID: "qwen", Name: "通义千问", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1"},
		{ID: "ollama", Name: "Ollama 本地", BaseURL: "http://127.0.0.1:11434/v1"},
	}
}

// LLMTarget 是一次模型调用解析后的全部目的地信息。
type LLMTarget struct {
	BaseURL       string
	APIKey        string
	Model         string
	ContextWindow int
}

func (a *App) providerByID(id string) *ProviderCfg {
	for i := range a.Cfg.Providers {
		if a.Cfg.Providers[i].ID == id {
			return &a.Cfg.Providers[i]
		}
	}
	return nil
}

// DefaultTarget 返回全局默认调用目标（第一个 provider 的默认模型）。
func (a *App) DefaultTarget() LLMTarget {
	if len(a.Cfg.Providers) == 0 {
		return LLMTarget{BaseURL: a.Cfg.BaseURL, APIKey: a.Cfg.APIKey, Model: a.Cfg.Model, ContextWindow: DefaultContextWindow}
	}
	return a.targetOf(&a.Cfg.Providers[0], "", "")
}

// resolveTarget 按会话解析调用目标：会话指定了 provider/model 就用会话的，
// 否则跟随全局默认（第一个 provider）。
func (a *App) resolveTarget(sess *SessionMeta) LLMTarget {
	p := a.providerByID(sess.Provider)
	if p == nil && len(a.Cfg.Providers) > 0 {
		p = &a.Cfg.Providers[0]
	}
	if p == nil {
		return LLMTarget{BaseURL: a.Cfg.BaseURL, APIKey: a.Cfg.APIKey, Model: sess.Model, ContextWindow: DefaultContextWindow}
	}
	return a.targetOf(p, sess.Provider, sess.Model)
}

func (a *App) targetOf(p *ProviderCfg, _, modelID string) LLMTarget {
	t := LLMTarget{BaseURL: p.BaseURL, APIKey: p.APIKey, ContextWindow: DefaultContextWindow}
	t.Model = modelID
	if t.Model == "" && len(p.Models) > 0 {
		t.Model = p.Models[0].ID
	}
	for _, m := range p.Models {
		if m.ID == t.Model && m.ContextWindow > 0 {
			t.ContextWindow = m.ContextWindow
			break
		}
	}
	if t.Model == "" {
		t.Model = a.Cfg.Model // 兜底：老配置里的裸模型名
	}
	return t
}

// FetchModels 从提供方拉取可用模型清单（GET {baseURL}/models）。
func (a *App) FetchModels(p *ProviderCfg) ([]ModelCfg, error) {
	url := strings.TrimRight(p.BaseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务返回 %d: %s", resp.StatusCode, truncateRunes(string(body), 200))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("响应不是合法的模型清单 JSON: %w", err)
	}
	models := []ModelCfg{}
	for _, m := range out.Data {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		models = append(models, ModelCfg{ID: m.ID})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("服务未返回任何模型")
	}
	return models, nil
}
