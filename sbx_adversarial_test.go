package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 对抗性验证：workspace-write 沙箱下的各类逃逸尝试。
// 预期：DSH 官方同款语义——写类穿越/注册表/子进程/硬链接必须被拒；
// 已知盲区（父目录清柜权删除路线、Everyone 可写位置）为官方自报缺口，
// 2026-09 决策接受并显式钉住当前行为（未来从工具层限制）。
func TestSandboxAdversarial(t *testing.T) {
	skipIfNestedSandbox(t)
	ws := t.TempDir()
	outside := t.TempDir()

	try := func(name, command string) string {
		t.Helper()
		r := sandboxRun{Dir: ws, Command: command, Shell: "powershell",
			Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws}
		out, err := spawnConfinedWindows(context.Background(), r)
		if err != nil {
			t.Fatalf("%s: 沙箱启动失败: %v", name, err)
		}
		return out
	}
	denied := func(name, out string) bool {
		l := strings.ToLower(out)
		return strings.Contains(l, "denied") || strings.Contains(out, "拒绝") ||
			strings.Contains(l, "not permitted") || strings.Contains(l, "unauthorized") ||
			strings.Contains(l, "ep") // eperm/eacces
	}

	t.Run("路径穿越写", func(t *testing.T) {
		out := try("traversal", `Set-Content -Path ..\..\escaped.txt -Value x`)
		if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "escaped.txt")); err == nil {
			t.Errorf("穿越写逃逸成功（不应发生）: %s", out)
		} else if !denied("traversal", out) {
			t.Logf("穿越写被拦但输出无明确拒绝字样: %s", out)
		} else {
			t.Logf("✓ 穿越写被拒")
		}
	})

	t.Run("Junction跳转写", func(t *testing.T) {
		try("junction-mk", `cmd /c mklink /J wslink '`+outside+`' >$null 2>&1`)
		out := try("junction-write", `Set-Content -Path .\wslink\j.txt -Value x`)
		if _, err := os.Stat(filepath.Join(outside, "j.txt")); err == nil {
			t.Errorf("Junction 写逃逸成功（不应发生）: %s", out)
		} else {
			t.Logf("✓ Junction 写被拒: %s", strings.TrimSpace(out))
		}
	})

	t.Run("删除外部文件（已知盲区）", func(t *testing.T) {
		victim := filepath.Join(outside, "victim.txt")
		_ = os.WriteFile(victim, []byte("keep"), 0644)
		out := try("del-out", `Remove-Item -LiteralPath '`+victim+`' -ErrorAction SilentlyContinue; if (Test-Path -LiteralPath '`+victim+`') { 'del-denied' } else { 'del-done' }`)
		// 判定实验（sbx_delete_matrix_test.go）：删除走父目录 FILE_DELETE_CHILD
		// （清柜权）路线时不触发 WRITE_RESTRICTED pass-2——%TEMP% 类用户
		// FullControl 位置可被非 -Force 删除（DSH 官方同款已知盲区，密级关卡
		// 撤除后接受，未来从工具层限制）。两种结果都记录不判败。
		if _, err := os.Stat(victim); err != nil {
			t.Logf("已知盲区：外部文件被删穿（官方同款，接受）: %s", strings.TrimSpace(out))
		} else {
			t.Logf("✓ 外部删除被拒: %s", strings.TrimSpace(out))
		}
	})

	t.Run("Public 目录写入（DSH 终版已关闭的 INTERACTIVE 逃逸）", func(t *testing.T) {
		// C:\Users\Public 默认授 INTERACTIVE 写权——restricting 列表若含
		// INTERACTIVE，pass-2 会放行。本产品列表与 DSH 终版一致（不含），
		// 此处必须被拒（对齐其 runner.spec 'ambient-writable escape regression'）。
		public := os.Getenv("PUBLIC")
		if public == "" {
			t.Skip("无 PUBLIC 环境变量（非标准 Windows 桌面）")
		}
		probe := filepath.Join(public, fmt.Sprintf("jx_probe_public_%d.txt", os.Getpid()))
		defer os.Remove(probe)
		out := try("public", `Set-Content -LiteralPath '`+probe+`' -Value x -ErrorAction SilentlyContinue; if (Test-Path -LiteralPath '`+probe+`') { 'pub-written' } else { 'pub-denied' }`)
		if _, err := os.Stat(probe); err == nil {
			t.Errorf("Public 写入成功（INTERACTIVE 逃逸未关死）: %s", strings.TrimSpace(out))
		} else {
			t.Logf("✓ Public 目录写入被拒（restricting 列表不含 INTERACTIVE，对齐 DSH 终版）")
		}
	})

	t.Run("写注册表HKCU", func(t *testing.T) {
		out := try("reg", `New-ItemProperty -Path HKCU:\Software\Classes -Name JXSbxProbe -Value 1 -PropertyType String -Force -ErrorAction SilentlyContinue | Out-Null; if (Get-ItemProperty -Path HKCU:\Software\Classes -Name JXSbxProbe -ErrorAction SilentlyContinue) { 'reg-written' } else { 'reg-denied' }`)
		if strings.Contains(out, "reg-written") {
			t.Errorf("注册表 HKCU 写逃逸成功（不应发生）")
		} else {
			t.Logf("✓ 注册表写被拒")
		}
	})

	t.Run("子进程继承令牌后写外部", func(t *testing.T) {
		out := try("child", `Start-Process -FilePath cmd -ArgumentList '/c echo x> '`+outside+`\child.txt`+`' -Wait -NoNewWindow -ErrorAction SilentlyContinue; if (Test-Path '`+outside+`\child.txt') { 'child-wrote' } else { 'child-denied' }`)
		if strings.Contains(out, "child-wrote") {
			t.Errorf("子进程写外部逃逸成功（不应发生）")
		} else {
			t.Logf("✓ 子进程继承受限令牌，写外部被拒")
		}
	})

	t.Run("网络仍可用（沙箱按设计不限网）", func(t *testing.T) {
		out := try("net", `(New-Object Net.Sockets.TcpClient('127.0.0.1', 1)).Close() 2>$null; 'net-call-ok'`)
		if !strings.Contains(out, "net-call-ok") {
			t.Errorf("沙箱内网络调用异常: %s", out)
		}
	})

	t.Run("硬链接穿透（已知盲区，两个顺序都记录）", func(t *testing.T) {
		// DSH 官方自报缺口（密级关卡撤除后重新打开）：工作区授权的继承 ACE
		// 会进入硬链接文件的共享 SD，外部别名随之对受限令牌可写。
		//   顺序1（先建链后授权）：授权传播直接打中共享 SD（探针实测：
		//   建链后一次 grantWriteAce，外部名字的 SD 即出现 S-1-80-*(I)(W,D,DC)）。
		//   顺序2（先授权后建链）：受限进程打开 ws 内链接名时，NTFS 懒传播
		//   把根上待传播的继承 ACE 应用到共享 SD，pass-2 随即放行（探针实测：
		//   建链后 SD 无该 ACE，沙箱内 Add-Content 后出现且写入成功）。
		// 曾以首授后全树扫描摘 ACE 收尾（fixupMultiLinked），因懒传播在下次
		// 打开时重新应用而无效，已移除。两种结果都记录不判败（未来从工具层
		// 限制）。
		outFile := filepath.Join(outside, "linked.txt")
		_ = os.WriteFile(outFile, []byte("orig"), 0644)
		link := filepath.Join(ws, "hardlink.txt")
		if err := os.Link(outFile, link); err != nil {
			t.Skipf("创建硬链接失败（跳过）: %v", err)
		}
		try("hardlink", `Add-Content -Path .\hardlink.txt -Value 'hacked'`)
		data, _ := os.ReadFile(outFile)
		if strings.Contains(string(data), "hacked") {
			t.Logf("已知盲区（官方同款，接受）：硬链接写穿透（顺序1：先建链后授权）")
		} else {
			t.Logf("✓ 硬链接写被拦（顺序1）: %s", string(data))
		}

		outFile2 := filepath.Join(outside, "linked2.txt")
		_ = os.WriteFile(outFile2, []byte("orig"), 0644)
		link2 := filepath.Join(ws, "hardlink2.txt")
		if err := os.Link(outFile2, link2); err != nil {
			t.Skipf("创建硬链接失败（跳过）: %v", err)
		}
		try("hardlink2", `Add-Content -Path .\hardlink2.txt -Value 'hacked'`)
		data2, _ := os.ReadFile(outFile2)
		if strings.Contains(string(data2), "hacked") {
			t.Logf("已知盲区（官方同款，接受）：硬链接写穿透（顺序2：先授权后建链，懒传播）")
		} else {
			t.Logf("✓ 硬链接写被拦（顺序2）: %s", string(data2))
		}
	})
}

