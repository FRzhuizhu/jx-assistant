package main

// Claude Code 插件组件桥：MCP servers（.mcp.json / manifest.mcpServers）。
// 纯 Go 实现的 MCP 客户端，两种传输：stdio（spawn command+args，行分隔
// JSON-RPC 2.0）与 streamable HTTP（POST JSON-RPC，响应为 JSON 或 SSE 流）。
// 每个服务器的工具以 mcp__<插件名>_<服务器>__<工具> 注册进工具目录（对齐
// Claude 的插件 MCP 工具命名，插件 hooks 的 matcher 可直接匹配）。
// 连接在插件重载时重建；调用失败自动重启一次。mcp 组件开关是本引擎的
// 信任边界（显式开启 = 放行该服务器的工具调用），不再逐工具审批。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ccMCPServerDef 是一个 MCP 服务器的定义（stdio 型或 http/sse 型）。
type ccMCPServerDef struct {
	Type    string            `json:"type"` // ""（stdio）| http | sse
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// loadMCP 合并 .mcp.json（默认，可省略 mcpServers 包裹层）与 manifest.mcpServers
// （内联对象或路径；同名服务器覆盖 .mcp.json 条目，对齐 Claude 规则）。
func (p *CCPlugin) loadMCP() {
	p.MCPDefs = map[string]ccMCPServerDef{}
	// .mcp.json 文件
	if data, err := os.ReadFile(filepath.Join(p.Root, ".mcp.json")); err == nil {
		ccParseMCPServers(data, p.MCPDefs)
	}
	// manifest.mcpServers：对象内联或路径
	raw := p.Manifest.MCPServers
	if len(raw) == 0 {
		return
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil && strings.HasSuffix(one, ".json") {
		if data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(strings.TrimPrefix(one, "./")))); err == nil {
			ccParseMCPServers(data, p.MCPDefs)
		}
		return
	}
	ccParseMCPServers(raw, p.MCPDefs)
}

