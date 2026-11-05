# 组件开发说明（组件/ 目录）

本目录是组件的本体。组件 = `组件/<名字>/` 一个目录，最低要求
`manifest.json` + `client.js`（`client.css` 可选）。本文件讲清楚三种组件形态、
各自的边界与开发规矩——**新组件怎么选形态，照此执行**。方针在《AGENT.md》
「组件管理」章节也有提要。

## 三种形态

### A. 纯浏览器半边组件（默认形态，新增组件一律优先这个）

目录里只有 manifest + client.js/css，全部逻辑跑在页面里，调主程序已有的
通用 API（/api/sessions、文件、市场……）。

- **开发循环零编译**：client.js 由服务端磁盘直读（每请求现读文件），改完
  F5 即生效——比核心前端（web/，需 -webdir 或重编）还短
- **增删即目录级**：市场安装（落盘 + Rescan + 前端热加载）、删目录=卸载；
  开关状态在 data/components.json 自动管理

### B. 宿主半边组件——存量「套皮」形态（不再新增，仅存量维护）

**todo-scheduler、git-panel、dangerous-cmd-guard、skill-market、sites-platform**
属于这类：后端是主程序的内置功能（根目录 todo.go / gitpanel.go / perm.go /
market.go / sitesproxy.go 里的完整实现，深度耦合 Store/会话/事件总线），组件
目录里只有皮肤（client 三件套），开关只控制 UI 显隐——后端接口保留但入口
消失（skill-market 与 sites-platform 是开源前按用户要求从本体抽出来的：当年
赶工直接做进本体，抽组件时后端 /api/market 与 /sites 代理保留在主程序，
入口、页面与设置区随组件开关出现/消失）。

- 历史原因：写的时候桥还不存在，且它们与主程序状态耦合太深，**现在也认定
  不可拆**——把状态访问全部 IPC 化的代价大于收益，维持现状
- 套皮的价值是「可关闭」：确实有用户不需要待办/Git 面板，开关是真实需求
- **规矩：不再新增这种形态**。新功能若有后端需求，走 C 形态

### C. 宿主半边组件——自包含 worker 形态（目标形态，voice 是样板，经声明式桥运行）

**voice（语音）** 是这个形态的样板：组件目录自包含一切——

```
组件/voice/
  manifest.json      # 组件声明
  client.js / css    # 浏览器半边（UI、录音、流式协议）
  asr_worker.py      # 后端 worker（推理逻辑全在这，跑共享便携运行时）
  models/            # 模型文件（gitignore 大文件，随包分发/首启下载）
```

主程序里只保留**薄胶水**（voiceasr.go：拉起子进程 + 端点桥接），不含业务
逻辑。后端的开发迭代（换模型、改识别逻辑）**不需要动主程序**。

第二个实例：**session-title**（AI 自动会话标题）——纯钩子 worker 的极简形态：
不声明任何 endpoint（`endpoints: []`），只订阅 `run.end` 通知型钩子，全部动作
经反向调用主程序 `/api/`（读消息 / 独立调模型 / 改标题）完成，worker 自身无
状态、无密钥。业务全在组件里（提示词、改写节奏、钉住规则），主程序只提供
通用原子端点（`/api/llm/ask` 单轮模型调用、消息轻量列表、PATCH 钉住语义）。

## 声明式桥（已实现，componentbridge.go）

manifest 顶层声明 `workers` 数组，主程序通用桥按声明拉起子进程并把端点挂到
`/api/c/<组件id>/<path>`——后端可以是脚本或**预编译产物**（小 exe），市场直装
（zip 落盘即生效，免编译免主程序更新）。声明示例：

```json
"workers": [
  { "name": "asr",
    "command": ["{python}", "asr_worker.py"],
    "endpoints": [
      { "method": "POST", "path": "asr/transcribe", "type": "call" },
      { "method": "GET",  "path": "asr/stream",    "type": "ws" } ] }
]
```

- **占位符**：`{python}`=共享便携运行时（技能/_runtime）；`{dir}`=组件绝对目录；
  命令其余参数原样，相对脚本路径自动锚到组件目录
- **协议**：json-lines 一行一答（call=HTTP body 原样进 worker stdin、应答行原样
  返回；ws=WebSocket 每帧一行双向哑转发，断开发 `{"cmd":"stream_end"}` 收尾）；
  桥是哑管，协议内容归组件
- **生命周期**：懒启动（首请求才 spawn）、死亡自动重启、组件关闭/卸载即杀
  worker（重开懒启复活）；进程退出由顶层 kill-on-close Job 兜底
- **限制**：哑管 worker 单飞行（一次一条请求/一个 ws 会话）；帧模式 worker
  （见下节）按 id 解复用可交织；请求上限 32MB、call 超时 180 秒；市场服务端
  对 workers/hooks 声明的校验待硬化（当前未知字段放行，可发布）

## 组件钩子（worker ⇄ 主程序后端的双向通道）

哑管只有「前端 → worker」的单向请求。worker 需要参与主程序流程——拦截对话、
收到生命周期事件、反查主程序状态——就在 worker 声明里加 `hooks` 订阅主程序
钩子事件。**声明了钩子的 worker 自动启用帧协议**，没声明的维持哑管（存量组件
零影响）：