// TestSandboxJobTreeKill 验证 Job Object 整树回收（P0-2）：
// 超时终止时派生的孙进程必须一并死掉；kill-on-close 保证主进程崩溃时清场。
func TestSandboxJobTreeKill(t *testing.T) {
	skipIfNestedSandbox(t)
	ws := t.TempDir()
	marker := filepath.Join(ws, "grandchild_alive.txt")
	// 派生孙进程：Start-Process 起一个 detached powershell，睡 3 秒后写标记；
	// 父命令自己睡到超时。若 Job 整树回收失效，孙进程活到 3 秒后把标记写出来。
	cmd := `Start-Process -FilePath powershell -ArgumentList '-NoProfile','-Command','Start-Sleep 3; Set-Content -LiteralPath ''' + marker + ''' -Value alive' -WindowStyle Hidden; Start-Sleep 30`
	r := sandboxRun{Dir: ws, Command: cmd, Shell: "powershell",
		Timeout: 1 * time.Second, Policy: permWorkspace, Workspace: ws}
	out, err := spawnConfinedWindows(context.Background(), r)
	if err != nil {
		t.Fatalf("沙箱启动失败: %v", err)
	}
	if !strings.Contains(out, "已终止") {
		t.Fatalf("超时未触发（测试装置问题）: %s", out)
	}
	// 等孙进程的原定写入时刻过去，再复查标记：必须不存在。
	time.Sleep(5 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		data, _ := os.ReadFile(marker)
		t.Errorf("孙进程逃过整树回收并写出标记: %s", data)
	} else {
		t.Logf("✓ 超时后孙进程被 Job 整树回收")
	}
}

