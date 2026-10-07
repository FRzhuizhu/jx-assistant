package main

// Claude Code 插件兼容层的单元测试：frontmatter 解析、命令占位符展开、
// hook 输出协议（exit 2 / JSON decision）、matcher 编译、插件加载与安装链路。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCCFrontmatter(t *testing.T) {
	// 带引号描述 + 列表 + 嵌套 metadata
	doc := "---\nname: pdf\ndescription: \"A, B: C\"\nallowed-tools:\n  - Read\n  - Bash(ls *)\nmetadata:\n  author: Z.AI\n  version: \"1.1\"\nuser-invocable: yes\n---\n\n正文第一行\n正文第二行\n"
	fm, body := parseCCFrontmatter(doc)
	if fm["name"] != "pdf" {
		t.Fatalf("name = %v", fm["name"])
	}
	if fm["description"] != "A, B: C" {
		t.Fatalf("description = %v", fm["description"])
	}
	tools, ok := fm["allowed-tools"].([]any)
	if !ok || len(tools) != 2 || tools[0] != "Read" || tools[1] != "Bash(ls *)" {
		t.Fatalf("allowed-tools = %#v", fm["allowed-tools"])
	}
	meta, ok := fm["metadata"].(map[string]any)
	if !ok || meta["author"] != "Z.AI" {
		t.Fatalf("metadata = %#v", fm["metadata"])
	}
	if fm["user-invocable"] != true {
		t.Fatalf("user-invocable = %#v（yes 应解析为 true）", fm["user-invocable"])
	}
	if body != "正文第一行\n正文第二行" {
		t.Fatalf("body = %q", body)
	}

	// 无 frontmatter：全文为正文
	fm2, body2 := parseCCFrontmatter("直接正文")
	if len(fm2) != 0 || body2 != "直接正文" {
		t.Fatalf("无 frontmatter 解析错: %#v %q", fm2, body2)
	}

	// frontmatter 未闭合：按无 frontmatter 处理（字段全空仍加载）
	fm3, _ := parseCCFrontmatter("---\nname: x\n正文")
	if len(fm3) != 0 {
		t.Fatalf("未闭合 frontmatter 应按正文处理，got %#v", fm3)
	}

	// 行内列表
	fm4, _ := parseCCFrontmatter("---\ntools: [Read, Write]\n---\nX")
	if v, ok := fm4["tools"].([]any); !ok || len(v) != 2 {
		t.Fatalf("行内列表 = %#v", fm4["tools"])
	}
}

func TestExpandCommandPlaceholders(t *testing.T) {
	got := expandCommandPlaceholders("你好 $ARGUMENTS / 首个 $1 / 第二 $2 / 价钱 \\$1.00", "alpha beta")
	want := "你好 alpha beta / 首个 alpha / 第二 beta / 价钱 $1.00"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := expandCommandPlaceholders("$9", "a b"); got != "" {
		t.Fatalf("缺位的 $9 应替换为空串，got %q", got)
	}
}

func TestCcStringList(t *testing.T) {
	if got := ccStringList(nil, "skills"); len(got) != 1 || got[0] != "skills" {
		t.Fatalf("nil 默认 = %v", got)
	}
	if got := ccStringList(json.RawMessage(`"extra"`), "skills"); len(got) != 1 || got[0] != "extra" {
		t.Fatalf("字符串 = %v", got)
	}
	if got := ccStringList(json.RawMessage(`["a","b"]`), "skills"); len(got) != 2 {
		t.Fatalf("数组 = %v", got)
	}
	if got := ccStringList(json.RawMessage(`{"x":"y"}`), "skills"); got != nil {
		t.Fatalf("对象应返回 nil = %v", got)
	}
}

