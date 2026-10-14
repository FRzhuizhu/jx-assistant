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
  [string]$ExeSource = ""
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
