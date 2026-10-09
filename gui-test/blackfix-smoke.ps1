# 黑屏修复冒烟：启动桌面版 → 正常截屏 → 最小化/还原截屏 → 置顶窗遮挡后关闭截屏。
# 三张都应是完整 UI（非纯黑），即修复无回归；本机不复现停帧时仅验证不回归。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File gui-test\blackfix-smoke.ps1 [-Exe 路径]
param(
  [string]$Exe = "D:\智能体工作区\软著版数字助理\坚祥数字助理GO.exe",
  [string]$OutDir = "D:\智能体工作区\软著版数字助理\gui-test-screenshots"
)
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName System.Windows.Forms
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class BW {
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string cls, string title);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L, T, R, B; }
}
"@

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$env:JX_DESKTOP_AUTOCLOSE = "120"
Start-Process -FilePath $Exe -WorkingDirectory (Split-Path -Parent $Exe)

$h = [IntPtr]::Zero
for ($i = 0; $i -lt 60 -and $h -eq [IntPtr]::Zero; $i++) {
  Start-Sleep -Milliseconds 500
  $p = Get-Process | Where-Object { $_.ProcessName -eq "坚祥数字助理GO" -and $_.MainWindowHandle -ne 0 } | Select-Object -First 1
  if ($p) { $h = [IntPtr]$p.MainWindowHandle }
}
if ($h -eq [IntPtr]::Zero) { throw "30 秒内未找到主窗口" }
Write-Host "窗口 hwnd=$h"

function Shot([string]$name) {
  Start-Sleep -Milliseconds 700
  $r = New-Object BW+RECT
  [BW]::GetWindowRect($h, [ref]$r) | Out-Null
  $w = $r.R - $r.L; $ht = $r.B - $r.T
  if ($w -le 0 -or $ht -le 0) { throw "窗口矩形异常 $w x $ht" }
  $bmp = New-Object System.Drawing.Bitmap($w, $ht)
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($r.L, $r.T, 0, 0, (New-Object System.Drawing.Size($w, $ht)))
  $g.Dispose()
  $p = Join-Path $OutDir $name
  $bmp.Save($p, [System.Drawing.Imaging.ImageFormat]::Png)
  $bmp.Dispose()
  Write-Host "已截 $p ($w x $ht)"
}

# 1) 正常态
[BW]::ShowWindow($h, 9) | Out-Null   # SW_RESTORE
[BW]::SetForegroundWindow($h) | Out-Null
Start-Sleep -Seconds 2
Shot "blackfix-1-normal.png"

# 2) 最小化 → 还原
[BW]::ShowWindow($h, 6) | Out-Null   # SW_MINIMIZE
Start-Sleep -Seconds 2
[BW]::ShowWindow($h, 9) | Out-Null   # SW_RESTORE
[BW]::SetForegroundWindow($h) | Out-Null
Shot "blackfix-2-restore.png"

# 3) 置顶窗整体遮挡 3 秒 → 关闭（遮挡解除，纯 Z 序变化路径）
$r2 = New-Object BW+RECT
[BW]::GetWindowRect($h, [ref]$r2) | Out-Null
$cover = New-Object System.Windows.Forms.Form
$cover.FormBorderStyle = "None"
$cover.StartPosition = "Manual"
$cover.Left = $r2.L - 10; $cover.Top = $r2.T - 10
$cover.Width = ($r2.R - $r2.L) + 20; $cover.Height = ($r2.B - $r2.T) + 20
$cover.BackColor = [System.Drawing.Color]::FromArgb(60, 60, 60)
$cover.TopMost = $true
$cover.Show()
$cover.Activate()
Start-Sleep -Seconds 3
$cover.Close()
Start-Sleep -Milliseconds 300
[BW]::SetForegroundWindow($h) | Out-Null
Shot "blackfix-3-uncover.png"

Write-Host "冒烟完成：三张截图已落盘，请人工核验画面完整（非纯黑）。"