func TestCcInterpretHookOutput(t *testing.T) {
	// exit 2：阻断，stderr 为原因
	oc := ccInterpretHookOutput(2, "", "危险操作被拒")
	if !oc.Block || oc.Reason != "危险操作被拒" {
		t.Fatalf("exit2 = %#v", oc)
	}
	// decision block
	oc = ccInterpretHookOutput(0, `{"decision":"block","reason":"不许写"}`, "")
	if !oc.Block || oc.Reason != "不许写" {
		t.Fatalf("decision block = %#v", oc)
	}
	// PreToolUse：permissionDecision + updatedInput
	oc = ccInterpretHookOutput(0, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no","updatedInput":{"command":"echo hi"}}}`, "")
	if oc.Permission != "deny" || oc.UpdatedInput == nil {
		t.Fatalf("permissionDecision = %#v", oc)
	}
	var in map[string]any
	_ = json.Unmarshal(oc.UpdatedInput, &in)
	if in["command"] != "echo hi" {
		t.Fatalf("updatedInput = %s", oc.UpdatedInput)
	}
	// PostToolUse：updatedToolOutput
	oc = ccInterpretHookOutput(0, `{"hookSpecificOutput":{"updatedToolOutput":"改写结果"},"systemMessage":"提示"}`, "")
	if oc.UpdatedOutput != "改写结果" || oc.SystemMsg != "提示" {
		t.Fatalf("updatedToolOutput = %#v", oc)
	}
	// 纯文本 stdout：无决议
	oc = ccInterpretHookOutput(0, "hello", "")
	if oc.Block || oc.UpdatedOutput != "" {
		t.Fatalf("纯文本不应产生决议 = %#v", oc)
	}
}

func TestCcMatcherRe(t *testing.T) {
	re := ccMatcherRe("Edit|Write")
	if re == nil || !re.MatchString("Edit") || re.MatchString("EditX") {
		t.Fatalf("精确多选应锚定: %v", re)
	}
	re = ccMatcherRe("^mcp__memory__")
	if re == nil || !re.MatchString("mcp__memory__save") || re.MatchString("mcp__other__save") {
		t.Fatalf("正则 matcher = %v", re)
	}
	if ccMatcherRe("") != nil || ccMatcherRe("*") != nil {
		t.Fatal("空与 * 匹配全部")
	}
}

// writeTestPlugin 在临时目录构造一个全组件测试插件。
func writeTestPlugin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mk(".claude-plugin/plugin.json", `{"name":"test-plug","version":"1.2.0","description":"测试插件",
		"userConfig":{"token":{"type":"string","title":"令牌","description":"d"}}}`)
	mk("skills/deploy/SKILL.md", "---\nname: deploy\ndescription: 部署说明\n---\n部署正文 ${CLAUDE_PLUGIN_ROOT}")
	mk("commands/db/migrate.md", "---\ndescription: 迁移数据库\nargument-hint: \"[env]\"\n---\n执行迁移 $ARGUMENTS（首个 $1）")
	mk("agents/reviewer.md", "---\nname: reviewer\ndescription: 评审代理\nmodel: sonnet\n---\n你是评审员。")
	mk("hooks/hooks.json", `{"hooks":{"PreToolUse":[{"matcher":"Write|Edit","hooks":[{"type":"command","command":"echo blocked","timeout":5}]}],"PostToolUse":[{"hooks":[{"type":"http","url":"http://127.0.0.1:1/done"}]}]}}`)
	mk(".mcp.json", `{"mcpServers":{"db":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/server.js"]}}}`)
	return root
}

func TestCCPluginLoadAll(t *testing.T) {
	root := writeTestPlugin(t)
	dataDir := t.TempDir()
	os.MkdirAll(filepath.Join(dataDir, "claude-plugins", "cache", "local"), 0755)
	// 手工落位（安装链路另有测试）：cache/local/test-plug/1.2.0
	dst := filepath.Join(dataDir, "claude-plugins", "cache", "local", "test-plug", "1.2.0")
	if err := copyDir(root, dst); err != nil {
		t.Fatal(err)
	}
	st := `{"version":1,"marketplaces":{},"plugins":{"test-plug@local":{"marketplace":"local","version":"1.2.0","path":"cache/local/test-plug/1.2.0"}}}`
	os.WriteFile(filepath.Join(dataDir, "claude-plugins", "plugins.json"), []byte(st), 0644)

	r := InitCCPlugins(nil, dataDir)
	if len(r.plugins) != 1 {
		t.Fatalf("应加载 1 个插件，got %d", len(r.plugins))
	}
	p := r.plugins[0]
	if len(p.Skills) != 1 || p.Skills[0].ID != "test-plug:deploy" {
		t.Fatalf("skills = %#v", p.Skills)
	}
	if len(p.Commands) != 1 || p.Commands[0].Name != "test-plug:db:migrate" {
		t.Fatalf("commands = %#v（子目录应拼进名字）", p.Commands)
	}
	if len(p.Agents) != 1 || p.Agents[0].Name != "test-plug:reviewer" {
		t.Fatalf("agents = %#v", p.Agents)
	}
	if len(p.HooksCfg) != 2 {
		t.Fatalf("hooks = %#v", p.HooksCfg)
	}
	if len(p.MCPDefs) != 1 || p.MCPDefs["db"].Command != "node" {
		t.Fatalf("mcp = %#v", p.MCPDefs)
	}
	// 技能正文变量替换
	body, err := p.ccSkillBody(&p.Skills[0], map[string]any{})
	if err != nil || body != "部署正文 "+p.Root {
		t.Fatalf("技能正文 = %q err=%v", body, err)
	}
	// 缺省开关：静态组件开、执行组件关
	if !p.componentEnabled("skills") || !p.componentEnabled("commands") || !p.componentEnabled("agents") {
		t.Fatal("静态组件缺省应开")
	}
	if p.componentEnabled("hooks") || p.componentEnabled("mods") || p.componentEnabled("mcp") {
		t.Fatal("执行型组件缺省应关")
	}
	// 斜杠命令展开
	app := &App{CCPlugins: r}
	msg := app.ccExpandSlashMessage(context.Background(), nil, "/test-plug:db:migrate prod abc")
	if msg == "/test-plug:db:migrate prod abc" {
		t.Fatal("斜杠命令应被展开")
	}
	wantFrag := "执行迁移 prod abc（首个 prod）"
	if !strings.Contains(msg, wantFrag) {
		t.Fatalf("展开缺 %q：%.120s", wantFrag, msg)
	}
	// 未知命令原样通过
	if m2 := app.ccExpandSlashMessage(context.Background(), nil, "/不是命令 hello"); m2 != "/不是命令 hello" {
		t.Fatalf("未知命令应原样: %q", m2)
	}
	// 专家桥
	experts := r.Experts()
	if len(experts) != 1 || experts[0].Name != "test-plug:reviewer" || experts[0].Prompt != "你是评审员。" {
		t.Fatalf("experts = %#v", experts)
	}
}

