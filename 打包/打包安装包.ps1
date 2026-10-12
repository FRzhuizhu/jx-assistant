# 坚祥数字助理GO 安装包构建脚本
#
# 用法（在仓库根目录）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File 打包\打包安装包.ps1
# 可选参数：
#   -Version     版本号（默认=今天日期+V1，如 20261008V1；同日重出多版手动传 V2 V3）
#   -ExeSource   使用已构建好的发行 exe（默认现场 go build；
#                需要排除工作区未完成改动时，先在干净 worktree 里构建再传入）
#   -SkillsSource 技能目录来源（默认仓库根的 技能\，含 _runtime 便携运行时）
#
# 前置条件：本机装有 Inno Setup 6（winget install JRSoftware.InnoSetup）。

param(
  [string]$Version = "",

  [string]$ExeSource = "",
  [string]$SkillsSource = ""
)
if (-not $Version) { $Version = (Get-Date -Format "yyyyMMdd") + "V1" }

$ErrorActionPreference = "Stop"
$packDir = $PSScriptRoot
$root = Split-Path -Parent $packDir
if ($SkillsSource -eq "") { $SkillsSource = Join-Path $root "技能" }
$stage = Join-Path $packDir "stage"

$pyExe = Join-Path $SkillsSource "_runtime\python\python.exe"
if (-not (Test-Path $pyExe)) {
  throw "技能便携运行时缺失：$pyExe（先在联网机器上跑一次任意技能触发装配，或手工 pip install）"
}

# 自包含原则：发行包必须内置全部依赖。逐个校验各技能 manifest 声明的
# 模块在便携运行时里真实可导入——装配是按需触发的，开发机上没跑过的
# 技能依赖可能没装，缺着出包会让最终用户在离线机器上踩坑，故缺一不出包。
$modules = @{}
# ★ 2026-10-12：递归扫描（含 user\ 子目录的用户技能，跳过 _runtime）——非递归会漏掉用户技能的依赖声明、校验假通过（实测曾漏 pywin32 全家；微信操作就在 user\ 下）
Get-ChildItem $SkillsSource -Recurse -Depth 2 -Directory | Where-Object {
  $_.FullName -notmatch '\\_runtime(\\|$)' -and
  Test-Path (Join-Path $_.FullName "manifest.json")
} | ForEach-Object {
  Test-Path (Join-Path $_.FullName "manifest.json")
} | ForEach-Object {
  # manifest 是 UTF-8：PS5.1 的 Get-Content 默认按 GBK 读会把中文读成
  # 乱码导致 JSON 解析失败、模块清单漏收（校验假通过），必须显式指定
  $m = Get-Content (Join-Path $_.FullName "manifest.json") -Raw -Encoding UTF8 | ConvertFrom-Json
  if ($m.python.modules) { $m.python.modules | ForEach-Object { $modules[$_] = $true } }
  if ($m.variants) {
    $m.variants.PSObject.Properties.Value | ForEach-Object {
      if ($_.modules) { $_.modules | ForEach-Object { $modules[$_] = $true } }
    }
  }
}
if ($modules.Count -eq 0) {
  throw "未从任何 manifest.json 收集到依赖模块——技能目录或解析有问题，拒绝出包"
}
$missing = @()
$ErrorActionPreference = "Continue"   # python 导入失败走 stderr，别被当成脚本错误中断
foreach ($mod in $modules.Keys) {
  & $pyExe -c "import $mod" *> $null
  if ($LASTEXITCODE -ne 0) { $missing += $mod }
}
$ErrorActionPreference = "Stop"
if ($missing.Count -gt 0) {
  throw "技能便携运行时依赖不完整，缺少: $($missing -join ', ')。请在联网机器上把对应技能各跑一遍（触发装配），或用该运行时的 python -m pip install 补装后再打包"
}
Write-Host "== 运行时依赖校验通过（$($modules.Count) 个模块全部可导入）"

if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null

