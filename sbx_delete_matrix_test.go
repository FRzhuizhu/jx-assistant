package main

// P0-0 判定实验（固化为常驻测试）：删除路线 × 父目录 ACL 矩阵。
//
// 判定实验结论（2026-09，两套沙箱真机对照）：**分支1——路线级盲区**。
// 密级关卡（Low IL + mandatory label）落地前，两套沙箱（本产品 Go 版与
// DSH 官方 sandbox-windows-acl 的 TypeScript 版，后者经 dsh runner 同矩阵
// 25 格实测）在「父目录给用户清柜权 FILE_DELETE_CHILD(0x40)」的位置上
// （%TEMP%/Documents/Desktop）均被非 -Force Remove-Item 与 cmd del 删穿，
// 而「父目录无 0x40」的位置（D:\智能体工作区）四路全拒——删除走父目录
// 清柜权路线时不触发 WRITE_RESTRICTED 第二道检查，官方同款。密级关卡
// 落地后本矩阵 16 格全拒（NO_WRITE_UP 在访问检查末端不分路线统一拦）。
//
//	位置：temp    —— %TEMP% 新建目录树（用户 FullControl，含清柜权 0x40）
//	      docs    —— 用户 Documents（同类 ACL）
//	      desktop —— 用户 Desktop（同类 ACL）
//	      dshroot —— JX_SBX_NO40_DIR（缺省 D:\智能体工作区）：用户 Modify 不含 0x40 的对照组
//	路线：Remove-Item 无 -Force / Remove-Item -Force / [IO.File]::Delete() / cmd /c del
//
// 断言：删除矩阵 16 格必须全拒；创建/修改对照组必须全拒；
// 工作区内创建/改名/删除必须成功（回归钉子）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxDeleteMatrix(t *testing.T) {
	ws := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法定位用户目录: %v", err)
	}
	no40 := os.Getenv("JX_SBX_NO40_DIR")
	if no40 == "" {
		no40 = `D:\智能体工作区`
	}

	type loc struct{ name, dir string }
	var locs []loc
	for _, l := range []loc{
		{"temp", t.TempDir()},
		{"docs", filepath.Join(home, "Documents")},
		{"desktop", filepath.Join(home, "Desktop")},
		{"dshroot", no40},
	} {
		if fi, err := os.Stat(l.dir); err != nil || !fi.IsDir() {
			t.Logf("位置 %s（%s）不可用，跳过", l.name, l.dir)
			continue
		}
		locs = append(locs, l)
	}
	if len(locs) < 2 {
		t.Fatalf("有效受害者位置过少: %v", locs)
	}

	runID := fmt.Sprintf("%d_%d", os.Getpid(), time.Now().Unix())
	var cleanup []string
	defer func() {
		for _, p := range cleanup {
			_ = os.Remove(p)
		}
	}()
	mkVictim := func(dir, tag, content string) string {
		p := filepath.Join(dir, fmt.Sprintf("jx_probe_%s_%s.txt", runID, tag))
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("创建探针文件 %s 失败: %v", p, err)
		}
		cleanup = append(cleanup, p)
		return p
	}
	mkTarget := func(dir, tag string) string {
		p := filepath.Join(dir, fmt.Sprintf("jx_probe_%s_%s.txt", runID, tag))
		cleanup = append(cleanup, p)
		return p
	}

	run := func(command string) string {
		r := sandboxRun{Dir: ws, Command: command, Shell: "powershell",
			Timeout: 120 * time.Second, Policy: permWorkspace, Workspace: ws}
		out, err := spawnConfinedWindows(context.Background(), r)
		if err != nil {
			t.Fatalf("沙箱启动失败: %v", err)
		}
		return out
	}

	routes := []struct {
		name string
		cmd  func(path string) string
	}{
		{"Remove-Item", func(p string) string {
			return fmt.Sprintf("Remove-Item -LiteralPath '%s' -ErrorAction SilentlyContinue", p)
		}},
		{"Remove-Item -Force", func(p string) string {
			return fmt.Sprintf("Remove-Item -LiteralPath '%s' -Force -ErrorAction SilentlyContinue", p)
		}},
		{"[IO.File]::Delete", func(p string) string {
			return fmt.Sprintf("try { [System.IO.File]::Delete('%s') } catch {}", p)
		}},
		{"cmd /c del", func(p string) string {
			return fmt.Sprintf(`cmd /c del "%s" 2>$null`, p)
		}},
	}

	// ---------- 删除矩阵：每条路线一次沙箱调用，四个位置各一个受害者 ----------
	type cell struct{ route, loc string; escaped bool }
	var results []cell
	for ri, rt := range routes {
		type job struct{ locName, path string }
		var jobs []job
		var cmds []string
		for _, l := range locs {
			p := mkVictim(l.dir, fmt.Sprintf("del%d_%s", ri, l.name), "keep")
			jobs = append(jobs, job{l.name, p})
			cmds = append(cmds, rt.cmd(p))
		}
		out := run(strings.Join(cmds, "; "))
		for _, j := range jobs {
			gone := false
			if _, err := os.Stat(j.path); err != nil {
				gone = true // 文件消失 = 删除成功 = 逃逸（受害者在工作区外）
			}
			results = append(results, cell{rt.name, j.locName, gone})
			if gone {
				t.Logf("路线 %s @ %s：⚠ 删除逃逸（文件已被沙箱内命令删除）", rt.name, j.locName)
			}
		}
		_ = out
	}

	// ---------- 创建对照组（已证实事实，硬断言）：沙箱外创建必须被拒 ----------
	var creCmds, modCmds []string
	creTargets := map[string]string{}
	modVictims := map[string]string{}
	for _, l := range locs {
		cp := mkTarget(l.dir, "cre_"+l.name)
		creTargets[l.name] = cp
		creCmds = append(creCmds, fmt.Sprintf("Set-Content -LiteralPath '%s' -Value x -ErrorAction SilentlyContinue", cp))
		mp := mkVictim(l.dir, "mod_"+l.name, "keep")
		modVictims[l.name] = mp
		modCmds = append(modCmds, fmt.Sprintf("Add-Content -LiteralPath '%s' -Value hacked -ErrorAction SilentlyContinue", mp))
	}
	run(strings.Join(creCmds, "; "))
	for _, l := range locs {
		if _, err := os.Stat(creTargets[l.name]); err == nil {
			t.Errorf("创建对照组：%s 逃逸成功（不应发生）", l.name)
		}
	}
	run(strings.Join(modCmds, "; "))
	for _, l := range locs {
		data, err := os.ReadFile(modVictims[l.name])
		if err == nil && strings.Contains(string(data), "hacked") {
			t.Errorf("修改对照组：%s 逃逸成功（不应发生）", l.name)
		}
	}

	// ---------- 工作区内对照（回归钉子）：创建/改名/删除必须成功 ----------
	run(`Set-Content -LiteralPath '.\matrix_a.txt' -Value x; Rename-Item -LiteralPath '.\matrix_a.txt' -NewName 'matrix_b.txt'`)
	if _, err := os.Stat(filepath.Join(ws, "matrix_b.txt")); err != nil {
		t.Errorf("工作区内创建/改名失败（回归钉子被拔）: %v", err)
	}
	wsVictim := mkVictim(ws, "delws", "keep")
	run(fmt.Sprintf("Remove-Item -LiteralPath '%s' -ErrorAction SilentlyContinue", wsVictim))
	if _, err := os.Stat(wsVictim); err == nil {
		t.Errorf("工作区内删除失败（回归钉子被拔）")
	}

	// ---------- 事实表 ----------
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n删除矩阵（DELETED=逃逸成功，BLOCKED=被拒）位置：%d 个\n%-20s", len(locs), "路线\\位置")
	for _, l := range locs {
		fmt.Fprintf(&sb, "%-10s", l.name)
	}
	sb.WriteString("\n")
	for _, rt := range routes {
		fmt.Fprintf(&sb, "%-20s", rt.name)
		for _, l := range locs {
			mark := "BLOCKED"
			for _, c := range results {
				if c.route == rt.name && c.loc == l.name && c.escaped {
					mark = "DELETED"
				}
			}
			fmt.Fprintf(&sb, "%-10s", mark)
		}
		sb.WriteString("\n")
	}
	t.Log(sb.String())

	escapes := 0
	for _, c := range results {
		if c.escaped {
			escapes++
			t.Errorf("删除逃逸 %s @ %s（密级关卡应拦住一切路线）", c.route, c.loc)
		}
	}
	if escapes == 0 {
		t.Logf("✓ 删除矩阵全拒（%d 格）", len(results))
	}
}
