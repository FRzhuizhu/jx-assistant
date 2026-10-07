# 在屏幕坐标点击一次（带前后停顿）。用法: click.ps1 <x> <y>
param([int]$X, [int]$Y)
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class M {
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
  public const uint LEFTDOWN = 0x02, LEFTUP = 0x04;
}
"@
[void][M]::SetCursorPos($X, $Y)
Start-Sleep -Milliseconds 150
[M]::mouse_event([M]::LEFTDOWN, 0, 0, 0, [UIntPtr]::Zero)
Start-Sleep -Milliseconds 60
[M]::mouse_event([M]::LEFTUP, 0, 0, 0, [UIntPtr]::Zero)
Start-Sleep -Milliseconds 400
Write-Output "clicked $X,$Y"
