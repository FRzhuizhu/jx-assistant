# 开发用桌面版：构建到仓库根的 坚祥数字助理GO.exe
# 双击即桌面窗口（构建期注入 desktopDefault=1）；数据/config 照旧落仓库
# 目录，与 jx-assistant.exe（浏览器形态）互不干扰。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File 打包\开发桌面版.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
go build -trimpath -ldflags "-s -w -X main.desktopDefault=1" -o (Join-Path $root "坚祥数字助理GO.exe") $root
if ($LASTEXITCODE -ne 0) { throw "go build 失败" }
Write-Host "完成：$root\坚祥数字助理GO.exe（开发桌面版，gitignore 的本地产物）"
