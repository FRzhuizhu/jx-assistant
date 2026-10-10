# 坚祥数字助理（GO版）

完全自主编写的本地智能助手：**Go 后端（LLM 主循环 + 工具集 + 编排 + 上下文管理）+ 原生 JS 前端**。

- 后端依赖均为纯 Go（modernc.org/sqlite 无 CGo；golang.org/x/crypto 与 pkg/sftp 用于 SSH 远程工作区），前端无框架、无构建链。
- 构建产物为单个可执行文件（前端静态资源经 `go:embed` 内嵌），双击即用。
- 架构参考 pi / Reasonix / DSH 等开源 Agent 的主循环与编排思路，代码全部为新写实现。

## 运行

```sh
go build -o jx-assistant.exe .
.\jx-assistant.exe            # 首次启动自动生成 config.json 并打开浏览器
.\jx-assistant.exe -port 9000 -noopen
.\jx-assistant.exe -desktop   # 桌面模式：系统 WebView2 开原生窗口（不开浏览器）
.\jx-assistant.exe -webdir web   # 开发模式：前端从磁盘读取，改 web/ 后刷新浏览器即生效
go test .                         # 单测（权限引擎 / 压缩管线 / 摘要折叠）
```

首次使用：左下角「设置」中填入 API Key；`baseURL` 兼容 OpenAI chat-completions
接口。`baseURL` 填 `mock` 可进入**离线演示模式**，无需 Key 即可体验完整链路。

## 开发规矩（AI 开发前必读）

> 本项目主要由 AI 开发，节奏快，且经常有多个 AI 在同一台电脑上并行开发。
> 动手前先读完本节；功能与架构细节看下文各节与「存储与模块结构」。
> （本节由原《开发规范.md》并入——两文档曾长期重复，已合并至此。）

### 写代码前的 7 个问题

思考一个新功能时，先问自己这 7 个问题，非常重要：

1. Does this need to exist? → no: skip it (YAGNI)
2. Already in this codebase? → reuse it, don't rewrite
3. Stdlib does it? → use it
4. Native platform feature? → use it
5. Installed dependency? → use it
6. One line? → one line
7. Only then: the minimum that works

放到本项目的语境里：

- 动手新写之前，先搜一遍代码库：工具实现看 `tools.go` / `historytools.go`，
  上下文处理看 `context.go` / `contextengine.go`，扩展机制看 `components.go` /
  `adapters.go`，前端按区域看 `web/`（对话区 `app-chat.js`、左栏+顶栏
  `app-left.js`、右栏 `app-right.js`、设置 `app-settings.js`、桌面壳右键菜单
  `app-ctxmenu.js`、共享地基 `app-core.js`）——大概率已经有了，复用而不是重写。
- 优先用 Go 标准库。本项目直接依赖只有 3 个（`modernc.org/sqlite`、
  `golang.org/x/crypto`、`pkg/sftp`），全部是不可替代项；想引入新依赖，
  先自问前 3 条问不问得过去。
- 前端是原生 JS、无框架、无构建链，这是有意的架构决策（软著全自主实现 +
  单文件交付）。"引入框架/打包器让代码好写一点"在本项目里默认是错误答案。

### 开发流程：调研规划 → 执行 → 验证 → 提交

五步顺序不可颠倒，任何一步不许跳过：**了解项目 → 制定计划 → 执行 →
测试验证 → 提交**。

1. **了解项目**：动手前读完本节与直接相关的源码，想清楚影响面（例如改
   `agent.go` 的工具回填，会同时影响 SSE 事件流 `server.go` 与前端渲染
   `web/app-chat.js`）。不明白的地方先弄明白，禁止"边写边猜接口"。
2. **制定计划**：列出改哪些文件、按什么顺序、每步怎么验证。方案有分叉
   或影响面拿不准时，先把方案讲清楚再继续。
3. **执行**：小步前进，每完成一个可验证的小步骤就 `go build` 一次，
   保证随时可回退。
4. **测试验证**：`go build` 与 `go test .` 必须通过；前端改动要实际打开
   页面看一眼（布局、交互、组件/插件注入是否正常）——完成标准是"看起来
   对"，不是"编译通过"。测试数据与辅助脚本只落本目录（`data/` 已
   gitignore，用完即删），禁止在系统临时目录或其他位置留测试文件。
   注：根目录的 Go 单测已搬到 `.回收站/`（点开头，Go 工具链自动忽略），
   所以 `go test .` 现在没有测试文件可跑；搬运原因与搬回命令见
   `.回收站/说明.md`。
5. **提交**：测试通过后必须提交——没有 commit 的改动等同于没做完。只提交
   本次任务自己的改动（明确 add 涉及的文件，禁止 `git add -A` 一把梭）；
   一次提交对应一件事，提交信息一句中文说清做了什么；提交后推送到远程
   （当前工作分支：`go`）。

### 多 AI 并行开发的特别约定

- **同一台电脑上经常有多个 AI 同时开发本项目**：文件可能正在被别的会话
  修改——工作区里出现不是自己做的未提交改动是常态，别动它、更别顺手提交。
- **测试验证用已在运行的主实例，不另开演示实例**：各种改动基本是热加载的
  （`-webdir web` 下前端改完刷新页面即生效；组件/插件开关即点即生效）。
  确实需要重启就直接重启，但**尽量不要关闭实例**——可能把其他 AI 正在
  跑的工作给停掉。
- **构建/运行突然失败，先怀疑"别人改到一半"**：失败原因指向正在变动的文件
  时不要急着"修复"，等约 5 分钟再看——多半已能正常启动，或已被对方启动。

### 代码约定与常见陷阱

约定：注释、提交信息、UI 文案均为中文，错误信息要求用户可读；运行时产物
不入库（`data/`、`config.json`、打包产物等，见 `.gitignore`），唯一例外
`jx-assistant.exe`（启动程序，必须入库）。

陷阱（每条都来自真实踩坑）：

- `插件/` 目录是 DSH 插件原件，**一个字都不能改**；一切适配写在
  `插件兼容层/` 与 `adapter_*.go`。
- `config.json` 与 `data/` 是运行时产物（含 API Key 与全部会话记录），永不
  入库，也不要当源码引用。
- 前端没有构建链：`web/` 里的文件就是最终产物。
- 权限判断只在 `perm.go` 一处做，别在工具或服务器层复制权限逻辑；fail
  closed 是原则（过期/重用凭证一律拒绝）。
- 插件目录的 `node_modules` 层级极深，Windows 下会触发路径超长错误，已
  gitignore，不要加回来。
- **别把执行绑在 HTTP 请求上**：`r.Context()` 随浏览器刷新/关页取消；新增
  会阻塞的调用一律传执行体的 ctx（`sessionRun.ctx`），不要退回
  `r.Context()`。
- **`Store.SaveMeta` 刻意不写 `status` 列**：这一列只由 `UpdateStatus`
  维护；要改状态就调 `UpdateStatus`，别往 `SaveMeta` 里加回去。
- **异常中断标记（侧栏「⚠ 已中断」）存在 `meta` 表**（`laststop:<会话id>`），
  别挪进 `sessions` 的列：不进 `sessionCols` 就不可能被 `SaveMeta` 拿着陈旧对象
  回写（同 `status` 列踩过的坑），也免一次结构迁移。写入只认
  `Store.SetLastStop` 一处调用点（`runtime.go` 的终态收口 + 启动时的
  `recoverInterrupted`），值域与清除时机见该函数注释。
- **`sessions.updated_at` 只认两个时刻**：用户最后一次输入、模型最后一次最终输出
  （无 tool_calls 的 assistant 行）。侧栏/会话列表的「最近更新」排序只看这一列，
  判定收在 `StoredMessage.touchesActivity()` 一处（`store.go` 落库层与 `agent.go`
  的 `appendMessage/appendRow/appendMessageThinking` 都调它）。工具结果、带
  tool_calls 的中间步、env/prompt/summary/report 等注入行一律不刷新——它们在一轮
  任务里成串出现，会把会话反复顶到最前，几个任务并行时侧栏来回跳、点谁错谁。
  系统通知真要刷新时间，由调用方显式写 `UpdatedAt` 再 `SaveMeta`（上传/回退/交付
  /网页查看现在就是这么做的），别去改 `touchesActivity` 的口径。
- **子进程一律压掉控制台窗口**：桌面形态是 `-windowsgui` 构建（自己没有控制台可
  复用），拉起 cmd / powershell / sh / python / icacls 这类 console 程序时若不带
  `CREATE_NO_WINDOW`，Windows 会给每个子进程新开一个控制台窗口——用户桌面上就是
  「一直往外冒黑窗」。凡是 `exec.Command*` 起 console 程序，落 Start/Run/Output
  之前一律过一遍 `hideConsoleWindow`（`consolehide_windows.go`，非 Windows 是空
  实现）：run_command 的受限（`sandbox_windows.go` 的 CreateProcessAsUser）与
  不受限两条路、后台作业（`jobs.go`）、技能脚本与 python 探测、icacls、打开浏览器
  都已接上；`openHideWindow` / `gitHideWindow` 是同款历史副本，新增代码别再加第三份。
- **异常收尾必须让用户看见，但不要往对话里塞话**：真实失败走输入框上方的
  报错条（`showRunError`，带重试）并在服务端记 `runError`；对话区只放真实
  对话，别写「（本轮…）」这类系统交代——但也不能什么都不报。
- **平台适配走「文件后缀」，不建子目录、不开分支**：平台实现一律
  `<域>_<平台>.go`（windows / linux / android / other，`_other` 为必须可
  编译的兜底桩）；平台判断用 `isWindowsRuntime / isLinuxRuntime /
  isAndroidRuntime` 常量，禁止散落 `runtime.GOOS` 比较；差异文件索引与
  新增平台 SOP 见《平台适配》节。

### 能不造轮子就不造轮子

市面上有知名、成熟的工具或库，且体积不大、许可允许时，**直接用现成的**，
不要自己重复实现：协议细节、边界情况、安全问题人家都替你踩过了。例子：
二维码的生成与识别用成熟库（不要手写编码矩阵），文件下载用 aria2c
（`技能/downloader/` 内置的官方二进制），APK 解析用纯 Go 库
（`apkinfo` 子命令，androidbinary + pkcs7）。自研只保留给两种情况：
没有可靠现成方案，或现成方案的集成成本明显更高。判断口诀——
**先找轮子，再决定要不要造**；引入时连同许可文本一起入库
（GPL 等强许可尤其要附全文）。

## 平台适配（单包 + 文件后缀）

多平台支持不开分支、也不拆目录：全部代码同属一个 `package main`，平台差异由
**build tags 按文件名后缀在编译期选择**（`GOOS=android go build` 自动只挑
android/other/无后缀文件），构建脚本零改动。拆包（建 android/、linux/ 子目录）
意味着接口抽象层与全局状态重构，收益低于成本；仅当出现「可独立构建的平台
子系统」才建独立子目录（先例：`sandboxhelper/`）。

后缀与命名：

| 形式 | 含义 |
|---|---|
| 无后缀 | 全平台通用（跨平台默认逻辑） |
| `<域>_windows.go` / `_linux.go` / `_android.go` | 平台实现；写 `_linux.go` 记得排除 android（`runtime_linux.go` 的约束是 `linux && !android`） |
| `<域>_other.go` | 兜底桩：必须可编译，不支持的能力返回明确报错（参考 `fileopen_other.go` / `pickfolder_other.go`），不得静默失败 |
| `<域>_<平台>_stub.go` | 为其他平台构建保留符号（如 `sandbox_windows_stub.go`） |

平台判断统一用 `isWindowsRuntime / isLinuxRuntime / isAndroidRuntime`
（`runtime_{windows,linux,android,other}.go` 四件套）；**禁止**散落的
`runtime.GOOS` 直接比较（`fileopen_other.go` 内的 darwin/linux 细分属于
「非 Windows 世界内的细分」，可留在平台文件里）。短分叉（文案/单条件）可留在
通用文件，成块逻辑下沉平台文件。

### 差异文件索引（新增平台照此逐项过）

| 能力域 | 文件组 | 备注 |
|---|---|---|
| 运行时判定 | `runtime_{windows,linux,android,other}.go` | 三常量，新平台起点 |
| 桌面壳 | `desktop_windows.go` / `_other.go` | WebView2 壳仅 Windows，其余报错 |
| 浏览器面板 | `browser_panel_windows.go` / `_other.go` | 原生面板仅 Windows；其余回退无头宿主 |
| 密钥保护 | `dpapi_windows.go` / `_other.go` | 非 Windows 加密直通（明文落盘，如实文档） |
| Git 面板 | `gitpanel_windows.go` / `_other.go` | 差异仅「窗口隐藏」细节 |
| 打开文件 | `fileopen_windows.go` / `_other.go` | _other 内细分 darwin/linux，android 落明确报错 |
| 选择框 | `pickfolder_windows.go` / `_other.go` | 非 Windows 报「仅支持 Windows」 |
| 进程守护 | `processguard_windows.go` / `_other.go` | 顶层 kill-on-close Job 仅 Windows |
| 原子文件/路径围栏 | `fsatomic_*` / `fspath_*` | 差异为 ACL 复制与 junction 规范化 |
| 命令沙箱 | `sandbox_windows.go`(+`_stub`) / `sandbox_linux.go`(+`_stub`) + `sandbox.go` 分派 | android 分支：无 OS 沙箱、直接执行，隔离由应用沙箱承担 |
| 沙箱审计 | `sandbox_audit_windows.go` / `_other.go` | 常驻 ACE/密级标签仅 Windows，其余恒空 |
| 电脑操控 | `tool_computer_windows.go` / `_other.go` | 仅 Windows，其余注册时报错 |
| 演示记录器 | `recorder_windows.go` / `_other.go`（定义在 `recorder.go`，跨平台） | 创建技能模式专用；仅 Windows 有真实现（全局钩子+截屏+MCI 录音），其余报「不支持」 |
| 进程退出信号 | `pdeathsig_android.go` / `_other.go` | android 用 PR_SET_PDEATHSIG |
| sqlite 兼容 | `sqlite_android_amd64.go` / `sqlite_android_stub.go` | 仅 android/amd64 打 seccomp 补丁 |
| 手机操控工具 | `tool_device_android.go` / `_other.go` | 仅安卓注册 device_*/adb_*（三件套，见 android/README.md）；桥端点 device_bridge.go 全平台编译（无壳轮询自然空闲） |
| ADB 主机客户端 | `adb.go`（平台中立） | 纯 Go 实现：RSA 认证 + shell,v2,raw（API 35 adbd 已移除 v1 shell:/exec:，报文字段小端、CNXN 零校验） |

