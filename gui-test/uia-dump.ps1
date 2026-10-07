# Dump UIA element tree of the assistant main window (ascii output only).
# usage: uia-dump.ps1 <pid> [maxDepth] [filterRegex]
param(
  [int]$ProcId = 17244,
  [int]$MaxDepth = 14,
  [string]$Filter = ""
)
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$cond = New-Object System.Windows.Automation.PropertyCondition(
  [System.Windows.Automation.AutomationElement]::ProcessIdProperty, $ProcId)
$root = [System.Windows.Automation.AutomationElement]::RootElement
$wins = $root.FindAll([System.Windows.Automation.TreeScope]::Children, $cond)
if ($wins.Count -eq 0) { Write-Output "no window"; exit }
function Dump($el, $depth) {
  if ($depth -gt $MaxDepth) { return }
  $name = $el.Current.Name
  $type = $el.Current.ControlType.ProgrammaticName -replace 'ControlType.', ''
  $val = ""
  try {
    $vp = $el.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern)
    $val = $vp.Current.Value
  } catch {}
  $line = ("  " * $depth) + $type + " | name=[" + $name + "] | value=[" + $val + "]"
  if ($Filter -eq "" -or $line -match $Filter) { Write-Output $line }
  $kids = $el.FindAll([System.Windows.Automation.TreeScope]::Children,
    [System.Windows.Automation.Condition]::TrueCondition)
  foreach ($k in $kids) { Dump $k ($depth + 1) }
}
Dump $wins[0] 0
