package main

// jx-assistant.exe sandbox acl-audit [--revoke] [--yes] [paths...]
// 常驻授权审计/清理（P1-4）：报告（并可确认后清理）沙箱在目录树上残留的
// 合成能力 SID 授权与 Low 密级标签。缺省 paths 用 config.json 的 workspace。
// --revoke 先报告再提示确认（--yes 免确认）后执行清理。

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func runSandboxCLI(args []string) int {
	if len(args) == 0 || args[0] != "acl-audit" {
		fmt.Fprintln(os.Stderr, "用法: jx-assistant.exe sandbox acl-audit [--revoke] [--yes] [paths...]")
		return 2
	}
	revoke, yes := false, false
	var roots []string
	for _, a := range args[1:] {
		switch a {
		case "--revoke":
			revoke = true
		case "--yes":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "未知选项: %s\n", a)
				return 2
			}
			roots = append(roots, a)
		}
	}
	if len(roots) == 0 {
		cfg, err := LoadConfig("config.json")
		if err != nil || cfg.Workspace == "" {
			fmt.Fprintln(os.Stderr, "无法确定工作区（config.json），请显式给出路径")
			return 1
		}
		roots = []string{cfg.Workspace}
	}

	lines, err := auditSandboxGrants(roots, false, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "审计失败: %v\n", err)
		return 1
	}
	if len(lines) == 0 {
		fmt.Println("未发现残留授权（合成 SID ACE / Low 标签）。")
		return 0
	}
	// 摘要归并：常驻 ACE 的整树传播会产生海量条目，先按 (类型,SID) 归并
	// 计数，明细只列前 20 条（重定向 stdout 可得全量）。
	byKind := map[string]int{}
	for _, l := range lines {
		parts := strings.SplitN(l, "\t", 3)
		key := l
		if len(parts) == 3 {
			key = parts[0] + "\t" + parts[2]
		}
		byKind[key]++
	}
	fmt.Printf("发现 %d 项残留授权，按类别汇总：\n", len(lines))
	for k, n := range byKind {
		fmt.Printf("  %4d × %s\n", n, k)
	}
	fmt.Println("明细（前 20 条）：")
	for i, l := range lines {
		if i >= 20 {
			fmt.Printf("  …（其余 %d 条略，重定向输出可得全量）\n", len(lines)-20)
			break
		}
		fmt.Println("  " + l)
	}
	if !revoke {
		fmt.Println("（加 --revoke 清理；清理后沙箱下次执行会按需重新授权）")
		return 0
	}
	if !yes {
		fmt.Print("确认清理以上残留授权？[y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			fmt.Println("已取消。")
			return 0
		}
	}
	cleaned, err := auditSandboxGrants(roots, true, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "清理失败: %v\n", err)
		return 1
	}
	ok := 0
	for _, l := range cleaned {
		if !strings.HasPrefix(l, "ERR\t") {
			ok++
		} else {
			fmt.Println("  " + l)
		}
	}
	fmt.Printf("已清理 %d 项。\n", ok)
	return 0
}