```json
"workers": [
  { "name": "w",
    "command": ["{python}", "worker.py"],
    "hooks": ["prompt.submit", "run.end"],
    "hooksTimeoutMs": 5000,
    "endpoints": [{ "method": "POST", "path": "do", "type": "call" }] }
]
```

- **事件表**（拦截型要等 worker 的决定，通知型送达即止）：
  - `prompt.submit`（**拦截型**）：用户消息提交，`data={sessionID, content}`。
    发射点在 handleChat、CC 插件兼容层 UserPromptSubmit **之前**（本栈组件优先）。
    worker 回 decision 三选一（字段可组合）：`{"block":true,"reason":"…"}`
    拒绝（403+理由）、`{"content":"…"}` 改写正文、`{"additionalContext":"…"}`
    附加上下文（用户消息后落一条系统注入行发给模型，与 CC hook 同口径）。
    多组件按 id 序串行；任一拒绝即止；改写依次应用（后面的组件看到的是改
    过的正文）。
  - `run.end`（**通知型**）：一轮执行结束（含排队续跑全部完成），
    `data={sessionID, status: done|stopped|error, error}`。
- **帧协议**：stdin/stdout 变成带 id 的 JSON 帧，双向解复用——
  - 桥 → worker：`{"type":"req","id":…,"body":…}`（call 端点请求，body=原请求
    JSON；应答等 `{"type":"res","id":…,"body":…}`，body 原样回 HTTP——前端
    调用方式不变）、`{"type":"event","id":…,"event":…,"data":{…}}`（钩子事件）、
    `{"type":"hostres","id":…,"status":…,"body":…}`（反向调用应答）
  - worker → 桥：`res`／`decision`（决定字段见上）／`{"type":"host","id":…,
    "method":"GET|POST","path":"/api/…","body":{…}}`（反向调用主程序 API）
- **反向调用**：worker 处理 req/event 时可向主程序要数据（读会话、查状态……），
  仅限 `/api/` 前缀，权限同前端（本地信任模型——能调的 API 与页面里的一样）；
  需要收紧时再加 manifest 权限声明。桥侧 60 秒兜底。
- **桥向组件提供的通用服务**（反向调用即可用，全部组件共享）：
  - `POST /api/llm/ask`（llmapi.go）——**独立单轮模型调用**：body
    `{session?, system?, user}`，一次发送一次输出；不进任何会话上下文、不落
    消息表、不带工具。调用目标跟随 `session` 指定的会话（缺省全局默认），
    密钥与路由由主程序统一管理——**worker 组件不需要也不应该持有模型密钥**。
    浏览器半边（client.js）也可直接 fetch 同一端点。首个使用方：session-title
    （会话标题生成）；先例：git 面板提交摘要（gitSummarize 同模式）。
  - `GET /api/sessions/{id}/messages`——会话消息轻量文本列表
    （`role` 过滤如 `user`、`limit` 从最新往回收；插话消息 origin=interject
    默认排除——按「正常轮次」计数的辅助场景用）。
- **超时与 fail-open**：拦截型等决定上限 `hooksTimeoutMs`（缺省 5000、上限
  30000）；超时/进程死/帧解析错一律**按放行处理**并记日志——组件故障不能
  拦住发送等主流程。
- **为什么不做「代码注入主程序后端」**：Go 没有安全的运行时代码加载；内嵌 JS
  引擎（goja）那条路 CC 插件兼容层的 mods 已有，属兼容层范式，组件不沿用。
  组件统一「子进程 + 消息协议」：一套范式、可用预编译产物、市场免编译直装、
  崩了只死自己。
- **前端侧的钩子**：发送前收尾等可等待钩子用 `JX.hook(ev, fn)` 注册（区别于
  `JX.on` 的同步通知）：`send()` 组装正文前 `await JX.awaitHooks('send', …)`，
  逐个等待、异常隔离、总超时 10 秒放行；组件 dispose 里 `JX.unhook` 对称注销。
  样板：voice 组件（发送前等听写尾段落定）。
- 自动化验证：`componentbridge_hook_test.go`（帧协议 call、拦截决定、超时
  fail-open、反向调用、订阅匹配——走真实桥链路，无 Python 运行时自动跳过）。

**这样设计的目的（开源后的通用性）**：

- **对社区开发者**：组件代码各归各的、独立开发，避免人人都改主程序——
  主程序改动是冲突和组件不通用 的根源
- **对终端用户**：组件是编译好的产物 + 桥自动发现直接运行，不需要具备
  任何编译能力，装上就能用（不更新主程序）

## 开发规矩（硬约束）

1. 新组件默认 A 形态；确需系统能力才上 C 形态；B 形态只存量维护
2. C 形态的后端逻辑放组件目录（worker + 资产），主程序只允许薄胶水；
   状态深耦合主程序的功能不算组件，算主程序内置功能
3. 运行时**共享**技能/_runtime（不每组件自带，除非有特定版本需求）
4. 模型等重资产归组件目录、gitignore，随安装包分发或首启下载
5. 用户端文案不出现模型/厂商名（内部型号与下载链接写在代码注释里）
