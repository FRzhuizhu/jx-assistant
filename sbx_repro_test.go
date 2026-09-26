package main

// 真实工作区初始化回归（曾全线失败的根因固化）：
//   - 场景1：真实 Modify 档 ACL 的工作目录（Authenticated Users:(M)，不含
//     WRITE_OWNER）上，授权+密级标签+沙箱执行必须全通。历史缺陷：写强制
//     标签要求 WRITE_OWNER，Modify 档与所有者隐式权限都不含——%TEMP% 的
//     FullControl 档测试目录测不出（setLabelAce 的补授权重试修复）。
//   - 场景2：悬空工作区路径（目录改名/移动残留）必须给出中文明确指引，
//     而非裸错误码。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSandboxRealWorkspaceInit(t *testing.T) {
	skipIfNestedSandbox(t)
	// 场景1：真实 Modify 档目录（存在才测，避免依赖特定机器）
	const realWS = `D:\智能体工作区\工作目录`
	if fi, err := os.Stat(realWS); err != nil || !fi.IsDir() {
		t.Skipf("%s 不可用，跳过真实工作区场景（err=%v）", realWS, err)
	}
	out, err := spawnConfinedWindows(context.Background(), sandboxRun{
		Dir: realWS, Command: `echo real-ok`, Shell: "cmd",
		Timeout: 120 * time.Second, Policy: permWorkspace, Workspace: realWS,
	})
	if err != nil {
		t.Fatalf("真实工作区初始化失败（WRITE_OWNER 补授权未生效？）: %v", err)
	}
	if !strings.Contains(out, "real-ok") {
		t.Fatalf("真实工作区命令输出异常: %q", out)
	}
	// 二次执行验证幂等（标签已存在，走快速路径）
	out2, err := spawnConfinedWindows(context.Background(), sandboxRun{
		Dir: realWS, Command: `echo real-ok-2`, Shell: "cmd",
		Timeout: 120 * time.Second, Policy: permWorkspace, Workspace: realWS,
	})
	if err != nil || !strings.Contains(out2, "real-ok-2") {
		t.Fatalf("真实工作区二次执行异常: err=%v out=%q", err, out2)
	}
	t.Logf("✓ 真实 Modify 档工作区：初始化+执行+幂等全通")

	// 场景2：悬空工作区路径 → 中文明确指引
	const deadWS = `D:\智能体工作区\不存在的目录_jxregress`
	_, err = spawnConfinedLocal(context.Background(), sandboxRun{
		Dir: deadWS, Command: `echo x`, Shell: "cmd",
		Timeout: 10 * time.Second, Policy: permWorkspace, Workspace: deadWS,
	})
	if err == nil || !strings.Contains(err.Error(), "工作区目录不存在") {
		t.Fatalf("悬空工作区路径应报明确指引，得到: %v", err)
	}
	t.Logf("✓ 悬空工作区路径给出明确指引: %v", err)
}
