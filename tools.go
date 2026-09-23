package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 工具集：每个工具 = JSON Schema 定义 + Go 实现。
// 所有会改动外界（写文件 / 执行命令）的工具都先过权限引擎 authorize()
// （见 perm.go）：评估 → 签发授权凭证 → 执行器核销凭证 → 执行；
// 只读与检索类工具做受保护路径检查后直接放行。
// 会话挂载 SSH 远程工作区时，文件操作走 SFTP、命令走远程 shell，
// 工具的参数与行为保持一致（执行环境抽象 env* 方法分发本地/远程）。

// Tool 是模型可调用的一个工具：JSON Schema 定义 + Go 实现。
type Tool struct {
	Spec ToolDef
	Run  func(t *toolContext, args json.RawMessage) (string, error)
}

// toolContext 是一次工具执行的上下文：请求级取消信号、应用引用、
// 当前会话（决定工作区范围与权限模式）与事件上报函数。
type toolContext struct {
	ctx  context.Context
	app  *App
	sess *SessionMeta
	emit func(Event)
}

// isRemote 判断会话是否挂载了 SSH 远程工作区。
func (t *toolContext) isRemote() bool {
	return t.sess != nil && t.sess.Remote != nil
}

// workspace 返回会话的工作区根目录（远程会话 = 远程目录）。
func (t *toolContext) workspace() string {
	if t.isRemote() {
		return t.sess.Remote.Dir
	}
	if t.sess.Workspace != "" {
		return t.sess.Workspace
	}
	return t.app.Cfg.Workspace
}

// resolve 把（可能相对的）路径解析为绝对路径；相对路径基于会话工作区。
// 远程会话按 POSIX 规则，本地按 OS 规则。
func (t *toolContext) resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	if t.isRemote() {
		if p == "" {
			return path.Clean(t.workspace()), nil
		}
		if !strings.HasPrefix(p, "/") {
			p = path.Join(t.workspace(), p)
		}
		return path.Clean(p), nil
	}
	if p == "" {
		return filepath.Abs(t.workspace())
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(t.workspace(), p)
	}
	return filepath.Abs(p)
}

// toolsFor 按会话模式返回可用工具。数字助理总控只拥有编排类工具
// （不亲自读写文件、不执行命令）；任务会话拥有全部实操工具；
// 辅助对话只有只读工具（定位是答疑，不修改任何东西）。
func (a *App) toolsFor(mode string) []Tool {
	history := []Tool{searchSessionsTool(), readSessionMessagesTool()}
	if mode == "assistant" {
		return append([]Tool{listSessionsTool(), readSummaryTool(), dispatchTaskTool(), sendToSessionTool()}, history...)
	}
	if mode == "aux" {
		return append([]Tool{readFileTool(), listDirTool(), globTool(), grepTool(), webFetchTool(), readImageTool()}, history...)
	}
	return append([]Tool{
		readFileTool(), writeFileTool(), editFileTool(), listDirTool(),
		globTool(), grepTool(), runCommandTool(), webFetchTool(), readImageTool(),
	}, history...)
}

// ---------- Schema 辅助 ----------

func objSchema(props map[string]any, required ...string) map[string]any {
	// required 为空时必须整体省略：序列化成 null 会被服务端按 schema 校验拒绝。
	if len(required) == 0 {
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func toolSpec(name, desc string, params map[string]any) ToolDef {
	return ToolDef{Type: "function", Function: ToolSpec{Name: name, Description: desc, Parameters: params}}
}

// ---------- 执行环境抽象（本地 / SSH 远程统一文件与目录操作） ----------

// envEntry 是与本地/远程无关的目录项信息。
type envEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

func (t *toolContext) envReadFile(p string) ([]byte, error) {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return nil, err
		}
		return rc.readFile(p)
	}
	return os.ReadFile(p)
}

// envWriteFile 写文件（不核销凭证；带凭证的写入口是 envWriteFileWithGrant）。
func (t *toolContext) envWriteFile(p string, data []byte) error {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return err
		}
		if err := rc.mkdirAll(path.Dir(p)); err != nil {
			return err
		}
		return rc.writeFile(p, data)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0644)
}

// envWriteFileWithGrant 先核销授权凭证再写文件（模型写文件的唯一入口）。
func (t *toolContext) envWriteFileWithGrant(p string, data []byte, g *Grant) error {
	if err := t.app.consumeGrant(g); err != nil {
		return err
	}
	return t.envWriteFile(p, data)
}

