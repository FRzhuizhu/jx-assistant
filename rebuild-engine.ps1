# 重编译引擎并重启（网页/远程形态服务）：go build 覆盖 jx-assistant.exe 后以 -noopen 重启。
# 用途：改了 web/ 前端或 Go 源码后让运行中的服务用上新版——前端经 go:embed 编译期内嵌，
#       且远程外壳（p2pShell）按进程 sync.Once 缓存；只改磁盘文件不重启进程，
#       本机页面与远程端都看不到变化（remoteaccess.go 注释：改前端只需重启本服务）。
# 注意：本脚本会结束 jx-assistant.exe——正在进行的 AI 会话与远程端连接会断开，
#       属预期行为：对话历史存 SQLite，重开数字助理即可继续；远程端稍候自动重连，
#       页面打开时外壳内容哈希已变，remote-boot 会自动清缓存拉取新外壳。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File rebuild-engine.ps1
#       或直接双击根目录「重编译引擎并重启.bat」（调用的就是本脚本）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

# 1) 关闭正在运行的引擎实例（Windows 下 exe 被进程占用，go build 写不进去）
$procs = @(Get-CimInstance Win32_Process -Filter "Name='jx-assistant.exe'" -ErrorAction SilentlyContinue)
if ($procs.Count -gt 0) {
  foreach ($p in $procs) {
    Write-Host ("关闭旧引擎实例 pid=" + $p.ProcessId)
    try { Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop } catch { }
  }
  Start-Sleep -Seconds 2
}

# 2) 编译（失败即抛出并拒绝启动：防止「以为是新版、实际跑的还是旧 exe」）
go build -o (Join-Path $root "jx-assistant.exe") $root
if ($LASTEXITCODE -ne 0) { throw "go build 失败（旧实例已关闭且未重启，修复后重跑本脚本）" }
Write-Host ("完成：" + (Join-Path $root "jx-assistant.exe"))

# 3) 按原参数形态重启（-noopen：不自动开本机浏览器，远程端/本机自己连）
Start-Process -FilePath (Join-Path $root "jx-assistant.exe") -ArgumentList "-noopen" -WorkingDirectory $root
Write-Host "已重启引擎。远程端稍等自动重连；下次打开页面自动拉取新外壳（无需手动清缓存）。"
