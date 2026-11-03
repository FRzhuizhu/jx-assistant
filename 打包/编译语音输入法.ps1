# 编译语音组件里的独立语音输入法（组件\voice\voicetype）
#
# 为什么需要单独一步：输入法是个**独立 Go 模块**（有自己的 go.mod），
# 所以主程序的 `go build ./...` 不会带上它（Go 有意跳过嵌套模块）。
# 而它又必须跟主程序一起发版，于是各构建脚本在编主程序之前统一调本脚本。
#
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File 打包\编译语音输入法.ps1
#   -SkipIfRunning  正在运行就跳过（默认关：会先关掉旧实例再编，避免文件占用）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$vtDir = Join-Path $root "组件\voice\voicetype"
$vtExe = Join-Path $vtDir "voicetype_go.exe"

if (-not (Test-Path (Join-Path $vtDir "go.mod"))) {
  Write-Host "跳过：组件\voice\voicetype\go.mod 不存在"
  exit 0
}

# 它可能正被主程序当作子进程持有（exe 被占用时 go build 写不进去）→ 先关掉
$old = @(Get-CimInstance Win32_Process -Filter "Name='voicetype_go.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.ExecutablePath -eq $vtExe })
foreach ($p in $old) {
  Write-Host ("关闭旧输入法实例 pid=" + $p.ProcessId)
  try { Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop } catch { }
}
if ($old.Count -gt 0) { Start-Sleep -Milliseconds 800 }

Push-Location $vtDir
try {
  # -H windowsgui：不要控制台窗口（它是后台常驻的输入法，不是命令行工具）
  go build -ldflags "-H windowsgui -s -w" -o $vtExe .
  if ($LASTEXITCODE -ne 0) { throw "输入法 go build 失败（组件\voice\voicetype）" }
  $size = [math]::Round((Get-Item $vtExe).Length / 1MB, 1)
  Write-Host "完成：$vtExe（${size}MB，随组件一起编译）"
} finally {
  Pop-Location
}