func (t *toolContext) envListDir(p string) ([]envEntry, error) {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return nil, err
		}
		return rc.listDir(p)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := []envEntry{}
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			out = append(out, envEntry{Name: e.Name(), IsDir: e.IsDir()})
			continue
		}
		out = append(out, envEntry{Name: e.Name(), IsDir: e.IsDir(), Size: info.Size()})
	}
	return out, nil
}

func (t *toolContext) envStat(p string) (isDir bool, size int64, err error) {
	if t.isRemote() {
		rc, rerr := t.app.getRemoteClient(t.sess)
		if rerr != nil {
			return false, 0, rerr
		}
		return rc.stat(p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return false, 0, err
	}
	return info.IsDir(), info.Size(), nil
}

// envWalk 遍历目录树（跳过 .git 与 node_modules），fn 收到相对 root 的
// 斜杠路径；fn 返回 filepath.SkipAll 时整体停止。远程经 SFTP 递归。
func (t *toolContext) envWalk(root string, fn func(rel string, e envEntry) error) error {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return err
		}
		visited := 0
		var rec func(dir, rel string) error
		rec = func(dir, rel string) error {
			if visited > 4000 {
				return filepath.SkipAll
			}
			entries, err := rc.listDir(dir)
			if err != nil {
				return nil // 单个目录读不了就跳过，不中断整个遍历
			}
			for _, e := range entries {
				visited++
				r := e.Name
				if rel != "" {
					r = rel + "/" + e.Name
				}
				if e.IsDir {
					if e.Name == ".git" || e.Name == "node_modules" {
						continue
					}
					if err := rec(dir+"/"+e.Name, r); err != nil {
						return err
					}
					continue
				}
				if err := fn(r, e); err != nil {
					return err
				}
			}
			return nil
		}
		err = rec(root, "")
		if err == filepath.SkipAll {
			return nil
		}
		return err
	}
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || (name == "node_modules" && p != root) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		return fn(filepath.ToSlash(rel), envEntry{Name: info.Name(), Size: info.Size()})
	})
}

// ---------- 文件类工具 ----------

func readFileTool() Tool {
	return Tool{
		Spec: toolSpec("read_file", "读取文本文件内容。path 支持相对工作区的路径。",
			objSchema(map[string]any{
				"path":   strProp("文件路径（相对或绝对）"),
				"offset": intProp("起始行号，从 1 开始，可选"),
				"limit":  intProp("最多读取行数，可选，默认 2000"),
			}, "path")),
		Run: runReadFile,
	}
}

func runReadFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "read_file", Path: path}); err != nil {
		return "", err
	}
	data, err := t.envReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s 是二进制文件，无法按文本读取", path)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start := p.Offset
	if start < 1 {
		start = 1
	}
	if start > len(lines) {
		return "（起始行超出文件范围）", nil
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 2000
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	out := strings.Join(lines[start-1:end], "\n")
	if len(out) > 256*1024 {
		out = out[:256*1024] + "\n…（内容过长已截断）"
	}
	suffix := ""
	if end < len(lines) {
		suffix = fmt.Sprintf("\n…（共 %d 行，可调大 limit 或用 offset 继续读取）", len(lines))
	}
	return out + suffix, nil
}

func writeFileTool() Tool {
	return Tool{
		Spec: toolSpec("write_file", "创建或覆盖文件。写权限由会话权限模式决定：「工作区内读写」仅允许工作区内的路径，「完全访问」不限。",
			objSchema(map[string]any{
				"path":    strProp("目标文件路径（相对或绝对）"),
				"content": strProp("要写入的完整内容"),
			}, "path", "content")),
		Run: runWriteFile,
	}
}

func runWriteFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	grant, err := t.authorize(permAction{Op: "write", Tool: "write_file", Path: path})
	if err != nil {
		return "", err
	}
	if err := t.envWriteFileWithGrant(path, []byte(p.Content), grant); err != nil {
		return "", err
	}
	return fmt.Sprintf("已写入 %s（%d 字节）", filepath.ToSlash(path), len(p.Content)), nil
}

func editFileTool() Tool {
	return Tool{
		Spec: toolSpec("edit_file", "把文件中的一段文本替换为新文本。old_string 必须在文件中恰好出现一次，除非 replace_all 为 true。",
			objSchema(map[string]any{
				"path":        strProp("文件路径"),
				"old_string":  strProp("要被替换的原文（需保证唯一）"),
				"new_string":  strProp("替换后的新文本"),
				"replace_all": boolProp("为 true 时替换全部出现，可选"),
			}, "path", "old_string", "new_string")),
		Run: runEditFile,
	}
}

func runEditFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.OldString == "" {
		return "", fmt.Errorf("old_string 不能为空")
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "edit_file", Path: path}); err != nil {
		return "", err
	}
	data, err := t.envReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, p.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string 在文件中不存在")
	}
	if count > 1 && !p.ReplaceAll {
		return "", fmt.Errorf("old_string 出现了 %d 次，请提供更长的片段保证唯一，或设置 replace_all", count)
	}
	grant, err := t.authorize(permAction{Op: "write", Tool: "edit_file", Path: path})
	if err != nil {
		return "", err
	}
	var updated string
	replaced := 1
	if p.ReplaceAll {
		updated = strings.ReplaceAll(content, p.OldString, p.NewString)
		replaced = count
	} else {
		updated = strings.Replace(content, p.OldString, p.NewString, 1)
	}
	if err := t.envWriteFileWithGrant(path, []byte(updated), grant); err != nil {
		return "", err
	}
	return fmt.Sprintf("已编辑 %s（替换 %d 处）", filepath.ToSlash(path), replaced), nil
}

func listDirTool() Tool {
	return Tool{
		Spec: toolSpec("list_dir", "列出目录内容。", objSchema(map[string]any{
			"path": strProp("目录路径，默认为工作区根目录"),
		})),
		Run: runListDir,
	}
}

func runListDir(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", err
		}
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "info", Tool: "list_dir", Path: path}); err != nil {
		return "", err
	}
	entries, err := t.envListDir(path)
	if err != nil {
		return "", err
	}
	lines := []string{}
	for _, e := range entries {
		if e.IsDir {
			lines = append(lines, e.Name+"/")
			continue
		}
		lines = append(lines, fmt.Sprintf("%s\t%d B", e.Name, e.Size))
	}
	if len(lines) == 0 {
		return "（空目录）", nil
	}
	return strings.Join(lines, "\n"), nil
}

func globTool() Tool {
	return Tool{
		Spec: toolSpec("glob", "按通配符模式查找文件，如 *.go 或 docs/*.md。返回相对 root 的路径。",
			objSchema(map[string]any{
				"pattern": strProp("通配符模式，如 *.go"),
				"path":    strProp("起始目录，默认工作区根目录"),
			}, "pattern")),
		Run: runGlob,
	}
}

func runGlob(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return "", fmt.Errorf("pattern 不能为空")
	}
	root, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if isDir, _, serr := t.envStat(root); serr != nil || !isDir {
		return "", fmt.Errorf("目录不存在: %s", root)
	}
	if _, err := t.authorize(permAction{Op: "info", Tool: "glob", Path: root}); err != nil {
		return "", err
	}
	matches := []string{}
	_ = t.envWalk(root, func(rel string, e envEntry) error {
		if len(matches) >= 500 {
			return filepath.SkipAll
		}
		ok := false
		if strings.Contains(p.Pattern, "/") || strings.Contains(p.Pattern, "\\") {
			ok, _ = path.Match(filepath.ToSlash(p.Pattern), rel)
		} else {
			ok, _ = path.Match(p.Pattern, e.Name)
		}
		if ok {
			matches = append(matches, rel)
		}
		return nil
	})
	if len(matches) == 0 {
		return "（无匹配文件）", nil
	}
	sort.Strings(matches)
	return strings.Join(matches, "\n"), nil
}

func grepTool() Tool {
	return Tool{
		Spec: toolSpec("grep", "在目录内的文本文件中搜索内容，pattern 为正则表达式（非法时按字面量匹配）。返回 文件:行号: 内容。",
			objSchema(map[string]any{
				"pattern": strProp("搜索模式（正则表达式）"),
				"path":    strProp("起始目录，默认工作区根目录"),
			}, "pattern")),
		Run: runGrep,
	}
}

func runGrep(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return "", fmt.Errorf("pattern 不能为空")
	}
	re, literalErr := regexp.Compile(p.Pattern)
	literal := literalErr != nil
	root, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "info", Tool: "grep", Path: root}); err != nil {
		return "", err
	}
	found := []string{}
	_ = t.envWalk(root, func(rel string, e envEntry) error {
		if len(found) >= 200 {
			return filepath.SkipAll
		}
		if e.Size > 2*1024*1024 {
			return nil
		}
		full := root
		if !t.isRemote() {
			full = filepath.Join(root, filepath.FromSlash(rel))
		} else {
			full = path.Join(root, rel)
		}
		data, rerr := t.envReadFile(full)
		if rerr != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, ln := range strings.Split(string(data), "\n") {
			hit := literal && strings.Contains(ln, p.Pattern)
			if !literal && re != nil {
				hit = re.MatchString(ln)
			}
			if hit {
				found = append(found, fmt.Sprintf("%s:%d: %s",
					filepath.ToSlash(full), i+1, truncateRunes(strings.TrimSpace(ln), 200)))
				if len(found) >= 200 {
					break
				}
			}
		}
		return nil
	})
	if len(found) == 0 {
		return "（无匹配）", nil
	}
	return strings.Join(found, "\n"), nil
}

