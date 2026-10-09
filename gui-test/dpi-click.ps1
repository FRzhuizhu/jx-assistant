# DPI-aware 版 fg-click：先声明 Per-Monitor DPI 感知，拿真实物理坐标再点击。
# 用法: dpi-click.ps1 <pid> <物理x> <物理y>
param([int]$ProcId, [int]$X, [int]$Y)
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class DPIFG {
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr value);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint dx, uint dy, uint d, UIntPtr e);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  public struct RECT { public int Left, Top, Right, Bottom; }
}
"@
# DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = ((DPI_CONTEXT_HANDLE)-4)
[void][DPIFG]::SetProcessDpiAwarenessContext([IntPtr]::new(-4))
$p = Get-Process -Id $ProcId
$h = $p.MainWindowHandle
if ($h -eq [IntPtr]::Zero) { Write-Output "no window"; exit 1 }
$r = New-Object DPIFG+RECT
[void][DPIFG]::GetWindowRect($h, [ref]$r)
Write-Output ("真实窗口矩形: {0},{1} - {2},{3} ({4}x{5})" -f $r.Left, $r.Top, $r.Right, $r.Bottom, ($r.Right-$r.Left), ($r.Bottom-$r.Top))
[void][DPIFG]::SetForegroundWindow($h)
Start-Sleep -Milliseconds 700
[DPIFG]::SetCursorPos($X, $Y) | Out-Null
Start-Sleep -Milliseconds 250
[DPIFG]::mouse_event(2,0,0,0,[UIntPtr]::Zero)
Start-Sleep -Milliseconds 70
[DPIFG]::mouse_event(4,0,0,0,[UIntPtr]::Zero)
Start-Sleep -Milliseconds 800
Write-Output "clicked $X,$Y"
