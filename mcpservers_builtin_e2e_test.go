//go:build windows

package main

// 内置连接器的端到端验证：释放 → 软性门控（清单过滤）→ 硬性门控依据
// → 真实握手（拉起 cua-driver 并列出工具）。
//
// 默认跳过：会起外部进程、要等冷启动。设 JX_E2E=1 才跑。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinCuaGatingE2E(t *testing.T) {
	if os.Getenv("JX_E2E") == "" {
		t.Skip("设 JX_E2E=1 才跑（会拉起 cua-driver 进程）")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := newTestMCPR(t)
	app := &App{Cfg: &Config{DataDir: t.TempDir(), SkillsDir: filepath.Join(wd, "技能")}, MCPServers: r}

	if err := app.materializeBuiltinMCP(); err != nil {
		t.Fatalf("释放内置连接器失败: %v", err)
	}
	if !r.HasName("cua") {
		t.Fatal("内置组件齐全时，cua 应被释放进注册表")
	}

	// 1) 软性门控：没授权「操作电脑」的会话，清单里根本不出现 cua
	if blk := r.PromptBlock(nil); strings.Contains(blk, "cua") {
		t.Fatalf("未授权时清单不该出现 cua，实得:\n%s", blk)
	}
	// 2) 授权后才出现
	blk := r.PromptBlock(&SessionMeta{ID: "e2e", Tools: []string{capComputer}})
	if !strings.Contains(blk, "cua") {
		t.Fatalf("授权「操作电脑」后清单应出现 cua，实得:\n%s", blk)
	}
	t.Logf("授权后的清单:\n%s", strings.TrimSpace(blk))

	// 3) 硬性门控的依据：这条带 RequiresCap
	if got := r.requiresCapOf("cua"); got != capComputer {
		t.Fatalf("cua 的 RequiresCap 应为 %q，实得 %q", capComputer, got)
	}

	// 4) 真实握手：连上 cua，确认真能列出工具
	tools, err := r.Test("cua")
	if err != nil {
		t.Fatalf("连接 cua 失败: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("cua 没有返回任何工具")
	}
	names := make([]string, 0, len(tools))
	has := map[string]bool{}
	for _, tl := range tools {
		names = append(names, tl.Name)
		has[tl.Name] = true
	}
	head := names
	if len(head) > 6 {
		head = head[:6]
	}
	t.Logf("cua 工具数=%d 前几个=%v", len(tools), head)

	// 关键工具名必须在（模型要靠它们干活）
	for _, want := range []string{"click", "type_text", "get_screen_size"} {
		if !has[want] {
			t.Fatalf("cua 缺少关键工具 %q，实得 %v", want, names)
		}
	}
	r.ShutdownAll()
}
