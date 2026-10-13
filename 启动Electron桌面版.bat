@echo off
rem ============================================================
rem  Start Electron desktop shell (double-click to run)
rem  Engine = repo-root jx-assistant.exe (latest build).
rem  If you changed Go/frontend code, rebuild first with
rem  the dev script in the build-scripts folder (da-bao) (see AGENT.md), then
rem  use this bat for fast daily launch.
rem  NOTE: keep this file pure ASCII + CRLF. cmd parses bat
rem  in ANSI/GBK; UTF-8 Chinese or bare-LF line endings
rem  corrupt commands (a corrupted fragment once launched the
rem  engine in browser mode = backend + webpage, not the app).
rem ============================================================
set "JX_GO_EXE=%~dp0jx-assistant.exe"
if not exist "%JX_GO_EXE%" (
  echo [ERROR] jx-assistant.exe not found in repo root. Run the dev script first.
  pause
  exit /b 1
)
cd /d "%~dp0..\jx-desktop"
if not exist "node_modules\electron\dist\electron.exe" (
  echo [ERROR] Electron not installed in jx-desktop. Run the dev script first.
  pause
  exit /b 1
)
npx electron .
