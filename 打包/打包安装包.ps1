# 坚祥数字助理GO 安装包构建脚本
#
# 用法（在仓库根目录）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File 打包\打包安装包.ps1
# 可选参数：
#   -Version     版本号（默认 1.0.0，同时写入安装包文件名与元数据）
#   -ExeSource   使用已构建好的发行 exe（默认现场 go build；
#                需要排除工作区未完成改动时，先在干净 worktree 里构建再传入）
#   -SkillsSource 技能目录来源（默认仓库根的 技能\，含 _runtime 便携运行时）
#
# 前置条件：本机装有 Inno Setup 6（winget install JRSoftware.InnoSetup）。

param(
  [string]$Version = "1.0.0",
  [string]$ExeSource = "",
  [string]$SkillsSource = ""
)
$ErrorActionPreference = "Stop"
$packDir = $PSScriptRoot
$root = Split-Path -Parent $packDir
if ($SkillsSource -eq "") { $SkillsSource = Join-Path $root "技能" }
$stage = Join-Path $packDir "stage"

if (-not (Test-Path (Join-Path $SkillsSource "_runtime\python\python.exe"))) {
  throw "技能便携运行时缺失：$SkillsSource\_runtime\python\python.exe（先在联网机器上跑一次任意技能触发装配，或手工 pip install）"
}

if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null

$exe = Join-Path $stage "坚祥数字助理GO.exe"
if ($ExeSource -ne "") {
  Copy-Item $ExeSource $exe -Force
} else {
  Write-Host "== 编译主程序（注入桌面模式默认值）"
  go build -trimpath -ldflags "-s -w -X main.desktopDefault=1" -o $exe $root
  if ($LASTEXITCODE -ne 0) { throw "go build 失败" }
}

Write-Host "== 暂存技能目录（含便携 Python 运行时，体积较大）"
Copy-Item $SkillsSource (Join-Path $stage "技能") -Recurse -Force
Copy-Item (Join-Path $packDir "使用说明.md") $stage -Force

Write-Host "== Inno Setup 编译安装包（LZMA2 压缩，耐心等待）"
$iscc = Join-Path $env:LOCALAPPDATA "Programs\Inno Setup 6\ISCC.exe"
if (-not (Test-Path $iscc)) { $iscc = "C:\Program Files (x86)\Inno Setup 6\ISCC.exe" }
if (-not (Test-Path $iscc)) { throw "找不到 ISCC.exe，请先安装 Inno Setup 6" }
& $iscc "/DMyAppVersion=$Version" (Join-Path $packDir "安装包.iss")
if ($LASTEXITCODE -ne 0) { throw "ISCC 编译失败" }

$out = Join-Path $packDir "输出\坚祥数字助理GO-安装包-$Version.exe"
Write-Host "完成：$out"
