package main

// Claude Code 插件组件桥：hooks（settings hooks 形态）。
// hooks/hooks.json 的 {"hooks": {<事件>: [{matcher, hooks: [...]}]}} 在引擎
// 关键点触发：spawn shell 命令（stdin 喂 hook 输入 JSON）或 POST HTTP。对齐
// Claude 输出协议：exit code 2 = 阻断（stderr 为原因）；stdout JSON 的
// decision:block / hookSpecificOutput.permissionDecision / updatedInput /
// updatedToolOutput / additionalContext / systemMessage 逐一解释。
// mcp_tool / prompt / agent 型 handler 与 async 语义不支持（记日志跳过）。
// mod 形态（{"modules": [...]}）由 ccplugin_mod.go 处理，事件桥共用。

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ccHookRule 是一条可执行的 settings hook。
type ccHookRule struct {
	Plugin  *CCPlugin
	Event   string // Claude 事件名（PreToolUse 等）
	Matcher string // 工具名 matcher（精确 / | 分隔 / 正则；空=全部）
	Type    string // command | http
	Command string
	Args    []string
	URL     string
	Headers map[string]string
	Timeout int // 秒；0=默认 60
}

// ccHookOutcome 是一次事件里全部 hook 决议的汇总（首个阻断/决策胜出）。
type ccHookOutcome struct {
	Block         bool
	Reason        string
	SystemMsg     string
	ExtraContext  string
	UpdatedInput  json.RawMessage // PreToolUse 改写后的工具参数
	UpdatedOutput string          // PostToolUse 改写后的工具结果
	Permission    string          // allow / deny / ask（PreToolUse 决策）
	PermissionWhy string
}

// ccHookEntry / ccHookMatcher / ccHooksFile 是 hooks.json 的解析结构。
type ccHookEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Timeout int               `json:"timeout"`
	Async   bool              `json:"async"`
}

type ccHookMatcher struct {
	Matcher string        `json:"matcher"`
	Hooks   []ccHookEntry `json:"hooks"`
}

type ccHooksFile struct {
	Description string                     `json:"description"`
	Modules     []string                   `json:"modules"`
	Hooks       map[string][]ccHookMatcher `json:"hooks"`
}

// loadHooks 解析 hooks/hooks.json 与 manifest.hooks（合并语义）。有 modules
// 键的是 mod（JS 运行时加载），不是 settings hooks。
func (p *CCPlugin) loadHooks() {
	p.HooksCfg = nil
	p.ModModules = nil
	sources := []map[string][]ccHookMatcher{}
	for _, rel := range ccStringList(p.Manifest.Hooks, "hooks/hooks.json") {
		var f ccHooksFile
		data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &f); err != nil {
			log.Printf("[CC插件] %s：hooks 文件解析失败: %v", p.Key, err)
			continue
		}
		// modules 路径相对 hooks.json 所在目录（对齐 Claude 规范）；
		// 统一换算成相对插件根的路径存下
		hooksDir := filepath.ToSlash(filepath.Dir(rel))
		for _, m := range f.Modules {
			mod := strings.TrimPrefix(m, "./")
			if hooksDir != "." && !filepath.IsAbs(mod) {
				mod = hooksDir + "/" + mod
			}
			p.ModModules = append(p.ModModules, mod)
		}
		if len(f.Hooks) > 0 {
			sources = append(sources, f.Hooks)
		}
	}
	// manifest.hooks 的内联形态（对象）
	if raw := p.Manifest.Hooks; len(raw) > 0 && raw[0] == '{' {
		var f ccHooksFile
		if err := json.Unmarshal(raw, &f); err == nil && len(f.Hooks) > 0 {
			sources = append(sources, f.Hooks)
		}
	}
	for _, byEvent := range sources {
		events := make([]string, 0, len(byEvent))
		for ev := range byEvent {
			events = append(events, ev)
		}
		sort.Strings(events)
		for _, ev := range events {
			for _, mg := range byEvent[ev] {
				for _, h := range mg.Hooks {
					p.addHookRule(ev, mg.Matcher, h)
				}
			}
		}
	}
}

// addHookRule 把一条 handler 登记为规则（不支持型记日志跳过）。
func (p *CCPlugin) addHookRule(event, matcher string, h ccHookEntry) {
	switch h.Type {
	case "command":
		if strings.TrimSpace(h.Command) == "" {
			return
		}
	case "http":
		if strings.TrimSpace(h.URL) == "" {
			return
		}
	default:
		if h.Type == "prompt" || h.Type == "agent" || h.Type == "mcp_tool" {
			log.Printf("[CC插件] %s：%s 型 settings hook 不支持，已跳过（事件 %s）", p.Key, h.Type, event)
		}
		return
	}
	p.HooksCfg = append(p.HooksCfg, ccHookRule{
		Plugin: p, Event: event, Matcher: matcher, Type: h.Type,
		Command: h.Command, Args: h.Args, URL: h.URL, Headers: h.Headers, Timeout: h.Timeout,
	})
}

