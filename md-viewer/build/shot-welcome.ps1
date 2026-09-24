$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public class WR2 {
    [DllImport("user32.dll")]
    public static extern bool GetWindowRect(IntPtr h, out RECT2 r);
    public struct RECT2 { public int L, T, R, B; }
}
'@
Add-Type -AssemblyName System.Windows.Forms,System.Drawing

$exe = 'D:\Desktop\童浩\智能体工作目录\坚祥数字助理\md-viewer\MD查看器.exe'
Start-Process $exe
Start-Sleep -Seconds 5
$p = Get-Process | Where-Object { $_.ProcessName -eq 'MD查看器' } | Select-Object -First 1
if (-not $p) { Write-Output 'no process'; exit }
$r = New-Object WR2+RECT2
[WR2]::GetWindowRect($p.MainWindowHandle, [ref]$r) | Out-Null
Write-Output ("rect: L={0} T={1} R={2} B={3}" -f $r.L, $r.T, $r.R, $r.B)
$b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
$bmp = New-Object System.Drawing.Bitmap($b.Width, $b.Height)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($b.Location, [System.Drawing.Point]::Empty, $b.Size)
$w = $r.R - $r.L; $h = $r.B - $r.T
$crop = New-Object System.Drawing.Bitmap($w, $h)
$g2 = [System.Drawing.Graphics]::FromImage($crop)
$dest = New-Object System.Drawing.Rectangle(0, 0, $w, $h)
$srcRect = New-Object System.Drawing.Rectangle($r.L, $r.T, $w, $h)
$g2.DrawImage($bmp, $dest, $srcRect, [System.Drawing.GraphicsUnit]::Pixel)
$crop.Save('D:\Desktop\童浩\智能体工作目录\md-viewer\build\welcome-crop.png', [System.Drawing.Imaging.ImageFormat]::Png)
$g2.Dispose(); $g.Dispose(); $bmp.Dispose(); $crop.Dispose()
Write-Output 'saved welcome-crop.png (process left open for close-button test)'