func TestCCInstallFromPath(t *testing.T) {
	root := writeTestPlugin(t)
	r := InitCCPlugins(nil, t.TempDir())
	key, err := r.ccInstallFromPath(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "test-plug@local" {
		t.Fatalf("key = %s", key)
	}
	if len(r.plugins) != 1 {
		t.Fatalf("安装后应加载 1 个，got %d", len(r.plugins))
	}
	// 重装同版本：幂等
	if _, err := r.ccInstallFromPath(root, ""); err != nil {
		t.Fatalf("重装失败: %v", err)
	}
	// 卸载
	if err := r.Uninstall(key); err != nil {
		t.Fatal(err)
	}
	if len(r.plugins) != 0 {
		t.Fatalf("卸载后应为 0，got %d", len(r.plugins))
	}
}

// TestCCRealPluginsSmoke 用本机 Claude Code 官方市场克隆做真实插件冒烟：
// 装入 hookify（settings hooks 实样本）验证 hooks 解析与事件规则命中。
// 本机没有该克隆时自动跳过（CI 环境不跑）。
func TestCCRealPluginsSmoke(t *testing.T) {
	const marketRoot = `C:/Users/54358/.zcode/cli/plugins/marketplaces/claude-plugins-official/plugins`
	if _, err := os.Stat(marketRoot); err != nil {
		t.Skip("本机没有 claude-plugins-official 克隆，跳过真实插件冒烟")
	}
	entries, err := os.ReadDir(marketRoot)
	if err != nil || len(entries) == 0 {
		t.Skipf("市场目录不可读: %v", err)
	}
	r := InitCCPlugins(nil, t.TempDir())
	installed := 0
	hooksLoaded := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key, err := r.ccInstallFromPath(filepath.Join(marketRoot, e.Name()), "")
		if err != nil {
			continue // 官方目录里有非插件/特殊来源条目，装不上不视为失败
		}
		installed++
		for _, p := range r.plugins {
			if p.Key == key && len(p.HooksCfg) > 0 {
				hooksLoaded += len(p.HooksCfg)
			}
		}
	}
	t.Logf("真实插件冒烟：安装 %d 个，hooks 规则 %d 条", installed, hooksLoaded)
	if installed == 0 {
		t.Fatal("一个真实插件都没装上")
	}
	// hookify 若在场，其 4 个 settings hooks 事件应全部解析
	for _, p := range r.plugins {
		if p.Name == "hookify" {
			events := map[string]int{}
			for _, rule := range p.HooksCfg {
				events[rule.Event]++
			}
			for _, want := range []string{"PreToolUse", "PostToolUse", "Stop", "UserPromptSubmit"} {
				if events[want] == 0 {
					t.Errorf("hookify 缺事件 %s（解析到 %v）", want, events)
				}
			}
		}
	}
	// 技能/命令/专家清单 API 形态完好
	_ = r.ccSkills()
	_ = r.ccCommands()
	_ = r.Experts()
	list := r.List()
	if len(list) != installed {
		t.Fatalf("List 数量 %d != 安装数 %d", len(list), installed)
	}
}
