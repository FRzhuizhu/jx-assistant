# 一键把 .md 文件关联到 MD查看器.exe（WebView2 独立窗口渲染，无黑框）。
# 仅写 HKCU 注册表，不需要管理员权限；会备份原关联，可随时用「还原关联.ps1」恢复。
# 用法：右键本文件 -> 使用 PowerShell 运行；或在 PowerShell 里执行 .\install.ps1
#
# 说明：ProgId 沿用 MarkdownViewer.md（早期 Python 版登记的名称）。系统记住的
# 「默认打开方式」绑定的是 ProgId 字符串本身，所以就地更新它的打开命令即可
# 无缝切换到 MD查看器.exe，无需重设默认方式。显示名与图标同时更新。
$ErrorActionPreference = 'Stop'

$dir    = Split-Path -Parent $MyInvocation.MyCommand.Path
$exe    = Join-Path $dir 'MD查看器.exe'
$progId = 'MarkdownViewer.md'
$backup = Join-Path $dir '原关联备份.txt'

if (-not (Test-Path $exe)) { Write-Error "找不到 MD查看器.exe：$exe" }

# 1) 写 ProgId：显示名、图标、打开命令（exe 路径由本脚本所在目录推导）
$base = "HKCU:\Software\Classes\$progId"
New-Item -Path $base -Force | Out-Null
Set-ItemProperty -Path $base -Name '(default)' -Value 'MD查看器'
New-Item -Path "$base\DefaultIcon" -Force | Out-Null
Set-ItemProperty -Path "$base\DefaultIcon" -Name '(default)' -Value "`"$exe`",0"
New-Item -Path "$base\shell\open\command" -Force | Out-Null
Set-ItemProperty -Path "$base\shell\open\command" -Name '(default)' `
    -Value "`"$exe`" `"%1`""

# 2) 加入 .md 的「打开方式」候选
$openwith = 'HKCU:\Software\Classes\.md\OpenWithProgids'
New-Item -Path $openwith -Force | Out-Null
New-ItemProperty -Path $openwith -Name $progId -Value ([byte[]]@()) -PropertyType Binary -Force | Out-Null

# 3) 备份当前默认值，再改为我们的 ProgId
$mdkey = 'HKCU:\Software\Classes\.md'
New-Item -Path $mdkey -Force | Out-Null
$old = (Get-ItemProperty -Path $mdkey -Name '(default)' -ErrorAction SilentlyContinue).'(default)'
if ($old -and $old -ne $progId -and -not (Test-Path $backup)) {
    Set-Content -Path $backup -Value $old -Encoding UTF8
    Write-Host "已备份原关联: $old"
}
Set-ItemProperty -Path $mdkey -Name '(default)' -Value $progId

# 4) 若 UserChoice 被其他程序抢占过，移除它让上面的默认值生效
#    （UserChoice 已指向 MarkdownViewer.md 时不动它，保持无缝切换）
$uc = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\.md\UserChoice'
if (Test-Path $uc) {
    $ucPid = (Get-ItemProperty -Path $uc -Name 'ProgId' -ErrorAction SilentlyContinue).ProgId
    if ($ucPid -and $ucPid -ne $progId) {
        Remove-Item -Path $uc -Recurse -Force
        Write-Host "已清除被抢占的默认方式（原为 $ucPid）"
    }
}

Write-Host ""
Write-Host "完成！现在双击 .md 文件就会用「MD查看器」独立窗口渲染打开。"
Write-Host "如果图标未立即刷新，重启一次资源管理器或电脑即可；"
Write-Host "个别窗口未生效时，右键 .md -> 打开方式 -> 选择「MD查看器」。"