通用文件中的平台分支共 4 文件 12 处（审计于 2026-09）：`sandbox.go`
72/79/81/100/119/143/203（沙箱分派与拒绝方言）、`agent.go` 217/225（Android
权限文案）、`perm.go` 398/472、`tools.go` 950——改造时逐处标注手机端语义，
能下沉到平台文件的逐步下沉。

### 新增平台接入 SOP

1. 加 `runtime_<平台>.go`（三常量）并复核 `_other.go` 的排除约束；
2. 按上表逐域过：需要实现的写 `<域>_<平台>.go`；不需要的确认 `_other.go`
   兜底报错对用户可读（参考 `pickfolder_other.go` 风格）；
3. 构建脚本加 `GOOS=<平台>` 目标（参考 `android/build-android.sh`）；
4. 编译矩阵全绿：`GOOS=windows / linux / android` 全部构建通过；
5. 真机/模拟器回归：本平台「不支持项」逐条确认报错文案。

### 手机端改造的新增文件预置（2026-09 计划）

| 新增 | 归属 |
|---|---|
| `termux_android.go` | Termux 快照装配、prefix 环境、进程组生命周期 |
| `tool_device_android.go` + `tool_device_other.go` | 无线 ADB / 截屏 / 无障碍的引擎侧工具 |
| Kotlin 桥（DeviceBridge、AccessibilityService 等） | `android/app/src/main/java/com/jianxiang/assistant/`（平台资产） |
| 快照制作脚本与选包清单 | `android/` 子目录或 `打包/`（非 Go 编译单元，可建子目录） |

## 桌面版（WebView2 壳）与安装包

- `-desktop` 参数（或安装包里的发行 exe **默认**形态，构建时
  `-ldflags "-H=windowsgui -X main.desktopDefault=1"` 注入：GUI 子系统
  双击无 CMD 黑窗，无控制台时日志改落 data\desktop.log）用系统自带的
  WebView2
  运行时开原生桌面窗口：标题「坚祥数字助理GO」，窗口/任务栏图标取 exe
  内嵌 LOGO 资源（`打包/make_icon.py` 由 web/logo.png 生成多尺寸 ico，
  go-winres 按 `go-winres.json` 嵌入为 RT_GROUP_ICON #1；
  `-browser` 参数可强制回浏览器形态）。
  开发期间想要桌面形态：`powershell -File 打包\开发桌面版.ps1` 在仓库根
  产出 `坚祥数字助理GO.exe`（双击即窗口，数据照旧落仓库目录）。
- 桌面模式行为：相对路径（config / data / 技能）锚到 exe 目录（开发目录
  有 go.mod 时不搬迁，避免扰动调试）；监听端口被占自动回退；命名互斥体
  单实例，二次启动唤起已有窗口；默认工作区不落在安装目录本身：改写为
  安装目录下的「默认工作区」子文件夹（模型工作区域与软件本体/配置隔离；
  目录不可写时回退「文档\坚祥数字助理」）并回写配置；旧版的「工作区」
  原地改名成「默认工作区」（装包时先做一次，启动时兜底重试，并把显式挂
  在旧路径上的历史会话一并改指新目录）；WebView2 运行时缺失时给官方下载
  指引。
- **安装包**（Inno Setup，内置技能便携运行时，约 350MB 源经 LZMA2 压到
  约 103MB，装完离线全功能）：

  ```sh
  winget install JRSoftware.InnoSetup      # 一次性装打包工具
  powershell -NoProfile -ExecutionPolicy Bypass -File 打包\打包安装包.ps1
  # 产物：打包\输出\坚祥数字助理GO-安装包-<版本>.exe
  ```

  安装到当前用户 `%LOCALAPPDATA%\Programs`（免管理员），中文向导、
  桌面/开始菜单快捷方式、注册表卸载项；卸载默认保留会话数据。
  升级安装时归置默认工作区：建好 `{app}\默认工作区`，并把旧版的
  `{app}\工作区` 原地改名过来（用户文件不动；改名失败留给程序启动时
  重试，改名成功则由程序把挂过旧路径的历史会话改指新目录）。
  脚本默认现场 `go build`；要排除工作区未提交改动时，先在干净 worktree
  里构建，再以 `-ExeSource <exe路径>` 传入。打包产物（stage/、输出/）
  已 gitignore，`config.json` 与 `data/` 永不进包。

  **打包清单纪律**（`打包安装包.ps1` 强制校验，缺一拒绝出包）：
  ① 主程序 exe（注入桌面默认形态）；② `技能/`（含 `_runtime` 便携运行时，
  依赖逐 manifest 校验可导入）；③ **`组件/` 内置组件**——git 里跟踪的
  全部默认组件必须随包分发（危险命令护栏、待办定时、Git 面板、语音），
  脚本会校验组件目录存在且至少含一个带 `manifest.json` 的组件；④
  `使用说明.md`；⑤ **`asr/`**（本地语音识别的 Python worker `asr_worker.py`，跑在便携运行时上——缺它时语音组件的「本地语音识别模型」档报「未找到 asr/asr_worker.py」，听写默认走云端不受影响）。给打包脚本加新的暂存项时，同步更新本清单。

## Electron 壳（jx-desktop，迁移期新形态）

仓库 `electron-migration` 分支上的第三种桌面形态：壳是独立目录
`D:\智能体工作区\jx-desktop`（Electron 44，自带 Chromium，版本随安装包
锁定——不再被 WebView2 Evergreen 运行时回归偷袭）。Go 仓库只加了薄薄一层：

- **sidecar 模式**：`-electron` 参数＝桌面模式的环境准备（cwd 锚定/默认
  工作区迁移/禁开浏览器）但不开任何窗口，纯当本地 HTTP 服务跑，由 Electron
  主进程拉起（`-port` 随机分配）；`main.go` 的 `anchored` 汇总两形态。
  端口被占时**硬失败**（不静默回退随机端口——Electron 要感知失败换端口重拉）。
