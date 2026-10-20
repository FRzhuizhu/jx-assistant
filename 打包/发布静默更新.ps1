# 坚祥数字助理GO 静默更新（分块）发布脚本
#
# 用法（在仓库根目录）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File 打包\发布静默更新.ps1
# 可选参数：
#   -Version   版本号（默认=今天日期+V1；同日重出多版手动传 V2 V3）
#   -组件      要发布的组件块，逗号分隔的组件目录名（如 voice,todo-scheduler；
#              默认不发组件——大多数版本只更新主程序）
#   -安装包    附带完整安装包的路径（可选；同时保留整包更新路）
#   -ExeSource 使用已构建好的发行 exe（默认现场 go build）
#   -Runtime   同时发共享运行时块（技能\_runtime 整目录包，压缩后约 100+ MB；
#              版本串默认取 python.exe -V，如 py3.14.8）
#   -Skills    同时发内置技能块（只压 pdf/ocr/office/downloader/apkinfo；
#              用户技能 技能\user 永不入包）
#   -RuntimeVersion 手工指定运行时版本串（缺省自动取）
#
# 产出：打包\静默更新\<版本>\ 目录——
#   engine-<版本>.zip       主程序块（zip 内单个 exe，客户端换名落位）
#   <组件>-<manifest版本>.zip 组件块（整目录包，版本读 manifest.json）
#   发布命令.txt            在服务器上执行的 publish-update 命令 + sha256 校验表
#
# 服务器端发布（jxai-server，在服务端机器上、数据目录旁执行）：
#   jxai-server publish-update -version <版本> -engine engine-<版本>.zip [-component voice=voice-x.zip …]
# 快照式清单：只传本版改动的块，没传的沿用上一版；客户端落后多版一步对齐每块最新。

param(
  [string]$Version = "",
  [string]$组件 = "",
  [string]$安装包 = "",
  [string]$ExeSource = "",
  # 技能区两块（客户端 update_runtime.go 消费）：
  #   -Runtime 发共享便携 Python 运行时（技能\_runtime 整目录包）
  #   -Skills  发内置技能包（只压白名单内置技能目录，用户技能永不入包）
  # 都不传 = 本版不动这两块（快照沿用上一版）。
  [switch]$Runtime,
  [switch]$Skills,
  [string]$RuntimeVersion = ""
)
if (-not $Version) { $Version = (Get-Date -Format "yyyyMMdd") + "V1" }

$ErrorActionPreference = "Stop"
$packDir = $PSScriptRoot
$root = Split-Path -Parent $packDir
$outDir = Join-Path $packDir "静默更新\$Version"
if (Test-Path $outDir) { Remove-Item $outDir -Recurse -Force }
New-Item -ItemType Directory -Path $outDir -Force | Out-Null

# ---- 主程序块：构建 exe（与打包安装包.ps1 同参数）→ 单 exe zip ----
$work = Join-Path $outDir "_build"
New-Item -ItemType Directory -Path $work | Out-Null
$exe = Join-Path $work "坚祥数字助理GO.exe"
if ($ExeSource -ne "") {
  Copy-Item $ExeSource $exe -Force
} else {
  Write-Host "== 编译主程序（注入桌面模式默认值与版本号——engine 块进程重启后要靠 appVersion 认出自己已是新版，漏注入会让待重启横幅永远不消）"
  go build -trimpath -ldflags "-s -w -H=windowsgui -X main.desktopDefault=1 -X main.appVersion=$Version" -o $exe $root
  if ($LASTEXITCODE -ne 0) { throw "go build 失败" }
}
$engineZip = Join-Path $outDir "engine-$Version.zip"
Compress-Archive -Path $exe -DestinationPath $engineZip -CompressionLevel Optimal
Remove-Item $work -Recurse -Force

