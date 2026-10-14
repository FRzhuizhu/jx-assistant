//go:build windows

package main

// 立即重启的接力助手：主进程挂在 kill-on-close Job 里（processguard_windows.go），
// 自己拉起自己会随 Job 陪葬。这里生成一个一次性的 PowerShell 脚本（文本文件，
// 不依赖主程序健康），以 CREATE_BREAKAWAY_FROM_JOB 逃出 Job 启动：等旧 PID
// 退出 → 按交换时记录的启动参数拉起新 exe → 脚本自身退出。不删 .old：留给
// 新版启动自清（万一新版起不来，.old 还在可手工救回）。

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// spawnRestartHelper 生成 restart.ps1 并启动接力。首选经 WMI 托管到 Job 外
// （WmiPrvSE 系统服务代为创建，与主进程的 kill-on-close Job 无任何从属关系，
// 主进程退出清场杀不到接力者）；WMI 不可用才回退 CREATE_BREAKAWAY_FROM_JOB
// 逃逸——该路径在部分环境（嵌套 Job/权限差异）会静默失败，回退实例又在
// Job 里随主进程陪葬，表现为用户点了「立即重启」只关不启（2026-10-14 案例）。
// 调用方 powershell 允许随 Job 陪葬：WMI 受理即成（本函数同步等它返回，
// 应答在受理确认之后才发、进程才退，无竞态）。
func spawnRestartHelper(a *App, st updateBlockState) error {
	exePath := currentExePath()
	if exePath == "" {
		return fmt.Errorf("取不到本程序路径")
	}
	argv := st.EngineArgv
	if len(argv) == 0 {
		argv = os.Args
	}
	updDir := filepath.Join(a.Cfg.DataDir, "updates")
	script := filepath.Join(updDir, "restart.ps1")
	logPath := filepath.Join(updDir, "restart.log")
	// UTF-8 BOM 必须带：Windows PowerShell 5.1 对无 BOM 的 .ps1 按 ANSI/GBK 解
	// 码，脚本里的中文路径会变乱码，Start-Process 静默失败（接力断链）
	if err := writeFileAtomic(script, []byte("\ufeff"+restartScript(os.Getpid(), exePath, argv[1:], logPath))); err != nil {
		return err
	}
	relayCmd := `powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File "` + script + `"`
	// 首选 WMI：Invoke-CimMethod 让 WmiPrvSE 创建接力进程（Job 外）。同步等
	// 待受理结果——失败时才走 breakaway，两条路都留 desktop.log 现场记录。
	caller := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = "+
			restartPSQuote(relayCmd)+" }; if ($r.ReturnValue -ne 0) { exit 1 }")
	caller.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := caller.Run(); err == nil {
		log.Printf("[更新] 重启接力已交 WMI 托管（旧进程退出后自动拉起新版，过程记于 %s）", logPath)
		return nil
	} else {
		log.Printf("[更新] WMI 托管失败（%v），回退 breakaway 接力", err)
	}
	return createBreakawayProcess(relayCmd)
}

// createBreakawayProcess 以 CREATE_BREAKAWAY_FROM_JOB 启动（Job 已配
// BREAKAWAY_OK）。进程不在 Job 里时该标志被系统忽略；在 Job 里但 Job 不许
// 逃逸会报错，去掉标志重试一次（此时助手会随主进程一起被清，重启接力失败，
// 用户仍可手动重启——磁盘文件已就位）。
func createBreakawayProcess(cmdLine string) error {
	argv16, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return err
	}
	var si windows.StartupInfo
	si.Flags = windows.STARTF_USESHOWWINDOW
	si.ShowWindow = windows.SW_HIDE
	flags := uint32(windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_NO_WINDOW)
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, argv16, nil, nil, false, flags, nil, nil, &si, &pi); err != nil {
		if err := windows.CreateProcess(nil, argv16, nil, nil, false, windows.CREATE_NO_WINDOW, nil, nil, &si, &pi); err != nil {
			return err
		}
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}

// restartPSQuote PowerShell 单引号字面量（内部单引号加倍）。
func restartPSQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// restartScript 生成一次性重启脚本；启动参数按 Windows 命令行习惯加引号。
func restartScript(oldPid int, exePath string, args []string, logPath string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"") {
			quoted[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		} else {
			quoted[i] = a
		}
	}
	argLine := strings.Join(quoted, " ")
	return fmt.Sprintf(`# 坚祥数字助理 更新重启助手（立即重启时由主程序生成，一次性脚本）
# 等旧进程退出（最多 60s）→ 启动新版（文件名未变、内容已换）→ 脚本退出。
# 不删 .old：留给新版启动自清；新版若起不来可手工把 .old 改回原名救回。
# 每步追加 restart.log：接力断链必须留下现场——Start-Process 失败在
# SilentlyContinue 下是静默的，没日志就没法查（本文件曾栽过：中文路径无 BOM
# 乱码导致启动静默失败，加了 BOM；这次把可观测性补全，再有断链一看便知）。
$ErrorActionPreference = 'SilentlyContinue'
$OldPid = %d
$Exe = %s
$ArgLine = %s
$Log = %s
function Log($m) { Add-Content -Path $Log -Value ("{0} {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $m) -Encoding UTF8 }
Log "助手启动：等待旧进程 $OldPid 退出（最长 60s）"
$p = Get-Process -Id $OldPid -ErrorAction SilentlyContinue
if ($p) { $p | Wait-Process -Timeout 60 }
Start-Sleep -Milliseconds 600
Log "旧进程已退出，启动新版：$Exe $ArgLine"
$ok = $false
try {
  # 空参数时不能带 -ArgumentList ''——那会把一个空字符串参数塞给新进程
  if ($ArgLine -ne '') {
    Start-Process -FilePath $Exe -ArgumentList $ArgLine -WorkingDirectory (Split-Path -Parent $Exe) -ErrorAction Stop
  } else {
    Start-Process -FilePath $Exe -WorkingDirectory (Split-Path -Parent $Exe) -ErrorAction Stop
  }
  $ok = $true
} catch { Log ("Start-Process 失败: " + $_.Exception.Message) }
if (-not $ok) {
  Log "改走 cmd start 兜底"
  cmd /c start "" $Exe
}
Start-Sleep -Milliseconds 1500
$running = @(Get-Process -Name (Split-Path -Leaf $Exe) -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $Exe })
Log ("收尾：新版进程在跑=" + ($running.Count -gt 0) + "，Start-Path 成功=" + $ok)
`, oldPid, restartPSQuote(exePath), restartPSQuote(argLine), restartPSQuote(logPath))
}