// hookRulesFor 取某事件的全部启用规则（hooks 组件放行的插件才算）。
func (r *CCPluginRegistry) hookRulesFor(event string) []ccHookRule {
	out := []ccHookRule{}
	for _, p := range r.enabled() {
		if !p.componentEnabled("hooks") {
			continue
		}
		for _, rule := range p.HooksCfg {
			if rule.Event == event {
				out = append(out, rule)
			}
		}
	}
	return out
}

// hasAnyHooks 报告是否注册了任意 settings hook（分发点快速短路用）。
func (r *CCPluginRegistry) hasAnyHooks() bool {
	if r == nil {
		return false
	}
	for _, p := range r.enabled() {
		if p.componentEnabled("hooks") && len(p.HooksCfg) > 0 {
			return true
		}
	}
	return false
}

// ccMatcherRe 把 Claude 的 matcher 语法编译为工具名正则：仅字母数字 _- | ,
// 空格时按精确/多选匹配（锚定），否则按正则原文（非锚定，对齐 Claude 规则）。
func ccMatcherRe(matcher string) *regexp.Regexp {
	m := strings.TrimSpace(matcher)
	if m == "" || m == "*" {
		return nil // 匹配全部
	}
	simple := true
	for _, r := range m {
		if !(r == ',' || r == '|' || r == ' ' || r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			simple = false
			break
		}
	}
	if simple {
		parts := strings.FieldsFunc(m, func(r rune) bool { return r == ',' || r == '|' || r == ' ' })
		if len(parts) == 0 {
			return nil
		}
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(strings.TrimSpace(p))
		}
		re, err := regexp.Compile("^(?:" + strings.Join(parts, "|") + ")$")
		if err != nil {
			return nil
		}
		return re
	}
	re, err := regexp.Compile(m)
	if err != nil {
		return nil
	}
	return re
}

// ccPreToolUse 工具执行前的 hook 桥（agent.go 工具循环调用）：返回空串放行，
// 非空为拒绝文本（模型可见）。可原地改写 tc.Function.Arguments（updatedInput）。
func (a *App) ccPreToolUse(ctx context.Context, sess *SessionMeta, tc *ToolCall) string {
	if a.CCPlugins == nil || !a.CCPlugins.hasAnyHooks() {
		return ""
	}
	var toolInput any
	if len(tc.Function.Arguments) > 0 {
		_ = json.Unmarshal(json.RawMessage(tc.Function.Arguments), &toolInput)
	}
	oc := a.ccEmitHookEvent(ctx, sess, "PreToolUse", tc.Function.Name, toolInput, nil)
	if oc.SystemMsg != "" {
		a.broadcast(Event{Type: "report", SessionID: sess.ID, Output: oc.SystemMsg})
	}
	switch {
	case oc.Block:
		return "工具调用被插件 hook 拒绝：" + oc.Reason
	case oc.Permission == "deny":
		reason := oc.PermissionWhy
		if reason == "" {
			reason = "插件 hook 拒绝"
		}
		return "工具调用被插件 hook 拒绝：" + reason
	}
	if len(oc.UpdatedInput) > 0 {
		tc.Function.Arguments = string(oc.UpdatedInput)
	}
	return ""
}

// ccPostToolUse 工具执行后的 hook 桥：返回改写后的结果（updatedToolOutput），
// 未改写返回空串。
func (a *App) ccPostToolUse(ctx context.Context, sess *SessionMeta, tc ToolCall, result string) string {
	if a.CCPlugins == nil || !a.CCPlugins.hasAnyHooks() {
		return ""
	}
	var toolInput any
	if len(tc.Function.Arguments) > 0 {
		_ = json.Unmarshal(json.RawMessage(tc.Function.Arguments), &toolInput)
	}
	payload := map[string]any{"tool_response": result}
	oc := a.ccEmitHookEvent(ctx, sess, "PostToolUse", tc.Function.Name, toolInput, payload)
	return oc.UpdatedOutput
}

