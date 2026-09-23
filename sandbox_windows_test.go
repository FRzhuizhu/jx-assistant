package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSandboxWindowsConfinement 真机验证受限令牌沙箱：
// workspace-write 下工作区内可写、工作区外被拒；read-only 下全部被拒。
func TestSandboxWindowsConfinement(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()

	run := func(policy, command string) string {
		r := sandboxRun{
			Dir: ws, Command: command, Shell: "powershell",
			Timeout: 60 * time.Second, Policy: policy, Workspace: ws,
		}
		out, err := spawnConfinedWindows(testCtx(), r)
		if err != nil {
			t.Fatalf("沙箱启动失败: %v", err)
		}
		return out
	}

	// 1) 沙箱内基本执行
	if out := run(permWorkspace, `echo sandbox-ok`); !strings.Contains(out, "sandbox-ok") {
		t.Fatalf("沙箱内 echo 失败: %s", out)
	}

	// 2) 工作区内写：应成功
	run(permWorkspace, `Set-Content -Path .\in-ws.txt -Value hi`)
	if _, err := os.Stat(filepath.Join(ws, "in-ws.txt")); err != nil {
		t.Fatalf("工作区内写文件失败: %v", err)
	}

	// 3) 工作区外写：应被拒
	out := run(permWorkspace, `Set-Content -Path '`+outside+`\out.txt' -Value hi`)
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); err == nil {
		t.Fatalf("沙箱没有拦住工作区外写入！输出: %s", out)
	} else if !strings.Contains(strings.ToLower(out), "denied") && !strings.Contains(out, "拒绝") {
		t.Logf("工作区外写被拦（输出未含明确拒绝字样）: %s", out)
	}

	// 4) read-only：工作区内也写不了
	out = run(permReadOnly, `Set-Content -Path .\ro.txt -Value hi`)
	if _, err := os.Stat(filepath.Join(ws, "ro.txt")); err == nil {
		t.Fatalf("read-only 沙箱没有拦住写入！输出: %s", out)
	}
}

func testCtx() context.Context { return context.Background() }
