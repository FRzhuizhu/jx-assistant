package main

// Claude Code 插件组件桥：agents（子代理预设）。
// 插件的 agents/<名>.md 注册为动态子对话专家：frontmatter 的 name/description
// 进专家清单（名字即 dispatch_sub 的调用参数），正文作为追加系统提示词。
// 与自定义专家同命名空间（名称逐字匹配）；model 别名（sonnet/opus）与
// tools 白名单在本引擎无对应物，忽略（已知差异，见 AGENT.md）。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ccAgentEntry 是插件携带的一个子代理预设。
type ccAgentEntry struct {
	Name        string // <插件>:<名>（专家名单里的调用名）
	Description string
	Prompt      string // 正文（追加系统提示词）
	Model       string // 原样保留（别名不映射，仅诊断展示）
	Tools       []string
	Path        string
}

// loadAgents 收集插件的全部子代理（默认 agents/，manifest.agents 替换；
// 递归子目录拼进名字，frontmatter name 只替换最后一段）。
func (p *CCPlugin) loadAgents() {
	p.Agents = nil
	for _, d := range ccStringList(p.Manifest.Agents, "agents") {
		p.loadAgentDir(filepath.Join(p.Root, filepath.FromSlash(d)), "")
	}
}

// loadAgentDir 递归扫描代理目录。
func (p *CCPlugin) loadAgentDir(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	fileNames := []string{}
	subDirs := []string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			subDirs = append(subDirs, e.Name())
		} else if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			fileNames = append(fileNames, e.Name())
		}
	}
	sort.Strings(subDirs)
	sort.Strings(fileNames)
	for _, name := range fileNames {
		p.addAgentFile(filepath.Join(dir, name), prefix+strings.TrimSuffix(name, filepath.Ext(name)))
	}
	for _, name := range subDirs {
		p.loadAgentDir(filepath.Join(dir, name), prefix+name+":")
	}
}

// addAgentFile 从 .md 文件登记一个代理（frontmatter name 覆盖文件名段）。
func (p *CCPlugin) addAgentFile(path, relName string) {
	fm, body := parseCCFrontmatter(readFileOr(path))
	if strings.TrimSpace(body) == "" {
		return
	}
	name := relName
	if v, ok := fm["name"].(string); ok && strings.TrimSpace(v) != "" && strings.Contains(relName, ":") {
		// frontmatter name 只替换最后一段（对齐 Claude 规则）
		if i := strings.LastIndex(relName, ":"); i >= 0 {
			name = relName[:i+1] + strings.TrimSpace(v)
		}
	} else if v, ok := fm["name"].(string); ok && strings.TrimSpace(v) != "" {
		name = strings.TrimSpace(v)
	}
	desc, _ := fm["description"].(string)
	model, _ := fm["model"].(string)
	var tools []string
	if t, ok := fm["tools"].([]any); ok {
		for _, item := range t {
			if s, ok := item.(string); ok {
				tools = append(tools, s)
			}
		}
	}
	p.Agents = append(p.Agents, ccAgentEntry{
		Name: p.Name + ":" + name, Description: desc, Prompt: strings.TrimSpace(body),
		Model: model, Tools: tools, Path: path,
	})
}

// Experts 把启用插件（组件放行）的代理转为专家预设（dispatch_sub 可派发；
// 不继承上下文——子代理在 Claude 里本就是自足上下文）。
func (r *CCPluginRegistry) Experts() []ExpertCfg {
	out := []ExpertCfg{}
	for _, p := range r.enabled() {
		if !p.componentEnabled("agents") {
			continue
		}
		for _, ag := range p.Agents {
			out = append(out, ExpertCfg{
				ID:          "cc:" + p.Key + "/" + ag.Name,
				Name:        ag.Name,
				Description: "[插件] " + ag.Description,
				Prompt:      ag.Prompt,
				Enabled:     true,
			})
		}
	}
	return out
}
