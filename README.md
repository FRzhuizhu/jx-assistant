<p align="center">
  <img src="web/logo.png" width="120" alt="jx-assistant logo" />
</p>

<h1 align="center">jx-assistant · AI 数字助理</h1>

<p align="center">
  <strong>一个 Go 二进制跑起来的本地 AI 数字助理</strong><br>
  LLM 主循环 + 全套工具集 + 多会话编排 + 技能系统，桌面 / 浏览器 / 安卓多端<br>
  A local-first AI assistant in a single Go binary — full toolset, multi-session orchestration & a skill system, on desktop, browser and Android.
</p>

<p align="center">
  <img alt="license" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <img alt="language" src="https://img.shields.io/badge/Go%20%2B%20vanilla%20JS-no%20framework-00ADD8">
  <img alt="platform" src="https://img.shields.io/badge/platform-Windows%20%7C%20Android%20%7C%20Web-lightgrey">
</p>

---

## 为什么再做一个 AI 助手？

市面上的 Agent 项目要么是云端 SaaS（数据不在你手里），要么是一堆脚本的组合（装环境先劝退一半人）。**jx-assistant 想做到的是：编译出一个 exe，双击就是完整产品。**

- **单文件、零环境**：Go 编译 + `go:embed` 内嵌前端，无 Node、无构建链、无 CGo（SQLite 用 modernc 纯 Go 实现）。技能所需的 Python 运行时在首次使用时自动装配，不需要预装。
- **完全本地**：会话、数据、密钥全在本机；模型接任意 OpenAI 兼容 API（官方、自建网关、内网中转都行），密钥落盘走系统级加密（Windows DPAPI）。
- **不只是聊天，是真能干活**：文件读写、命令执行、浏览器自动化、桌面键鼠操作、OCR、PDF、Office 文档……每个工具都有独立的权限与沙箱约束。
- **离线也能体验**：`baseURL` 填 `mock` 即进入离线演示模式，不配任何 Key 就能跑通完整链路。

## 功能全景

| 能力 | 说明 |
| --- | --- |
| 🗨️ **多会话与编排** | 多会话管理、子对话派发（把子任务交给后台 AI 并行执行）、上下文自动压缩、会话快照回退、待办与定时任务 |
| 🛠️ **工具集** | 文件（带历史快照）、命令（多级权限沙箱 + 审批）、浏览器驱动、桌面键鼠与截屏、图片识别、OCR、PDF、Word/Excel/PPT、APK 解析、下载器 |
| 🔐 **权限模型** | 只读 / 修改工作区 / 完全访问三档权限，命令沙箱（Windows 受限令牌），危险动作逐次审批，全程留痕 |
| 🧩 **技能系统** | `SKILL.md` 声明式技能，随包分发 + 市场热更；配套的技能市场服务端可私有部署 |
| 🖥️ **多端** | 桌面模式（系统 WebView2 原生窗口）、浏览器模式、安卓端（`android/` 同源工程，含专属命令沙箱） |
| 🌐 **远程访问** | 跨设备远程操作与 P2P 直连，手机上接着用家里电脑的会话 |
| 🔄 **静默更新** | 版本化更新分发（Gitea Release 兼容协议），换装接力不丢进程 |

## 📸 截图

<!-- TODO: 补充产品截图（建议 2-4 张：主对话 + 工具调用卡片、多会话侧栏、桌面模式、安卓端） -->
<!-- 放入 docs/screenshots/ 目录后按以下格式引用：
<p align="center">
  <img src="docs/screenshots/main.png" width="720" alt="主界面" />
</p>
-->

## 🚀 快速开始

```sh
git clone https://github.com/<你的用户名>/jx-assistant.git
cd jx-assistant
go build -o jx-assistant.exe .

./jx-assistant.exe            # 首次启动自动生成 config.json 并打开浏览器
./jx-assistant.exe -desktop   # 桌面模式：WebView2 原生窗口
./jx-assistant.exe -webdir web -port 9000   # 开发模式：前端改完刷新即生效
```

首次使用：左下角「设置」填入任意 OpenAI 兼容 API 的 `baseURL` + `apiKey`。
没有 Key？把 `baseURL` 填成 `mock`，离线跑通全部交互。

**安卓端**：`android/` 为正式工程（Gradle 构建），与桌面版共用同一套核心代码。

## 🏗️ 架构

```
┌─────────────────────────────────────────────┐
│  前端（原生 JS · 无框架 · go:embed 内嵌）      │
├─────────────────────────────────────────────┤
│  LLM 主循环 ─ 工具调度 ─ 多会话编排            │
│  ├── 工具层：文件/命令/浏览器/桌面/文档…        │
│  ├── 权限层：三档模式 · 沙箱 · 审批 · 留痕      │
│  ├── 技能层：SKILL.md 技能 · 市场 · 热更        │
│  └── 存储：SQLite（modernc 纯 Go）· 快照       │
├─────────────────────────────────────────────┤
│  桌面（WebView2）· 浏览器 · 安卓 · 远程/P2P    │
└─────────────────────────────────────────────┘
```

- 后端全部纯 Go，核心依赖极少（SQLite、SSH/SFTP、webview2 绑定——后者是本地维护的补丁版，见 `第三方库/go-webview2`）
- 前端零构建：改完 `web/` 下的 JS/CSS 刷新即生效，没有 npm 那一套

## 🤖 一个有趣的细节：这个项目主要由 AI 编写

从 2026 年 9 月的第一个提交起，绝大部分代码由**多个 AI 会话并行开发**完成（5 周内 590+ 提交）：开发规矩、架构口径、模块契约全部沉淀在 [AGENT.md](AGENT.md) 里，AI 照章办事、编译即验证、验证过即提交。它既是一个产品，也是一套「AI 军团怎么真正协作写代码」的实践样本——欢迎翻提交历史。

## 📜 开发历史

- **2026-08**：坚祥定制版（基于 DSH 的 TypeScript 定制）
- **2026-09-24**：推倒重写为纯 Go 版，本仓库首个提交
- **至今**：Go 版独立演进（见 git 历史）

## 📄 许可证

[Apache License 2.0](LICENSE)：允许商用、修改与分发；使用者自动获得贡献者授予的专利许可，若对使用者发起专利诉讼则许可自动终止。
