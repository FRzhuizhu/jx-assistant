@echo off
rem ============================================================
rem  Rebuild engine (jx-assistant.exe, web/ embedded via go:embed)
rem  and restart it with -noopen. ASCII-only on purpose: cmd parses
rem  this file with the ANSI code page, so no literal CJK here.
rem  NOTE: running this closes the current assistant session and
rem  remote connections (expected: history is kept in SQLite).
rem ============================================================
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0rebuild-engine.ps1"
if errorlevel 1 (
  echo.
  echo [FAILED] go build error. Old instance was closed and NOT restarted.
  echo Fix the source, then run this again.
  echo.
  pause
  exit /b 1
)
exit /b 0
