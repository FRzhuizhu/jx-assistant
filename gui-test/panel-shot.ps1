# Capture a screen region to PNG (no mouse/keyboard interaction).
# usage: panel-shot.ps1 <x> <y> <w> <h> <outfile>
param([int]$X, [int]$Y, [int]$W, [int]$H, [string]$OutFile)
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public class SS2 {
  [DllImport("user32.dll")] public static extern IntPtr GetDC(IntPtr h);
  [DllImport("user32.dll")] public static extern int ReleaseDC(IntPtr h, IntPtr dc);
  [DllImport("gdi32.dll")] public static extern bool BitBlt(IntPtr dst, int xd, int yd, int w, int h, IntPtr src, int xs, int ys, int rop);
}
"@
$bmp = New-Object System.Drawing.Bitmap($W, $H)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$dc = $g.GetHdc()
$sdc = [SS2]::GetDC([IntPtr]::Zero)
[void][SS2]::BitBlt($dc, 0, 0, $W, $H, $sdc, $X, $Y, 0x00CC0020)
[void][SS2]::ReleaseDC([IntPtr]::Zero, $sdc)
$g.ReleaseHdc($dc)
$g.Dispose()
$bmp.Save($OutFile, [System.Drawing.Imaging.ImageFormat]::Png)
$bmp.Dispose()
Write-Output "saved $OutFile"