// ---------- shell 工具（本地 Windows：powershell / cmd；远程：POSIX shell） ----------

func runCommandTool() Tool {
	return Tool{
		Spec: toolSpec("run_command", "在工作目录中执行一条 shell 命令并返回输出。默认在会话工作区内执行（沙箱内：最小环境变量、限时、输出截断）。远程工作区会话在远程主机执行（shell 参数忽略）。cwd 指到工作区之外属于沙箱外执行：需要用户开启相应能力并逐次审批。",
			objSchema(map[string]any{
				"command":     strProp("要执行的命令"),
				"shell":       strProp("本地执行时的 shell：powershell（默认）或 cmd；远程执行忽略"),
				"cwd":         strProp("工作目录（可选，默认会话工作区；工作区外目录需审批）"),
				"timeout_sec": intProp("超时秒数，默认 60，上限 600"),
			}, "command")),
		Run: runCommand,
	}
}

func runCommand(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Command    string `json:"command"`
		Shell      string `json:"shell"`
		Cwd        string `json:"cwd"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Command) == "" {
		return "", fmt.Errorf("command 不能为空")
	}
	cwd := p.Cwd
	if r, err := t.resolve(cwd); err == nil {
		cwd = r // 统一解析为绝对路径（空 = 会话工作区；远程为 POSIX 绝对路径）
	}
	grant, err := t.authorize(permAction{Op: "exec", Tool: "run_command", Command: p.Command, Cwd: cwd})
	if err != nil {
		return "", err
	}
	if err := t.app.consumeGrant(grant); err != nil {
		return "", err
	}
	timeout := time.Duration(60) * time.Second
	if p.TimeoutSec > 0 {
		timeout = time.Duration(min(p.TimeoutSec, 600)) * time.Second
	}

	// 远程工作区：SSH 远程 shell，cd 到目标目录后执行。
	if t.isRemote() {
		rc, rerr := t.app.getRemoteClient(t.sess)
		if rerr != nil {
			return "", rerr
		}
		out, rerr := rc.run(t.ctx, cwd, p.Command, timeout)
		if rerr != nil {
			return "", rerr
		}
		return out + "\n[远程执行于 " + t.sess.Remote.Display() + "]", nil
	}

	// 本地执行（沙箱内 = 工作区内目录 + 最小环境变量；沙箱外凭证携带完整环境）。
	if info, serr := os.Stat(cwd); serr != nil || !info.IsDir() {
		return "", fmt.Errorf("工作目录不存在: %s", cwd)
	}
	shell := strings.ToLower(strings.TrimSpace(p.Shell))
	if shell == "" {
		shell = "powershell"
	}
	ctx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	switch shell {
	case "cmd":
		cmd = exec.CommandContext(ctx, "cmd", "/C", p.Command)
	case "pwsh", "powershell":
		exe := "powershell"
		if shell == "pwsh" {
			exe = "pwsh"
		}
		cmd = exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", p.Command)
	default:
		return "", fmt.Errorf("不支持的 shell: %s（可选 powershell / cmd）", p.Shell)
	}
	cmd.Dir = cwd
	cmd.Env = sandboxEnv(grant.FullEnv)
	out, err := cmd.CombinedOutput()
	note := ""
	if ctx.Err() == context.DeadlineExceeded {
		note += "\n[命令超时被终止]"
	}
	if len(out) > 64*1024 {
		out = out[:64*1024]
		note += "\n[输出过长已截断]"
	}
	res := strings.TrimRight(string(out), "\r\n") + note
	if err != nil && ctx.Err() == nil {
		res += fmt.Sprintf("\n[进程退出异常: %v]", err)
	}
	if strings.TrimSpace(res) == "" {
		res = "（命令执行完成，无输出）"
	}
	return res, nil
}

// sandboxEnvWhitelist 是沙箱内执行保留的环境变量白名单：只留运行常规
// 工具链必需的变量，其余（含宿主环境里可能的密钥类变量）全部剔除。
var sandboxEnvWhitelist = []string{
	"PATH", "PATHEXT", "SystemRoot", "windir", "COMSPEC", "TEMP", "TMP",
	"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
	"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "PROGRAMDATA",
	"ALLUSERSPROFILE", "OS", "COMPUTERNAME", "NUMBER_OF_PROCESSORS",
	"PROCESSOR_ARCHITECTURE", "USERNAME", "LANG", "LC_ALL", "TERM", "HOME",
}

// sandboxEnv 返回执行环境变量：沙箱内取白名单子集，沙箱外/完全访问取全量。
func sandboxEnv(full bool) []string {
	if full {
		return os.Environ()
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		for _, w := range sandboxEnvWhitelist {
			if strings.EqualFold(kv[:i], w) {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

// ---------- 联网工具 ----------

func webFetchTool() Tool {
	return Tool{
		Spec: toolSpec("web_fetch", "抓取一个 http(s) 地址并返回正文文本；HTML 自动剥离标签。用于查阅文档、API 说明、教程等。内网/本机地址在非「完全访问」模式下会被拒绝。",
			objSchema(map[string]any{
				"url":       strProp("要抓取的 http(s) 地址"),
				"max_chars": intProp("最多返回的字符数，默认 20000，上限 100000"),
			}, "url")),
		Run: runWebFetch,
	}
}

func runWebFetch(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	u := strings.TrimSpace(p.URL)
	if u == "" {
		return "", fmt.Errorf("url 不能为空")
	}
	if _, err := t.authorize(permAction{Op: "net", Tool: "web_fetch"}); err != nil {
		return "", err
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("仅支持 http/https 地址")
	}
	// 防 SSRF：非「完全访问」模式下拒绝内网 / 本机 / 链路本地目标
	if permModeOf(t.sess) != permFull && isPrivateHost(parsed.Hostname()) {
		return "", fmt.Errorf("权限拒绝：%s 是内网/本机地址，非「完全访问」模式下不允许抓取", parsed.Hostname())
	}
	maxChars := p.MaxChars
	if maxChars <= 0 {
		maxChars = 20000
	}
	if maxChars > 100000 {
		maxChars = 100000
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; JXAssistant/0.1)")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncateRunes(string(body), 300))
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	text := string(body)
	if strings.Contains(ct, "html") || looksLikeHTML(text) {
		text = stripHTML(text)
	} else if !strings.Contains(ct, "text") && !strings.Contains(ct, "json") &&
		!strings.Contains(ct, "xml") && !strings.Contains(ct, "javascript") && ct != "" {
		return "", fmt.Errorf("不支持的响应类型：%s（仅支持文本类内容）", ct)
	}
	r := []rune(strings.TrimSpace(text))
	if len(r) > maxChars {
		r = r[:maxChars]
	}
	return fmt.Sprintf("HTTP %d｜%s\n\n%s", resp.StatusCode, u, string(r)), nil
}

// looksLikeHTML 粗判正文是不是 HTML（Content-Type 缺失时的兜底）。
func looksLikeHTML(s string) bool {
	head := s
	if len(head) > 2048 {
		head = head[:2048]
	}
	l := strings.ToLower(head)
	return strings.HasPrefix(strings.TrimSpace(l), "<!doctype html") ||
		strings.Contains(l, "<html") || (strings.Contains(l, "<body") && strings.Contains(l, "<"))
}

var (
	htmlBlockRe   = regexp.MustCompile(`(?is)<(script|style|noscript)\b[^>]*>.*?</(script|style|noscript)\s*>`)
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	htmlSpaceRe   = regexp.MustCompile(`\n{3,}`)
)

// stripHTML 剥离 HTML 标签并解码常见实体，尽量保留可读的正文文本。
func stripHTML(s string) string {
	s = htmlBlockRe.ReplaceAllString(s, "")
	s = htmlCommentRe.ReplaceAllString(s, "")
	s = htmlTagRe.ReplaceAllString(s, "\n")
	replacer := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&#x27;", "'", "&nbsp;", " ", "&mdash;", "—", "&hellip;", "…",
	)
	s = replacer.Replace(s)
	lines := []string{}
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimRight(ln, " \t\r")
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	out := strings.Join(lines, "\n")
	out = htmlSpaceRe.ReplaceAllString(out, "\n\n")
	return out
}

func isPrivateHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false // 域名交由 DNS；仅拦截字面内网地址
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
