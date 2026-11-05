# 开发桌面版（并行实例）：编译到仓库根的 开发桌面版.exe，以专属互斥体 +
# 随机端口启动 WebView 桌面窗口——与安装版 / 坚祥数字助理GO.exe 互不干扰：
#   - 互斥体：JX_DESKTOP_MUTEX 覆盖为开发专属名（desktop_windows.go 内置
#     支持），不与正式实例互斥；kill 旧实例只按本脚本产物的完整路径匹配，
#     绝不误杀 D:\123\ 等处的安装版
#   - 端口：-port 传随机高位端口；窗口 URL 用实际监听端口，极端碰撞时
#     main.go 监听失败自动回退系统分配（:0），永远起得来
#   - 控制台子系统（不注入 -H=windowsgui）：日志/panic 直接落在开发版
#     自己的控制台窗口，启动期自检炸了也看得见原因（桌面 GUI 版闪退无感）
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File 打包\开发桌面版-并行.ps1
#       或直接双击根目录「编译并启动开发版.bat」（调用的就是本脚本）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$exe = Join-Path $root "开发桌面版.exe"

# 1) 只关本脚本产物的旧实例（按完整路径匹配：安装版与其他桌面 exe 不受影响）
$old = @(Get-CimInstance Win32_Process -Filter "Name='开发桌面版.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.ExecutablePath -eq $exe })
foreach ($p in $old) {
  Write-Host ("关闭旧开发实例 pid=" + $p.ProcessId)
  try { Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop } catch { }
}
if ($old.Count -gt 0) { Start-Sleep -Seconds 2 }

# 2) 先编译语音组件里的独立输入法（独立 Go 模块，主程序 build 不会带上它）
& (Join-Path $PSScriptRoot "编译语音输入法.ps1")
if ($LASTEXITCODE -ne 0) { throw "语音输入法编译失败" }

# 3) 编译主程序（失败即抛出，调用方按退出码提示；桌面形态由 -desktop 参数开启）
go build -trimpath -ldflags "-s -w -X main.appVersion=20991231-1" -o $exe $root  # appVersion 注入未来版本号：开发版永远比服务器新，不弹自动更新
if ($LASTEXITCODE -ne 0) { throw "go build 失败" }
Write-Host "完成：$exe（开发桌面版·并行实例，gitignore 的本地产物）"

# 4) 启动：专属互斥体 + 随机高位端口（子进程继承本进程环境变量）
$env:JX_DESKTOP_MUTEX = "JX-Desktop-Dev"
$port = Get-Random -Minimum 20000 -Maximum 60000
Write-Host ("启动：互斥体 JX-Desktop-Dev（不与正式实例互斥），端口 $port（碰撞时自动改系统分配）")
Start-Process -FilePath $exe -ArgumentList @("-desktop", "-port", "$port", "-noopen") -WorkingDirectory $root
Write-Host "已启动开发桌面版：日志在它自己的控制台窗口（关闭本窗口不影响它）。"