$exe = Join-Path $stage "坚祥数字助理GO.exe"
if ($ExeSource -ne "") {
  Copy-Item $ExeSource $exe -Force
} else {
  Write-Host "== 编译主程序（注入桌面模式默认值）"
  go build -trimpath -ldflags "-s -w -H=windowsgui -X main.desktopDefault=1" -o $exe $root
  if ($LASTEXITCODE -ne 0) { throw "go build 失败" }
}

Write-Host "== 暂存技能目录（含便携 Python 运行时，体积较大）"
Copy-Item $SkillsSource (Join-Path $stage "技能") -Recurse -Force

# 内置组件（默认组件必须随包）：组件/ 目录整体暂存，缺失或没有任何
# 带 manifest.json 的组件都拒绝出包——安装包交付离线全功能，默认组件
# （危险命令护栏/待办定时/Git 面板/语音）是其中一部分，不能少。
Write-Host "== 暂存内置组件（默认组件必须随包）"
$compDir = Join-Path $root "组件"
if (-not (Test-Path $compDir)) { throw "内置组件目录缺失：$compDir（默认组件必须随包分发）" }
$compCount = (Get-ChildItem $compDir -Directory | Where-Object {
  Test-Path (Join-Path $_.FullName "manifest.json")
}).Count
if ($compCount -eq 0) { throw "组件目录里没有任何带 manifest.json 的组件，拒绝出包" }
Copy-Item $compDir (Join-Path $stage "组件") -Recurse -Force
Write-Host "== 内置组件 $compCount 个已暂存"

# 本地语音识别的 Python worker（asr/asr_worker.py，跑在 技能\_runtime 的便携运行时上）：
# 与 技能/、组件/ 同口径随包——缺它时语音组件的「本地语音识别模型」档一按听写就报
# 「未找到 asr/asr_worker.py」（听写默认走云端，本地档可选，但缺文件仍要出包前拦住）。
Write-Host "== 暂存本地语音识别 worker（asr/）"
$asrDir = Join-Path $root "asr"
if (-not (Test-Path (Join-Path $asrDir "asr_worker.py"))) {
  throw "asr\asr_worker.py 缺失：$asrDir（本地语音识别通道的 worker，必须随包）"
}
Copy-Item $asrDir (Join-Path $stage "asr") -Recurse -Force

# MD 查看器（装完即有）：单文件 exe 全自包含（WebView2 托管程序集与查看器
# 模板均已内嵌），随包只带 exe + 使用说明两个文件；.md 关联由安装器装完时
# 以 /register 静默写入（见 安装包.iss [Run]），缺 exe 拒绝出包。
Write-Host "== 暂存 MD 查看器（md-viewer/）"
$mdvDir = Join-Path $root "md-viewer"
if (-not (Test-Path (Join-Path $mdvDir "MD查看器.exe"))) {
  throw "md-viewer\MD查看器.exe 缺失：$mdvDir（装完即有的 MD 查看器必须随包）"
}
New-Item -ItemType Directory -Path (Join-Path $stage "md-viewer") | Out-Null
Copy-Item (Join-Path $mdvDir "MD查看器.exe") (Join-Path $stage "md-viewer") -Force
Copy-Item (Join-Path $mdvDir "使用说明.txt") (Join-Path $stage "md-viewer") -Force

Copy-Item (Join-Path $packDir "使用说明.md") $stage -Force

Write-Host "== Inno Setup 编译安装包（LZMA2 压缩，耐心等待）"
$iscc = Join-Path $env:LOCALAPPDATA "Programs\Inno Setup 6\ISCC.exe"
if (-not (Test-Path $iscc)) { $iscc = "C:\Program Files (x86)\Inno Setup 6\ISCC.exe" }
if (-not (Test-Path $iscc)) { throw "找不到 ISCC.exe，请先安装 Inno Setup 6" }
& $iscc "/DMyAppVersion=$Version" (Join-Path $packDir "安装包.iss")
if ($LASTEXITCODE -ne 0) { throw "ISCC 编译失败" }

$out = Join-Path $packDir "输出\坚祥数字助理GO-安装包-$Version.exe"
Write-Host "完成：$out"