// ccEmitHookEvent 在引擎关键点触发一次事件：逐个执行匹配的 hook 并汇总决议。
// toolName 非空时做 matcher 过滤；payload 附加事件特有字段。
func (a *App) ccEmitHookEvent(ctx context.Context, sess *SessionMeta, event string, toolName string, toolInput any, payload map[string]any) ccHookOutcome {
	var zero ccHookOutcome
	if a.CCPlugins == nil || !a.CCPlugins.hasAnyHooks() {
		return zero
	}
	rules := a.CCPlugins.hookRulesFor(event)
	if len(rules) == 0 {
		return zero
	}
	input := a.ccHookInputJSON(sess, event, toolName, toolInput, payload)
	out := zero
	for _, rule := range rules {
		if re := ccMatcherRe(rule.Matcher); re != nil && toolName != "" && !re.MatchString(toolName) {
			continue
		}
		res := a.ccRunHook(ctx, rule, sess, input)
		// 决议合并：首个阻断/决策胜出，消息类逐条保留
		if res.SystemMsg != "" && out.SystemMsg == "" {
			out.SystemMsg = res.SystemMsg
		}
		if res.ExtraContext != "" && out.ExtraContext == "" {
			out.ExtraContext = res.ExtraContext
		}
		if res.Block && !out.Block {
			out.Block, out.Reason = true, res.Reason
		}
		if res.UpdatedInput != nil && out.UpdatedInput == nil {
			out.UpdatedInput = res.UpdatedInput
		}
		if res.UpdatedOutput != "" && out.UpdatedOutput == "" {
			out.UpdatedOutput = res.UpdatedOutput
		}
		if res.Permission != "" && out.Permission == "" {
			out.Permission, out.PermissionWhy = res.Permission, res.PermissionWhy
		}
	}
	return out
}

// ccHookInputJSON 组装 hook 的 stdin JSON（对齐 Claude 公共字段 + 工具事件字段）。
func (a *App) ccHookInputJSON(sess *SessionMeta, event, toolName string, toolInput any, payload map[string]any) []byte {
	m := map[string]any{
		"session_id":      sess.ID,
		"transcript_path": a.ccTranscriptPath(sess),
		"cwd":             sess.Workspace,
		"hook_event_name": event,
		"permission_mode": ccPermissionMode(sess),
	}
	if toolName != "" {
		m["tool_name"] = toolName
	}
	if toolInput != nil {
		m["tool_input"] = toolInput
	}
	for k, v := range payload {
		m[k] = v
	}
	data, _ := json.Marshal(m)
	return data
}

// ccPermissionMode 把本引擎的权限档位映射到 Claude 的命名。
func ccPermissionMode(sess *SessionMeta) string {
	switch sess.Permission {
	case "read-only":
		return "default"
	case "workspace-write":
		return "acceptEdits"
	case "full":
		return "bypassPermissions"
	}
	return "default"
}

// ccTranscriptPath 生成会话的简化 JSONL 导出（每行 {role,content}），供 hook
// 读 transcript_path。惰性生成：有 hook 事件才写（简化快照，对齐 Claude
// 「可能滞后」的语义；完整记录以本引擎会话存储为准）。
func (a *App) ccTranscriptPath(sess *SessionMeta) string {
	dir := filepath.Join(a.CCPlugins.Root(), "transcript")
	_ = os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, sess.ID+".jsonl")
	msgs, _ := a.Store.Messages(sess.ID)
	var b bytes.Buffer
	for _, m := range msgs {
		content, _ := m.Content.(string)
		if strings.TrimSpace(content) == "" {
			continue
		}
		line, _ := json.Marshal(map[string]any{"role": m.Role, "content": content})
		b.Write(line)
		b.WriteByte('\n')
	}
	_ = os.WriteFile(path, b.Bytes(), 0644)
	return path
}

