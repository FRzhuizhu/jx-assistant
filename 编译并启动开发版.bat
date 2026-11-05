@echo off
rem ============================================================
rem  编译并启动开发版（并行实例，双击运行）
rem  1) 调用 打包\开发桌面版-并行.ps1 编译 开发桌面版.exe
rem  2) 专属互斥体 + 随机端口启动 WebView 桌面窗口
rem     与安装版 / 坚祥数字助理GO.exe 互不干扰，可同时开
rem  日志在开发版自己的控制台窗口（含启动期 panic 原因）
rem  （本文件须保存为 ANSI/GBK 编码：cmd 按 GBK 解析批处理）
rem ============================================================
cd /d "%~dp0"
echo 正在编译开发桌面版（并行实例）...
powershell -NoProfile -ExecutionPolicy Bypass -File "打包\开发桌面版-并行.ps1"
if errorlevel 1 (
  echo.
  echo [失败] 编译未通过。若提示无法写入 exe，请先关闭正在运行的开发版窗口再试。
  echo.
  pause
  exit /b 1
)
exit /b 0