// ccParseMCPServers 解析一段 MCP 配置 JSON（容忍 {"mcpServers": {...}} 包裹层）。
func ccParseMCPServers(data []byte, into map[string]ccMCPServerDef) {
	var wrapped struct {
		MCPServers map[string]ccMCPServerDef `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.MCPServers) > 0 {
		for k, v := range wrapped.MCPServers {
			into[k] = v
		}
		return
	}
	var flat map[string]ccMCPServerDef
	if err := json.Unmarshal(data, &flat); err == nil {
		for k, v := range flat {
			into[k] = v
		}
	}
}

// ---------- MCP 客户端 ----------

// mcpToolDef 是服务器上报的一个工具。
type mcpToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpHost 是 mcpClient 的宿主：变量替换与子进程工作目录的来源。
// CC 插件路径用 ccPluginHost（${CLAUDE_PLUGIN_ROOT} 等替换 + 插件根做工作
// 目录）；独立 MCP 服务器（mcpservers.go）用 mcpPlainHost（仅 ${环境变量}）。
type mcpHost interface {
	ExpandVars(s string) string
	WorkDir() string
}

// ccPluginHost 是 CC 插件侧宿主。
type ccPluginHost struct {
	p   *CCPlugin
	reg *CCPluginRegistry
}

func (h ccPluginHost) ExpandVars(s string) string {
	return h.p.expandVars(s, h.reg.UserConfigOf(h.p))
}

func (h ccPluginHost) WorkDir() string { return h.p.Root }

// mcpPlainHost 是独立服务器宿主：${VAR} 展开为环境变量，工作目录是
// mcp-servers 数据根（相对路径命令按此解析）。
type mcpPlainHost struct{ dir string }

func (h mcpPlainHost) ExpandVars(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.Expand(s, os.Getenv)
}

func (h mcpPlainHost) WorkDir() string { return h.dir }

// mcpClient 是一个已连接的 MCP 服务器（stdio 或 http）。
type mcpClient struct {
	fullName string // 插件路径 <插件名>_<服务器>；独立路径就是服务器名
	def      ccMCPServerDef
	host     mcpHost

	mu       sync.Mutex
	started  bool
	cmd      *exec.Cmd
	stdinW   interface{ Write([]byte) (int, error) }
	stdinC   interface{ Close() error }
	stdout   *bufio.Reader
	httpSID  string // streamable HTTP 的会话 id（Mcp-Session-Id）
	nextID   int64
	tools    []mcpToolDef
}

// jsonrpcRequest / jsonrpcResponse 是 JSON-RPC 2.0 的帧。
type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method,omitempty"`
	Params  any    `json:"params,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// start 建立连接并完成 initialize 握手 + tools/list（幂等；已启动直接返回）。
func (c *mcpClient) start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	if err := c.connect(ctx); err != nil {
		return err
	}
	if err := c.initialize(ctx); err != nil {
		c.shutdownLocked()
		return err
	}
	tools, err := c.listTools(ctx)
	if err != nil {
		c.shutdownLocked()
		return err
	}
	c.tools = tools
	c.started = true
	return nil
}

// connect 建立传输（spawn 进程或准备 HTTP 会话）。
func (c *mcpClient) connect(ctx context.Context) error {
	if c.def.Type == "http" || c.def.Type == "sse" {
		return nil // http 无预连接，逐请求进行
	}
	cmdName := c.host.ExpandVars(c.def.Command)
	args := make([]string, len(c.def.Args))
	for i, a := range c.def.Args {
		args[i] = c.host.ExpandVars(a)
	}
	cmd := exec.Command(cmdName, args...)
	env := os.Environ()
	for k, v := range c.def.Env {
		env = append(env, c.host.ExpandVars(k)+"="+c.host.ExpandVars(v))
	}
	cmd.Env = env
	cmd.Dir = c.host.WorkDir()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil // 服务器自身日志走它自己的输出，不混入协议流
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 MCP 服务器 %s 失败: %w", c.fullName, err)
	}
	c.cmd = cmd
	c.stdinW = stdin
	c.stdinC = stdin
	c.stdout = bufio.NewReaderSize(stdout, 1<<20)
	return nil
}

// initialize 完成 MCP 握手（initialize → initialized 通知）。
func (c *mcpClient) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "jx-digital-assistant", "version": appVersion},
	}
	if _, err := c.rpc(ctx, "initialize", params); err != nil {
		return err
	}
	// initialized 通知（无 id、不期待响应）
	if c.def.Type == "http" || c.def.Type == "sse" {
		_, _ = c.httpPost(ctx, jsonrpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}, false)
		return nil
	}
	_, err := fmt.Fprintln(c.stdinW, rpcLine(jsonrpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}))
	return err
}

// listTools 拉取工具清单。
func (c *mcpClient) listTools(ctx context.Context) ([]mcpToolDef, error) {
	res, err := c.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var page struct {
		Tools []mcpToolDef `json:"tools"`
	}
	raw, _ := json.Marshal(res)
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("tools/list 结果解析失败: %w", err)
	}
	return page.Tools, nil
}

// callTool 调用一个工具并返回文本结果（isError 语义转为错误文本）。
func (c *mcpClient) callTool(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		// 单次自愈：未启动或上次死亡时重连一次
		if err := c.start(ctx); err != nil {
			return "", err
		}
		c.mu.Lock()
	}
	c.mu.Unlock()
	params := map[string]any{"name": tool, "arguments": json.RawMessage(args)}
	if len(args) == 0 {
		params["arguments"] = map[string]any{}
	}
	res, err := c.rpc(ctx, "tools/call", params)
	if err != nil {
		// stdio 服务器可能已死：重启一次再试
		if c.def.Type != "http" && c.def.Type != "sse" {
			c.mu.Lock()
			c.shutdownLocked()
			c.mu.Unlock()
			if err2 := c.start(ctx); err2 == nil {
				if res2, err3 := c.rpc(ctx, "tools/call", params); err3 == nil {
					res, err = res2, nil
				}
			}
		}
		if err != nil {
			return "", err
		}
	}
	return mcpResultText(res), nil
}

// mcpResultText 从 tools/call 结果提取文本（content 数组的 text 项拼接）。
func mcpResultText(res any) string {
	raw, _ := json.Marshal(res)
	var doc struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool   `json:"isError"`
		Text    string `json:"text"`
	}
	_ = json.Unmarshal(raw, &doc)
	var b strings.Builder
	for _, item := range doc.Content {
		if item.Type == "text" && item.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(item.Text)
		}
	}
	if b.Len() == 0 && doc.Text != "" {
		b.WriteString(doc.Text)
	}
	if doc.IsError {
		return "（MCP 工具报告错误）" + b.String()
	}
	if b.Len() == 0 {
		return "（工具执行完成，无文本输出）"
	}
	return b.String()
}

// rpc 发一个 JSON-RPC 请求并等匹配 id 的响应。
func (c *mcpClient) rpc(ctx context.Context, method string, params any) (any, error) {
	if c.def.Type == "http" || c.def.Type == "sse" {
		c.nextID++
		return c.httpRPC(ctx, jsonrpcRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params})
	}
	c.nextID++
	id := c.nextID
	line := rpcLine(jsonrpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if _, err := fmt.Fprintln(c.stdinW, line); err != nil {
		return nil, fmt.Errorf("MCP %s 写入失败: %w", c.fullName, err)
	}
	return c.readResponse(id)
}

// readResponse 读 stdio 行直到拿到匹配 id 的响应（跳过服务器的 notification）。
func (c *mcpClient) readResponse(id int64) (any, error) {
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("MCP %s 连接断开: %w", c.fullName, err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var resp jsonrpcRequest
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue // 非 JSON 行（服务器日志混入等）：跳过
		}
		if resp.ID == 0 || resp.Method != "" {
			continue // notification / 服务器发起的请求：本客户端只做应答方以外的角色
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP %s 错误 %d: %s", c.fullName, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
	return nil, fmt.Errorf("MCP %s 响应超时", c.fullName)
}

// httpRPC 走 streamable HTTP：POST 请求体，响应为 JSON 或 SSE（取匹配 id 的 data）。
func (c *mcpClient) httpRPC(ctx context.Context, req jsonrpcRequest) (any, error) {
	data, err := c.httpPost(ctx, req, true)
	if err != nil {
		return nil, err
	}
	var resp jsonrpcRequest
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("MCP %s HTTP 响应解析失败: %w", c.fullName, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("MCP %s 错误 %d: %s", c.fullName, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// httpPost 发一个 JSON-RPC 帧，期待响应时返回响应体（处理 SSE 包装）。
func (c *mcpClient) httpPost(ctx context.Context, req jsonrpcRequest, wantResponse bool) ([]byte, error) {
	url := c.host.ExpandVars(c.def.URL)
	body, _ := json.Marshal(req)
	hctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(hctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.def.Headers {
		hreq.Header.Set(k, c.host.ExpandVars(v))
	}
	if c.httpSID != "" {
		hreq.Header.Set("Mcp-Session-Id", c.httpSID)
	}
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("MCP %s HTTP 请求失败: %w", c.fullName, err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.httpSID = sid
	}
	if !wantResponse {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("MCP %s HTTP %d", c.fullName, resp.StatusCode)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEFirstData(resp.Body)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return buf.Bytes(), nil
}

// readSSEFirstData 从 SSE 流里取第一个含 JSON-RPC 响应的 data 帧并读到流尾
//（streamable HTTP 的响应流可能带多个事件，取首个 id 匹配的由调用方校验）。
func readSSEFirstData(body interface{ Read([]byte) (int, error) }) ([]byte, error) {
	br := bufio.NewReaderSize(body, 1<<20)
	deadline := time.Now().Add(120 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
				if payload != "" && payload != "[DONE]" {
					var probe jsonrpcRequest
					if err := json.Unmarshal([]byte(payload), &probe); err == nil && probe.ID != 0 {
						return []byte(payload), nil
					}
					last = []byte(payload) // 兜底：最后一个能解析的 data
				}
			}
		}
		if err != nil {
			break // 流结束
		}
	}
	if last != nil {
		return last, nil
	}
	return nil, fmt.Errorf("SSE 流里没有 JSON-RPC 响应")
}

// rpcLine 序列化一个 JSON-RPC 帧为单行。
func rpcLine(req jsonrpcRequest) string {
	data, _ := json.Marshal(req)
	return string(data)
}

// shutdownLocked 关闭连接（调用方持锁）。
func (c *mcpClient) shutdownLocked() {
	if c.stdinC != nil {
		_ = c.stdinC.Close()
		c.stdinC = nil
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
		c.cmd = nil
	}
	c.stdout = nil
	c.stdinW = nil
	c.started = false
	c.tools = nil
}

// shutdown 对外关闭。
func (c *mcpClient) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdownLocked()
}

// ---------- 注册表侧的 MCP 管理 ----------

// mcpClients 返回全部应启动的 MCP 客户端（mcp 组件放行的插件的每个服务器）。
func (r *CCPluginRegistry) mcpClients() []*mcpClient {
	out := []*mcpClient{}
	for _, p := range r.enabled() {
		if !p.componentEnabled("mcp") {
			continue
		}
		names := make([]string, 0, len(p.MCPDefs))
		for n := range p.MCPDefs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, &mcpClient{
				fullName: p.Name + "_" + n, def: p.MCPDefs[n], host: ccPluginHost{p: p, reg: r},
			})
		}
	}
	return out
}

// MCPStartAll 连接全部启用的 MCP 服务器（引擎启动时后台执行；失败记日志不阻断）。
func (r *CCPluginRegistry) MCPStartAll() {
	if r == nil {
		return
	}
	clients := r.mcpClients()
	if len(clients) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *mcpClient) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.start(ctx); err != nil {
				log.Printf("[CC插件] MCP %s 启动失败: %v", c.fullName, err)
			} else {
				log.Printf("[CC插件] MCP %s 已连接，工具 %d 个", c.fullName, len(c.tools))
			}
		}(c)
	}
	go wg.Wait() // 后台进行，不阻塞启动
}

// MCPTools 返回全部已连接服务器的工具（挂进 toolsFor 的动态目录段）。
// 未连接成功的服务器不出现——工具目录只在重载边界变化，会话中保持稳定。
func (r *CCPluginRegistry) MCPTools() []Tool {
	if r == nil {
		return nil
	}
	out := []Tool{}
	for _, c := range r.mcpClients() {
		c.mu.Lock()
		tools, started := c.tools, c.started
		c.mu.Unlock()
		if !started {
			continue
		}
		for _, t := range tools {
			tl := t
			full := "mcp__" + c.fullName + "__" + tl.Name
			params := tl.InputSchema
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, Tool{
				Spec: ToolDef{Type: "function", Function: ToolSpec{
					Name: full, Description: ccToolDesc(tl, c.fullName), Parameters: params,
				}},
				Run: func(t *toolContext, args json.RawMessage) (string, error) {
					return t.app.CCPlugins.mcpCall(t.ctx, full, args)
				},
			})
		}
	}
	return out
}

// ccToolDesc 组装 MCP 工具描述（含来源标注）。
func ccToolDesc(t mcpToolDef, server string) string {
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = "MCP 工具（来自插件服务器 " + server + "）"
	} else {
		desc += "（MCP 工具，来自插件服务器 " + server + "）"
	}
	return desc
}

// mcpCall 按完整工具名路由到客户端执行。
func (r *CCPluginRegistry) mcpCall(ctx context.Context, full string, args json.RawMessage) (string, error) {
	// 名形如 mcp__<插件>_<服务器>__<工具>：按工具清单反查
	for _, c := range r.mcpClients() {
		prefix := "mcp__" + c.fullName + "__"
		if strings.HasPrefix(full, prefix) {
			c.mu.Lock()
			started := c.started
			c.mu.Unlock()
			if !started {
				if err := c.start(ctx); err != nil {
					return "", err
				}
			}
			return c.callTool(ctx, strings.TrimPrefix(full, prefix), args)
		}
	}
	return "", fmt.Errorf("MCP 工具 %s 不在已连接清单里", full)
}
