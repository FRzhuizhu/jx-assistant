//go:build windows

package main

// 立即重启的接力助手：主进程挂在 kill-on-close Job 里（processguard_windows.go），
// 自己拉起自己会随 Job 陪葬。这里生成一个一次性的 PowerShell 脚本（文本文件，
// 不依赖主程序健康），以 CREATE_BREAKAWAY_FROM_JOB 逃出 Job 启动：等旧 PID
// 退出 → 按交换时记录的启动参数拉起新 exe → 脚本自身退出。不删 .old：留给
// 新版启动自清（万一新版起不来，.old 还在可手工救回）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// spawnRestartHelper 生成 restart.ps1 并以逃逸 Job 的方式启动它。
func spawnRestartHelper(a *App, st updateBlockState) error {
	exePath := currentExePath()
	if exePath == "" {
		return fmt.Errorf("取不到本程序路径")
	}
	argv := st.EngineArgv
	if len(argv) == 0 {
		argv = os.Args
	}
	script := filepath.Join(a.Cfg.DataDir, "updates", "restart.ps1")
	// UTF-8 BOM 必须带：Windows PowerShell 5.1 对无 BOM 的 .ps1 按 ANSI/GBK 解
	// 码，脚本里的中文路径会变乱码，Start-Process 静默失败（接力断链）
	if err := writeFileAtomic(script, []byte("\ufeff"+restartScript(os.Getpid(), exePath, argv[1:]))); err != nil {
		return err
	}
	cmdLine := `powershell.exe -NoProfile -ExecutionPolicy Bypass -File "` + script + `"`
	return createBreakawayProcess(cmdLine)
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
func restartScript(oldPid int, exePath string, args []string) string {
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
$ErrorActionPreference = 'SilentlyContinue'
$OldPid = %d
$Exe = %s
$ArgLine = %s
$p = Get-Process -Id $OldPid -ErrorAction SilentlyContinue
if ($p) { $p | Wait-Process -Timeout 60 }
Start-Sleep -Milliseconds 600
Start-Process -FilePath $Exe -ArgumentList $ArgLine -WorkingDirectory (Split-Path -Parent $Exe)
`, oldPid, restartPSQuote(exePath), restartPSQuote(argLine))
}
