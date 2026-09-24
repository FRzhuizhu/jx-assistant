package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sandboxRunOnce(t *testing.T, ws, policy, command string) string {
	r := sandboxRun{Dir: ws, Command: command, Shell: "powershell",
		Timeout: 60 * time.Second, Policy: policy, Workspace: ws}
	out, err := spawnConfinedWindows(context.Background(), r)
	if err != nil {
		t.Fatalf("沙箱启动失败: %v", err)
	}
	return out
}

func TestSandboxWhoamiDebug(t *testing.T) {
	ws := t.TempDir()
	out := sandboxRunOnce(t, ws, permWorkspace, `C:\Windows\System32\whoami.exe /groups`)
	t.Logf("OUTPUT:\n%s", out)
}

func TestSandboxConfinement(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()

	// 工作区内写：应成功
	sandboxRunOnce(t, ws, permWorkspace, `Set-Content -Path .\in-ws.txt -Value hi`)
	if _, err := os.Stat(filepath.Join(ws, "in-ws.txt")); err != nil {
		t.Fatalf("工作区内写文件失败: %v", err)
	}

	// 工作区外写：应被拒
	out := sandboxRunOnce(t, ws, permWorkspace, `Set-Content -Path '`+outside+`\out.txt' -Value hi`)
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); err == nil {
		t.Fatalf("沙箱没有拦住工作区外写入！输出: %s", out)
	}
	t.Logf("工作区外写被拒，输出: %s", strings.TrimSpace(out))

	// read-only：工作区内也写不了
	out = sandboxRunOnce(t, ws, permReadOnly, `Set-Content -Path .\ro.txt -Value hi`)
	if _, err := os.Stat(filepath.Join(ws, "ro.txt")); err == nil {
		t.Fatalf("read-only 沙箱没有拦住写入！输出: %s", out)
	}
	t.Logf("read-only 写被拒，输出: %s", strings.TrimSpace(out))
}