// TestSandboxEveryoneWriteKnownGap 钉住官方自报缺口1（Everyone 可写位置）
// 的当前行为：显式授 Everyone:(M) 的目录，沙箱内写入会被放行——Everyone
// 在 restricting 列表里，pass-2 双检对其放行（DSH 官方同款；密级关卡撤除
// 后重新打开，2026-09 决策接受，未来从工具层限制）。后续若重新收紧，
// 翻转此断言即可。
func TestSandboxEveryoneWriteKnownGap(t *testing.T) {
	skipIfNestedSandbox(t)
	ws := t.TempDir()
	everyoneDir := t.TempDir()
	ev, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := grantWriteAce(everyoneDir, ev); err != nil {
		t.Fatalf("授 Everyone ACE 失败: %v", err)
	}
	probe := filepath.Join(everyoneDir, "ev.txt")
	r := sandboxRun{Dir: ws, Command: `Set-Content -LiteralPath '` + probe + `' -Value x`,
		Shell: "powershell", Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws}
	out, err := spawnConfinedWindows(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Errorf("Everyone 可写位置写入被拒（当前语义应放行——若已收紧防护请翻转本断言）: %s", out)
	} else {
		t.Logf("已知盲区（官方同款，接受）：Everyone 可写位置被写入")
	}
}

// cmd shell 也要能跑（buildShellCmdline 的另一分支）；顺带钉住 NUL 重定向
// 兼容性（受限令牌写 NUL 设备）。
func TestSandboxCmdShell(t *testing.T) {
	skipIfNestedSandbox(t)
	ws := t.TempDir()
	r := sandboxRun{Dir: ws, Command: `cmd /c "echo x > NUL 2> NUL & echo cmd-ok"`, Shell: "cmd",
		Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws}
	out, err := spawnConfinedWindows(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cmd-ok") {
		t.Fatalf("cmd shell 输出异常（NUL 重定向失败？）: %s", out)
	}
	t.Logf("✓ cmd shell + NUL 重定向正常")
}

// 后台作业走沙箱（startConfinedAsync）。
func TestSandboxBackgroundJob(t *testing.T) {
	skipIfNestedSandbox(t)
	ws := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	r := sandboxRun{Dir: ws, Command: `Set-Content .\bg.txt -Value bg-ok; Start-Sleep 30`,
		Shell: "powershell", Policy: permWorkspace, Workspace: ws}
	var jobOut strings.Builder
	wait, err := startConfinedAsync(ctx, r, &jobOut)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(ws, "bg.txt")); err == nil {
			cancel() // job_kill 等价
			wait()   // 等受限子进程真正退出
			t.Logf("✓ 后台作业沙箱内写出文件，随后被取消终止")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	wait()
	t.Fatalf("后台作业 15 秒内未产出文件，输出: %s", jobOut.String())
}
