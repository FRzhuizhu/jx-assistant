package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 多模型接入：providers 预置清单 + 会话级模型解析。
// 只支持 OpenAI 兼容协议（chat/completions）；各家（DeepSeek/智谱/Kimi/通义/
// OpenAI/Ollama 本地）都有兼容端点。Anthropic 原生协议暂不支持。

// ModelCfg 是一个可选模型的定义；ContextWindow 为 0 表示未知，按默认处理。
type ModelCfg struct {
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	ContextWindow   int      `json:"contextWindow,omitempty"`
	MaxOutput       int      `json:"maxOutput,omitempty"`
	SupportsImages  bool     `json:"supportsImages,omitempty"`
	ReasoningLevels []string `json:"reasoningLevels,omitempty"` // 思考档位：enabled/disabled/low/medium/high
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
			{ID: "deepseek-flash", Name: "DeepSeek-V4.1-Flash", ContextWindow: 1048576, ReasoningLevels: []string{"enabled", "disabled"}},
			{ID: "deepseek-v4-pro", Name: "DeepSeek-V4-Pro", ContextWindow: 1048576, ReasoningLevels: []string{"enabled", "disabled"}},
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

// PresetByID 返回内置预置提供方模板（预置方直接用系统存好的 API 地址）。
func PresetByID(id string) (ProviderCfg, bool) {
	for _, p := range DefaultProviders() {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderCfg{}, false
}

// guessModelMeta 按模型 ID 猜测上下文窗口与图片能力，供「拉取模型」自动配置。
// 猜不准就返回 0（运行时按 DefaultContextWindow 处理）；图片能力只报有把握的。
func guessModelMeta(id string) (ctx int, images bool) {
	s := strings.ToLower(id)
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(s, x) {
				return true
			}
		}
		return false
	}
	images = has("4o", "gpt-4-turbo", "gpt-4.1", "gpt-5", "o3", "o4-mini", "claude",
		"gemini", "glm-4v", "qwen-vl", "-vl-", "vision", "llava", "pixtral", "grok-4", "vlmodel")
	switch {
	case has("moonshot-v1-128k"):
		return 131072, images
	case has("moonshot-v1-32k"):
		return 32768, images
	case has("moonshot-v1-8k"):
		return 8192, images
	case has("deepseek"):
		return 131072, images
	case has("kimi"), has("moonshot"), has("k2"):
		return 262144, images
	case has("gemini"):
		return 1048576, images
	case has("claude"):
		return 200000, images
	case has("gpt-5"), has("gpt-4.1"):
		return 1048576, images
	case has("gpt-4o"), has("gpt-4-turbo"):
		return 131072, images
	case has("o3"), has("o4"):
		return 200000, images
	case has("gpt-3"):
		return 16384, images
	case has("glm-4v"):
		return 8192, true
	case has("glm-4"), has("glm-5"):
		return 131072, images
	case has("qwen-long"):
		return 10000000, images
	case has("qwen3"), has("qwen-max"), has("qwen-plus"), has("qwen-turbo"), has("qwen2.5"):
		return 131072, images
	case has("qwen"):
		return 32768, images
	case has("doubao"):
		return 262144, images
	case has("hunyuan"):
		return 256000, images
	case has("llama"):
		if has("128k", "3.1", "3.2", "3.3", "llama4") {
			return 131072, images
		}
		return 8192, images
	case has("mistral-large", "mistral-medium"):
		return 131072, images
	}
	return 0, images
}

// guessReasoningLevels 按模型 ID 推断可用的思考档位（/models 接口不提供能力
// 信息，只能按已知命名规则推断；返回 nil 表示无思考控制，菜单不显示二级）。
// enabled/disabled → 请求体 thinking{type}；low/medium/high → reasoning_effort。
func guessReasoningLevels(id string) []string {
	s := strings.ToLower(id)
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(s, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("o1"), has("o3"), has("o4"), has("gpt-5"):
		return []string{"low", "medium", "high"} // OpenAI effort 档
	case has("deepseek-reasoner"), has("-r1"):
		return []string{"enabled"} // 思考专用模型，只能开
	case has("deepseek"):
		return []string{"enabled", "disabled"} // V3.2+ 混合思考
	case has("glm-4.5"), has("glm-4.6"), has("glm-5"):
		return []string{"enabled", "disabled"}
	case has("qwen3"), has("kimi"), has("k2"):
		return []string{"enabled", "disabled"}
	}
	return nil
}

// ModelTestResult 是单个模型的连通性测试结果。
type ModelTestResult struct {
	ID  string `json:"id"`
	OK  bool   `json:"ok"`
	MS  int64  `json:"ms"`
	Err string `json:"err,omitempty"`
}

// TestProviderModels 对目录里每个模型发一个 1 token 的极小请求测连通，
// 限 4 路并发，单请求 30 秒超时。ids 为空时测整个目录。
func TestProviderModels(p *ProviderCfg, ids []string) []ModelTestResult {
	if len(ids) == 0 {
		for _, m := range p.Models {
			ids = append(ids, m.ID)
		}
	}
	results := make([]ModelTestResult, len(ids))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := ModelTestResult{ID: id}
			start := time.Now()
			if err := pingModel(p, id); err != nil {
				res.Err = truncateRunes(err.Error(), 160)
			} else {
				res.OK = true
			}
			res.MS = time.Since(start).Milliseconds()
			results[i] = res
		}(i, id)
	}
	wg.Wait()
	return results
}

// pingModel 发一次最小化的 chat/completions 请求，能通即可。
func pingModel(p *ProviderCfg, model string) error {
	url := strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
	payload, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// FetchModels 从提供方拉取可用模型清单（GET {baseURL}/models），
// 新条目按 ID 猜测上下文窗口与图片能力自动填好。
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
		ctx, img := guessModelMeta(m.ID)
		models = append(models, ModelCfg{
			ID: m.ID, ContextWindow: ctx, SupportsImages: img,
			ReasoningLevels: guessReasoningLevels(m.ID),
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("服务未返回任何模型")
	}
	return models, nil
}
