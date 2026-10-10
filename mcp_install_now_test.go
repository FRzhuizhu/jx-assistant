package main

// 手动实装入口（不入 CI）：JX_MCP_INSTALL_NOW=<逗号分隔目录id> 时驱动真实
// 安装器（下载+venv+pip+注册+连通测试）落在开发数据目录 data/ 下。

import (
	"os"
	"strings"
	"testing"
)

func TestMCPInstallNow(t *testing.T) {
	ids := os.Getenv("JX_MCP_INSTALL_NOW")
	if ids == "" {
		t.Skip("set JX_MCP_INSTALL_NOW=comma-separated catalog ids to run the real installer")
	}
	r := InitMCPServers(&App{}, "data")
	for _, id := range strings.Split(ids, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		t.Logf("== 安装 %s …", id)
		name, notes, err := r.InstallCatalog(id)
		if err != nil {
			if id == "freecad" {
				t.Logf("[freecad] 预期内失败（本机 Python<3.12）: %v", err)
				continue
			}
			t.Errorf("安装 %s 失败: %v", id, err)
			continue
		}
		for _, n := range notes {
			t.Logf("[%s] 提示: %s", id, n)
		}
		tools, err := r.Test(name)
		if err != nil {
			t.Errorf("%s 连通测试失败: %v", name, err)
			continue
		}
		names := []string{}
		for _, tl := range tools {
			names = append(names, tl.Name)
		}
		t.Logf("[%s] 已连接，工具 %d 个: %s", name, len(tools), strings.Join(names, ", "))
	}
	r.ShutdownAll()
}
