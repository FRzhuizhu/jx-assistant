# 坚祥数字助理（软著版）

完全自主编写的本地智能助手：**Go 后端（LLM 主循环 + 工具集 + 编排 + 上下文管理）+ 原生 JS 前端**。

- 后端依赖均为纯 Go（modernc.org/sqlite 无 CGo；golang.org/x/crypto 与 pkg/sftp 用于 SSH 远程工作区），前端无框架、无构建链。
- 构建产物为单个可执行文件（前端静态资源经 `go:embed` 内嵌），双击即用。
- 架构参考 pi / Reasonix / DSH 等开源 Agent 的主循环与编排思路，代码全部为新写实现。

## 运行

```sh
go build -o jx-assistant.exe .
.\jx-assistant.exe            # 首次启动自动生成 config.json 并打开浏览器
.\jx-assistant.exe -port 9000 -noopen
.\jx-assistant.exe -webdir web   # 开发模式：前端从磁盘读取，改 web/ 后刷新浏览器即生效
go test .                         # 单测（权限引擎 / 压缩管线 / 摘要折叠）
```

首次使用：左下角「设置」中填入 API Key；`baseURL` 兼容 OpenAI chat-completions
接口。`baseURL` 填 `mock` 可进入**离线演示模式**，无需 Key 即可体验完整链路。

## 权限与执行模型（移植自 DSH 的核心理念，本栈重写）

DSH（DeepSeek 官方 Agent）权限管理的核心思想：**三档权限模式 + 沙箱内执行 +
一次性授权凭证 + 越权逐次审批**。本产品按同一套模型重写实现（`perm.go`）：

| DSH 理念 | 本栈实现 |
|---|---|
| 权限三档（read-only / workspace-write / danger-full-access） | 会话粒度权限模式（权限芯片单选），后端强制执行 |
| 沙箱内执行命令（可联网、可写工作区） | 命令默认在会话工作区内执行：cwd 锁定、环境变量白名单最小化、限时、输出截断；`web_fetch` 提供联网抓取 |
| 向执行环境索取授权凭证 | 每次写文件 / 执行命令必须先经权限引擎评估，签发**短时效（5 分钟）一次性凭证**，执行器核销后才动手；过期/重用即拒绝（fail closed） |
| 沙箱外执行（escalation） | 命令工作目录放到工作区之外 = 沙箱外执行：需先开启「允许工作区外执行命令」能力（权限面板多选），之后每次越界执行弹**审批卡**（允许 / 本会话总是允许 / 拒绝），2 分钟不响应视为拒绝；完全访问模式不弹审批 |
| 保护自身配置与密钥 | 助手自身的配置文件与数据目录（含 API Key、全部会话记录）任何模式禁读禁写 |
| 破坏性命令拦截 | 不可逆危险命令（格式化盘 / 删根目录 / 写系统注册表 / fork 炸弹等）任何模式硬拦截 |

文件工具的权限（DSH 同款思路）：只读模式禁写禁执行；工作区内读写模式写文件
仅限工作区（读不受限）；完全访问全域可写（受保护路径除外）。`web_fetch` 在非
完全访问模式下拒绝内网/本机地址（防 SSRF）。

> 说明：Windows 本地的"沙箱"是策略级沙箱（目录锁定 + 环境最小化 + 凭证核销），
> 未做 OS 级进程隔离（AppContainer/受限令牌）；联网管控亦为策略级。

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

- **首页（新建对话）**：居中问候区 + 输入卡片；输入框上方选择**工作目录**
  （候选 = 默认目录 + 历史用过的目录，也支持输入任意绝对路径；目录只在新建时选）。
- **输入区工具栏**：左下角加号菜单（文件上传 / 计划模式 / 目标模式 / 导出日志 /
  SSH 远程工作区）与权限芯片；发送按钮左侧是**模型芯片**（既是显示也是选择，
  按提供方分组）。
- **权限面板**（点权限芯片弹出，两列）：左列权限单选（只读 / 工作区内读写 /
  完全访问），右列能力多选——「允许工作区外执行命令」已生效（开启后越界执行
  逐次审批）；「操作浏览器 / 操作电脑」等插件类能力暂未接入，置灰预留。
- **审批卡**：模型请求越权动作时右下角弹卡（允许 / 本会话总是允许 / 拒绝），
  经全局事件流实时推送；远程工作区会话顶栏显示「远程 user@host:dir」徽标。
