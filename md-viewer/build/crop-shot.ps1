$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public class WR {
    [DllImport("user32.dll")]
    public static extern bool GetWindowRect(IntPtr h, out RECT r);
    public struct RECT { public int L, T, R, B; }
}
'@
$p = Get-Process -Id 20756 -ErrorAction SilentlyContinue
if (-not $p) { Write-Output 'process exited'; exit }
$r = New-Object WR+RECT
[WR]::GetWindowRect($p.MainWindowHandle, [ref]$r) | Out-Null
Write-Output ("window rect: L={0} T={1} R={2} B={3}" -f $r.L, $r.T, $r.R, $r.B)
Add-Type -AssemblyName System.Drawing
$src = New-Object System.Drawing.Bitmap('D:\Desktop\童浩\智能体工作目录\md-viewer\build\screen1.png')
$w = $r.R - $r.L; $h = $r.B - $r.T
if ($w -le 0 -or $h -le 0) { Write-Output 'bad rect'; exit }
$crop = New-Object System.Drawing.Bitmap($w, $h)
$g = [System.Drawing.Graphics]::FromImage($crop)
$destRect = New-Object System.Drawing.Rectangle(0, 0, $w, $h)
$srcRect = New-Object System.Drawing.Rectangle($r.L, $r.T, $w, $h)
$g.DrawImage($src, $destRect, $srcRect, [System.Drawing.GraphicsUnit]::Pixel)
$g.Dispose()
$crop.Save('D:\Desktop\童浩\智能体工作目录\md-viewer\build\viewer-window.png', [System.Drawing.Imaging.ImageFormat]::Png)
$crop.Dispose(); $src.Dispose()
Write-Output 'cropped -> viewer-window.png'
