# click at screen (x,y), type text, press Enter. ascii only.
param(
  [int]$X,
  [int]$Y,
  [string]$Text
)
Add-Type -AssemblyName System.Windows.Forms
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public class M2 {
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
  public const uint LEFTDOWN = 0x02, LEFTUP = 0x04;
}
"@
[void][M2]::SetCursorPos($X, $Y)
Start-Sleep -Milliseconds 150
[M2]::mouse_event([M2]::LEFTDOWN, 0, 0, 0, [UIntPtr]::Zero)
Start-Sleep -Milliseconds 60
[M2]::mouse_event([M2]::LEFTUP, 0, 0, 0, [UIntPtr]::Zero)
Start-Sleep -Milliseconds 500
[System.Windows.Forms.SendKeys]::SendWait($Text)
Start-Sleep -Milliseconds 300
[System.Windows.Forms.SendKeys]::SendWait('{ENTER}')
Start-Sleep -Milliseconds 300
Write-Output ("typed '" + $Text + "' at " + $X + "," + $Y + " + Enter")
