$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public class MouseHelper {
    [DllImport("user32.dll")]
    public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")]
    public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, IntPtr extra);
}
'@
Add-Type -AssemblyName System.Windows.Forms

$exe = 'D:\Desktop\童浩\智能体工作目录\坚祥数字助理\md-viewer\MD查看器.exe'
Start-Process $exe
Start-Sleep -Seconds 5
$p = Get-Process | Where-Object { $_.ProcessName -eq 'MD查看器' } | Select-Object -First 1
if (-not $p) { Write-Output 'FAIL: process not found'; exit 1 }
Write-Output ("process: " + $p.Id)

# Window rect -> primary-button center. Card is centered; primary button sits
# right of center at the bottom action row. 100% DPI: window 686x634.
$r = $p.MainWindowHandle
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public class WR3 {
    [DllImport("user32.dll")]
    public static extern bool GetWindowRect(IntPtr h, out RECT3 r);
    public struct RECT3 { public int L, T, R, B; }
}
'@
$rect = New-Object WR3+RECT3
[WR3]::GetWindowRect($r, [ref]$rect) | Out-Null
$cx = [int](($rect.L + $rect.R) / 2) + 62
$cy = [int]($rect.B - 46)
Write-Output ("click at $cx,$cy (window L=$($rect.L) R=$($rect.R) B=$($rect.B))")
[MouseHelper]::SetCursorPos($cx, $cy) | Out-Null
Start-Sleep -Milliseconds 250
[MouseHelper]::mouse_event(2, 0, 0, 0, [IntPtr]::Zero)
Start-Sleep -Milliseconds 80
[MouseHelper]::mouse_event(4, 0, 0, 0, [IntPtr]::Zero)
Start-Sleep -Seconds 3
if (Get-Process -Id $p.Id -ErrorAction SilentlyContinue) {
    Write-Output 'FAIL: window still open after primary-button click'
    exit 1
}
Write-Output 'PASS: primary button closed the window'