// ccRunHook 执行一条 hook 并解释输出。命令型带插件路径变量环境与工作目录；
// shell 形态过危险命令护栏（与 run_command 同一道闸）。
func (a *App) ccRunHook(ctx context.Context, rule ccHookRule, sess *SessionMeta, input []byte) ccHookOutcome {
	timeout := time.Duration(rule.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	userCfg := a.CCPlugins.UserConfigOf(rule.Plugin)
	pluginEnv := append(os.Environ(),
		"CLAUDE_PLUGIN_ROOT="+rule.Plugin.Root,
		"CLAUDE_PLUGIN_DATA="+rule.Plugin.DataDir,
		"ZCODE_PLUGIN_ROOT="+rule.Plugin.Root,
		"ZCODE_PLUGIN_DATA="+rule.Plugin.DataDir,
	)
	if sess != nil && sess.Workspace != "" {
		pluginEnv = append(pluginEnv, "CLAUDE_PROJECT_DIR="+sess.Workspace, "ZCODE_PROJECT_DIR="+sess.Workspace)
	}
	var stdout, stderr bytes.Buffer
	exitCode := 0
	switch rule.Type {
	case "http":
		url := rule.Plugin.expandVars(rule.URL, userCfg)
		req, err := http.NewRequestWithContext(cctx, "POST", url, bytes.NewReader(input))
		if err != nil {
			return ccHookOutcome{}
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range rule.Headers {
			req.Header.Set(k, rule.Plugin.expandVars(v, userCfg))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[CC插件] hook %s %s HTTP 失败: %v", rule.Plugin.Key, rule.Event, err)
			return ccHookOutcome{}
		}
		defer resp.Body.Close()
		_, _ = stdout.ReadFrom(resp.Body)
	case "command":
		var cmd *exec.Cmd
		if len(rule.Args) > 0 {
			// exec 形态：无 shell，逐参变量替换
			argv := make([]string, len(rule.Args))
			for i, arg := range rule.Args {
				argv[i] = rule.Plugin.expandVars(arg, userCfg)
			}
			cmd = exec.CommandContext(cctx, rule.Plugin.expandVars(rule.Command, userCfg), argv...)
		} else {
			// shell 形态：路径变量替换后走平台 shell（对齐 run_command 的选择）
			line := strings.TrimSpace(rule.Plugin.expandVars(rule.Command, userCfg))
			if line == "" {
				return ccHookOutcome{}
			}
			if why := destructiveCommandHit(line); why != "" && a.guardEnabled() {
				log.Printf("[CC插件] hook %s %s 命令命中危险命令护栏，已拦截（%s）", rule.Plugin.Key, rule.Event, why)
				return ccHookOutcome{Block: true, Reason: "hook 命令被危险命令护栏拦截：" + why}
			}
			cmd = ccShellCommand(cctx, line)
		}
		cmd.Stdin = bytes.NewReader(input)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		cmd.Env = pluginEnv
		if sess != nil && sess.Workspace != "" {
			if info, err := os.Stat(sess.Workspace); err == nil && info.IsDir() {
				cmd.Dir = sess.Workspace
			}
		}
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				log.Printf("[CC插件] hook %s %s 启动失败: %v", rule.Plugin.Key, rule.Event, err)
				return ccHookOutcome{}
			}
		}
	default:
		return ccHookOutcome{}
	}
	return ccInterpretHookOutput(exitCode, stdout.String(), stderr.String())
}

// ccShellCommand 按平台选择 shell（对齐 run_command：Windows powershell，其他 sh）。
func ccShellCommand(ctx context.Context, line string) *exec.Cmd {
	if isWindowsRuntime {
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", line)
	}
	return exec.CommandContext(ctx, "sh", "-c", line)
}

// ccInterpretHookOutput 解释 hook 输出：exit code 2 = 阻断（stderr 作原因）；
// stdout 是 JSON 对象时逐字段取 decision / hookSpecificOutput / systemMessage。
func ccInterpretHookOutput(exitCode int, stdout, stderr string) ccHookOutcome {
	out := ccHookOutcome{}
	if exitCode == 2 {
		out.Block = true
		out.Reason = strings.TrimSpace(stderr)
		if out.Reason == "" {
			out.Reason = "hook 以 exit code 2 阻断"
		}
		return out
	}
	s := strings.TrimSpace(stdout)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return out // 纯文本 stdout：Claude 会进上下文的事件（UserPromptSubmit 等）本引擎不注入
	}
	var doc struct {
		SystemMsg string `json:"systemMessage"`
		Decision  string `json:"decision"`
		Reason    string `json:"reason"`
		HookSpecific *struct {
			Permission    string          `json:"permissionDecision"`
			PermissionWhy string          `json:"permissionDecisionReason"`
			UpdatedInput  json.RawMessage `json:"updatedInput"`
			UpdatedOutput string          `json:"updatedToolOutput"`
			AdditionalCtx string          `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		return out
	}
	if doc.SystemMsg != "" {
		out.SystemMsg = doc.SystemMsg
	}
	if strings.EqualFold(doc.Decision, "block") {
		out.Block = true
		out.Reason = doc.Reason
	}
	if hs := doc.HookSpecific; hs != nil {
		switch strings.ToLower(hs.Permission) {
		case "allow", "deny", "ask":
			out.Permission = strings.ToLower(hs.Permission)
			out.PermissionWhy = hs.PermissionWhy
		}
		if len(hs.UpdatedInput) > 0 {
			out.UpdatedInput = hs.UpdatedInput
		}
		if hs.UpdatedOutput != "" {
			out.UpdatedOutput = hs.UpdatedOutput
		}
		if hs.AdditionalCtx != "" {
			out.ExtraContext = hs.AdditionalCtx
		}
	}
	return out
}