- **右栏面板桥**：`browser_panel_electron.go` 实现 browserTarget 的第三个
  形态（chromedp 宿主 / 原生 WebView2 面板 / 本桥）。Electron 主进程的
  `panel-bridge.js` 在 127.0.0.1 随机端口起带令牌 HTTP 服务驱动页面内
  `<webview id="bpw-webview" partition="persist:webview-browser">`；地址与
  令牌经 `JX_ELECTRON_BRIDGE`/`JX_ELECTRON_BRIDGE_TOKEN` 环境变量注入，
  `main.go` 发现即挂 `App.nativePanel`（缺省＝旧形态，互不影响）。工具栏
  路径不变（仍走 /api/browser/*）；导航到达由桥 POST
  `/api/browser/electron-report` → `onReport` 广播 browser_nav（与旧壳
  onNavCompleted 同一事件面）。
- **前端分支**：URL 带 `&electron=1` 时 app-core.js 给 `<html>` 加
  `electron-shell` 类并置 `window.JX_ELECTRON`；app-right.js 的 bpwReport
  矩形上报整体短路（webview 是 DOM 元素，无矩形同步）；style.css 控制
  webview 仅在该形态显示。**语音听写暂缺**（Web Speech API 在 Electron
  无免费识别通道）：按钮与快捷键提示在 electron 形态隐藏，`-browser` 与
  旧壳不受影响，等本地 ASR（SenseVoice 路线，《语音后端化》§七）落地放开。
- 已知 Electron webview 坑：**无 `src` 的 `<webview>` 不会创建 guest 进程**
  （did-attach-webview 不触发），标签必须带 `src="about:blank"`。
- 开发/打包：`打包\开发桌面版-Electron.ps1`（编引擎+npm+启动，含
  npmmirror 二进制补装）；jx-desktop\`打包-Electron.ps1` 出 NSIS 安装包
  （resources\go\ 是引擎自包含世界，与旧安装版同口径）。GUI 回归测试：
  壳加 `--remote-debugging-port=9223`，gui-test 的 CDP 脚本直接可用
  （Electron 不封调试端口），参考 electron-smoke.mjs / electron-panel.mjs。
- **旧 WebView2 壳保留不删**（回退与语音过渡载体）；Electron 版稳定 +
  语音落地后再清理 desktop_windows.go / browser_panel_windows.go。

## 自带的 MD 查看器（md-viewer/）

项目自带一个独立小应用「MD 查看器」：双击 .md 文件弹出独立窗口，用系统
WebView2 内核渲染（浏览器组件已全部内嵌进 exe，整个程序就
`MD查看器.exe` 一个文件），不依赖 Python，主线程序没在运行也能单独使用。

- 第一次使用：双击运行 `md-viewer/MD查看器.exe` 自动完成注册，之后
  双击 .md 文件选择「MD查看器」并勾选「始终使用此应用」即可；
  取消关联用 `MD查看器.exe /unregister`（或说明窗口里的「取消关联」按钮）。
- 未装 WebView2 运行时自动降级用系统浏览器打开；详见 `md-viewer/使用说明.txt`。

## 权限与执行模型（移植 DSH 官方沙箱机制，本栈重写）

与官方同构的完整沙箱栈——**OS 级强制隔离**，不是策略约定：

- **三档沙箱策略**（会话粒度，权限芯片选择）：
  `read-only`（沙箱内执行、禁止任何写入）/ `workspace-write`（默认；只能写
  工作区与沙箱私有临时目录，联网不限）/ `full`（不受限，官方
  danger-full-access 语义）。
- **文件工具三道防线**（`perm.go`/`fsatomic.go`，对齐官方 fs-sandbox/
  fs-observation-policy/fs-local writeFileAtomic；文件工具跑在主进程不经
  OS 沙箱，须自带边界）：① 进程内路径围栏 `writeFenceAllows`——写前
  GetFinalPathNameByHandle 原生规范化（junction/符号链接/subst 全解析；
  Go 的 EvalSymlinks 不解析 junction）+ 词法包含 + dev/ino 身份比对回退，
  可写根 = 工作区 + 系统临时目录（官方 writableRoots 同款）；② 观察策略
  ——既有文件本会话 read 过才能被 write 整体覆盖（防盲覆盖；刚创建/刚
  改过的视为已观察，会话间隔离）；③ 原子替换写——同目录临时文件 +
  rename 换指新 inode，工作区内指向外部文件的 NTFS 硬链接不被写穿
  （外部别名保持旧内容），Windows 上替换前尽力保留原文件安全描述符。
- **Windows 后端**（`sandbox_windows.go`，对齐官方 sandbox-windows-acl）：
  `CreateRestrictedToken` 造受限令牌（剥全部特权 + LUA + 写受限 Flags），
  restricting 列表 = 登录 SID + Everyone + 工作区/临时目录合成 SID（由路径
  SHA-256 派生，长期 ACE 授权工作区、一次性授权每运行私有临时目录）；子进程
  每次写都过内核两段检查。令牌默认 DACL 按官方 setTokenDefaultDaclGrant
  补授（临时 SID 优先，新建对象不携带共享工作区能力），保证管道/临时文件
  可创建。NTFS 硬链接写穿属官方自报缺口（密级关卡撤除后重新打开：受限
  进程打开工作区内硬链接名时，NTFS 懒传播会把继承授权 ACE 应用到共享
  SD，外部别名随之可写——首授后全树扫描摘 ACE 的收尾因懒传播在下次
  打开时重新应用而无效，已移除）。
- **密级关卡（已撤除）**：曾在官方之上叠加 Low 完整性级别封堵——受限
  令牌降为低完整性（S-1-16-4096）、工作区与私有临时目录打 Low 强制标签，
  内核 NO_WRITE_UP 不分路线统一拒绝「写/删密级更高的对象」。因常驻
  Low 标签污染真实工作区（把程序自身目录、数据文件一并降级，低级别
  进程链连锁出各种「权限过低」），2026-09 整体撤除，回到官方同款语义。
  由此接受官方已知盲区：删除走**父目录 FILE_DELETE_CHILD（清柜权）路线**
  时不触发写受限双检（%TEMP%/Documents/Desktop 等用户 FullControl 位置可
  被 非 -Force Remove-Item 与 cmd del 删穿，判定实验矩阵固化在
  sbx_delete_matrix_test.go，其中无清柜权位置仍四路全拒）；**Everyone
  可写位置**可写穿（ Everyone 在 restricting 列表内，pass-2 放行）。
  后续计划从工具层（命令限制/审批）补防。历史残留的 Low 标签由启动时
  `restoreStaleIntegrityLabels` 自动还原（WO 引导三步，见其注释），手动
  清理用 `sandbox acl-audit --revoke`。
- **Job Object 进程树兜底**（官方 createKillOnCloseJob 同款）：子进程挂起
  创建（CREATE_SUSPENDED）→ 分配 kill-on-close Job → 恢复线程；超时/取消
  TerminateJobObject 整树终止（含孙进程），主进程崩溃句柄关闭自动清场。
- **顶层进程树兜底**（processguard_windows.go）：主进程启动即挂入一个
  kill-on-close Job——沙箱外路径（完全访问模式、后台任务、chromedp
  浏览器、技能 python、`start` 派生的孙进程）全部继承成员身份；助手
  无论正常退出、被强杀还是崩溃，内核都会终止它启动过的一切，不留
  无人认领的后台残留。对照实验：有守护时父进程退出子进程即清，无守护
  时子进程成孤儿存活（Windows 默认语义）。
- **敏感字段静态加密**：API Key 与 SSH 密码落盘前 DPAPI 加密
  （`dpapi:v1:` 前缀，绑定当前 Windows 用户），读取时透明解密、首启自动
  迁移——沙箱读不受限语义下 `type config.json` 看到的只是密文。
- **同源校验**：HTTP 面去掉 `Access-Control-Allow-Origin: *`，Origin 与
  Host 不符一律 403——堵死本机恶意网页跨域点「允许」/切 full 绕过沙箱
  体系的 CSRF 面。
- **常驻授权审计**：工作区 ACE（及历史密级标签）是跨会话复用的常驻授权
  （官方同款设计，副作用是换工作区后留残留）。`jx-assistant.exe sandbox
  acl-audit [--revoke] [--yes]` 报告并确认后清理两代合成 SID（本产品
  S-1-80-* 与 DSH 官方 S-1-4-*）授权与 Low 标签残留。
- **Linux 后端**：额外放行 `/dev/null` sink（对齐官方 POSIX 后端），沙箱内
  `>/dev/null`、`2>/dev/null` 类重定向正常工作。
- **Linux 后端**（`sandboxhelper`，对齐官方 Landlock 方案）：非特权
  Landlock ruleset「handled」全部写类访问，workspace-write 下仅给工作区与
  私有临时目录加 path-beneath 规则；`PR_SET_NO_NEW_PRIVS` 防提权。本地
  Linux 主程序自重入施限；`build.bat` 交叉编译 amd64/arm64 helper 并内嵌。
- **SSH 远程沙箱**（对齐官方 sandbox-ssh「远端选择它安装的本地后端」）：
  首次执行时按远端架构把 helper 经 SFTP 上传到 `~/.jx-assistant/bin/`，
  命令在远端沙箱内运行；远端内核不支持 Landlock 时如实报
  `sandbox unavailable`（fail closed）。helper 带语义版本号：每次连接
  远端执行 `version` 比对，不一致自动重传。私有临时目录由外层包装创建
  并经 `trap` 清理（历史版本曾按次泄漏 /tmp/jx-sbx-* 目录，已修复）。
- **拒绝驱动的升级**（对齐官方 bash 工具）：不做威胁命令猜测、不做执行前
  预检——命令先按会话策略进沙箱真实执行，被拒结果带
  `[sandbox: … denied under <mode> mode]` 标记；模型同轮带
  `sandbox_permissions`（够用的最宽档）+ 一句 `justification` 重试同一命令
  一次，弹**审批卡**由用户决定；拒绝后该命令本轮终局。
- **能力开关 = 常设授权**（权限面板右侧多选）：审批卡点「始终允许」时，命令
  升级点亮对应能力开关（如 `允许工作区外执行命令`），工作区外写入则把会话
  权限**提升为完全访问**。切到完全访问会预设捆绑点亮
  `允许工作区外执行命令`、`操作浏览器`、`操作电脑` 三项能力（前端勾选联动
  置灰、不可单独关闭，权限引擎同样视为开启；切回其他档位时自动取消勾选，但
  **默认能力会补回来**——「工作区内读写」默认开启 `操作浏览器`（`perm.go`
  `capDefaultsForMode`：新建会话、`dispatch_sub` / `dispatch_task` 派发、分支
  复制、以及从完全访问切回时一律补上；用户在权限芯片里显式取消后不再覆盖）。
  **能力开关与命令沙箱是两套门**：`browser_*` / `computer_*` / `device_*` 由
  助手进程直接驱动本机浏览器/桌面/手机，**不经命令沙箱**、也不需要
  `sandbox_permissions` 升级；反过来，沙箱内的命令启动不了浏览器（受限令牌下
  access denied），要看网页只能用 `browser_*`（前提是该会话已开启能力）。这条
  已写进环境快照的「能力」段（`agent.go` `envPrompt`：开关状态逐项列出，关着时
  说明后果与正确出路）。
  **能力门控有自己的升级参数**（`perm.go` `requireCapability`）：能力关着被拦后，
  模型同轮带 `request_capability=true` + `justification` 重试同一工具即弹审批卡——
  「允许」=本次放行（不点亮开关），「始终允许」=点亮对应能力开关（常设授权）。
  与 `sandbox_permissions` 同一套拒绝驱动语义；每个门控工具的描述都带权限提醒、
  schema 都带这对参数（`tools.go` `capRequestProps`）。
  点亮后该类动作不再逐次审批。
- **一次性授权凭证**：每次写文件/执行命令先签发 5 分钟时效的一次性凭证，
  执行器核销后才动手；过期/重用即拒绝（fail closed）。
- **受保护路径**：助手自身配置与数据目录（API Key、全部会话记录）任何模式
  禁止访问——确定性的路径边界，非命令内容猜测。
- `web_fetch` 在非完全访问模式拒绝内网/本机地址（防 SSRF）。
- **对抗验证**：sbx_adversarial_test.go（穿越/Junction/删外部/注册表/子进程
  继承/网络/硬链接双顺序九项，缺口项均为硬失败断言）+ sbx_delete_matrix_test.go
  （16 格删除矩阵）+ sbx_confinement_test.go。已知边界如实记录：读取、网络、
  进程可见性不限（官方同款语义）；多链接（硬链接）文件在沙箱内只读（共享
  SD 还原后外部别名与工作区内名都不可写，pnpm 等写普通单链接文件不受影响）。

## SSH 远程工作区（提前实现 DSH 的远程环境能力）

DSH 官方的远程工作区只在 mac/Linux 宿主上可用；本产品用纯 Go SSH 协议栈
（`remote.go`）实现了同等模型，**Windows 宿主同样可用**——把一套运行环境搬到
远程：

- 会话挂上远程工作区后：**文件工具走 SFTP、命令经 SSH 远程 shell 在远程目录执行**，
  权限引擎照常把关（工作区边界 = 远程目录，POSIX 路径规则）。
- 入口：加号菜单「SSH 远程工作区」→ 填主机/端口/用户/认证（私钥文件或密码）/
  远程目录 → 「测试连接」（拨号 + 命令通道 + SFTP + 目录检查，回显主机指纹）。
- 主机密钥 OpenSSH 式 TOFU：首连记录指纹到 `data/ssh_known_hosts`，之后变更即
  拒连（防中间人）；会话内绑定指纹，换主机需重新测试保存。
- 认证支持无口令私钥文件与密码；连接按会话缓存 + keepalive，断线自动重拨。
- 数字助理派发的子任务自动继承主对话的远程工作区。

## 工作区模型

工作区即文件夹（或 SSH 远程目录）。每个会话挂载在一个工作区下（未单独设置时
挂默认工作区），**会话能写哪个目录由它挂载的工作区决定**：文件写入与 shell
执行都被限制在该目录内（权限引擎强制，见上节）。侧边栏按工作区分组展示顶层
会话；子会话/辅助对话不占侧栏，从对话内右栏进入。

## 界面（对齐 DSH 风格）

- **首页（新建对话）**：居中问候区 + 输入卡片；输入框上方两枚胶囊——**工作模式**
  在前（左，标准办公 / 编程 / 创建技能，决定该会话的系统提示词与交付倾向，
  `CHAT_MODES`；选「创建技能」时联动把权限默认提到完全访问——该模式要看懂
  用户演示并亲自验证技能步骤，见 `applyDraftMode`；切回别的模式且权限是联动
  自动设的，则还原为工作区内读写），
  **工作目录**在后（右缘对齐输入卡；候选 = 默认目录 + 历史用过的目录，也支持输入
  任意绝对路径；目录只在新建时选）。手机端（窄屏 <500px）没有工作目录胶囊、
  加号菜单也没有 SSH 远程挂载项——工作区由引擎锚定在应用私有目录
  （`JX_WORKSPACE_DEFAULT`），目录/远程挂载到电脑端操作（CSS 媒体查询 +
  JS `isNarrow` 守卫双保险，见 style.css 窄屏节）。
  **左上角「新工作任务」点一下就出会话**（不必先在首页写第一条消息）：`newTaskSession`
  （`web/app-chat.js`）立刻用首页草稿（模式/目录/远程/权限/模型/计划·目标）建会话并上屏——
  本地先上乐观壳顶住点击瞬间，后端返回后由 `openSession` 整套接管；首页草稿里已写下的字
  与引用随会话迁移（迁移后清首页草稿，免得下次回首页"复活"已发送内容），建不出来退回首页
  且不丢字。**空会话回收**：`newTaskBlank` 只记"本次运行里由这个入口创建、还没发出任何
  消息"的会话 id——再点一次复用同一条（不堆一串 `(未命名)`），切走/回首页即静默删除；
  刷新或重启后遗留的空会话一律不动。空标题在顶栏显示「新工作任务」（侧栏仍 `(未命名)`）。
  首页本身保留：启动、删除会话后、待办组件的兜底返回（`goHomeNewTask`）仍走它。
- **输入区工具栏**：左下角加号菜单（文件上传 / 计划模式 / 目标模式 /
  导出日志（运行时间线 Markdown）/ 导出原始数据（最近发给模型的请求）/
  SSH 远程工作区）与权限芯片；
  发送按钮左侧是**模型芯片**（既是显示也是选择，按提供方分组）。
- **手机端设置页的两级返回**（`app-settings.js` 的「设置导航」节）：窄屏下
  设置＝一级菜单＋二级分区页，返回头/手势逐级退（二级→一级→关设置）。历史
  栈契约：返回键**UI 直切优先**（`setNav.level` 改完 `renderNav`，不依赖
  popstate），`history` 只做栈同步且**只退实际压入的层数**（`setHistPushed`
  与 `setHistDepth` 分开记账），`pushState` 一律走 `setHistPush` 的
  try/catch——宿主环境（远程外壳一类）不支持历史 API 时降级为纯 UI 导航，
  绝不让 `back()`/`go()` 越过自己压的栈把用户带出应用。左栏返回守卫的
  `backGuardArm` 同样容错（压哨兵失败＝守卫不生效，不向上抛）。
- **附件区**（输入框正上方一条轨道，主对话与右栏子对话同款，渲染与采集见
  `app-chat.js` 的 `renderAttachments` / `stageFiles` / `flushAttachments`）：
  选择、粘贴（截图）、拖拽进来的文件先在这里**暂存**——图片给缩略图，其余给
  「图标 + 文件名 + 大小 + ✕」卡片，横向排卡、滚轮横向平移（对齐 DSH
  `ui-attachment` 的 rail/卡片）。**点发送才写进会话工作区**（远程经 SFTP）并
  注入那条【文件上传】记录消息（通知文案与生成逻辑没变，只是时机跟到了发送）。
  形态是**输入卡内部第一层**（挂在 `.composer-card` 里、引用胶囊之上）：有附件时
  白卡自身向上长高「展开」，与下方输入区同底色同圆角连为一体、底部一条细分隔线
  ——不做浮在卡外的「第二层条带」（与卡断开、间距里漏页面底色，难看，已废弃）。
  几条边界：没附件时整块 `display:none`（CSS 层另有 `.attach-rail:empty` 兜底）——
  不占高度、不留白边、也没有空状态提示；
  落盘失败即中止本轮发送（失败卡片标红留在轨道上、输入内容也留着，不会出现
  「文件没带进去、消息却发出去了」）；只挂附件不写字不给发（不替用户编消息）；
  换会话/新建对话/刷新即丢（暂存只在内存里）。载荷沿用原有两条路：桌面壳的本地
  会话走原生选择框采本机真实路径（该路径可作 `deliver_file` 交付目标），浏览器 /
  手机 / 远程 / 粘贴 / 拖拽走 multipart（单文件 32MB 上限、一次最多 20 个）。
- **桌面壳右键菜单**（`web/app-ctxmenu.js`，仅 `desktop-shell` 形态注册；网页/
  手机模式保留浏览器原生右键）：go-webview2 库在 `Debug=false` 时把 WebView2
  默认右键菜单与 DevTools 一并禁了，桌面壳右键原本什么都不弹——这里补自定义
  菜单，弹层复用左栏 `openCtxMenu`（条目扩展了 `{sep}` 分隔线与 `{disabled}`
  置灰）。按右键目标组装：输入控件给 撤销/重做/剪切/复制/粘贴/全选（编辑动作
  对焦后走 `document.execCommand`，保留原生撤销栈）；对话区选区给
  复制/引用到输入框/问AI/朗读；消息气泡给 复制全文/朗读（Web Speech API，
  代码块丢弃、markdown 剥壳，中文声库优先，单飞、朗读中变「停止朗读」）；
  网页链接给 打开/在系统浏览器中打开/复制链接；文件路径链接与
  交付卡行给 预览/在系统中打开/在文件夹中定位/复制路径；工具调用截图给 在右栏
  查看/复制图片。空白处不弹。**粘贴的剪贴板读取有平台分叉**：桌面壳里
  `navigator.clipboard.readText` 会永久挂起（WebView2 的剪贴板读权限停在
  prompt 且没有授权 UI，go-webview2 的 SetPermission 对它不生效——真机诊断
  见 `gui-test/ctxmenu-clipboard-diag.mjs`），所以桌面壳直接读本机端点
  `GET /api/clipboard`（`clipboard_windows.go` 读系统剪贴板：文本 +
  截图常带的 PNG 注册格式，文本优先、PNG 入附件与 Ctrl+V 同口径；
  剪贴板属敏感数据，loopbackOnly 仅回环可达）；网页模式 readText 行为正常
  走标准 API。配套端点：`POST /api/open-url`（仅 http/https）、
  `POST /api/sessions/{id}/open-path|reveal-path`（`insideWorkspace` 限
  工作区内，远程会话拒绝），用户主动操作、不经模型权限门控（同交付卡片口径）。
- **权限面板**（点权限芯片弹出，两列）：左列权限单选（只读 / 工作区内读写 /
  完全访问），右列能力多选（允许工作区外执行命令 / 操作浏览器 / 操作电脑）。
  切到完全访问时三项能力联动勾选并置灰（不可单独关闭）。新建会话默认权限是
  工作区内读写，因此**新建对话默认就勾着「操作浏览器」**（显式取消后不被覆盖；
  从完全访问切回工作区内读写时会自动补回）。
- **待决定的浮窗**（审批 / 助手提问 / 计划审批，渲染见 `app-chat.js` 的
  「待用户决定的浮窗」一节）：**按「这张卡属于谁」分两处落**，各司其职：
  1) **本对话的卡 → 输入框上方**（主对话 `#ask-bar` / 右栏子对话 `#sub-ask-bar`）：
     位置在附件区之上、输入卡之上，宽度与输入卡一致（吃 `--content-w`），就地处理
     最顺手；手机端只要还有待办，输入区滚动收起就不生效（否则卡跟着滑出屏幕）。
  2) **别的对话的卡 → 右下角角标堆**（`#ask-dock`）：落在**对话区右侧那段留白**里
     （锚在 `#main` 右缘内 12px，宽度取 `min(300, 留白−12)`），绝不压正文列、也不跟
     右栏抢空间。卡片顶部多一行「来自「某对话」」——点一下切到那个对话，不点也能就地
     审批/回答。生灭由留白决定（`dockMetrics`，与进度面板同一套 auto 模型，右栏开合 /
     拖把手 / 窗口缩放都会重算，防抖 150ms 等过渡落定）：
     · 留白 ≥240px 且纵向可用高 ≥220px → 自动展开（高度上限 560px，再多在堆内滚动）；
     · 内容本身塞不下当前可用高 → 也自动缩成胶囊（用户手动点开过就尊重他，展开态滚动）；
     · 留白不够展开 → 只留胶囊 `#ask-dock-pill`（只报条数，缩着时来了新的会脉冲一下）；
     · 连胶囊都放不下（右栏占光、留白 <96px）→ 整块不显示，靠左侧栏橙点标出谁在等。
     顶部「—」按钮可显式把整块缩成胶囊；窄屏退回窗口右下角、按视口宽度收窄（左右栏
     整屏浮层开着时不铺开）。
  另外两条（两处共用）：
  · **重绘不动用户的字**：轮询/事件重画按「卡片签名」比对——签名没变一个节点
    都不碰，只有内容真的变了才重建那一张卡，自由输入框的文字按请求 id 存草稿
    并回填（`askDraftGet/Set`）。十几秒一次的兜底轮询不再清空已输入的内容
    （旧实现每次把容器 `innerHTML` 清空重画，用户打的字被冲掉）。
  · **计划审批不在这里铺长文，整张卡可点**：`kind=plan` 的审批卡只给标题 + 首段，
    点卡片任何位置（输入框与三个按钮之外）→ 打开右栏「计划」视图看全文（Markdown 渲染）；
    三个动作是「驳回（带反馈）/ 忽略计划 / 批准并开始执行」。
    **时间限制不一样**：普通提问与「进入计划模式」确认等 5 分钟（超时按「用户未回应」
    返回），**计划审批没有时限**——提交那一刻就落库，用户不处置它就一直在：哪怕关掉软件、
    重启进程之后再打开，那张卡仍挂在对话里等审批（本轮被停止/中断也照样留着）。
    「忽略计划」＝搁置：本轮对话就此停下（与点「停止」同一效果），计划本身不撤销，
    之后随时能再批准或驳回。
  · **对话流里另有一条常显的计划卡**（条目类型 `plancard`，跟着提交它的那一轮、不折进
    过程段）：标题 + 首段 + 一行「点击这张卡看计划全文」，整张卡可点 → 打开右栏并**定位到
    这一份**（按提交序号）。审批卡批了 / 驳了 / 离场之后，它就是「再点开看这份计划」的
    常住入口——所以每一份提交过的计划在对话里都留得下痕迹。
  · **计划审批的正文与来历**：`GET /api/sessions/{id}/plans` 读回历史里的每一次
    `exit_plan_mode` 提交（见 plans.go）；待审批的那一份另有落库副本（`store.go` 的
    `PendingPlan`，存 meta 表 `sessdata:pendingplan:<会话id>`），所以重启后
    `/api/questions` 仍能把它挂回来（会话被删、或已退出计划模式时自动作废清理）。
  兜底轮询只在事件流断连时才真的去拉（`app.js`）。事件经全局事件流实时推送；
  远程工作区会话顶栏显示「远程 user@host:dir」徽标。
- 文件上传：先暂存进输入框上方的**附件区**，点发送时才写进会话工作区（远程会话
  经 SFTP）并注入记录消息（见上一条）。**两种导出**
  统一向上下文引擎申请（引擎是记录者：它收到什么都落日志，发给模型的请求
  由它环形缓存），都取自 messages 表原始历史（append-only，与发给模型的
  压缩视图无关）：
  **导出日志**＝运行时间线——从会话创建起按时间排列：上下文组装（系统
  提示词 / 环境快照 / 工具定义全文，后续轮次自动对比并标出系统提示词与
  工具集的变更及差异行）、环境快照更新（行级 diff）、用户消息、每次模型
  调用的发送规模 / **前缀指纹**（系统提示词与环境快照的短 hash，指纹变化
  即缓存必不命中）/ 返回 / 用量 / **缓存命中率** / 耗时、模型逐步输出与
  工具结果、以及**不发给模型但影响判断的事件**（运行中报错——含网关错误
  响应摘要、用户停止、上下文超限重试、规则压缩与摘要生成、上下文事件——
  权限/模型/思考档位/模式开关/工作区/远程挂载变更，它们正是缓存命中率
  突降的常见原因；文首附调用一览表便于对照）；**导出原始数据**＝引擎缓存
  的**最近三轮**真正发给模型的完整请求体（发送前捕获、与线上逐字节同源，
  不含 API Key）+ 相邻两两对比（消息条数 / token 估算变化、工具目录与
  sys/env 指纹是否变化、**首条变化消息的位置**——它之前的前缀逐字相同、
  理论上仍命中缓存），用于核对「到底给模型发了什么、缓存为何变化」。
  控制台输出按 UTF-8/GB18030 解码入库，导出恒为合法 UTF-8。
- **对话区的提示词材料按「变化」展示**：首轮在真实拼装顺序上完整交代——
  「系统提示词已拼装」（`messages[0]`）与「工具定义」（请求体**顶层字段**，与
  `messages` 平级、不属于消息序列，由服务端按自身模板排进提示）在用户消息**上方**，
  「运行环境」（追加式快照，作为 user 消息进消息序列：有变化才追加、追加在当轮
  用户消息之后，首轮它就是那次请求的最后一条消息）在用户消息下方，其后才是模型输出；
  展示顺序即请求真实顺序：系统提示词 → 工具定义 → 消息序列 → 运行环境（尾部）。
  之后每轮内容不变就不再出现任何提示词卡片——只有系统提示词
  / 工具定义变更、「运行环境更新」（含行级 diff，如能力开关、权限模式、
  工作区、技能清单等**真实变化**）才在发生位置补一条折叠行。每次请求都会
  完整重发系统提示词（协议如此），「不变」指逐字节相同，前缀缓存据此命中。
- **环境快照首行是「用户最近发消息时间」，不是挂钟**（`agent.go` 的 `envPrompt`
  → `Store.LastUserMessageAt`）：取会话里最新一条**真人用户消息**（`kind=chat` +
  `role=user`）的落库时刻，用户在哪一刻说的话就记哪一刻——模型自己执行期间
  （工具调用之间、目标模式续跑、系统注入回合）不会随时间流逝变化，所以环境快照
  不会凭空多发「环境更新」；用户真发了新消息，这一行才随新快照更新一次。会话里
  还没有用户消息（纯派发/待办驱动的会话）时不出这一行。**别把这里改回 `time.Now()`**：
  那会让「每分钟跨过分钟边界」变成一次环境变化，重发又变成时间驱动。
- **前后端消息类型契约**：数据库怎么存（`messages.kind`）、SSE 怎么发、前端
  用什么样式展示，统一定义在**《消息类型规范.md》**（对话内容 / 上下文注入
  prompt·env·summary / 系统注入 report / 审计工件 request·audit，report 按
  origin 分子类）；前端按 kind 分发渲染（`ROW_ITEM_BUILDERS` 注册表），
  改展示只动前端、加类型只动后端；**改类型必先改规范再动代码，禁止按正文
  前缀或 JSON 内容猜类型**（旧库兼容除外，清单见规范 §5）。
- **右侧边栏**（`#rpanel`，对齐数字助理2 ui-layout 的 auto 宽度模型）：弹出即
  占用对话区以外的剩余宽度——不动左侧栏、不动对话区宽度；拖左缘同时调宽对话区
  与右栏，落盘的只有对话区宽度（`jx.contentWidth`，与对话区两侧拖把手共享），
  右栏宽度从不记忆，关闭再打开重新占剩余宽，等效记住拖拽结果；窗口缩放 / 左侧栏
  折叠时右栏跟随重算，窄视口停在最小宽 300px 由对话列让步兜底。面板为通用外壳
  （`rpRegister` 视图注册 + 标签头），视图分两型：`native`（容器是 index.html
  静态 DOM，行为代码挂在本仓 js，如子对话与浏览器）与 `iframe`（独立面板页
  `web/panel/<name>`，惰性创建 iframe、首开加载此后常驻不重载）——面板页自成
  一体（自己的 HTML/JS/CSS + 直连同源 API），加新面板只需建目录 + 一行
  `rpRegister`。「浏览器」视图是 native 型：工具栏/状态条为本界面 DOM，网页
  区域是占位 div，桌面模式下原生 WebView2 控件按上报矩形精确覆盖（见下）。
- **两侧开合动画**：左右侧栏共用同一个 `slidePane()`（宽度过渡，起始宽先于
  `.anim` 提交保证起点正确；`transitionend` + 460ms 兜底双保险定版；拖拽调宽
  即时生效不经动画；`prefers-reduced-motion` 下自动禁用）；左侧栏宽度可拖
  （右缘把手，272~560，记忆 `jx.sbw` 经 `--sb-w` 生效，双击恢复默认；窄屏浮层
  不可拖），内容装在与栏同宽的 `.sb-inner` 里，收起时内容不回流、像抽屉一样
  滑出。时间线分组下每条对话行首带一枚工作目录小胶囊（目录名开头两三个字，
  塞不下从右缘直接裁断、不用省略号），悬停可见全名。
- **左侧栏的状态标记**（`app-left.js` 的 `sessionItem`）：一行 = 左侧圆点 + 标题 +
  时间位。圆点只放「要你做点什么」的三态（橙=待审批 / 绿=完成未查看 / 红=标为未读，
  打开即清）；**运行态占用最右的时间位**——`⟳ 运行中`（后台还在跑，切走不等于停掉）、
  `⚠ 已中断`（最近一轮没正常收口：达到步数上限 / 执行失败 / 服务重启，悬停给原因与
  下一步），两个都没有才回落到相对时间；同槽即天然互斥，渲染优先级
  运行中 > 已中断 > 时间。异常标记**打开会话不清**（「最近一轮没跑完」这个事实
  没变），重跑一轮、重试成功即消失，用户主动停止不算异常；值存 meta 表
  （`laststop:<会话id>` = step / error / restart），重启后仍在（`Store.SetLastStop`）。
  带标记的行与「运行中」完全同款：两个标记的字符、颜色类与悬停文案收在
  `app-left.js` 的 `STATUS_CELLS` + `sessionStatusCell()` 一处，样式只给 `.s-time`
  叠一个颜色类（**别给某一个状态单独写位置规则**，写歪两个状态就对不齐）；悬停时
  与时间文本一样让位给右侧行内按钮（`.row-acts` 钉在 `right:6px`）。
- **子对话视图**（右栏首个视图）：顶栏最右「收起/展开右侧栏」按钮开关右栏
  整体（收起后再展开回到上次看的视图；页签条切换子对话/浏览器等视图）；视图顶部下拉
  菜单选择某个子对话或**新建**（人工发起的分支克隆；助理派发的工作任务是
  顶层会话，不在此列）。子对话
  **只在右栏显示、绝不切换主对话区**：右栏内嵌完整的对话区+输入框，与主对话
  共用同一套渲染/发送代码（`renderMessages` / `send` / `handleEvent` 全部按
  `view` 参数化：`mainView` / `subView` 只差 DOM 挂点与状态），后台自动汇报
  的流式输出也实时进右栏。没选子对话时点发送会弹出下拉提示。
  **样式与底部控件也完全同款**：消息列与输入区（`.msg-col` / `.composer-wrap` /
  `.composer-card` / `.composer-input` / `.cbtn` / `.chip-btn` / `.ctx-ring` /
  `.mode-flags`）都是 class 共用规则，两侧各挂一套（右栏那套 id 加 `sub-` 前缀），
  `viewEl(view, id)` 按视图取元素；权限芯片、模型芯片、上下文圆环、加号菜单
  （上传/计划/目标/导出/远程）、语音与发送全部作用于各自视图的会话，弹层是单例
  但记住是谁打开的（`popView`）。插件经 `JX.addComposerControl` 注入的控件在
  右栏镜像一份（点击转发给原件、class 同步）。
- **计划视图**（右栏，`rpRegister('plan')`，标签顺序 `order`：子对话 1 / 计划 2 /
  浏览器 3）：把 `exit_plan_mode` 提交过的计划当文档看——**Markdown 渲染**（表格/
  清单/代码块都正常展开），顶部可选多份计划（下拉里标「待审批」）、**导出**、复制
  全文、重新读取，底部在待审批时钉着「批准并开始执行 / 反馈意见后驳回 / 忽略计划」
  （忽略＝搁置，计划仍留着）。数据两路
  合一：历史走 `GET /api/sessions/{id}/plans`（后端从会话日志里读回那一次调用参数，
  与对话同寿命——刷新、切走、重启进程后都还在），待审批的走 `/api/questions`
  里 `kind=plan` 的提问（按正文前缀与历史配对，配不上先用提问自带正文兜底）。
  **导出**走 `POST /api/sessions/{id}/plans/export`：把当前这份写成会话工作区里的
  `计划-YYYYMMDD-HHMM-<标题>.md`（标题清掉文件名非法字符、限 40 字；本地直写 /
  远程经 SFTP，与「文件上传」同一条文件环境），**同名不覆盖**（顺延 `-2`、`-3`），
  视图顶部随即显示落盘路径 + 「打开所在文件夹」（走 `POST /api/open-folder`）。
  **选中哪一份**：优先「一次性定位」——刚点的那张计划卡（按提交序号）、刚到达的待审批
  计划（按提问 id）；其次是用户在下拉里显式选过的那份（它消失、或又来了新计划就作废）；
  再其次是**最新的那份待审批**（新计划提交后右栏默认翻到新的，不再停在上一版——这正是
  「提交了 V2、右栏还显示 V1」的修法）；最后才退到上次显示的那份 / 最新一份。
  **计划提问到达时自动打开**这个视图（用户手动关掉后该提问不再自动弹，点那张计划卡
  ——整卡可点——随时能再打开）；提问被批准/驳回离场前先留一份正文副本，右栏不会
  突然空掉。
- **上下文用量圆环**（输入区，模型选择左侧）：小圆环以弧长动画展示当前会话
  下一次请求占上下文窗口的比例，中心显示百分比（≥60% 变琥珀、≥85% 变红）；
  鼠标悬停浮出详情卡：总量/窗口、分类占比（消息 / 系统工具 / 系统提示词 /
  摘要 / 环境快照 / 其他，堆叠进度条）与**缓存命中率**。纯展示无点击。数据来自
  `GET /api/sessions/{id}/context`：按 buildContext 同一组装规则估算各分类
  token；命中率来自每次模型调用流尾 `usage`（`stream_options.include_usage`，
  含 DeepSeek 的 `prompt_cache_hit_tokens`），按会话累计落库（sessions 表
  `usage_prompt/usage_cache_hit/usage_calls` 三列，迁移 0005）。
- **对话区选中文字浮窗**：在主对话区或右栏子对话区选中文字后，浮出深色胶囊
  工具条（对齐主流 AI 编程助手的选区交互）：**复制**、**引用到下方输入框**
  （插入主输入框光标处）、**在右侧辅助问答**——自动打开右栏子对话视图并
  找/建子对话，把选文填入子对话输入框由用户发出，全程不影响主对话区；
  点浮窗不冲掉选区，选区消失 / 滚动 / Escape 即收起。
- **会话进度面板**（`progress_write` 的渲染）是对话区右上角的一枚可折叠浮窗：
  折叠＝一枚「进度」胶囊，展开＝从同一个右上角往左下长出的卡片（右缘与顶边不动，
  原地展开不跳位）。宽度按对话区右侧留白算：放得下就塞进留白、不遮正文，放不下
  就用一张舒适宽度的卡；默认只在留白够的时候自动展开，窄屏/中等窗口一律折叠成
  胶囊，用户点过之后按用户的选择来（刷新复位）。清单超过半屏高卡内滚动。它永远
  是浮层（不占对话区高度、切换不触发重排），层级压在消息之上、输入区之下——报错
  条必须始终看得见。宽度算在 JS（pgMetrics），CSS 只管摆位与两种形态。

## 子对话：上下文继承与一级限制

子对话只有一级（无论 AI 派发还是人工新建，都不能再派发/再建子对话）；
数字助理派发的工作任务是顶层会话，不占一级名额。

- **人工新建＝分支克隆**：右栏「＋ 新建」建的子对话照抄父会话的模式、工作区、
  远程配置、模型、权限与能力开关（因此**系统提示词、工具目录、历史全部一致**），
  并把父会话消息逐行复制过去（含摘要与压缩标记，`covers_up_to` 按新 seq 重映射）
  ——子会话下一次请求的前缀与父会话逐条相同，命中模型 KV 前缀缓存，成本很低。
  计划/目标模式刻意重置，避免子对话替父会话自动续跑。老 `aux` 只读答疑会话原样保留。
- **继承段折叠显示**：复制内容前有一条 `kind='inherit'` 标记行（组装上下文时跳过），
  前端据此把整段历史折成一条「来自主对话 · N 条（含系统提示词）」折叠条，展开即
  完整对话流；父对话的系统提示词快照行也在其中。
- **AI 派发按专家预设**：`dispatch_sub` 的模式选择只有一条路径——`expert=名称`。
  内置两条系统专家（常开、不可修改）：**标准子对话**（不传 expert 时的默认，
  只收到一段自足的 task）、**继承上下文子对话**（完整继承本会话聊天记录，
  子会话保留自己的 task 系统提示词与工具，task 只用写「接下来要做什么」）。
  独立的一次性任务按默认派、写清描述更省 token（工具说明里写明了这条取舍）。
  `dispatch_task`（工作任务）**永远不带上下文**：像用户发消息一样只给一段
  自足描述，与派发方的聊天记录完全隔离。
- **子对话专家（专家团预设）**：用户可在设置页「子对话专家」预制定义专家——
  名称、描述、**追加提示词**（拼在子对话模式基础系统提示词之后）、默认模型
  （空 = 继承主对话）、是否继承上下文、启用开关。**名称即调用参数，与内置
  专家共用同一条 `expert=名称` 路径**：设置页展示的名称直接来自后端清单
  （`/api/experts` 的 `builtin` / `user`），模型侧的可用名单由常驻的
  `list_experts` 工具按需查询（**名单不嵌进工具描述**——工具目录恒定，
  专家增删不断前缀缓存；对齐 read_skill 的技能模式），所见名称 = 可填
  参数，100% 一致，不存在第二条按参数调用的路径（历史上的
  `inherit_context` 参数已删除，旧调用里传了也会被忽略、继承与否完全由预设
  决定）。`model` 参数仍可覆盖预设模型。**预设模型不可用自动回退**：专家会话
  调用失败且错误属于「该模型/提供方用不了」（余额或额度耗尽、鉴权失败、
  模型不存在、提供方连不上；纯限流不算）时，自动切回全局默认模型（= 主对话
  的默认模型）续跑并落库，工具区提示一条「已切回默认模型」；普通会话手选的
  模型不做这种静默降级——显式选择被拒时应如实报错。会话上只记专家名（`sessions.expert`
  列），提示词构建时按名解析拼接——停用专家只是不能再派（存量子对话的追加段
  保留），删除才会让这段消失。自定义专家不得与内置专家重名；存
  `config.json` 的 `experts` 键（`GET/POST /api/experts`、
  `POST /api/experts/delete`），老配置首次升级自动补种演示用的
  「对抗性审查编程助手」（继承上下文、模型随主对话），删掉后不再复活。
- **一级限制**：`POST /api/sessions` 校验父会话自身无 parent（400 拒绝），
  `dispatch_task` / `dispatch_sub` / `send_to_session` 在带 `ParentID` 的会话里
  调用被硬拦
  （人工把总控克隆成子对话后，那个子会话手里也有编排工具），环境快照里也会告知模型。
  父会话正在执行时只复制到最后一条完整答复，悬空工具调用不带进子对话。

## 会话的四个层级

| 模式 | 说明 | 工具 |
|---|---|---|
| `task` | 任务会话，实际干活 | 读/写/编辑/glob/grep/shell（含后台作业 job_*）/web_fetch + ask_user_question / exit_plan_mode / progress_write + **dispatch_sub（派子对话）** + 历史查询 |
| `assistant` | 数字助理总控（左上角唯一入口，不占侧栏） | 编排：list_sessions / read_session_summary / **dispatch_task（派顶层工作任务）** / **dispatch_sub（派子对话）** / send_to_session + 历史查询 |
| `aux` | 辅助对话：早期的只读答疑小会话（右栏可见；新建子对话自 0007 起改为分支克隆，见下节） | 只读文件 + web_fetch + 历史查询 |
| `skill-create` | 创建技能：用户演示操作 → 沉淀成给后续 AI 照做的 SKILL.md 教程；**默认完全访问**（演示记录与亲自验证都刚需电脑/浏览器）；提示词核心是「顿悟操作逻辑 + 写大致步骤不写死细节」（推荐/避免方式、快捷键、抗界面变化，见 agent.go `skillPrompt`） | task 全量工具 + **demo_record_start/stop/status/transcribe（演示记录器，仅 Windows 真实现）+ register_skill（注册/覆盖技能）** |

（各模式共有的常驻工具不再逐行列出：`read_skill` 对**所有模式**注册，
技能清单经环境快照对**所有模式**注入——见下文「技能」节与启动自检
`assertSkillVisibility`。）

**派发关系**：数字助理有两个派发工具——`dispatch_task` 派出**顶层工作任务**
（进左侧栏，`dispatched_by` 记派发它的助理、作为回报链），**永远不带上下文**，
像用户发消息一样只给一段自足描述；`dispatch_sub` 派出**子对话**（挂在当前会话
下、右栏抽屉可见，完成后回报并触发父会话继续），按专家预设选择是否继承上下文
（`expert=名称`，默认标准子对话不带）。工作任务同样只有 `dispatch_sub`。右栏
子对话因此可能来自 AI 派发或人工新建（人工的挂 `parent_id`、`origin=human`，
统一带完整上下文；一级限制对所有子对话生效）。此外总控还有 `list_sessions`
（找任务）、`read_session_summary`（看最终答复等摘要）、`read_session_messages` /
`search_sessions`（翻查内容）与 `send_to_session`（给已有任务发消息）。

## 执行的生死与对话中途发消息

**执行体独立于发起它的 HTTP 请求。** `POST /chat` 只是「发起执行 + 接上这条流」，
后端 `executeRun` 在独立 goroutine 里持有自己的 `ctx`（`runtime.go`）：

- 浏览器刷新、关页、网络抖动只断开那条订阅流，**执行照旧跑完并落库**；
  重新打开这个会话时前端会自动 `GET /stream` 接回去，接着看实时输出。
- 只有 `POST /stop`（停止按钮）或进程退出才会终止执行；`sessions.status` 只由
  内存注册表与 `UpdateStatus` 维护（`SaveMeta` 刻意不写这一列，否则会被陈旧对象冲掉）。
- **中断不破坏历史（对齐 DSH 的 abort 语义）**：任何中断（人工停止/网络异常/
  进程崩溃）都会给未执行/被中止的工具调用补上合成结尾（「（会话被中断，…）」，
  `origin=interrupt`）；已在执行的调用不被 orphan（等它结算），未启动的不再启动，
  历史里绝不留下「有调用、无结果」的悬空配对（发送视图 `repairToolPairing` 还会
  兜底重排/修复；重试与新消息开轮前先扫尾补齐残留）。模型下一轮能看到这些结尾，
  自行决定要不要重做。

**执行中发消息（两种行为，设置页「对话中途发送行为」切换）：**

| 模式 | 生效时机 | 前端呈现 |
|---|---|---|
| 插话 `steer` | 主循环下一次调用模型前注入，作为一条真实用户消息进入上下文 | 立刻贴在对话最底下（虚线气泡 +「插话中」角标），落库后换成正常气泡 |
| 排队 `queue` | 本轮结束后作为**独立一轮**自动发送 | 进输入框上方的排队坞；可逐条删除，或点 ↑ 提前为插话 |

两边的队列都在服务端（`PendingMsg`），刷新页面/换窗口不丢、多窗口一致；本轮结束时
还没注入的插话自动转为排队，消息不会卡在队列里。会话空闲时发来排队消息会自动拉起一轮。
**排队项与插话气泡都可点 ✏️ 编辑**（撤回式，参考 ZCode）：先从服务端队列权威删除
（复用 `remove`），成功后才把原文回填输入框当草稿——消息要么在队列、要么在输入框，
不会有两份；输入框里已有草稿时拒绝（防覆盖），改完重发按当前模式重新排队/插话（排到
队尾）。编辑只对用户自己发的消息开放（kind 空/chat），数字助理派发注入的不参与。

**排队坞增强**（对齐 ZCode 的队列面板）：多条消息可**拖拽排序**——行首把手拖动，
落在上半行=插到该行之前、下半行=插到该行之后，锚点 id 上送服务端定序（`move`），
顺序的唯一权威在服务端（触屏浏览器不支持 HTML5 拖拽，排序走桌面端）；头部带
**暂停/继续**开关（会话级、内存态，重启即清）：暂停后本轮照常跑完，排队消息保留
不自动续发（空闲时新来的也只排队不拉起执行），恢复时空闲即接着把队列发完；执行
停止/出错后队列同样保留，排队坞横幅给「继续发送」入口（= `resume`，空闲即拉起）。
暂停状态随 pending 快照（`paused` 字段）下发，多窗口一致。

**不静默、也不往对话里塞话**：对话区只放真实对话（用户消息 + 助手输出 + 工具过程）。
正常完成与用户手动停止**不产生任何附加内容**；模型调用因网络失败（连接失败/超时/
流中断/网关 5xx/限流 429）先**自动重试**：总共尝试 5 次（间隔 1s/2s/3s/4s 递增，
输入区上方提示条显示进度，期间可随时点停止），仍不通才收手；请求级错误（鉴权失败/
余额不足/模型不存在）与上下文超限不自动重试。只有真实的失败（模型/网关/网络——余额不足、
限流、鉴权失败、连不上、超时，以及模型一条都没输出、只出思考、达到最大步数）才显示在
**输入框上方的报错条**里：原样呈现模型侧返回的原因，配「重试」按钮（`POST /retry`——
不重复落库用户消息，直接带着当前上下文再跑一轮）与关闭按钮。重试开始时会先补齐历史里
残留的悬空工具结尾（`repairDanglingToolCalls`），保证重发的历史配对完整。报错原因存在
服务端会话上，刷新/切回来还在，新一轮开始自动清掉。异常收尾同时写日志，服务端不留无声的停摆。

## 后台派发与自动回报

`dispatch_task` / `dispatch_sub` / `send_to_session` 立即返回，被派发的会话在后台
goroutine 并行执行；
进度经全局事件流 `GET /api/events` 实时推送（sub_start / sub_tool / sub_end /
report_ready / done / stopped），前端常驻订阅：父对话里的子任务卡片实时滚动工具
活动，完成后自动注入【派发回报】（助理主对话收到「向用户汇报」的指示、工作任务
收到「带着结果继续推进」的指示），父会话空闲则自动跑一轮消化回报——正看着父
会话时会自动接上这轮流、实时看输出；没在看则侧栏亮「完成未查看」绿点，打开即清。
服务重启时未完成的执行会补
【系统中断】记录，发「继续」即可接续（原始历史完整保留）。

## 上下文管理（核心特色）

原始历史 append-only 永久保存在 SQLite；压缩只影响「发给模型的内容」。

- **历史查询工具**（所有模式可用）：`search_sessions` 跨会话全文搜索、
  `read_session_messages` 分页读任何会话的未压缩原文。
- **规则压缩**（默认，零模型调用）：用户消息与模型正文永远保留原文；工具调用
  保留名字+参数（超长截断）；工具结果只留开头片段。每轮对话后增量检查：新增
  轮数达到间隔（默认 5）即把保留窗口（默认最近 5 轮）之外的轮次折叠落库。
- **模型摘要压缩**（设置可切）：上下文用量达阈值（80%/90% 可选）自动触发；
  也可在任意助手消息上点「压缩」手动触发。手动摘要独立叠加、永不重压；自动
  摘要按 DSH 式合并为单一检查点。上下文超限时强制压缩一次并重发（溢出兜底）。
- 组装管线：`[系统提示词] + [活跃摘要] + [边界后消息（规则折叠视图 + 最近 K 轮原文）]`。

## 多模型接入

设置页「模型服务」可配置多个 OpenAI 兼容提供方（预置 DeepSeek / 小米 MiMo /
智谱 GLM（按量与 Coding 套餐两个端点）/ 火山方舟·豆包（按量与 Coding 套餐）/
Kimi / 通义千问 / 百度千帆 / 腾讯混元 / MiniMax / 讯飞星火 / 阶跃星辰 / 硅基流动 /
魔搭 ModelScope / OpenAI / Ollama 本地 / 自定义），填 Key 后一键拉取模型清单；
预置模板带 Note 备注（计费方式、Key 获取入口），设置页卡片实时取模板展示——
同一厂商的套餐端点与按量端点地址不同，填错端点扣不到套餐额度，这是拆成
多条预置的原因；
会话顶栏可单独切换本会话模型；派发时可指定子会话模型（providerId/modelId）。

**API Key 的存储（各平台各用各家，原则：不回显、不被顺走）**：落盘前经
`protectSecret` 保护——Windows 用 DPAPI 加密（`dpapi:v1:` 前缀，绑定本机
账户）；安卓/Linux 没有系统级密钥服务，明文落盘但 config 文件一律 0600
（安卓上配置位于应用私有目录，系统层已隔离其他应用）。任何情况下接口层
都不回显明文：`GET /api/config` 只返回掩码（`maskKey`，前4+****+后4），
保存时空 Key = 保持不变。手机版后续接入各家的系统凭据服务（如
Android Keystore）时，只需替换 `dpapi.go` 的平台实现，文件格式不变。

## 技能（图片 / PDF / OCR / Office）与能力组件

在基础工具之上，能力分两类组织：

**技能（`技能/` 目录）**——文件类能力，标准技能包形态：
每个技能一个独立文件夹 = `SKILL.md`（标准 frontmatter 说明）+
`manifest.json`（Python 依赖清单）+ `scripts/`（执行脚本）。
自带三个技能：`pdf`（diagnose/text/table/image/embed 五模式，先诊断
文字层可信度再选通路）、`ocr`（离线 RapidOCR 识别图片文字）、
`office`（docx/xlsx/pptx 操作要点与结构校验），以及两个**不带 Python
的工具型技能**：`downloader`（内置 aria2c 官方二进制，Windows/安卓
下载落盘，缺 curl/wget 环境的兜底）、`apkinfo`（引擎子命令 `apkinfo`
读取 APK 包名/版本/签名指纹，安装前验包，纯 Go 实现）。自带技能常开、不可
删除；`技能/user/` 下可放用户技能，设置页「技能」面板可添加（指向
本机含 SKILL.md 的文件夹）、开关、删除。用户技能按 **清单先行、内容按需**
送达模型：环境快照里只列 名字（id）与一句话描述，要用哪个技能先调用
`read_skill`（常驻工具，**所有模式注册**）读它的完整 SKILL.md 说明——技能增减
不改变工具定义；清单变化经环境快照补在对话**最底部**，并附一行
「技能变更：新增 X／移除 Y」明说增删了哪个技能，上文历史快照一律不动。
**技能清单是环境快照的固定条目，所有模式一律注入**（与操作系统/权限模式
同级，不挂模式谓词——技能功能晚于 office/coding 落地，曾因手写门控漏掉
这两个模式；现由启动自检 `assertSkillVisibility` 逐模式看护，任何模式丢了
清单注入或 read_skill 都拒绝启动，模式全集以 `sessionModes` 为单一事实源）。

**创建技能模式（`skill-create`）**——技能的生产端：用户演示操作（或口述），
AI 观察学习后写成 SKILL.md 并注册进 `技能/user/`。闭环三件套：
①**演示记录器**（`recorder.go` 定义 + `recorder_windows.go` 实现，纯 syscall
无 cgo）：`demo_record_start` 起录——WH_MOUSE_LL/WH_KEYBOARD_LL 全局钩子
（回调只投递 channel，慢活全在 worker；带 LLMHF_INJECTED 标记的自家
computer_* 注入事件被过滤）在每次点击、回车/Tab、窗口切换时截屏（复用
BitBlt 截屏、逐字节去重、上限 400 张），连续打字聚合成 text 事件（Shift
上档与 Ctrl/Alt 组合键还原；中文输入法只记原始按键，上屏结果以截图为准），
MCI waveaudio 全程录 16kHz 单声道 audio.wav（无麦克风自动降级）；
`demo_record_stop` 收录成 `工作区/演示记录/record-<时间戳>/{NNN.png,
events.jsonl, audio.wav, INDEX.md}`——INDEX.md 是时间线对照表（事件↔截图↔
字幕段），模型 read INDEX.md 定位、read_image 看图。事件毫秒时间轴与录音
同源，SRT 字幕接上后天然对齐。仅 Windows 真实现，其他平台空桩报「不支持」
（环境快照提前公告，模型引导用户口述替代）。②**语音转写接口位预留**：
`recorder.go` 的 `activeSpeechTranscriber` 当前恒 nil（不引 whisper、不下载
模型），配置结构 `Config.SpeechAPI`（provider/endpoint/apiKey/language）已
留好——微软语音 API 调稳后实现一个注册进来，stop/`demo_record_transcribe`
即自动产出 讲解.srt，历史录音可随时补转（语音组件的朗读走 Edge 免密钥通道、
听写走浏览器 SpeechRecognition，都不读这份配置）；未接入时理解演示以截图+
事件+用户对话讲解为准。③**注册闭环**：`register_skill(path[, update])` 走
`addUserSkill` 装进 `技能/user/`（update=true 先删旧再装，支持迭代改进），
`read_skill` 读回自查。权限默认完全访问（`handleCreateSession` 分支 +
前端 `applyDraftMode` 联动），演示记录器工具门控 `capComputer`（fail closed）。

**运行环境自包含**：技能一律使用随软件分发的便携运行时
`技能/_runtime/python`，**不探测、不依赖用户机器上的系统 Python**——
面向非编程用户，安装包内置全部运行时与依赖，装完离线全功能。
开发目录缺 `_runtime` 时首次使用自动装配一次（python.org
embeddable，约 11MB + 清华镜像装 wheel），此后离线可用；装配进度只写
引擎日志（`skills.go` 的 `skillRuntimeNotice`），不进对话——曾经经 `tool_wait`
弹「技能运行时就绪（后续离线可用）」一类提示，被用户判为噪声、已下线；
打包脚本
会校验运行时依赖齐全，缺一不出包。`skillsDir` 配置可指定
技能目录位置（默认取可执行文件旁，不可写时落数据目录）。
**安卓走内置 Linux 运行时**（`skills.go` 的 `ensureTermuxSkillRuntime`）：
技能解释器 = Termux 快照里的 `python3`（执行带 `termuxEnv`，缺
`LD_LIBRARY_PATH` 动态链接器找不到 libpython）；office/pdf 依赖烘焙进
快照（清单与校验见 `android/runtime/MANIFEST.json`，PC 侧组装脚本
`android/runtime/pc-build/build-runtime-pc.sh`——WSL Ubuntu 里跑，不依赖
设备；设备路径备选 `make-runtime.ps1`，**制作机 ABI 即快照 ABI**，v1.0
误用 x86_64 模拟器产物导致 arm64 真机全 ENOEXEC，引擎装配期已加 ELF
架构防呆），快照缺的纯 Python 依赖现场经 pip 补装、装不上如实报错
（PyPI 无安卓轮子的 C 扩展依赖——如 onnxruntime/rapidocr——需加进快照
才能支持）；每次技能调用都核对依赖（探测结果缓存，常态零开销）。

**能力组件（与权限绑定的操作类能力）**：

- **图片查看**：`read_image` 工具 + 内容寻址附件库（`data/attachments/`）+
  `GET /attachments/` 端点。工具结果只带文本信封；前端在工具卡片直接
  渲染原图；视觉模型按预算内联 base64（单次最多 8 张），纯文本模型
  降级为信封占位不报错。
- **浏览器操作**（能力开关 `browser`）：`browser_open / content / click /
  type / scroll / viewport / evaluate / screenshot / close` 九件工具，工具层
  形态无关（`browser_ops.go` 的 browserTarget 接口），内嵌形态按运行形态取
  目标。三种形态可并存、各自独立进程与配置目录：**桌面版右栏原生面板**
  （`browser_open` 默认，mode=headless）——第二个 WebView2 控件真浏览器嵌入
  桌面壳窗口（`browser_panel_windows.go`），原生滚动/输入法/右键菜单/下载，
  AI 经控件官方 CDP 通道（`CallDevToolsProtocolMethod`）驱动同一控件，人机
  共操；**网页版后台无头**（chromedp 纯 Go CDP 优先驱动系统 Edge，异常缺失
  兜底 Chrome，仅供 AI 工具使用）；`mode=headed` 弹**独立可见窗口**（旧版
  行为）。其余工具作用于最近一次 `browser_open` 选定的实例，`browser_close`
  关当前实例并自动回退到仍在运行的另一个。点击/输入前在目标页面内注入 AI
  位置标记（原生面板 HTML 无法浮于原生子窗口之上）；文本输入统一走
  `Input.insertText`（中文/输入法正确）；桌面面板登录态落
  `data/webview-browser` 与日常浏览器隔离。注意：新版 WebView2（Chromium 136
  起）封禁 `--remote-debugging-port`，chromedp 直连嵌入控件不可靠，官方替代
  即控件级 CDP 调用接口。接口：`POST
  /api/browser/{panel-rect,navigate,viewport,open-view}`、`GET
  /api/browser/{state,view}`；全局事件 `browser_nav`（工具栏地址/标题）、
  `browser_resize`（AI 的视口宽度请求，前端让对话区让位调宽面板）。右栏收起
  只是隐藏面板（AI 继续操作）；工具条的「在系统浏览器打开」把当前网址跳到
  用户日常浏览器（跳转后不受 AI 控制）。
- **聊天链接点开（人机共看）**：对话正文里的 markdown 链接与裸 http(s) 网址
  渲染为可点链接——桌面版左键点击一律在右栏浏览器面板打开（Ctrl/中键走
  浏览器默认新标签）；网页版没有原生面板，一律新标签页打开。这是用户操作，
  不经模型权限门控；打开时内嵌形态自动成为工具作用目标（active）。**不注入
  任何对话回报**（用户口径：打开了什么是个人的事）——模型需要看画面时用户
  会主动说，模型用 `panel_screenshot` 截右栏（见下）。本地 html 交付文件转
  `file://`（同目录相对资源可用），SSH 远程会话经 `/api/browser/view` 流式
  渲染（相对资源不加载）。
- **右栏「文件」查看页**（`fileview.go`）：代码/文本/图片/PDF 类交付文件与
  聊天文件链接的展示位（iframe 自包含页：等宽+行号+截断保护，图片/PDF 经
  `/api/fileraw` 内嵌）——不依赖原生面板 overlay 与 file://，桌面/网页/远程
  三端一致。交付卡「打开」三路路由：这类文件进「文件」页；html/htm 与网址
  进右栏「浏览器」；Word/Excel/exe 等二进制走系统默认应用（设置·常规
  「交付文件打开方式」可整体改回系统应用；远程会话尽力进查看页）。
- **`panel_screenshot`**（无能力门控，仅桌面版）：对右侧栏截图（GDI 截屏
  像素，浏览器页/文件查看页/任何视图通用）。实现：注入主界面探针 JS 读
  `#rpanel` 矩形 POST `/api/shell/panel-view` 回传，Go 侧客户区原点+CSS×dpr
  换算屏幕坐标后 BitBlt 截图入附件；ExecuteScript 必须 UI 线程执行
  （runDesktop 里经 `w.Dispatch` 包了一层）。右栏收起时报「没有打开内容」。
- **电脑操作**（能力开关 `computer`）：`computer_screenshot / click /
  type / key / scroll / windows` 六件工具，win32 SendInput + GDI
  DIB Section 直调，无 cgo；点击坐标以截屏图片像素为准。

## 组件管理（原生外围扩展，热开关）

与插件（DSH 适配）相互独立的轻量扩展体系，全部为本栈原生态编写：

- **`组件/<名字>/`**：`manifest.json`（id/名称/版本/描述/资产）+ `client.js`
  （`client.css` 可选）——纯浏览器半边，没有宿主代码、没有转译。
- **发现与开关**：启动扫描 `组件/*/manifest.json`（损坏即跳过告警）；
  开关状态存 `data/components.json`（缺省启用）；接口 `GET/POST /api/components`、
  资产 `GET /api/components/{id}/{file}`（仅启用时提供）。资产挂 `/api/` 下且
  `web/cx.js` 统一用 fetch 拿文本注入（不插相对 src 标签）——远程中继模式下
  `/api/` 前缀被 remote-boot 的 fetch 劫持自动走隧道，组件在远程同样可用。
- **热开关（核心特性）**：内嵌运行时 `web/cx.js` 随页面加载，负责发现注入与
  运行期开关。组件脚本调用 `CX.register({id, init, dispose})` 注册：
  `init()` 建立界面与状态，`dispose()` 完整撤销（删节点、卸样式、清监听）。
  设置页切换开关时**前端即时加载/卸载，不需要刷新页面、不需要重启服务**；
  组件未提供 `dispose` 时退化为提示刷新。
- **组件设置区**：组件在 `init` 里调 `CX.registerSettings({id, title, mount})` 把自己的
  设置交给运行时；`CX.unregisterSettings(id)` 可撤。注册/注销/热开关都会派发
  `window` 事件 `cx:change`，设置页收到后重挂。设置样式与偏好由组件自理
  （组件 CSS + `localStorage`）。呈现形态两种（`app-settings.js` 渲染，只渲染当前
  激活的组件）：①缺省＝「内置组件」页清单下方的设置段；②注册时带
  `pane: {label, icon}` ＝**独立设置页**——设置一级菜单出现专属入口（插在
  「内置组件」之后）+ 专属分区页（`set-pane-component-<id>`，动态创建于
  `.set-panes`），导航状态机按 data-pane 寻址与静态页同一条路径；组件停用时
  导航项与页面一并撤掉（正停在该页时退回常规页）。`voice` 组件用的是独立页
  （设置 → 语音）。
- **宿主半边可以落在主程序里**：组件本身是纯浏览器半边，但它掌管的功能若需要
  后端（持久化、定时、接口、模型工具），就写在主程序对应文件里、按
  `Components.IsEnabled(<id>)` 自查开关——组件关掉时那部分功能一并停。
- 组件随源码入库（`组件/` 目录全部跟踪），**打包时必须随安装包分发**（见
  「桌面版与安装包」一节的打包清单）。曾作为最小组件模板的 `input-counter`
  （字数统计）已删除，新建组件照「manifest.json + client.js（client.css 可选）」
  的最小结构手写即可。界面强调色（点缀色）原为示例组件，现已并入主程序
  `web/app-settings.js`（常规 → 外观 → 点缀色）。
- 现成组件：`todo-scheduler`（待办与定时任务——侧栏入口 + 持久清单 + 定时调度，
  宿主半边在 `todo.go`，关掉后入口、`/api/todos` 接口与调度一起停）、
  `git-panel`（Git 面板——右栏「Git」页签：同屏双区（默认上下堆叠、可切左右分栏、
  分隔条可拖）——更改区为 VSCode 式清单（暂存/取消暂存/提交/AI 摘要/逐行 diff；已暂存与未暂存的文件可右键「移出跟踪」变未跟踪），
  图谱区为分支泳道图（全部分支拓扑、合并分叉、引用徽标、HEAD 高亮、提交详情），
  顶栏可选仓库、切换本地分支、「⟲ 同步」（fetch → 快进或无冲突自动合并 → push 当前分支；无上游/分叉冲突/推送被拒等一律原样退回并弹卡建议回对话新建工作任务）和「?」悬停帮助（列面板没做的 Git 能力，引导去对话里说）。宿主半边在 `gitpanel.go`，写边界只有暂存/取消暂存/
  提交/移出跟踪/切换本地分支/同步（上述安全范围），不做 reset/revert/discard/stash/rebase）、
  `dangerous-cmd-guard`（危险命令硬拦截，只有 manifest、没有浏览器半边）、
  `voice`（语音（朗读与听写），**零配置**——朗读走后端、听写走浏览器，都不需要
  任何密钥。**朗读（TTS）**：宿主半边在 `voiceapi.go`，直连微软 Edge「大声朗读」
  通道（speech.platform.bing.com 的 readaloud，无密钥免配置，与 Edge/WebView2
  内置朗读同一套服务；WebSocket 协议逐项对齐聊天服务 chat-server 的 Node 实现
  ——Sec-MS-GEC 令牌/Edge 扩展 Origin/随机 muid；前端 POST `/api/voice/tts`
  → mp3 流回 blob 播放，音色清单 `/api/voice/voices` 同源 322 个神经音色、中文
  排前）。朗读不再用浏览器 speechSynthesis——部分手机浏览器没有它，旧版远程
  手机端连朗读设置区都不渲染；现在设置区恒渲染、任何浏览器行为一致。**听写
  （STT）两档，默认「云端」**：云端＝浏览器自带的 SpeechRecognition（WebView2/
  Edge/Chrome 内置的微软识别接口，即本组件一直以来的实现：灰字暂定层/断线退避重连/
  倒计时徽标、按钮三态，连接失败提示建议换 Edge）；本地＝`voiceasr.go` 通道的离线模型
  （说完一句停顿约 3 秒精修落黑字，本机走 ws 流式、远程 https 回退整段 POST），**需
  `技能/_runtime` 带 sherpa-onnx 且 `data/asr-models` 模型齐备**，缺任一即弹「识别通道
  错误」，前端引导切回云端；环境没有浏览器识别能力时（部分远程手机端浏览器、Electron
  壳）自动落到本地档。发行版默认「云端」——装完即用，不依赖模型文件。
  **听写提示已精简**：识别期间不再报「已出 N 句（继续说，或点麦克风结束）」这类
  计数提示（用户判为噪声且常滞后——消息都发走了还挂着）；按停后到尾段精修回包
  这一秒（原先无任何反馈）按钮转圈 +「正在识别…」，收尾时提示一并收掉。**语音快捷键（主副双键，
  永久并存）**：默认鼠标侧键（主，前后两枚都算）与 Alt 键（副）**同时可触发**
  ——有侧键的用侧键、没有的自然用 Alt；独按语义：按住 Alt 期间按了别的键不
  触发，点按在独按松开时触发；设置页可录制自定义键盘单键/组合键、鼠标左右
  键同按（不允许单独的左键/右键）；设置页**主/副两个键帽框独立录制**（默认
  主=侧键、副=Alt；旧单快捷键偏好自动迁移），「清空」＝停用全部、「恢复默认」
  ＝回到侧键+Alt；点按切换与长按输入两种模式；仅窗口聚焦时
  生效（失焦自动松手结束长按）；主输入框占位灰字由基座 `sendKeyHint` 追加
  组件交出的
  `window.JXVoiceHint` 片段（子输入框组件自己拼）。**发送前自动停听写**：
  `send()` 执行点（app-chat.js）先调组件交出的 `window.JXVoicePause`——未定稿
  灰字同步落进输入框随消息发出，再硬收尾；回车/Ctrl+回车/发送按钮/子对话
  全覆盖。**远程手机端**
  （`window.JX_REMOTE` 且视口 <500）自动隐藏听写按钮（resize 跨阈值即时
  显隐），快捷键触发给提示不启动。设置＝**独立设置页「语音」**（设置 →
  语音：语速默认 1.2、Edge 音色、试听、听写时长上限、快捷键与触发方式）；
  偏好存 `localStorage` 的 `cx:voice:` 命名空间。关掉即热卸载：语音端点 404、
  按钮隐藏、朗读按钮撤掉、快捷键引擎与占位提示一并撤掉、设置页连导航项
  一起消失）。

## 插件与插件兼容层

**统一架构（pluginlayers.go）**：目标是兼容各种插件体系（DSH、Claude
Code、…），各家后端逻辑一致。主体只认「插件兼容层」这一层抽象——启动时
`discoverPluginLayers` 发现全部兼容层（层在 init() 里 `registerPluginLayer`
自注册），此后只按 `PluginLayer` 契约调用（列清单 / 总开关），不认识任何
具体插件格式；兼容层自己去发现自己生态的插件。统一入口
`GET/POST /api/plugins`：GET 返回 `{layers, plugins}`（每条插件带 `layer`
来源标记），POST 总开关带 `{layer, id, enabled}`（layer 缺省=按 id 全层
唯一匹配）。设置页只有**一个「插件」页**：按体系分组展示，安装区在页内
（当前仅 Claude 层支持安装）。新增一个体系 = 写一个层实现并注册，主体与
设置页零改动（前端可按层 id 注册富行渲染器，缺省通用行）。

对 DSH 插件体系的手写兼容层（组件/开关机制从简：一体编译 + 扫描 + 开关）：

- **`插件/`**：DSH 插件原件，原样存放、不改一字、不参与编译——功能来源与对照标准。
- **`插件兼容层/<插件名>/`**：每个插件一份手写转译适配——`manifest.json`（id/名称/
  版本/描述/浏览器资产）+ `client.js` / `client.css`（浏览器半边）+ `README.md`
  （能力映射表：转译了什么、有意不做什么）。Go 半边在根目录 `adapter_<名>.go`
  （一体编译，按名与目录配对：可挂自己的 HTTP 端点、给系统提示词追加段）。
- **扫描与开关**：启动扫描 `插件兼容层/*/manifest.json`（损坏即跳过告警，不阻断）；
  `插件/` 里有原件但无适配器的在统一「插件」页灰显为「未适配」。开关状态存
  `data/plugins.json`（扁平映射，缺省启用），经统一 `/api/plugins` 落盘，
  **即时生效**：浏览器半边资产注入
  与 Go 半边端点每请求检查，界面部分刷新页面后生效，无需重启。
- **前端接入**：页面注入的插件脚本经 `JX` 接口接入原生界面——
  `JX.registerMessageAction`（消息操作行按钮）、`JX.registerSettingsSection`
  （设置分区）、`JX.addComposerControl`（输入区控件）、`JX.on/emit`（事件总线：
  send / message:done / session:open）；`JX.unregisterMessageAction` / `JX.off`
  是对应的注销口（组件/插件热卸载时把钩子摘干净，不留悬挂按钮）。
- 首个适配：**dsh-speech**（消息朗读 / 浏览器听写 / 转写润色），映射表见
  `插件兼容层/dsh-speech/README.md`。为下一个插件写适配 = 原件拷进 `插件/`，
  在 `插件兼容层/` 建同名目录写资产与说明，必要时加一个 `adapter_<名>.go`。

## Claude Code 插件兼容层（ccplugin*.go）

对 Anthropic Claude Code 插件格式（`.claude-plugin/plugin.json` + skills /
commands / agents / hooks / MCP / mods）的通用兼容层：一次实现吃全部该格式
插件，不是逐个手写适配。它是 pluginlayers.go 统一注册表里的一个层实现
（与 DSH 手写适配层平级：清单与总开关走统一 `/api/plugins`，组件级开关/
配置/装卸走本层自有 `/api/cc-plugins/*`）。设置页统一「插件」页的
Claude 分区管理；输入框 `/` 触发插件命令弹层。

- **目录布局**（全在受保护的数据区内）：`data/claude-plugins/cache/<市场>/<插件>/<版本>/`
  插件本体（`${CLAUDE_PLUGIN_ROOT}`）、`data/claude-plugins/data/<插件>@<市场>/`
  运行时数据与 mod store（`${CLAUDE_PLUGIN_DATA}`）、`marketplaces/<市场id>/`
  市场克隆、`plugins.json` 安装记录与开关。安装四来源：本地目录/zip、git/zip
  URL、市场条目三形态（`./相对路径` | `git-subdir` | `url`）。
- **组件映射**：skills → `read_skill` 按需读取 + 环境快照清单（id 带命名空间
  `插件名:技能名`）；commands → 斜杠命令注册表，handleChat 落库前展开
  （`$ARGUMENTS`/`$1..$9`/`\$` 转义）；agents → 动态子对话专家（experts.go
  三处合并，名称即 dispatch_sub 参数）；hooks（settings 形态）→ 事件桥 +
  命令/http 执行器（stdin JSON、exit 2 阻断、stdout JSON decision 协议：
  permissionDecision/updatedInput/updatedToolOutput/additionalContext）；
  `.mcp.json` → 纯 Go MCP 客户端（stdio 行帧 JSON-RPC 与 streamable HTTP），
  工具名 `mcp__<插件>_<服务器>__<工具>` 保持 Claude 原样（插件 hooks 的
  matcher 可直接匹配）。
- **mods（`{"modules":["./register.js"]}` 形态）**：goja 纯 Go 内嵌 JS 运行时
  （四平台一致含安卓）。ESM→CJS 薄转换（import/export 行级改写，mod 的
  import 形态受 Claude 规范约束故可行）；`register(on)` 装载时调用一次；
  事件：session.start / prompt.submit / tool.call / command.run /
  turn.complete（与 settings hooks 共用分发点，mods 在后）；matcher 支持
  值/数组/正则；`next` 链内联递归，返回 undefined=放行（e 取最后一次 next
  的参数）、返回对象=短路答案（deny/result/text/drop）。`$` API 十六个命名
  空间见 ccplugin_modapi.go；`$.command.register`/`$.tool.register` 注册的
  命令与工具（`mcp__<插件名>__<工具>`）经 command.run / tool.call 事件路由
  回 mod。
- **权限模型**（与 Claude 的有意差异，见下）：skills/commands/agents 静态组件
  随插件开关生效；**hooks/mods/mcp 执行型组件缺省关闭**，设置页逐类显式开启
  （显式组件开关 > 总开关 > 缺省）。mod 的 `$.fs.write`/`$.process.run` 过
  会话权限门控（read-only 拒绝）+ 危险命令护栏；settings hooks 的 shell
  命令同样过护栏。
- **接入点**：agent.go（工具循环 PreToolUse→tool.call→执行→PostToolUse、
  收尾 Stop/SubagentStop→turn.complete、开轮 SessionStart）；handleChat
  （斜杠展开→UserPromptSubmit→prompt.submit）；tools.go 的 ccMCPTools()
  （MCP+mod 工具，只在重载边界变化——前缀缓存契约）；experts.go（动态专家）；
  skill_manager.go 的 read_skill（用户技能查不到回落插件技能）。
- **API**：`GET /api/cc-plugins`（清单+市场）、`POST /api/cc-plugins/toggle`
  （总开关/组件开关/userConfig 三合一）、`/install`、`/uninstall`、
  `/marketplace/add`、`GET /marketplace/{id}`（浏览）、`GET /commands`
  （弹层）。
- **测试**：ccplugin_test.go（frontmatter/占位符/decision 协议/matcher/加载/
  安装/真实插件冒烟——本机有 claude-plugins-official 克隆时装 25 个真插件
  验证）+ ccplugin_mod_test.go（goja 语法矩阵/ESM 转换/handler 链/mods
  端到端：官方 first-mod 示例原文全链路）。

## 独立 MCP 连接器（mcpservers*.go / mcpcatalog.go）

不装整个 CC 插件、直接添加一个 MCP 服务的注册表（stdio 命令或 http/sse
地址）。连接复用 ccplugin_mcp.go 的 mcpClient（宿主经 mcpHost 接口注入：
插件侧 ccPluginHost 做 ${CLAUDE_PLUGIN_ROOT} 等替换，独立侧 mcpPlainHost
只做 ${环境变量}）。

- **按需加载（技能式）**：一个连接器可带几十个工具，全量注册会撑爆工具
  目录、打断前缀缓存、误导模型（第一版正是这么做的，已废弃
  `mcp__<服务器>__<工具>` 全量注册）。现与用户技能同一纪律——清单先行、
  内容按需：环境快照只列启用的连接器（PromptBlock，名字+描述+工具数），
  模型按需 `mcp_read_connector(name)` 读工具清单与参数 schema、
  `mcp_call_tool(connector, tool, arguments)` 执行。两个工具常驻目录且
  定义恒定，连接器增删只动环境快照尾部，工具目录形状与前缀缓存不受扰动。
- **状态**：`<dataDir>/mcp-servers.json`（增删/开关/来源 manual 或
  catalog:<id>）；桥代码与各桥独立 venv 落 `<dataDir>/mcp-servers/<id>/`。
  信任边界与 CC 插件 mcp 组件一致：显式添加并启用 = 放行该连接器工具，
  不逐工具审批。不进统一插件页（有独立的 设置→MCP 分区，app-mcp.js）。
- **设计软件桥目录**（mcpcatalog.go，内置制造业常用软件的社区 MCP 桥，
  设置页一键安装）：solidworks（yimu0824/solidworks-mcp，COM）、multicad
  （AnCode666/multiCAD-mcp，中望 ZWCAD/AutoCAD/GstarCAD/BricsCAD）、blender
  （PyPI mcp-for-blender + addon.py）、freecad（PyPI freecad-mcp，需
  Python 3.12+，安装器检测拦截）、autocad（vigneshpbmenon，轻量 2D）。
  安装器走 GitHub codeload tar.gz（国内网络下 github.com 主站 git 协议常
  断，codeload/raw 一般可达）+ venv pip。上游依赖钉版与垫片：solidworks/
  multicad/autocad 均 import mcp 1.x 低层 API，pip 装 `mcp<2`
  （fastmcp<4）；autocad 的 requirements 写了不存在的 pyautocad>=2.0.0、
  `FastMCP(title=)` 老 kwarg，安装器直装可用版本并做一行替换。
- **API**：`GET /api/mcp`（清单+连通状态）、`POST /api/mcp`（增改）、
  `/delete`、`/test`（连通并列工具）、`GET /api/mcp/catalog`（目录+已装
  标记）、`POST /api/mcp/catalog/install`（一键装，分钟级）。
- **测试**：mcpservers_test.go（名称域/状态回读/清单段与常驻工具恒定性/
  读取守卫/目录完整性/tar.gz 解压防穿越）；mcp_install_now_test.go 为手动
  实装入口（`JX_MCP_INSTALL_NOW=<目录id逗号列表> go test -run
  TestMCPInstallNow`，驱动真实下载安装+握手验证，缺省跳过）。

### 与 Claude Code 的已知差异（有意为之或暂不支持）

1. mod 敏感 API 过会话权限门控与危险命令护栏（Claude 对 mods 明确无沙箱，
   本引擎收紧）；`$.env.set` 限 `JXCC_<插件名>_` 前缀、`$.env.get` 拒读密钥类
   环境变量；`$.settings.read` 只回脱敏提示。
2. `.ts` mods 不支持（明确报错）；goja 覆盖 ES2017 常用语法，超新语法
   （类私有字段等）不保证。`.mcpb`/`.dxt` 打包、lspServers/outputStyles/
   themes/workflows/monitors/channels 组件忽略（扫描时记日志）。
3. UI 渲染类事件（ui.render/press/panes/blit…）不触发，注册了也收不到；
   `$.ui` 仅 toast/status/log 映射为全局事件流通知；`$.process.spawn`、
   `$.audio`、`$.telemetry` 调用即抛错。
4. settings hooks 事件子集：UserPromptSubmit / PreToolUse / PostToolUse /
   Stop / SubagentStop / SessionStart / PreCompact（其余 30+ 事件无对应物
   不触发）；prompt/agent/mcp_tool 型 handler 与 async 语义跳过；Stop 的
   block 决策不改变行为（只观察）。
5. `transcript_path` 提供简化 JSONL 导出（每行 {role,content}），非 Claude
   完整格式。
6. agents 的 model 别名（sonnet/opus）与 tools 白名单不生效；skills 的
   `!`cmd`` 动态注入、allowed-tools 免批、context:fork 不生效。
7. `$.model.fork` 不继承会话历史（与 complete 同路径）；`$.session.usage`
   未实现。
8. mods 无 `--plugin-dir` 热重载与 `claude plugin validate/test` CLI；装卸
   经设置页，重载即插件重载边界。

## 存储与模块结构

数据存于 `data/jx.db`（SQLite 单文件，WAL 模式）：`sessions`（会话元信息）、
`messages`（全层级共用消息表：seq/role/kind/compact/摘要边界）、`meta`（版本与
迁移标记）。旧版 JSONL 数据首次启动自动迁移，原文件保留为备份。

| 文件 | 职责 |
|---|---|
| `main.go` | 启动入口：配置、HTTP 服务、自动开浏览器、-webdir 开发模式 |
| `config.go` | 运行配置（含 providers 与压缩设置）的加载/保存/兼容折算 |
| `models.go` | 多 provider 模型清单、调用目标解析、模型列表拉取 |
| `llm.go` | OpenAI 兼容流式客户端（SSE 解析、工具调用增量聚合）+ mock 演示模型 |
| `contextengine.go` | 上下文引擎：请求材料的唯一组装者 + 上下文事件总线。系统提示词（可被 `data/prompts/<mode>.md` 引用文件覆盖，版本行记来源与内容 hash）、环境快照、工具目录、历史视图、视觉内联经 `Assemble` 成包（确定性：同一状态逐字节一致——前缀缓存命中的根基，有测试看护），token 估算与前缀指纹随包产出；每轮开轮的 prompt 展示快照行经 `OpenRound` 落库；需把上下文发给模型的侧路调用（模型摘要）由 `CallSummarizer` 拼装并发送（留痕 audit scope=summarize，不进请求环形缓存）；权限/模型/思考档位/计划目标/工作区/远程挂载等配置变更经 `Notify` 落时间线（audit type=ctx，含前后值）；技能增减由环境快照 diff 在尾部留痕，同一事实不重复记账 |
| `agent.go` | 主循环：每步经上下文引擎取包 → 流式请求 → 执行工具 → 回填（只做编排，不亲手拼装——发给模型的内容一律来自引擎 Assembly）；环境快照首行「用户最近发消息时间」在其 `envPrompt` 里取（`Store.LastUserMessageAt`，不是挂钟） |
| `perm.go` | 权限引擎：三档模式、授权凭证（签发/核销）、审批队列、受保护路径、危险命令拦截 |
| `plans.go` | 计划读取端点：从会话日志里取回 exit_plan_mode 的正文（右栏「计划」视图的数据源） |
| `interact.go` | 模型↔用户交互工具（ask_user_question / enter·exit_plan_mode / update_goal）；计划审批无时限且提交即落库，重启后由 resolvePersistedPlan 收口批准/驳回/忽略 |
| `interact_plan_test.go` | 计划审批的时限与持久化语义测试（跨重启挂待审批、批准/驳回/忽略三分支、plan 无 TTL） |
| `remote.go` | SSH 远程工作区：连接池、TOFU 指纹库、远程 exec、SFTP 文件操作、连接测试 |
| `tools.go` | 任务会话工具集（接入权限引擎与本地/远程执行环境抽象；含 web_fetch） |
| `browser_ops.go` | browserTarget 操作面：browser_* 工具与面板端点的形态无关接口 |
| `browser_host.go` | chromedp 浏览器宿主：有头独立窗口 + 网页形态的后台无头内嵌目标 |
| `browser_panel_windows.go` | 桌面右栏原生浏览器面板：第二个 WebView2 控件 + 控件级官方 CDP 通道 + 矩形同步 |
| `tool_browser.go` | browser_* 九件工具（形态无关，作用于 browserTarget） |
| `assistant.go` | 数字助理总控：编排工具 + 会话压缩摘要 |
| `historytools.go` | 跨会话历史查询工具（search / read_session_messages） |
| `context.go` | 上下文组装管线（环境快照**追加式**：首条整份、后续按行差分为短更新，已发送前缀绝不改写）+ 规则压缩 + 模型摘要 + 溢出兜底 |
| `runtime.go` | 后台运行时：执行注册表、全局事件总线、派发执行体、中断恢复 |
| `session.go` | 会话元信息与工具函数 |
| `store.go` | SQLite 存储层与旧数据迁移（`LastUserMessageAt` 供环境快照取「用户最近发消息时间」） |
| `adapters.go` | DSH 插件兼容层实现：扫描、开关联查、资产清单、Go 半边注册点 |
| `pluginlayers.go` | 插件兼容层注册表（主体第一层）：`PluginLayer` 契约 + 编译期自注册 + 统一清单/总开关（`/api/plugins` 的 layers+plugins） |
| `adapter_dsh_speech.go` | dsh-speech 的 Go 半边：润色端点（复用已配置模型）+ 听写提示词段 |
| `components.go` | 组件管理：扫描、开关状态、组件资产服务、`Rescan`（市场装卸后重扫） |
| `update.go` | 更新检查：定期拉发布源（updateURL，Gitea releases/latest 或 jxai-server 的 /updates/latest，两者同格式），tag_name 比当前新就经事件流推横幅；versionCompare 双体系（日期+V序号 恒新于 旧点分号） |
| `wizard.go` | 公司接入向导后端：POST /api/wizard/discover（UDP 广播找同公司服务端——浏览器发不了 UDP 故在 Go 侧做，255.255.255.255+子网定向广播、公司名=口令、registryURL 查询兜底）+ POST /api/wizard/connect（验证 healthz → POST /provision → 内部提供方 company-internal 置顶即默认、marketURL/updateURL/wizardDone 一键落位；重跑替换不堆积）；config 增 wizardDone/registryURL |
| `market.go` | 市场客户端半边：拉 jxai-server 清单（60s 缓存）、下载 zip+sha256 校验、三类型安装路由（技能→addUserSkill / 组件→组件目录+Rescan / 插件→ccInstallFromPath 落 `jx-market`）、装卸记账 `data/market_installed.json`、`GET /api/market` + `/install` + `/uninstall`；地址在 `config.marketURL`（设置·网络访问） |
| `todo.go` / `todotools.go` | 待办与定时任务（组件 `todo-scheduler` 的宿主半边）：清单、调度、两类执行 / 模型工具 |
| `gitpanel.go`（+`gitpanel_windows.go`/`_other.go`） | Git 面板（组件 `git-panel` 的宿主半边）：仓库扫描、porcelain/图谱/分支解析、暂存/提交（白名单 argv 直调 git，永不过 shell） |
| `server.go` | 路由、聊天 SSE、全局事件流、压缩接口、插件注入、内嵌前端 |
| `web/` | 前端：工作区分组侧栏、子对话抽屉、子任务卡片、模型选择器、设置页、市场页（`app-market.js`：左侧栏底部入口、主区整页视图 body.market-mode、三 tab+搜索+一键装卸；组件装卸后 `CX.refresh()` 热生效） |
> **服务端已独立成仓库**（`D:\智能体工作区\jxaiserver`，module jxai-server）：远程访问中继 + 技能/组件/插件市场（含 /admin 管理页）+ 更新分发（日期+V序号版本体系）+ 公司接入（登记/UDP 发现/按人发 new-api 令牌/每月额度）。本仓库只保留客户端半边（market.go 市场客户端、wizard.go 接入向导、update.go 更新检查——与服务端的协议契约见各自文件头注释）；其部署与用法见该仓库 README/部署。开发完全分开，两边接口变更需双侧同步。

## 前后端接口

| 方法与路径 | 说明 |
|---|---|
| `GET /api/status` | 服务状态 |
| `GET/POST /api/config` | 读取/修改配置（Key 回传掩码，providers/压缩设置） |
| `GET /api/models` / `POST /api/providers/fetch` | 模型清单 / 从提供方拉取模型 |
| `GET/POST /api/sessions` | 会话列表（`?parent=` 查子会话；列表项带 `status=running` 与异常中断标记 `lastStop`）/ 创建（mode/parent/provider/model） |
| `GET/PATCH/DELETE /api/sessions/{id}` | 详情（含 `running` / `pending` / `error`：执行中、待生效消息、最近一次失败原因）/ 改标题、工作区、模型 / 删除 |
| `POST /api/sessions/{id}/chat` | 发送消息；SSE 流式返回。**执行体独立于本请求**：浏览器刷新/关页只断开这条流，执行照旧跑完并落库 |
| `GET /api/sessions/{id}/stream` | 接上正在执行的会话流（刷新页面/换窗口后继续看实时输出）；未在执行时立刻回一帧 `done` |
| `POST /api/sessions/{id}/stop` | 显式停止本会话执行（流断开不再等于停止） |
| `POST /api/sessions/{id}/retry` | 失败后重试：不重复落库用户消息，带着当前上下文再跑一轮（SSE 流式返回，同 /chat） |
| `POST /api/sessions/{id}/interject` | 执行中发消息：`{content, mode}`，`steer`=插话（下一次调用模型前注入）、`queue`=排队（本轮结束后作为独立一轮发送）；未在执行时按排队处理并顺手拉起执行 |
| `POST /api/sessions/{id}/pending` | 排队坞操作：`{id, action:'remove'\|'steer'}`（移出队列 / 提前为插话）、`{id, action:'move', before:锚点id\|空=队尾}`（拖拽排序，只动排队队列顺序）、`{action:'pause'\|'resume'}`（队列级暂停/继续，不需要 id；恢复时空闲且有排队消息顺手拉起执行）；前端「编辑排队/插话消息」＝ `remove` 后把原文回填输入框草稿，复用 remove，无新 action |
| `POST /api/sessions/{id}/compact` | 手动压缩为一条摘要 |
| `POST /api/sessions/{id}/remote` | 设置/解除会话的 SSH 远程工作区（`{remote:{…}}` 或 `{clear:true}`） |
| `GET /api/approvals` | 待决定的越权审批清单（轮询兜底） |
| `GET /api/questions` | 待回答的助手提问 / 计划审批清单（轮询兜底） |
| `POST /api/questions/{id}` | 回答提问；计划批准传 `__approve__`，驳回传反馈文本，**忽略（搁置：本轮停下、计划仍留着）传 `__ignore__`**；内存里没有等待者的（重启后遗留的待审批计划）走 resolvePersistedPlan |
| `GET /api/sessions/{id}/progress` | 会话进度清单（progress_write 的持久化结果） |
| `GET /api/sessions/{id}/plans` | 会话历史里提交过的计划（exit_plan_mode 的正文 + 出处，右栏「计划」视图的数据源；plans.go） |
| `POST /api/sessions/{id}/plans/export` | 把某份计划写成工作区里的 `计划-<时间>-<标题>.md`（`{seq}` 缺省最新一份；同名不覆盖、顺延 -2/-3） |
| `POST /api/approvals/{id}` | 审批决定：`{approved, always}`（always=本会话总是允许） |
| `POST /api/workspace/test` | 测试 SSH 远程工作区配置（拨号/SFTP/目录，回显主机指纹） |
| `GET /api/events` | 全局事件流（后台派发进度、回报就绪、会话更新、审批请求） |
| `GET/POST /api/plugins` | 统一插件清单（`{layers, plugins}`，带来源体系）/ 切换开关（`{layer,id,enabled}`，即时生效，界面部分刷新后生效） |
| `GET /_plugins/{id}/{file}` | 插件浏览器半边资产（仅启用时提供） |
| `GET/POST /api/components` | 组件清单 / 切换开关（前端热生效，无需刷新） |
| `GET /api/components/{id}/{file}` | 组件浏览器半边资产（仅启用时提供；挂 `/api/` 下让中继隧道覆盖，cx.js fetch 文本注入） |
| `GET /api/market` | 市场清单视图（jxai-server 清单 + 本机安装态；`?refresh=1` 绕过 60s 缓存；未配置时给建议地址） |
| `POST /api/market/install` `/uninstall` | 一键安装/更新/卸载 `{type: skill\|component\|plugin, id}`（下载 zip+sha256 校验后路由到三套体系；组件装卸后前端 `CX.refresh()` 热生效） |
| `GET/POST /api/todos` | 待办/定时任务清单 / 新建（组件 `todo-scheduler`；未启用时 404。**跨会话的全局清单，与会话内 `progress_write` 的进度清单是两回事**） |
| `POST /api/todos/update` `/delete` `/run` `/missed` | 修改 / 删除 / 立即执行 / 处理错过的槽点（`{id, action:'run'\|'dismiss'}`） |
| `GET /api/todos/workspaces` | 程序任务可选的工作目录（默认工作区 + 历史会话用过的目录） |
| `GET /api/git/status` `/diff` `/graph` `/commit` `/branches` | Git 面板读接口（组件 `git-panel`；未启用时 404。graph 为 `--all --topo-order` 的父母哈希+装饰引用，泳道由前端计算） |
| `POST /api/git/stage` `/unstage` `/untrack` `/commit` `/summarize` `/checkout` `/sync` | Git 面板写接口：暂存 / 取消暂存 / 移出跟踪变未跟踪（`rm --cached`，保留工作区文件）/ 提交 / AI 生成提交信息（只生成不提交）/ 切换本地分支（分支名限服务端列出的清单，改动会被覆盖时 git 拒绝）/ 远程同步（fetch → 快进或无冲突自动合并 → push 当前分支；需要决策时 `{needHelp:true, summary}` 原样退回，前端建议派工作任务）。仓库只能选服务端扫描出的候选，路径经 pathspec 校验 |
| `POST /api/plugins/dsh-speech/polish` | 转写润色（dsh-speech 适配器 Go 半边） |
| `GET /api/voice/voices` | Edge 神经音色清单（朗读音色下拉数据源；中文区排前，进程内缓存 6 小时；组件 `voice`，未启用时 404） |
| `POST /api/voice/tts` | `{text, rate, voice}` → mp3 音频流（后端走 Edge「大声朗读」无密钥通道；前端 blob 化 `<audio>` 播放）。听写不走后端：浏览器 SpeechRecognition |
| `GET /` | 内嵌前端页面（按开关注入插件资产） |

聊天 SSE 事件：`message` / `delta` / `tool_start` / `tool_wait`（等待审批）/ `tool_end` /
`retry`（重试开始，半截流式作废）/ `auto_retry`（自动重试提示）/ `sub_start` / `sub_end` /
`title` / `done` / `stopped` / `error`。全局事件流另含 `sub_tool`、
`report_ready`、`session_updated`、`approval_required` / `approval_resolved`（审批卡）、
`question_required` / `question_resolved`（助手提问 / 计划审批卡）、`todos`（会话待办刷新）、
`todo_tasks`（待办/定时任务清单变化，组件 `todo-scheduler`）
（均带 sessionId，前端按会话过滤渲染）。
