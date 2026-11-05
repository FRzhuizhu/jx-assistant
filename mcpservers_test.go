package main

// mcpservers.go / mcpcatalog.go 的单元测试：名称域与定义校验、状态文件
// 落盘回读、清单段与常驻工具恒定性、读取守卫、目录完整性与 tar.gz 解压
// （含路径穿越防护）。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestMCPR(t *testing.T) *MCPServersRegistry {
	t.Helper()
	return InitMCPServers(&App{}, t.TempDir())
}

func TestMCPServerEntryValidate(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"solidworks", true}, {"a", true}, {"ZwCAD-2026", true},
		{"with_underscore", false}, {"1starts-digit", false}, {"", false},
		{"中文名", false}, {"has space", false},
	}
	for _, c := range cases {
		e := mcpServerEntry{Name: c.name, Command: "python", Enabled: true}
		err := e.validate()
		if c.ok && err != nil {
			t.Errorf("名称 %q 应合法，报错: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("名称 %q 应被拒绝", c.name)
		}
	}
	// 传输形态校验
	http := mcpServerEntry{Name: "ok", Type: "http", URL: "https://x/mcp"}
	if err := http.validate(); err != nil {
		t.Errorf("http 定义应合法: %v", err)
	}
	http.URL = ""
	if err := http.validate(); err == nil {
		t.Error("http 定义缺 url 应被拒绝")
	}
	stdio := mcpServerEntry{Name: "ok", Type: "stdio"}
	if err := stdio.validate(); err == nil {
		t.Error("stdio 定义缺 command 应被拒绝")
	}
}

func TestMCPServersStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := InitMCPServers(&App{}, dir)
	if err := r.Upsert(mcpServerEntry{Name: "alpha", Command: "python", Args: []string{"a.py"}, Enabled: true, Source: "catalog:solidworks"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(mcpServerEntry{Name: "beta", Type: "http", URL: "https://x/mcp", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled("alpha", false); err != nil {
		t.Fatal(err)
	}
	// 重新初始化：从 mcp-servers.json 回读
	r2 := InitMCPServers(&App{}, dir)
	list := r2.List()
	if len(list) != 2 {
		t.Fatalf("回读后应有 2 条，得到 %d", len(list))
	}
	if list[0].Name != "alpha" || list[0].Enabled {
		t.Errorf("alpha 应为停用状态: %+v", list[0])
	}
	if list[0].Source != "catalog:solidworks" {
		t.Errorf("Source 未回读: %+v", list[0])
	}
	if list[1].Name != "beta" || list[1].Type != "http" || list[1].URL != "https://x/mcp" {
		t.Errorf("beta 回读不符: %+v", list[1])
	}
	// 删除后回读只剩一条
	if err := r2.Remove("alpha"); err != nil {
		t.Fatal(err)
	}
	r3 := InitMCPServers(&App{}, dir)
	if got := len(r3.List()); got != 1 {
		t.Fatalf("删除回读后应剩 1 条，得到 %d", got)
	}
}

func TestMCPPromptBlockAndConnectorTools(t *testing.T) {
	r := newTestMCPR(t)
	// 空注册表：清单段为空，两个常驻工具仍在（工具目录形状恒定）
	if blk := r.PromptBlock(nil); blk != "" {
		t.Errorf("空注册表不应有清单段: %q", blk)
	}
	tools := r.ConnectorTools()
	if len(tools) != 2 || tools[0].Spec.Function.Name != "mcp_read_connector" || tools[1].Spec.Function.Name != "mcp_call_tool" {
		t.Fatalf("应有 mcp_read_connector/mcp_call_tool 两个常驻工具: %+v", tools)
	}
	specEmpty, _ := json.Marshal(tools[0].Spec)

	// 添加（启用/停用各一）：清单段只列启用的；工具定义不变（前缀缓存契约）
	if err := r.Upsert(mcpServerEntry{Name: "alpha", Command: "python", Enabled: true, Description: "甲连接器"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(mcpServerEntry{Name: "beta", Type: "http", URL: "https://x/mcp", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	blk := r.PromptBlock(nil)
	if !strings.Contains(blk, "alpha") || strings.Contains(blk, "beta") {
		t.Errorf("清单段应只含启用的连接器: %q", blk)
	}
	if !strings.Contains(blk, "mcp_read_connector") {
		t.Errorf("清单段应提示按需读取的用法: %q", blk)
	}
	tools2 := r.ConnectorTools()
	specPopulated, _ := json.Marshal(tools2[0].Spec)
	if string(specEmpty) != string(specPopulated) {
		t.Error("连接器增删不应改变常驻工具定义（前缀缓存契约）")
	}
}

func TestMCPReadConnectorGuards(t *testing.T) {
	r := newTestMCPR(t)
	ctx := context.Background()
	// 不存在 / 已停用：报可操作的错误，不触发连接
	if _, err := r.ReadConnector(ctx, "ghost"); err == nil {
		t.Error("不存在的连接器应报错")
	}
	if err := r.Upsert(mcpServerEntry{Name: "beta", Type: "http", URL: "https://x/mcp", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadConnector(ctx, "beta"); err == nil {
		t.Error("已停用的连接器应报错")
	}
	// CallToolByName 同一对守卫
	if _, _, err := r.CallToolByName(ctx, "ghost", "t", nil); err == nil {
		t.Error("不存在连接器的调用应报错")
	}
	if _, _, err := r.CallToolByName(ctx, "beta", "t", nil); err == nil {
		t.Error("已停用连接器的调用应报错")
	}
}

func TestMCPCatalogIntegrity(t *testing.T) {
	r := newTestMCPR(t)
	seen := map[string]bool{}
	for _, it := range mcpCatalog {
		if it.ID == "" || it.Name == "" || it.Vendor == "" {
			t.Errorf("目录条目缺字段: %+v", it)
		}
		if seen[it.ID] {
			t.Errorf("目录 id 重复: %s", it.ID)
		}
		seen[it.ID] = true
		if it.install == nil {
			t.Errorf("目录条目 %s 没有 install 函数", it.ID)
		}
	}
	// 安装状态标记：注册了 catalog:<id> 来源的服务器 → 已装
	if err := r.Upsert(mcpServerEntry{Name: "solidworks", Command: "x", Enabled: true, Source: "catalog:solidworks"}); err != nil {
		t.Fatal(err)
	}
	view := r.CatalogView()
	var sw *mcpCatalogView
	for i := range view {
		if view[i].ID == "solidworks" {
			sw = &view[i]
		}
	}
	if sw == nil || !sw.Installed || sw.InstalledServer != "solidworks" {
		t.Errorf("solidworks 桥应标记已装: %+v", sw)
	}
}

func TestMCPExtractTarGz(t *testing.T) {
	// 内存里构造一个 GitHub 形态的 tar.gz（首层 repo-branch/ 目录）
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"repo-main/server.py":        "print('hi')",
		"repo-main/.claude/x.json":   "{}",
		"repo-main":                  "", // 目录条目
	}
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0644}
		if content == "" {
			hdr.Typeflag = tar.TypeDir
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(content))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if content != "" {
			tw.Write([]byte(content))
		}
	}
	// 恶意条目：穿越到上级
	tw.WriteHeader(&tar.Header{Name: "repo-main/../evil.txt", Typeflag: tar.TypeReg, Size: 5})
	tw.Write([]byte("boom!"))
	tw.Close()
	gz.Close()

	dst := t.TempDir()
	if err := mcpExtractTarGz(bytes.NewReader(buf.Bytes()), dst); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "server.py"))
	if err != nil || string(data) != "print('hi')" {
		t.Errorf("server.py 解压不符: %v %q", err, data)
	}
	if _, err := os.Stat(filepath.Join(dst, ".claude", "x.json")); err != nil {
		t.Errorf("嵌套文件缺失: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "evil.txt")); err == nil {
		t.Error("路径穿越未被拦截")
	}
}

func TestParseMCPServersDoc(t *testing.T) {
	// 形态一：mcpServers 包裹层
	m, err := parseMCPServersDoc([]byte(`{"mcpServers":{"sw":{"command":"python","args":["s.py"]}}}`), "fallback")
	if err != nil || len(m) != 1 {
		t.Fatalf("包裹层形态解析失败: %v %v", m, err)
	}
	// 形态二：扁平
	m, err = parseMCPServersDoc([]byte(`{"a":{"command":"x"},"b":{"type":"http","url":"https://x"}}`), "fallback")
	if err != nil || len(m) != 2 {
		t.Fatalf("扁平形态解析失败: %v %v", m, err)
	}
	// 形态三：单服务器对象，用文件名当名
	m, err = parseMCPServersDoc([]byte(`{"command":"python","args":["s.py"]}`), "my-bridge")
	if err != nil || len(m) != 1 {
		t.Fatalf("单对象形态解析失败: %v %v", m, err)
	}
	if _, ok := m["my-bridge"]; !ok {
		t.Fatalf("单对象形态应以 fallbackName 命名: %v", m)
	}
	// 坏输入：非 JSON / 缺 command / http 缺 url
	for _, bad := range []string{`not-json`, `{"a":{"args":["x"]}}`, `{"a":{"type":"http"}}`} {
		if _, err := parseMCPServersDoc([]byte(bad), "fallback"); err == nil {
			t.Errorf("坏输入应报错: %s", bad)
		}
	}
}

func TestMCPUpsertFromDefsAndRemoveBySource(t *testing.T) {
	r := newTestMCPR(t)
	defs := map[string]mcpServerDef{
		"alpha": {Command: "python"},
		"beta":  {Type: "http", URL: "https://x/mcp"},
	}
	names, err := r.UpsertFromDefs(defs, "market:cad-pack", "CAD 桥合集")
	if err != nil || len(names) != 2 {
		t.Fatalf("批量注册失败: %v %v", names, err)
	}
	if !r.HasSource("market:cad-pack") {
		t.Fatal("HasSource 应为真")
	}
	if r.HasSource("market:other") {
		t.Fatal("别的来源应为假")
	}
	// 整组注销
	removed := r.RemoveBySource("market:cad-pack")
	if len(removed) != 2 || removed[0] != "alpha" || removed[1] != "beta" {
		t.Fatalf("整组注销应删两个: %v", removed)
	}
	if r.HasSource("market:cad-pack") || r.Count() != 0 {
		t.Fatal("注销后应清空")
	}
}
