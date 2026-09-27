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
  `app-left.js`、右栏 `app-right.js`、设置 `app-settings.js`、共享地基
  `app-core.js`）——大概率已经有了，复用而不是重写。
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
- **异常收尾必须让用户看见，但不要往对话里塞话**：真实失败走输入框上方的
  报错条（`showRunError`，带重试）并在服务端记 `runError`；对话区只放真实
  对话，别写「（本轮…）」这类系统交代——但也不能什么都不报。

### 能不造轮子就不造轮子

市面上有知名、成熟的工具或库，且体积不大、许可允许时，**直接用现成的**，
不要自己重复实现：协议细节、边界情况、安全问题人家都替你踩过了。例子：
二维码的生成与识别用成熟库（不要手写编码矩阵），文件下载用 aria2c
（`技能/downloader/` 内置的官方二进制），APK 解析用纯 Go 库
（`apkinfo` 子命令，androidbinary + pkcs7）。自研只保留给两种情况：
没有可靠现成方案，或现成方案的集成成本明显更高。判断口诀——
**先找轮子，再决定要不要造**；引入时连同许可文本一起入库
（GPL 等强许可尤其要附全文）。

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
  单实例，二次启动唤起已有窗口；首启默认工作区指到「文档\坚祥数字助理」
  并回写配置；WebView2 运行时缺失时给官方下载指引。
- **安装包**（Inno Setup，内置技能便携运行时，约 350MB 源经 LZMA2 压到
  约 103MB，装完离线全功能）：

  ```sh
  winget install JRSoftware.InnoSetup      # 一次性装打包工具
  powershell -NoProfile -ExecutionPolicy Bypass -File 打包\打包安装包.ps1
  # 产物：打包\输出\坚祥数字助理GO-安装包-<版本>.exe
  ```

  安装到当前用户 `%LOCALAPPDATA%\Programs`（免管理员），中文向导、
  桌面/开始菜单快捷方式、注册表卸载项；卸载默认保留会话数据。
  脚本默认现场 `go build`；要排除工作区未提交改动时，先在干净 worktree
  里构建，再以 `-ExeSource <exe路径>` 传入。打包产物（stage/、输出/）
  已 gitignore，`config.json` 与 `data/` 永不进包。

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
  置灰、不可单独关闭，权限引擎同样视为开启；切回其他档位时自动取消勾选）。
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

- **首页（新建对话）**：居中问候区 + 输入卡片；输入框上方选择**工作目录**
  （候选 = 默认目录 + 历史用过的目录，也支持输入任意绝对路径；目录只在新建时选）。
- **输入区工具栏**：左下角加号菜单（文件上传 / 计划模式 / 目标模式 /
  导出日志（运行时间线 Markdown）/ 导出原始数据（最近发给模型的请求）/
  SSH 远程工作区）与权限芯片；
  发送按钮左侧是**模型芯片**（既是显示也是选择，按提供方分组）。
- **权限面板**（点权限芯片弹出，两列）：左列权限单选（只读 / 工作区内读写 /
  完全访问），右列能力多选（允许工作区外执行命令 / 操作浏览器 / 操作电脑）。
  切到完全访问时三项能力联动勾选并置灰（不可单独关闭）。
- **审批卡**：模型请求越权动作时右下角弹卡（允许 / 本会话总是允许 / 拒绝），
  经全局事件流实时推送；远程工作区会话顶栏显示「远程 user@host:dir」徽标。
- 文件上传保存进会话工作区（远程会话经 SFTP）并注入记录消息。**两种导出**
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
  「系统提示词已拼装」折叠行在用户消息上方（请求最前部），「运行环境」
  （请求尾部）与「工具定义」（请求体平级数组）紧随用户消息，其后才是
  模型输出；之后每轮内容不变就不再出现任何提示词卡片——只有系统提示词
  / 工具定义变更、「运行环境更新」（含行级 diff，如时间翻页、能力开关
  变化）才在发生位置补一条折叠行。每次请求都会完整重发系统提示词
  （协议如此），「不变」指逐字节相同，前缀缓存据此命中。
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
- **上下文用量圆环**（输入区，模型选择左侧）：小圆环以弧长动画展示当前会话
  下一次请求占上下文窗口的比例，中心显示百分比（≥60% 变琥珀、≥85% 变红）；
  鼠标悬停浮出详情卡：总量/窗口、分类占比（消息 / 系统工具 / 系统提示词 /
  摘要 / 环境快照 / 其他，堆叠进度条）与**缓存命中率**。纯展示无点击。数据来自
  `GET /api/sessions/{id}/context`：按 buildContext 同一组装规则估算各分类
  token；命中率来自每次模型调用流尾 `usage`（`stream_options.include_usage`，
  含 DeepSeek 的 `prompt_cache_hit_tokens`），按会话累计落库（sessions 表
  `usage_prompt/usage_cache_hit/usage_calls` 三列，迁移 0005）。