- 文件上传保存进会话工作区（远程会话经 SFTP）并注入记录消息；导出日志下载
  整段对话的 Markdown。

## 会话的四个层级

| 模式 | 说明 | 工具 |
|---|---|---|
| `task` | 任务会话，实际干活 | 读/写/编辑/glob/grep/shell/web_fetch + 历史查询 |
| `assistant` | 数字助理总控（左上角唯一入口，不占侧栏） | 编排：list_sessions / read_session_summary / dispatch_task / send_to_session + 历史查询 |
| `aux` | 辅助对话（挂在某会话下的答疑小会话，右栏「＋ 新建」） | 只读文件 + web_fetch + 历史查询 |

## 后台派发与自动回报

`dispatch_task` / `send_to_session` 立即返回，子会话在后台 goroutine 并行执行；
进度经全局事件流 `GET /api/events` 实时推送（sub_start / sub_tool / sub_end /
report_ready），前端常驻订阅：主对话里的子任务卡片实时滚动工具活动，完成后
自动注入【派发回报】并触发一轮面向用户的汇报。服务重启时未完成的执行会补
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

设置页「模型服务」可配置多个 OpenAI 兼容提供方（预置 DeepSeek / 智谱 GLM /
Kimi / 通义千问 / OpenAI / Ollama 本地 / 自定义），填 Key 后一键拉取模型清单；
会话顶栏可单独切换本会话模型；派发时可指定子会话模型（providerId/modelId）。

## 组件管理（原生外围扩展，热开关）

与插件（DSH 适配）相互独立的轻量扩展体系，全部为本栈原生态编写：

- **`组件/<名字>/`**：`manifest.json`（id/名称/版本/描述/资产）+ `client.js`
  （`client.css` 可选）——纯浏览器半边，没有宿主代码、没有转译。
- **发现与开关**：启动扫描 `组件/*/manifest.json`（损坏即跳过告警）；
  开关状态存 `data/components.json`（缺省启用）；接口 `GET/POST /api/components`、
  资产 `GET /_components/{id}/{file}`（仅启用时提供）。
- **热开关（核心特性）**：内嵌运行时 `web/cx.js` 随页面加载，负责发现注入与
  运行期开关。组件脚本调用 `CX.register({id, init, dispose})` 注册：
  `init()` 建立界面与状态，`dispose()` 完整撤销（删节点、卸样式、清监听）。
  设置页切换开关时**前端即时加载/卸载，不需要刷新页面、不需要重启服务**；
  组件未提供 `dispose` 时退化为提示刷新。
- 示例组件：`accent-theme`（输入区调色盘，8 预设 + 自定义强调色，选择记入
  本机，热开关完整还原/恢复）、`input-counter`（输入字数实时统计，最小组件
  模板——新建组件直接复制该目录改 manifest 与 client.js 即可）。

## 插件与插件兼容层

对 DSH 插件体系的手写兼容层（组件/开关机制从简：一体编译 + 扫描 + 开关）：

- **`插件/`**：DSH 插件原件，原样存放、不改一字、不参与编译——功能来源与对照标准。
- **`插件兼容层/<插件名>/`**：每个插件一份手写转译适配——`manifest.json`（id/名称/
  版本/描述/浏览器资产）+ `client.js` / `client.css`（浏览器半边）+ `README.md`
  （能力映射表：转译了什么、有意不做什么）。Go 半边在根目录 `adapter_<名>.go`
  （一体编译，按名与目录配对：可挂自己的 HTTP 端点、给系统提示词追加段）。
- **扫描与开关**：启动扫描 `插件兼容层/*/manifest.json`（损坏即跳过告警，不阻断）；
  `插件/` 里有原件但无适配器的在设置页灰显为「未适配」。开关状态存
  `data/plugins.json`（扁平映射，缺省启用），**即时生效**：浏览器半边资产注入
  与 Go 半边端点每请求检查，界面部分刷新页面后生效，无需重启。
- **前端接入**：页面注入的插件脚本经 `JX` 接口接入原生界面——
  `JX.registerMessageAction`（消息操作行按钮）、`JX.registerSettingsSection`
  （设置分区）、`JX.addComposerControl`（输入区控件）、`JX.on/emit`（事件总线：
  send / message:done / session:open）。
