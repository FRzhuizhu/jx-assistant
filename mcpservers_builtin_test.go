//go:build windows

package main

// 内置 MCP 连接器的行为约束：
//   - addQuiet 只落盘、不建连接（引擎启动期零 MCP 进程）；
//   - PromptBlock 按 RequiresCap 过滤——能力没开时模型看不到（软性管控）；
//   - requiresCapOf 供两个元工具做硬性拦截。
//
// 这三条共同支撑「工具定义不常驻、按需进上下文」的设计：模型永远只看到
// mcp_read_connector / mcp_call_tool 两个孤儿工具，缓存前缀不随权限变化。

import (
	"os"
	"strings"
	"testing"
)

func TestBuiltinMCPQuietAddAndCapGating(t *testing.T) {
	r := newTestMCPR(t)

	// 1) addQuiet：落盘成功，但不建立任何客户端连接
	e := mcpServerEntry{
		Name: "cua", Description: "测试用内置连接器", Enabled: true,
		Source: "builtin:cua", Command: "x", RequiresCap: capComputer,
	}
	if err := r.addQuiet(e); err != nil {
		t.Fatalf("addQuiet 失败: %v", err)
	}
	r.mu.Lock()
	clients := len(r.clients)
	r.mu.Unlock()
	if clients != 0 {
		t.Fatalf("addQuiet 不应建立连接，实得 %d 个客户端", clients)
	}
	if !r.HasName("cua") {
		t.Fatal("addQuiet 后应有 cua 条目")
	}

	// 2) 重复登记保持原状（不重复追加）
	if err := r.addQuiet(e); err != nil {
		t.Fatalf("重复 addQuiet 失败: %v", err)
	}
	if got := len(r.List()); got != 1 {
		t.Fatalf("重复登记应只有 1 条，实得 %d", got)
	}

	// 3) 清单段的能力门控
	if blk := r.PromptBlock(nil); strings.Contains(blk, "cua") {
		t.Errorf("无会话上下文时不应列出受门控的连接器: %q", blk)
	}
	sessOff := &SessionMeta{ID: "s1", Mode: "task", Permission: "workspace-write"}
	if blk := r.PromptBlock(sessOff); strings.Contains(blk, "cua") {
		t.Errorf("未勾选能力时不应列出: %q", blk)
	}
	sessOn := &SessionMeta{ID: "s2", Mode: "task", Permission: "workspace-write", Tools: []string{capComputer}}
	if blk := r.PromptBlock(sessOn); !strings.Contains(blk, "cua") {
		t.Errorf("勾选「操作电脑」后应列出: %q", blk)
	}
	sessFull := &SessionMeta{ID: "s3", Mode: "task", Permission: "full"}
	if blk := r.PromptBlock(sessFull); !strings.Contains(blk, "cua") {
		t.Errorf("完全访问（捆绑能力）应列出: %q", blk)
	}

	// 4) 元工具据此硬拦
	if got := r.requiresCapOf("cua"); got != capComputer {
		t.Errorf("requiresCapOf(cua) = %q，期望 %q", got, capComputer)
	}
	if got := r.requiresCapOf("不存在"); got != "" {
		t.Errorf("未知连接器应返回空串，实得 %q", got)
	}
}

// TestBuiltinComponentsDirMissing 本机没有 内置组件 目录时，必须解析成空——
// 内置连接器据此跳过注册，不造一个"点了就报错"的空壳。
func TestBuiltinComponentsDirMissing(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	if got := builtinComponentsDir(); got != "" {
		t.Fatalf("空目录里不应解析出内置组件路径，实得 %q", got)
	}
}

// TestMaterializeCuaWhenComponentPresent 仓库/安装目录里带着 cua 驱动时，
// 迁移释放应把它注册成 enabled + RequiresCap=computer 的内置连接器。
func TestMaterializeCuaWhenComponentPresent(t *testing.T) {
	dir := t.TempDir()
	app := &App{Cfg: &Config{DataDir: dir}}
	app.MCPServers = InitMCPServers(app, dir)

	cmd, args, _, ok := resolveCuaDriver(app)
	if !ok {
		t.Skip("本机没有 内置组件/cua-driver/cua-driver.exe，跳过")
	}
	if !strings.HasSuffix(strings.ToLower(cmd), "cua-driver.exe") {
		t.Errorf("解析出的命令不对: %q", cmd)
	}
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("应以 stdio MCP 方式启动，实得 args=%v", args)
	}

	if err := app.materializeBuiltinMCP(); err != nil {
		t.Fatalf("materializeBuiltinMCP 失败: %v", err)
	}
	if !app.MCPServers.HasName("cua") {
		t.Fatal("cua 应被注册")
	}
	app.MCPServers.mu.Lock()
	var got mcpServerEntry
	for _, s := range app.MCPServers.file.Servers {
		if s.Name == "cua" {
			got = *s
		}
	}
	app.MCPServers.mu.Unlock()
	if !got.Enabled || got.RequiresCap != capComputer || got.Source != "builtin:cua" {
		t.Errorf("注册结果不对: enabled=%v cap=%q source=%q", got.Enabled, got.RequiresCap, got.Source)
	}
	// 启动期零连接（addQuiet 的承诺）
	app.MCPServers.mu.Lock()
	clients := len(app.MCPServers.clients)
	app.MCPServers.mu.Unlock()
	if clients != 0 {
		t.Errorf("释放内置连接器不应建立连接，实得 %d", clients)
	}
}
