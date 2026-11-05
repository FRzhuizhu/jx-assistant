//go:build windows

package main

// 内置连接器的硬性门控：能力没开时，两个元工具必须在执行前就拦下，
// 而不是"能读到清单、只是执行报错"。软性（清单里不出现）已由
// mcpservers_builtin_e2e_test.go 覆盖，这里专测硬性这一半。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuiltinConnectorHardGating(t *testing.T) {
	app := &App{Cfg: &Config{DataDir: t.TempDir()}}
	app.MCPServers = InitMCPServers(app, t.TempDir())
	// 造一条带能力门控的连接器（等同内置 cua 的形态）
	if err := app.MCPServers.addQuiet(mcpServerEntry{
		Name:        "gated",
		Enabled:     true,
		Source:      "builtin:gated",
		RequiresCap: capComputer,
		Command:     "cmd.exe",
		Args:        []string{"/c", "echo hi"},
	}); err != nil {
		t.Fatalf("造门控连接器失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 可写档、但没有「操作电脑」能力：应该卡在能力上
	sess := &SessionMeta{ID: "nogrant", Permission: permWorkspace}

	tools := app.MCPServers.ConnectorTools()
	var readTool, callTool *Tool
	for i := range tools {
		switch tools[i].Spec.Function.Name {
		case "mcp_read_connector":
			readTool = &tools[i]
		case "mcp_call_tool":
			callTool = &tools[i]
		}
	}
	if readTool == nil || callTool == nil {
		t.Fatal("MCP 元工具不在目录里")
	}

	// 1) 读清单被拦
	tc := &toolContext{ctx: ctx, app: app, sess: sess, toolName: "mcp_read_connector"}
	_, err := readTool.Run(tc, json.RawMessage(`{"name":"gated"}`))
	if err == nil {
		t.Fatal("无「操作电脑」能力时，mcp_read_connector 必须被拦下")
	}
	if !strings.Contains(err.Error(), "操作电脑") {
		t.Fatalf("拦截原因应指向「操作电脑」，实得: %v", err)
	}
	t.Logf("读清单被拦: %v", err)

	// 2) 执行也被拦（只拦前者的话，知道工具名就能绕过）
	tc2 := &toolContext{ctx: ctx, app: app, sess: sess, toolName: "mcp_call_tool"}
	_, err = callTool.Run(tc2, json.RawMessage(`{"connector":"gated","tool":"Click","arguments":{}}`))
	if err == nil {
		t.Fatal("无「操作电脑」能力时，mcp_call_tool 必须被拦下")
	}
	if !strings.Contains(err.Error(), "操作电脑") {
		t.Fatalf("拦截原因应指向「操作电脑」，实得: %v", err)
	}
	t.Logf("执行被拦: %v", err)

	// 3) 授权之后不再卡在能力上（放开后会走到"连不上 cmd 的 MCP"之类的别的问题）
	granted := &SessionMeta{ID: "granted", Permission: permWorkspace, Tools: []string{capComputer}}
	tc3 := &toolContext{ctx: ctx, app: app, sess: granted, toolName: "mcp_read_connector"}
	_, err = readTool.Run(tc3, json.RawMessage(`{"name":"gated"}`))
	if err != nil && strings.Contains(err.Error(), "「操作电脑」") {
		t.Fatalf("授权后不该再卡在能力上，实得: %v", err)
	}
	t.Logf("授权后（预期报连接类错误）: %v", err)
}
