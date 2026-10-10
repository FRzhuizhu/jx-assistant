@echo off
rem ============================================================
rem  Start Electron desktop shell (double-click to run)
rem  Engine = repo-root jx-assistant.exe (latest build).
rem  If you changed Go/frontend code, rebuild first by running:
rem    打包\开发桌面版-Electron.ps1   (compile + install + start)
rem  This bat is start-only (fast daily launch).
rem ============================================================
set "JX_GO_EXE=%~dp0jx-assistant.exe"
if not exist "%JX_GO_EXE%" (
  echo [ERROR] jx-assistant.exe not found in repo root. Run 打包\开发桌面版-Electron.ps1 first.
  pause
  exit /b 1
)
cd /d "%~dp0..\jx-desktop"
if not exist "node_modules\electron\dist\electron.exe" (
  echo [ERROR] Electron not installed in jx-desktop. Run 打包\开发桌面版-Electron.ps1 first.
  pause
  exit /b 1
)
npx electron .
