//go:build windows

package main

// 回归测试：受限档（workspace-write）下命令必须真的执行，且「启动即死」不得
// 被报成成功。
//
// 关键的一点是**在无控制台的进程里跑**：桌面形态的引擎是 GUI 进程、没有控制台，
// 而受限令牌下的子进程既建不出自己的控制台（CREATE_NO_WINDOW /
// CREATE_NEW_CONSOLE 都是一次「新建控制台」，一律 0xC0000142 秒退），也曾经
// 因为执行层不看退出码而被显示成「（命令执行完成，无输出）」。所以这里先用
// DETACHED_PROCESS 重新拉起自己，拿到一个真正没有控制台的父进程，再在其中
// 走完整执行路径——否则在有控制台的 go test 里这个 bug 根本复现不出来。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestConfinedCommandRunsWithoutConsole(t *testing.T) {
	logPath := os.Getenv("JX_SBX_TEST_LOG")
	if os.Getenv("JX_SBX_TEST_CHILDLESS") != "1" {
		logPath = t.TempDir() + `\sbx-result.txt`
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run", "TestConfinedCommandRunsWithoutConsole")
		cmd.Env = append(os.Environ(), "JX_SBX_TEST_CHILDLESS=1", "JX_SBX_TEST_LOG="+logPath)
		// DETACHED_PROCESS：不继承、也不新建控制台，等价于生产里的 GUI 引擎。
		cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if b, rerr := os.ReadFile(logPath); rerr == nil && strings.Contains(string(b), "FINISHED") {
				t.Logf("无控制台进程内的实测结果：\n%s", string(b))
				if strings.Contains(string(b), "FAIL") {
					t.Fatalf("受限档命令执行不正确（详见上方报告）")
				}
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		b, _ := os.ReadFile(logPath)
		t.Fatalf("无控制台进程未产出结果：%s", string(b))
	}

	// —— 以下在「没有控制台」的进程里执行 ——
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	report := func(format string, a ...any) {
		_, _ = f.WriteString(strings.TrimRight(fmt.Sprintf(format, a...), "\n") + "\n")
		_ = f.Sync()
	}
	defer report("FINISHED")

	ws := t.TempDir()
	run := func(command string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		return spawnConfinedWindows(ctx, sandboxRun{
			Dir: ws, Command: command, Shell: "powershell",
			Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws,
		})
	}

	// 1) 能真正执行并拿到输出（曾整段丢失：子进程秒退 + 不校验退出码）
	out, rerr := run(`Write-Output "hi-confined"; Set-Content -Path .\sbx-out.txt -Value ok`)
	if rerr != nil {
		report("FAIL 普通命令报错: %v", rerr)
		return
	}
	if !strings.Contains(out, "hi-confined") {
		report("FAIL 普通命令没有输出: %q", out)
		return
	}
	if b, berr := os.ReadFile(ws + `\sbx-out.txt`); berr != nil || !strings.Contains(string(b), "ok") {
		report("FAIL 沙箱内写文件没有生效: %v", berr)
		return
	}
	report("OK 普通命令输出与副作用正常")

	// 2) 非零退出码必须如实上报（不能再是「完成，无输出」）
	out, rerr = run(`exit 3`)
	if rerr != nil {
		report("FAIL exit 3 报错: %v", rerr)
		return
	}
	if !strings.Contains(out, "退出异常") || !strings.Contains(out, "exit code 3") {
		report("FAIL 非零退出码没有上报: %q", out)
		return
	}
	report("OK 非零退出码如实上报")

	// 3) 报告里提到的那批命令：回显、whoami、列目录、写文件、删自建文件、
	//    执行不存在的命令——都必须有真实反馈（不是「成功无输出」）
	outputs := map[string]string{}
	for _, c := range []struct{ name, command string }{
		{"echo", `echo hi-echo`},
		{"whoami", `whoami`},
		{"dir", `Get-ChildItem . | Measure-Object | Select-Object -ExpandProperty Count`},
		{"out-file", `"x" | Out-File -FilePath .\of.txt; Test-Path .\of.txt`},
		{"delete", `Remove-Item -Force .\of.txt; if (Test-Path .\of.txt) { "STILL-THERE" } else { "GONE" }`},
		{"nonexistent", `thiscmd_does_not_exist_xyz`},
	} {
		o, e := run(c.command)
		if e != nil {
			report("FAIL [%s] 报错: %v", c.name, e)
			return
		}
		if strings.TrimSpace(o) == "" || o == "（命令执行完成，无输出）" {
			report("FAIL [%s] 仍是空输出: %q", c.name, o)
			return
		}
		outputs[c.name] = o
		report("OK [%s] -> %q", c.name, strings.TrimSpace(o))
	}
	if !strings.Contains(outputs["delete"], "GONE") {
		report("FAIL 删除自建文件没有生效: 见上一条 delete 输出")
		return
	}

	// 4) cmd 路径同样可用（另一个 shell 分支）
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()
	out, rerr = spawnConfinedWindows(ctx2, sandboxRun{
		Dir: ws, Command: `echo hi-cmd & whoami`, Shell: "cmd",
		Timeout: 30 * time.Second, Policy: permWorkspace, Workspace: ws,
	})
	if rerr != nil || !strings.Contains(out, "hi-cmd") {
		report("FAIL cmd 路径: out=%q err=%v", out, rerr)
		return
	}
	report("OK cmd 路径可用")

	// 5) 安全语义没有削弱：工作区外写入必须被拒（受限令牌的限制照旧生效）
	escape := `C:\Windows\jx-sbx-escape-probe.txt`
	_, _ = os.Stat(escape)
	if err := os.Remove(escape); err == nil {
		report("FAIL 测试前置失败：%s 事先存在", escape)
		return
	}
	out, _ = run(`Set-Content -Path '` + escape + `' -Value nope -ErrorAction SilentlyContinue; Write-Output ok`)
	if _, serr := os.Stat(escape); serr == nil {
		_ = os.Remove(escape)
		report("FAIL 工作区外写入成功了——沙箱限制被削弱")
		return
	}
	report("OK 工作区外写入仍被拒（沙箱语义未削弱）")
}
