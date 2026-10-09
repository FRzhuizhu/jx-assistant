# Enumerate top-level explorer windows (CabinetWClass) as JSON lines:
# hwnd / visible / title. ASCII-only (PS5.1 reads BOM-less UTF-8 as ANSI).
# Callbacks collect into an ArrayList: Write-Output inside a .NET delegate
# gets swallowed if the EnumWindows call itself is piped to Out-Null.
Add-Type @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public class JxWEnum {
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc cb, IntPtr lp);
  public delegate bool EnumWindowsProc(IntPtr h, IntPtr lp);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern int GetWindowText(IntPtr h, StringBuilder sb, int n);
  [DllImport("user32.dll")] public static extern int GetClassName(IntPtr h, StringBuilder sb, int n);
}
'@
$out = New-Object System.Collections.ArrayList
$cb = {
  param($h, $lp)
  $cn = New-Object System.Text.StringBuilder 256
  [JxWEnum]::GetClassName($h, $cn, 256) | Out-Null
  if ($cn.ToString() -eq 'CabinetWClass') {
    $t = New-Object System.Text.StringBuilder 256
    [JxWEnum]::GetWindowText($h, $t, 256) | Out-Null
    [void]$out.Add(('{{"hwnd":{0},"visible":{1},"title":"{2}"}}' -f $h.ToInt64(), ([JxWEnum]::IsWindowVisible($h).ToString().ToLower()), ($t.ToString() -replace '"','\"')))
  }
  return $true
}
[JxWEnum]::EnumWindows($cb, [IntPtr]::Zero) | Out-Null
$out
