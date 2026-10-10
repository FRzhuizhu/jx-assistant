package main

// mcpservers.go / mcpcatalog.go 的单元测试：名称域与定义校验、状态文件
// 落盘回读、插件层注册、目录完整性与 tar.gz 解压（含路径穿越防护）。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
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

func TestMCPLayerRegistration(t *testing.T) {
	a := &App{MCPServers: InitMCPServers(&App{}, t.TempDir())}
	// 同名服务器与插件路径的 mcp__<插件>_<服务器>__ 命名隔离：服务器名不含下划线
	if err := a.MCPServers.Upsert(mcpServerEntry{Name: "zw", Command: "x", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	layers := discoverPluginLayers(a)
	found := false
	for _, l := range layers.Layers() {
		if l.Info().ID == "mcp" {
			found = true
			ps := l.Plugins()
			if len(ps) != 1 || ps[0].ID != "zw" || !ps[0].Enabled {
				t.Errorf("mcp 层清单不符: %+v", ps)
			}
		}
	}
	if !found {
		t.Fatal("插件层注册表里没有 mcp 层")
	}
	// 统一总开关走层协议
	if err := layers.SetEnabled("mcp", "zw", false); err != nil {
		t.Fatalf("层开关失败: %v", err)
	}
	if st := a.MCPServers.List(); st[0].Enabled {
		t.Error("层开关未生效")
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
