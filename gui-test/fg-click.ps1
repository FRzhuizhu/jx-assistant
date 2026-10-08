# 置前目标窗口 -> 点击 -> （可选）等待后连点更多坐标。
# 用法: fg-click.ps1 <pid> <x> <y> [x2 y2] [x3 y3] ...
param(
  [int]$ProcId,
  [Parameter(ValueFromRemainingArguments = $true)][int[]]$Coords
)
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class FGC {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint dx, uint dy, uint d, UIntPtr e);
}
"@
$p = Get-Process -Id $ProcId
$h = $p.MainWindowHandle
if ($h -eq [IntPtr]::Zero) { Write-Output "no window"; exit 1 }
[FGC]::SetForegroundWindow($h) | Out-Null
Start-Sleep -Milliseconds 700
for ($i = 0; $i -lt $Coords.Count; $i += 2) {
  $x = $Coords[$i]; $y = $Coords[$i + 1]
  [FGC]::SetCursorPos($x, $y) | Out-Null
  Start-Sleep -Milliseconds 250
  [FGC]::mouse_event(2, 0, 0, 0, [UIntPtr]::Zero)
  Start-Sleep -Milliseconds 70
  [FGC]::mouse_event(4, 0, 0, 0, [UIntPtr]::Zero)
  Write-Output "clicked ($x,$y)"
  Start-Sleep -Milliseconds 1200
}