# ---- 组件块：整目录打包，版本读 manifest.json ----
$compArgs = @()
if ($组件 -ne "") {
  foreach ($id in ($组件 -split "," | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" })) {
    $dir = Join-Path $root "组件\$id"
    $manifest = Join-Path $dir "manifest.json"
    if (-not (Test-Path $manifest)) { throw "组件 $id 没有 manifest.json：$manifest" }
    # manifest 是 UTF-8：PS5.1 默认按 GBK 读会乱码，必须显式指定
    $m = Get-Content $manifest -Raw -Encoding UTF8 | ConvertFrom-Json
    $ver = $m.version
    if (-not $ver) { throw "组件 $id 的 manifest.json 没有 version" }
    $zipName = "$id-$ver.zip"
    Write-Host "== 打包组件块 $id v$ver"
    Compress-Archive -Path (Join-Path $dir "*") -DestinationPath (Join-Path $outDir $zipName) -CompressionLevel Optimal
    $compArgs += "-component", "$id=$zipName"
  }
}

# ---- 技能区块 1：共享运行时（技能\_runtime 整目录包）----
$runtimeArgs = @()
if ($Runtime) {
  $rtDir = Join-Path $root "技能\_runtime"
  if (-not (Test-Path $rtDir)) { throw "运行时目录不存在：$rtDir" }
  $pyExe = Join-Path $rtDir "python\python.exe"
  if (-not (Test-Path $pyExe)) { throw "运行时里没有 python.exe：$pyExe" }
  # 版本串以解释器自报为准（pythonVersion 常量改了、运行时没换时这里会暴露）
  if (-not $RuntimeVersion) {
    $v = (& $pyExe -V 2>&1) -replace '^Python\s+', ''
    $v = $v.Trim()
    if ($v -notmatch '^\d+\.\d+') { throw "读不出运行时版本：$v" }
    $RuntimeVersion = "py" + $v
  }
  Write-Host "== 打包运行时块 $RuntimeVersion（$rtDir）"
  # 版本标记写进包内：客户端据此判断"已经是最新，不必重下几百兆"
  Set-Content -Path (Join-Path $rtDir ".version") -Value $RuntimeVersion -Encoding ASCII
  $runtimeZip = Join-Path $outDir "runtime-$RuntimeVersion.zip"
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  # 用 .NET 压缩（Compress-Archive 会漏掉 .version 这种点开头的文件）
  [IO.Compression.ZipFile]::CreateFromDirectory($rtDir, $runtimeZip,
    [IO.Compression.CompressionLevel]::Optimal, $false)
  $runtimeArgs += "-runtime", (Split-Path -Leaf $runtimeZip), "-runtime-version", $RuntimeVersion
}

# ---- 技能区块 2：内置技能（白名单目录；用户技能绝不入包）----
$skillsArgs = @()
if ($Skills) {
  # 与客户端内置技能白名单一致（skill_manager.go: builtinSkillIDs）
  $builtin = @("pdf", "ocr", "office", "downloader", "apkinfo")
  $stageSkills = Join-Path $outDir "_skills_stage"
  New-Item -ItemType Directory -Path $stageSkills -Force | Out-Null
  $packed = @()
  foreach ($name in $builtin) {
    $src = Join-Path $root "技能\$name"
    if (-not (Test-Path $src)) { throw "内置技能目录不存在：$src" }
    Copy-Item $src (Join-Path $stageSkills $name) -Recurse -Force
    $packed += $name
  }
  # 防呆：暂存区里绝不能出现 user / _runtime（白名单之外的目录一律不入包）
  Get-ChildItem $stageSkills -Directory | Where-Object { $builtin -notcontains $_.Name } | ForEach-Object {
    throw "暂存区出现非内置技能目录：$($_.Name)（拒绝入包）"
  }
  $skillsZip = Join-Path $outDir "skills-builtin.zip"
  Write-Host ("== 打包内置技能块（{0}）" -f ($packed -join "、"))
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  [IO.Compression.ZipFile]::CreateFromDirectory($stageSkills, $skillsZip,
    [IO.Compression.CompressionLevel]::Optimal, $false)
  Remove-Item $stageSkills -Recurse -Force
  # 文件名带上内容哈希前缀：内容变了就是新文件，客户端中途下载不会撞上"同名但内容已换"
  $skHash = (Get-FileHash $skillsZip -Algorithm SHA256).Hash.ToLower().Substring(0, 16)
  $skillsNamed = Join-Path $outDir "skills-builtin-$skHash.zip"
  Move-Item $skillsZip $skillsNamed -Force
  $skillsArgs += "-skills", (Split-Path -Leaf $skillsNamed)
}

# ---- 完整安装包（可选，整包路与静默更新并存） ----
$installerArg = @()
if ($安装包 -ne "") {
  if (-not (Test-Path $安装包)) { throw "安装包不存在：$安装包" }
  Copy-Item $安装包 $outDir -Force
  $installerArg += "-installer", (Split-Path -Leaf $安装包)
}

# ---- 发布命令 + 校验表 ----
$lines = @()
$lines += "# 在服务器上（jxai-server 数据目录旁、上传本目录全部文件后）执行："
$cmd = "jxai-server publish-update -version $Version -engine engine-$Version.zip"
if ($compArgs.Count -gt 0) { $cmd += " " + ($compArgs -join " ") }
if ($runtimeArgs.Count -gt 0) { $cmd += " " + ($runtimeArgs -join " ") }
if ($skillsArgs.Count -gt 0) { $cmd += " " + ($skillsArgs -join " ") }
if ($installerArg.Count -gt 0) { $cmd += " " + ($installerArg -join " ") }
$lines += $cmd
$lines += ""
$lines += "# 文件清单与 sha256（传输后核对，客户端下载也会校验）："
Get-ChildItem $outDir -File | ForEach-Object {
  $h = Get-FileHash $_.FullName -Algorithm SHA256
  $lines += ("{0}  {1,10:N1} MB  {2}" -f $_.Name, ($_.Length / 1MB), $h.Hash.ToLower())
}
$lines | Out-File (Join-Path $outDir "发布命令.txt") -Encoding UTF8

Write-Host ""
Write-Host "== 静默更新产物就绪：$outDir" -ForegroundColor Green
Get-ChildItem $outDir -File | ForEach-Object {
  Write-Host ("   {0}  {1,10:N1} MB" -f $_.Name, ($_.Length / 1MB))
}
Write-Host "== 把整个目录上传到服务器后，按 发布命令.txt 执行 publish-update"
