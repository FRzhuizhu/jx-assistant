# 出服务端包：交叉编译 jxai-server 的 Linux 静态二进制 + 部署脚本，产 zip 到 打包\输出服务端\
# 用法：powershell -File 打包\出服务端包.ps1 [-Version 0.1.0]
param([string]$Version = "0.1.0")
$ErrorActionPreference = "Stop"

# 仓库根 = 本脚本的上一级
$root = Split-Path -Parent $PSScriptRoot
$outDir = Join-Path $PSScriptRoot "输出服务端"
$zipPath = Join-Path $outDir "jxai-server-linux-$Version.zip"
$stage = Join-Path $outDir "stage"
New-Item -ItemType Directory -Force -Path $stage | Out-Null

Write-Host "==> go vet ./jxaiserver/"
Push-Location $root
go vet ./jxaiserver/
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "go vet 失败" }

Write-Host "==> 交叉编译 GOOS=linux GOARCH=amd64（CGO 关闭，纯静态）"
$env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o (Join-Path $stage "jxai-server") ./jxaiserver
$buildErr = $LASTEXITCODE
$env:GOOS = ""; $env:GOARCH = ""; $env:CGO_ENABLED = ""
if ($buildErr -ne 0) { Pop-Location; throw "go build 失败" }
Pop-Location

Copy-Item (Join-Path $root "jxaiserver\部署\*") $stage -Recurse -Force
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zipPath -Force
Remove-Item -Recurse -Force $stage

Write-Host "已产出 $zipPath"
Write-Host "部署：scp 该 zip 到 jxmes-git:/opt/ 后按 jxaiserver\部署\README.md 安装"