- **对话区选中文字浮窗**：在主对话区或右栏子对话区选中文字后，浮出深色胶囊
  工具条（对齐主流 AI 编程助手的选区交互）：**复制**、**添加到当前对话**
  （插入主输入框光标处）、**在右侧辅助问答**——自动打开右栏子对话视图并
  找/建子对话，把选文填入子对话输入框由用户发出，全程不影响主对话区；
  点浮窗不冲掉选区，选区消失 / 滚动 / Escape 即收起。

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

**执行中发消息（两种行为，设置页「对话中途发送行为」切换）：**

| 模式 | 生效时机 | 前端呈现 |
|---|---|---|
| 插话 `steer` | 主循环下一次调用模型前注入，作为一条真实用户消息进入上下文 | 立刻贴在对话最底下（虚线气泡 +「插话中」角标），落库后换成正常气泡 |
| 排队 `queue` | 本轮结束后作为**独立一轮**自动发送 | 进输入框上方的排队坞；可逐条删除，或点 ↑ 提前为插话 |

两边的队列都在服务端（`PendingMsg`），刷新页面/换窗口不丢、多窗口一致；本轮结束时
还没注入的插话自动转为排队，消息不会卡在队列里。会话空闲时发来排队消息会自动拉起一轮。

**不静默、也不往对话里塞话**：对话区只放真实对话（用户消息 + 助手输出 + 工具过程）。
正常完成与用户手动停止**不产生任何附加内容**；只有真实的失败（模型/网关/网络——余额不足、
限流、鉴权失败、连不上、超时，以及模型一条都没输出、只出思考、达到最大步数）才显示在
**输入框上方的报错条**里：原样呈现模型侧返回的原因，配「重试」按钮（`POST /retry`——
不重复落库用户消息，直接带着当前上下文再跑一轮）与关闭按钮。报错原因存在服务端会话上，
刷新/切回来还在，新一轮开始自动清掉。异常收尾同时写日志，服务端不留无声的停摆。

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

设置页「模型服务」可配置多个 OpenAI 兼容提供方（预置 DeepSeek / 智谱 GLM /
Kimi / 通义千问 / OpenAI / Ollama 本地 / 自定义），填 Key 后一键拉取模型清单；
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
`read_skill`（常驻工具，task 模式）读它的完整 SKILL.md 说明——技能增减
不改变工具定义；清单变化经环境快照补在对话**最底部**，并附一行
「技能变更：新增 X／移除 Y」明说增删了哪个技能，上文历史快照一律不动。

**运行环境自包含**：技能一律使用随软件分发的便携运行时
`技能/_runtime/python`，**不探测、不依赖用户机器上的系统 Python**——
面向非编程用户，安装包内置全部运行时与依赖，装完离线全功能。
开发目录缺 `_runtime` 时首次使用自动装配一次（python.org
embeddable，约 11MB + 清华镜像装 wheel），此后离线可用；打包脚本
会校验运行时依赖齐全，缺一不出包。`skillsDir` 配置可指定
技能目录位置（默认取可执行文件旁，不可写时落数据目录）。

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
  渲染为可点链接，交付卡片的 `.html/.htm/.svg` 文件带「打开」——桌面版左键
  点击一律在右栏浏览器面板打开（Ctrl/中键走浏览器默认新标签）；网页版没有
  原生面板，一律新标签页打开。这是用户操作，不经模型权限门控；打开时内嵌
  形态自动成为工具作用目标（active），并注入`【网页查看】`回报（origin=
  webview，与能力无关——没开「操作浏览器」能力时模型只知道网址看不到画面，
  开启后 browser_screenshot/browser_content 看到的就是用户眼前这页）。本地
  交付文件转 `file://`（同目录相对资源可用），SSH 远程会话经
  `/api/browser/view` 流式渲染（相对资源不加载）。