- 首个适配：**dsh-speech**（消息朗读 / 浏览器听写 / 转写润色），映射表见
  `插件兼容层/dsh-speech/README.md`。为下一个插件写适配 = 原件拷进 `插件/`，
  在 `插件兼容层/` 建同名目录写资产与说明，必要时加一个 `adapter_<名>.go`。

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
| `agent.go` | 主循环：组装上下文 → 流式请求 → 执行工具 → 回填；三级系统提示词（按权限模式与本地/远程环境动态生成） |
| `perm.go` | 权限引擎：三档模式、授权凭证（签发/核销）、审批队列、受保护路径、危险命令拦截 |
| `remote.go` | SSH 远程工作区：连接池、TOFU 指纹库、远程 exec、SFTP 文件操作、连接测试 |
| `tools.go` | 任务会话工具集（接入权限引擎与本地/远程执行环境抽象；含 web_fetch） |
| `assistant.go` | 数字助理总控：编排工具 + 会话压缩摘要 |
| `historytools.go` | 跨会话历史查询工具（search / read_session_messages） |
| `context.go` | 上下文组装管线 + 规则压缩 + 模型摘要 + 溢出兜底 |
| `runtime.go` | 后台运行时：执行注册表、全局事件总线、派发执行体、中断恢复 |
| `session.go` | 会话元信息与工具函数 |
| `store.go` | SQLite 存储层与旧数据迁移 |
| `adapters.go` | 插件兼容层：扫描、开关联查、资产清单、Go 半边注册点 |
| `adapter_dsh_speech.go` | dsh-speech 的 Go 半边：润色端点（复用已配置模型）+ 听写提示词段 |
| `components.go` | 组件管理：扫描、开关状态、组件资产服务 |
| `server.go` | 路由、聊天 SSE、全局事件流、压缩接口、插件注入、内嵌前端 |
| `web/` | 前端：工作区分组侧栏、子对话抽屉、子任务卡片、模型选择器、设置页 |

## 前后端接口

| 方法与路径 | 说明 |
|---|---|
| `GET /api/status` | 服务状态 |
| `GET/POST /api/config` | 读取/修改配置（Key 回传掩码，providers/压缩设置） |
| `GET /api/models` / `POST /api/providers/fetch` | 模型清单 / 从提供方拉取模型 |
| `GET/POST /api/sessions` | 会话列表（`?parent=` 查子会话）/ 创建（mode/parent/provider/model） |
| `GET/PATCH/DELETE /api/sessions/{id}` | 详情 / 改标题、工作区、模型 / 删除 |
| `POST /api/sessions/{id}/chat` | 发送消息，SSE 流式返回 |
| `POST /api/sessions/{id}/compact` | 手动压缩为一条摘要 |
| `POST /api/sessions/{id}/remote` | 设置/解除会话的 SSH 远程工作区（`{remote:{…}}` 或 `{clear:true}`） |
| `GET /api/approvals` | 待决定的越权审批清单（轮询兜底） |
| `POST /api/approvals/{id}` | 审批决定：`{approved, always}`（always=本会话总是允许） |
| `POST /api/workspace/test` | 测试 SSH 远程工作区配置（拨号/SFTP/目录，回显主机指纹） |
| `GET /api/events` | 全局事件流（后台派发进度、回报就绪、会话更新、审批请求） |
| `GET/POST /api/plugins` | 插件清单 / 切换开关（即时生效，界面部分刷新后生效） |
| `GET /_plugins/{id}/{file}` | 插件浏览器半边资产（仅启用时提供） |
| `GET/POST /api/components` | 组件清单 / 切换开关（前端热生效，无需刷新） |
| `GET /_components/{id}/{file}` | 组件浏览器半边资产（仅启用时提供） |
| `POST /api/plugins/dsh-speech/polish` | 转写润色（dsh-speech 适配器 Go 半边） |
| `GET /` | 内嵌前端页面（按开关注入插件资产） |

聊天 SSE 事件：`message` / `delta` / `tool_start` / `tool_wait`（等待审批）/ `tool_end` /
`sub_start` / `sub_end` / `title` / `done` / `error`。全局事件流另含 `sub_tool`、
`report_ready`、`session_updated`、`approval_required` / `approval_resolved`
（审批请求与结束，前端弹右下角审批卡；均带 sessionId，前端按会话过滤渲染）。
