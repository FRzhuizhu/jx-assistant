# 还原 .md 文件关联到安装前的状态，并移除 MD查看器注册的条目。
# 用法：右键本文件 -> 使用 PowerShell 运行
$ErrorActionPreference = 'SilentlyContinue'

$dir    = Split-Path -Parent $MyInvocation.MyCommand.Path
$backup = Join-Path $dir '原关联备份.txt'
$progId = 'MarkdownViewer.md'
$mdkey  = 'HKCU:\Software\Classes\.md'
$uc     = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\.md\UserChoice'

# 1) 若「记住的默认方式」指向本工具，移除它，让系统回到 .md 的默认值
$ucPid = $null
if (Test-Path $uc) { $ucPid = (Get-ItemProperty -Path $uc -Name 'ProgId' -ErrorAction SilentlyContinue).ProgId }
if ($ucPid -and $ucPid -eq $progId) {
    Remove-Item -Path $uc -Recurse -Force
    Write-Host "已清除指向 MD查看器 的默认方式记录"
}

# 2) 还原 .md 默认值到备份（无备份时回到系统自动类型）
$old = if (Test-Path $backup) { (Get-Content $backup -Raw).Trim() } else { 'md_auto_file' }
if ($old) { Set-ItemProperty -Path $mdkey -Name '(default)' -Value $old }

# 3) 移除本工具的 ProgId 与候选登记
Remove-ItemProperty -Path "$mdkey\OpenWithProgids" -Name $progId -ErrorAction SilentlyContinue
Remove-Item -Path "HKCU:\Software\Classes\$progId" -Recurse -Force -ErrorAction SilentlyContinue

Write-Host "已还原 .md 关联到: $old"
Write-Host "如需彻底卸载 MD查看器，现在可以删除整个 md-viewer 文件夹。"