- **电脑操作**（能力开关 `computer`）：`computer_screenshot / click /
  type / key / scroll / windows` 六件工具，win32 SendInput + GDI
  DIB Section 直调，无 cgo；点击坐标以截屏图片像素为准。

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
- **宿主半边可以落在主程序里**：组件本身是纯浏览器半边，但它掌管的功能若需要
  后端（持久化、定时、接口、模型工具），就写在主程序对应文件里、按
  `Components.IsEnabled(<id>)` 自查开关——组件关掉时那部分功能一并停。
- 示例组件：`input-counter`（输入字数实时统计，最小组件模板——新建组件
  直接复制该目录改 manifest 与 client.js 即可）。界面强调色（点缀色）原为
  示例组件，现已并入主程序 `web/app-settings.js`（常规 → 外观 → 点缀色）。
- 现成组件：`todo-scheduler`（待办与定时任务——侧栏入口 + 持久清单 + 定时调度，
  宿主半边在 `todo.go`，关掉后入口、`/api/todos` 接口与调度一起停）、
  `git-panel`（Git 面板——右栏「Git」页签：同屏双区（默认上下堆叠、可切左右分栏、
  分隔条可拖）——更改区为 VSCode 式清单（暂存/取消暂存/提交/AI 摘要/逐行 diff），
  图谱区为分支泳道图（全部分支拓扑、合并分叉、引用徽标、HEAD 高亮、提交详情），
  顶栏可选仓库与切换本地分支。宿主半边在 `gitpanel.go`，写边界只有暂存/取消暂存/
  提交/切换本地分支，不做 push/pull/reset/discard）、
  `dangerous-cmd-guard`（危险命令硬拦截，只有 manifest、没有浏览器半边）。

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
| `contextengine.go` | 上下文引擎：请求材料的唯一组装者 + 上下文事件总线。系统提示词（可被 `data/prompts/<mode>.md` 引用文件覆盖，版本行记来源与内容 hash）、环境快照、工具目录、历史视图、视觉内联经 `Assemble` 成包（确定性：同一状态逐字节一致——前缀缓存命中的根基，有测试看护），token 估算与前缀指纹随包产出；每轮开轮的 prompt 展示快照行经 `OpenRound` 落库；需把上下文发给模型的侧路调用（模型摘要）由 `CallSummarizer` 拼装并发送（留痕 audit scope=summarize，不进请求环形缓存）；权限/模型/思考档位/计划目标/工作区/远程挂载等配置变更经 `Notify` 落时间线（audit type=ctx，含前后值）；技能增减由环境快照 diff 在尾部留痕，同一事实不重复记账 |
| `agent.go` | 主循环：每步经上下文引擎取包 → 流式请求 → 执行工具 → 回填（只做编排，不亲手拼装——发给模型的内容一律来自引擎 Assembly） |
| `perm.go` | 权限引擎：三档模式、授权凭证（签发/核销）、审批队列、受保护路径、危险命令拦截 |
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
| `store.go` | SQLite 存储层与旧数据迁移 |
| `adapters.go` | 插件兼容层：扫描、开关联查、资产清单、Go 半边注册点 |
| `adapter_dsh_speech.go` | dsh-speech 的 Go 半边：润色端点（复用已配置模型）+ 听写提示词段 |
| `components.go` | 组件管理：扫描、开关状态、组件资产服务 |
| `todo.go` / `todotools.go` | 待办与定时任务（组件 `todo-scheduler` 的宿主半边）：清单、调度、两类执行 / 模型工具 |
| `gitpanel.go`（+`gitpanel_windows.go`/`_other.go`） | Git 面板（组件 `git-panel` 的宿主半边）：仓库扫描、porcelain/图谱/分支解析、暂存/提交（白名单 argv 直调 git，永不过 shell） |
| `server.go` | 路由、聊天 SSE、全局事件流、压缩接口、插件注入、内嵌前端 |
| `web/` | 前端：工作区分组侧栏、子对话抽屉、子任务卡片、模型选择器、设置页 |

## 前后端接口

