# 坚祥数字助理（jx-assistant）

完全自主编写的本地智能助手：**Go 后端（LLM 主循环 + 工具集 + 编排 + 上下文管理）+ 原生 JS 前端**。

- 后端依赖均为纯 Go（`modernc.org/sqlite` 无 CGo；`golang.org/x/crypto` 与 `pkg/sftp` 用于 SSH 远程工作区），前端无框架、无构建链。
- 构建产物为单个可执行文件（前端静态资源经 `go:embed` 内嵌），双击即用。

## 功能一览

- **多会话智能体**：多会话管理、子对话派发、上下文压缩与快照、待办/定时任务
- **工具集**：文件读写、命令执行（多级权限沙箱）、浏览器操作、桌面操作（截图/点击/输入）、图片识别、OCR、PDF、Office 文档
- **技能系统**：pdf / ocr / office / downloader / apkinfo 等内置技能，支持用户自建技能与技能市场分发
- **桌面模式**：系统 WebView2 开原生窗口（`-desktop`）
- **安卓端**：`android/` 目录为正式安卓工程（与桌面版同源）
- **远程访问**：跨设备远程/中继访问与 P2P 直连
- **离线演示**：`baseURL` 填 `mock` 进入离线演示模式，无需 API Key 即可体验完整链路

## 构建与运行

```sh
go build -o jx-assistant.exe .
.\jx-assistant.exe            # 首次启动自动生成 config.json 并打开浏览器
.\jx-assistant.exe -port 9000 -noopen
.\jx-assistant.exe -desktop   # 桌面模式：原生窗口
.\jx-assistant.exe -webdir web   # 开发模式：前端从磁盘读取，改 web/ 后刷新即生效
go test -run TestXxx .           # 单测（只跑与改动相关的）
```

首次使用：左下角「设置」中填入 API Key；`baseURL` 兼容 OpenAI chat-completions 接口。

## 大文件说明（不随仓库分发）

为保持仓库轻量，以下内容**不随源码仓库分发**，克隆后按需自动装配或另行获取：

| 路径 | 说明 | 获取方式 |
| --- | --- | --- |
| `技能/_runtime/` | 便携 Python 运行时（内置技能与语音组件共用） | 首次使用相关技能时自动联网装配 |
| `组件/voice/models/` | 语音识别 onnx 模型（体积较大） | 见 `组件/voice` 组件文档 |

## 开发历史

- **2026-08**：坚祥定制版（基于 DSH 的 TypeScript 定制开发）
- **2026-09-24**：完全重写为 Go 版，本仓库首个提交
- 此后 Go 版独立演进至今（见 git 历史与各版本 tag）

## 文档

开发规矩、架构与模块结构详见 [AGENT.md](AGENT.md)。