| 方法与路径 | 说明 |
|---|---|
| `GET /api/status` | 服务状态 |
| `GET/POST /api/config` | 读取/修改配置（Key 回传掩码，providers/压缩设置） |
| `GET /api/models` / `POST /api/providers/fetch` | 模型清单 / 从提供方拉取模型 |
| `GET/POST /api/sessions` | 会话列表（`?parent=` 查子会话）/ 创建（mode/parent/provider/model） |
| `GET/PATCH/DELETE /api/sessions/{id}` | 详情（含 `running` / `pending` / `error`：执行中、待生效消息、最近一次失败原因）/ 改标题、工作区、模型 / 删除 |
| `POST /api/sessions/{id}/chat` | 发送消息；SSE 流式返回。**执行体独立于本请求**：浏览器刷新/关页只断开这条流，执行照旧跑完并落库 |
| `GET /api/sessions/{id}/stream` | 接上正在执行的会话流（刷新页面/换窗口后继续看实时输出）；未在执行时立刻回一帧 `done` |
| `POST /api/sessions/{id}/stop` | 显式停止本会话执行（流断开不再等于停止） |
| `POST /api/sessions/{id}/retry` | 失败后重试：不重复落库用户消息，带着当前上下文再跑一轮（SSE 流式返回，同 /chat） |
| `POST /api/sessions/{id}/interject` | 执行中发消息：`{content, mode}`，`steer`=插话（下一次调用模型前注入）、`queue`=排队（本轮结束后作为独立一轮发送）；未在执行时按排队处理并顺手拉起执行 |
| `POST /api/sessions/{id}/pending` | 排队坞操作：`{id, action:'remove'\|'steer'}`（移出队列 / 提前为插话） |
| `POST /api/sessions/{id}/compact` | 手动压缩为一条摘要 |
| `POST /api/sessions/{id}/remote` | 设置/解除会话的 SSH 远程工作区（`{remote:{…}}` 或 `{clear:true}`） |
| `GET /api/approvals` | 待决定的越权审批清单（轮询兜底） |
| `GET /api/questions` | 待回答的助手提问 / 计划审批清单（轮询兜底） |
| `POST /api/questions/{id}` | 回答提问；计划批准传 `__approve__`，驳回传反馈文本 |
| `GET /api/sessions/{id}/progress` | 会话进度清单（progress_write 的持久化结果） |
| `POST /api/approvals/{id}` | 审批决定：`{approved, always}`（always=本会话总是允许） |
| `POST /api/workspace/test` | 测试 SSH 远程工作区配置（拨号/SFTP/目录，回显主机指纹） |
| `GET /api/events` | 全局事件流（后台派发进度、回报就绪、会话更新、审批请求） |
| `GET/POST /api/plugins` | 插件清单 / 切换开关（即时生效，界面部分刷新后生效） |
| `GET /_plugins/{id}/{file}` | 插件浏览器半边资产（仅启用时提供） |
| `GET/POST /api/components` | 组件清单 / 切换开关（前端热生效，无需刷新） |
| `GET /_components/{id}/{file}` | 组件浏览器半边资产（仅启用时提供） |
| `GET/POST /api/todos` | 待办/定时任务清单 / 新建（组件 `todo-scheduler`；未启用时 404。**跨会话的全局清单，与会话内 `progress_write` 的进度清单是两回事**） |
| `POST /api/todos/update` `/delete` `/run` `/missed` | 修改 / 删除 / 立即执行 / 处理错过的槽点（`{id, action:'run'\|'dismiss'}`） |
| `GET /api/todos/workspaces` | 程序任务可选的工作目录（默认工作区 + 历史会话用过的目录） |
| `GET /api/git/status` `/diff` `/graph` `/commit` `/branches` | Git 面板读接口（组件 `git-panel`；未启用时 404。graph 为 `--all --topo-order` 的父母哈希+装饰引用，泳道由前端计算） |
| `POST /api/git/stage` `/unstage` `/commit` `/summarize` `/checkout` | Git 面板写接口：暂存 / 取消暂存 / 提交 / AI 生成提交信息（只生成不提交）/ 切换本地分支（分支名限服务端列出的清单，改动会被覆盖时 git 拒绝）。仓库只能选服务端扫描出的候选，路径经 pathspec 校验 |
| `POST /api/plugins/dsh-speech/polish` | 转写润色（dsh-speech 适配器 Go 半边） |
| `GET /` | 内嵌前端页面（按开关注入插件资产） |

聊天 SSE 事件：`message` / `delta` / `tool_start` / `tool_wait`（等待审批）/ `tool_end` /
`sub_start` / `sub_end` / `title` / `done` / `error`。全局事件流另含 `sub_tool`、
`report_ready`、`session_updated`、`approval_required` / `approval_resolved`（审批卡）、
`question_required` / `question_resolved`（助手提问 / 计划审批卡）、`todos`（会话待办刷新）、
`todo_tasks`（待办/定时任务清单变化，组件 `todo-scheduler`）
（均带 sessionId，前端按会话过滤渲染）。
