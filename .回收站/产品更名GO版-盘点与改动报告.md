# 产品更名「软著版」→「GO版」：盘点、改动与验证报告

- 执行时间：2026-09-29 02:10–02:40
- 工作目录（未改动）：`D:\Desktop\童浩\智能体工作目录\软著版数字助理`
- 改动方式：**只改文件内容，未重命名任何文件/目录** → 不存在引用断链问题
- 未触碰任何构建产物（`坚祥数字助理GO.exe`、`jx-assistant.exe`、`.apk`、安装包等）

---

## 一、第 1 步：全仓库盘点清单（改前）

盘点手段（全库、含隐藏目录）：
1. 自写 PowerShell 全库文本扫描（自动判定 UTF-8 / UTF-16LE），命中行落盘 `.jxai/tmp/rename/report.tsv`；
2. GBK 编码兜底扫描（按 GBK 字节序列 `C8 ED D6 F8` 逐文件搜）→ **0 命中**，确认没有 GBK 编码漏网；
3. 文件名/目录名扫描（`Get-ChildItem -Recurse -Force | ? Name -match '软著'`）→ **工作区内无任何含「软著」的文件/目录名**（唯一含该字样的目录是最外层工作目录本身）；
4. 错别字变体「软著板」→ **全库 0 命中**；传统字变体亦无。

### A. 需要改的（产品显示名 / 代码注释 / 文档标题）

| # | 绝对路径 | 行号 | 原文片段 | 改为 |
|---|---|---|---|---|
| 1 | `D:\Desktop\童浩\智能体工作目录\软著版数字助理\web\index.html` | 577 | `<b>坚祥数字助理（软著版）</b>` | `<b>坚祥数字助理（GO版）</b>` ← **关于页面项目名** |
| 2 | `…\软著版数字助理\web\app-core.js` | 4 | `/* 坚祥数字助理（软著版）前端逻辑：原生 JS，无框架、无构建步骤。风格对齐 DSH。 */` | `（GO版）` |
| 3 | `…\软著版数字助理\web\style.css` | 1 | `/* 坚祥数字助理（软著版）—— 亮色主题，风格对齐 DSH Web UI。 */` | `（GO版）` |
| 4 | `…\软著版数字助理\AGENT.md` | 1 | `# 坚祥数字助理（软著版）` | `# 坚祥数字助理（GO版）` |
| 5 | `…\软著版数字助理\消息类型规范.md` | 1 | `# 【坚祥数字助理（软著版）】消息类型规范` | `# 【坚祥数字助理（GO版）】消息类型规范` |
| 6 | `…\软著版数字助理\main.go` | 1 | `// 坚祥数字助理（软著版）—— 单进程本地智能助手服务。` | `（GO版）` |
| 7 | `…\软著版数字助理\main.go` | 192 | `log.Printf("坚祥数字助理（软著版）已启动: %s", url)` | `坚祥数字助理（GO版）已启动` |

### B. 刻意保留（不改）——「软著」指软件著作权含义

| 路径 | 行 | 原文 | 理由 |
|---|---|---|---|
| `…\AGENT.md` | 51 | `- 前端是原生 JS、无框架、无构建链，这是有意的架构决策（软著全自主实现 +` | 此处「软著」= **软件著作权**（说明架构决策动因），不是产品名。按规则原地保留，见「待确认事项 C」 |

### C. 刻意保留（不改）——真实文件系统路径（改了就断链）

| 路径 | 行 | 原文 | 理由 |
|---|---|---|---|
| `…\config.json` | 36 | `"workspace": "D:\\Desktop\\童浩\\智能体工作目录\\软著版数字助理\\默认工作区"` | 指向**真实存在的目录**（该目录本次不改名） |
| `…\环境更新重发-研究与改动说明.md` | 8 | `源码目录：\`D:\Desktop\童浩\智能体工作目录\软著版数字助理\`` | 历史研究文档里的真实路径 |
| `…\交付试点.html` | 26 | `<li>位置：D:\智能体工作区\软著版数字助理\</li>` | 静态演示件里的路径展示 |
| `…\.回收站\说明.md` | 14 | `cd "D:\Desktop\童浩\智能体工作目录\软著版数字助理"` | 回收站历史文件，含真实路径 |
| `…\.回收站\decodecmd_test.go` | 15 | ``const want = `D:\智能体工作区\软著版数字助理> dir / 输出` `` | 同上（`.回收站` 为点目录，不参与 Go 构建） |
| `…\gui-test\linkify-test.js` | 18 | ``[S`绝对路径 D:\智能体工作区\软著版数字助理\AGENT.md 已更新`, S`D:\智能体工作区\软著版数字助理\AGENT.md`, 0]`` | linkify 单元测试的**样例路径**夹具（同一行出现 2 次），非产品文案 |
| `…\技能\_runtime\python\Scripts\{vba_extract.py, pdf2txt.py, dumppdf.py}` | 1 | `#!D:\Desktop\童浩\智能体工作目录\软著版数字助理\技能\_runtime\python\python.exe` | 内嵌 Python 虚拟环境的**解释器 shebang 绝对路径**，改则运行时报错 |
| `…\打包\stage\技能\_runtime\python\Scripts\{同上三个 .py}` | 1 | 同上 | 打包舞台目录里的同一份副本 |

### D. 刻意保留（不改）——示例词、历史副本、缓存与运行时数据

| 路径 | 行 | 原文 | 理由 |
|---|---|---|---|
| `…\插件兼容层\回收站\dsh-speech\client.js` | 328 | `wt.placeholder = '顿号或换行分隔，如：坚祥、RustDesk、软著';` | 输入框**占位示例词**（且位于 `回收站/` 内，非生效代码）；「软著」此处是示例术语 |
| `…\默认工作区\.jxai\tmp\backup-20260928-013759\{index.html:532, app-core.js:4}` | 532 / 4 | `<b>坚祥数字助理（软著版）</b>` | 历史备份副本，改它会伪造历史 |
| `…\data\testrun\web\{index.html:537, app-core.js:4, style.css:1}`、`…\data\testrun-old\web\{同}` | 1–537 | 同上文案 | 旧测试运行留下的前端副本（非产品资源） |
| `…\data\stage-commit\style.css` | 1 | `/* 坚祥数字助理（软著版）—— 亮色主题… */` | 同上 |
| `…\data\testrun\config.json`、`…\data\testrun-old\config.json`、`…\默认工作区\.jxai\tmp\{build,modeltest,run,smoke}\config.json` | 8/36/38/42 | workspace / skillsDir 绝对路径 | 旧测试配置，含真实路径 |
| `…\默认工作区\.jxai\tmp\{agent-md-patch.py, patch-model-settings.py, pg-*.py, wsfix-*.mjs, modeltest\*.mjs, run\stderr.log, smoke\data\desktop.log}` | 多处 | 绝对路径 / 启动日志 | 历史脚本与日志 |
| `…\data\webview*\EBWebView\...`、`…\默认工作区\.jxai\tmp\edge-cdp\...`、`…\android*\app\build\...`（含 manifest-merger 报告、merger.xml） | 大量 | 缓存的 HTML/JS 副本、路径文本 | 浏览器缓存与 Gradle 构建中间产物，重建即变 |

> 说明：B/C 两类（源码、文档、测试夹具）合计 **12 个文件、14 处命中**，已在上表逐条列出；D 类是示例词与历史副本/缓存/运行数据，其余命中全在缓存与构建中间产物里。

### E. 无需改动（已是 GO 命名）

- `go-winres.json`（ProductName `坚祥数字助理GO`、`JianXiang Digital Assistant GO`、exe 名）
- `打包\安装包.iss`（`MyAppName "坚祥数字助理GO"`、`MyAppExeName`、`SetupIconFile=坚祥数字助理GO.ico`）
- `打包\开发桌面版.ps1`、`打包\打包安装包.ps1`、`打包\使用说明.md`、`打包\stage\使用说明.md`
- `desktop_windows.go:29`（`desktopAppTitle = "坚祥数字助理GO"`）、`main.go:184`（桌面版启动日志）、`go-winres` 生成的 `rsrc_windows_amd64.syso`
- 安装包/APK 等产物文件名（`坚祥数字助理GO-安装包-0.1.7.exe`、`坚祥数字助理GO-安卓版0.1.6.apk`）
- 安卓侧 `android\app\src\main\AndroidManifest.xml`（`android:label="数字助理"`）等 Android 源文件：无「软著」字样

---

## 二、第 2 步：实际改动清单（改后）

共 **6 个文件、7 处**替换（全部为等长语义替换，未动任何代码标识符、未动逻辑）：

| # | 文件（绝对路径） | 行号 | 改前 → 改后 |
|---|---|---|---|
| 1 | `…\软著版数字助理\web\index.html` | 577 | `坚祥数字助理（软著版）` → `坚祥数字助理（GO版）` |
| 2 | `…\软著版数字助理\web\app-core.js` | 4 | 同上（注释头） |
| 3 | `…\软著版数字助理\web\style.css` | 1 | 同上（注释头） |
| 4 | `…\软著版数字助理\AGENT.md` | 1 | 同上（文档标题） |
| 5 | `…\软著版数字助理\消息类型规范.md` | 1 | 同上（文档标题，含【】括号形式） |
| 6 | `…\软著版数字助理\main.go` | 1 | 同上（文件头注释） |
| 7 | `…\软著版数字助理\main.go` | 192 | `坚祥数字助理（软著版）已启动` → `坚祥数字助理（GO版）已启动` |

**文件名/目录名改动：无。** 因此没有任何脚本、配置、代码、文档里的路径引用需要同步更新，也不存在断链。

替换后全文再扫一遍，`GO版` 命中位置恰好是上表这 7 处（另加二进制内嵌副本，见下）；「软著版」在源码/文档/文案中已 0 残留（仅剩 B/C/D 类刻意保留项）。

---

## 三、验收验证（命令与结果）

### 1. 残留扫描
- 全库 UTF-8/UTF-16 文本扫描：`.jxai/tmp/rename/report.tsv`（含缓存共 18744 命中行）
- GBK 兜底字节扫描：`powershell -File .jxai\tmp\rename\gbkscan.ps1` → `gbk_files=0`
- 文件名扫描：`Get-ChildItem -Recurse -Force | ? Name -match '软著'` → 无
- 「软著板」错别字：0 命中
- **结论：无「应改而未改」的残留**；未改项即上表 B/C/D，均附理由。

### 2. 构建验证（Go 后端）
沙箱内不能写 `%LOCALAPPDATA%`，故把构建缓存指到工作区内：

```powershell
$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'
$env:GOCACHE='<工作区>\.jxai\tmp\rename\gocache'
$env:GOTMPDIR='<工作区>\.jxai\tmp\rename\gotmp'

# 根包（主程序）
go build -o .jxai\tmp\rename\build_check.exe .        # EXIT=0，产物 35,711,488 字节
# 全部包（含 sandboxhelper）
go build -o .jxai\tmp\rename\bin\ ./...               # EXIT=0，产出 helper.exe + jxassistant.exe
# 模拟正式发布构建
go build -trimpath -ldflags "-s -w" -o .jxai\tmp\rename\jx-trimpath.exe .   # EXIT=0，27,938,816 字节
```

### 3. 前端资源与「关于」页文案验证
- 后端 `server.go:24` 用 `//go:embed web` 内嵌前端资源（默认走内嵌副本；`-webdir` 时读磁盘）。
- 对构建产物做字节级文案校验（UTF-8 字节串计数）：

| 文件 | 「软著版」 | 「GO版」 | 「坚祥数字助理（GO版）」 |
|---|---|---|---|
| 新构建 `jx-trimpath.exe` | **0** | **4** | **4** |
| 现役 `坚祥数字助理GO.exe` | 4 | 0 | 0 |

  → 新产物内嵌的前端副本已是 GO 版文案；现役程序仍是旧副本。
- **关于页面改动位置：`web\index.html` 第 577 行**（`<h3>关于</h3>` 区块内第一个 `<b>`；同区块第 575 行是 logo 的 `alt`，第 578 行的「版本 」行由 `web\app-settings.js:195` 动态填 `st.version`，不涉及本词）。
- 运行时实测（不干扰现役实例，只读一次 HTTP）：运行中的实例（PID 27220，监听 `0.0.0.0:8799`）返回 `GET http://127.0.0.1:8799/index.html` → `200`，页面内仍为旧文案 → **因为它服务的是内嵌副本**，界面要看到「GO版」必须重新构建并重启（或开发模式启动）。

### 4. 旁证
- 未加 `-trimpath` 构建时，二进制里会带 66 处编译机源码路径（含「软著版数字助理」目录名）；`-trimpath` 后降为 0，正式脚本 `打包\开发桌面版.ps1` 已带 `-trimpath`，故正式产物不泄露该字样。

---

## 四、生效方式（需要重新构建/重启）

改动只在**源码**层面完成，界面生效必须二选一：

1. **正式路径（推荐）**：退出正在运行的 `坚祥数字助理GO.exe`（否则 exe 被占用无法覆盖）→ 在仓库根执行 `powershell -File 打包\开发桌面版.ps1`（等价于 `go build -trimpath -ldflags "-s -w -H=windowsgui -X main.desktopDefault=1" -o 坚祥数字助理GO.exe .`）→ 重新启动。要走安装包则再执行 `打包\打包安装包.ps1`。
2. **开发模式**：用 `坚祥数字助理GO.exe -webdir web -port 8799 -noopen` 启动（前端从磁盘 `web\` 读），改完刷新页面即生效。Android 包若也要带新文案，需重跑安卓构建（本次未动 Android 侧文件，其文案本来就没有「软著版」）。

另：本次没有覆盖任何构建产物；验证用的临时产物都在 `.jxai\tmp\rename\` 下。其中体积大的构建缓存与验证用 exe（`gocache\`、`gotmp\`、`bin\`、`build_check.exe`、`jx-trimpath.exe`，约 500 MB）已在验证完成后删除，仅保留扫描脚本与命中清单（`scan.ps1`、`filter.ps1`、`gbkscan.ps1`、`report.tsv`、`report2.tsv`、`gbk_hits.tsv`）。

---

## 五、待用户确认事项

### A（重点）最外层工作目录 `软著版数字助理` 是否改名
**本次未改，也建议暂不改。** 现在这个目录名是引擎运行目录、且多个会话的工作区都指向它，改名会导致：

- 运行中的引擎（PID 27220）仍在用旧路径的句柄，必须**先完全退出程序**才能改目录名；
- 改后需要同步更新：`config.json:36` 的 `workspace`、`data\*.json` 里的路径、`技能\_runtime\python\Scripts\*.py` 与 `打包\stage\技能\_runtime\...` 的 venv shebang、`默认工作区\.jxai\tmp\*` 历史脚本、`.回收站\*`、`交付试点.html`、`环境更新重发-研究与改动说明.md`、以及任何快捷方式/启动脚本里的绝对路径；
- `.jxai` 内的快照/历史/数据库引用的是绝对路径，改名后旧快照路径全部失效；
- 需要重新构建（`-trimpath` 可避免把新路径写进 exe）、重打安装包、必要时重打 APK；
- 建议做法：新建 `D:\Desktop\童浩\智能体工作目录\GO版数字助理`，把仓库整体移动过去，再做一遍「旧路径 → 新路径」的全库替换 + 启动验证；**不要**在原地直接改名后再逐个修路径。

请确认是否要安排这次目录迁移（我可以在你退出程序后执行）。

### B 关于页/文档的书写形式
本次按你给的规则做了字面替换，结果是 **「坚祥数字助理（GO版）」**；而窗口标题、exe 名、安装包用的是 **「坚祥数字助理GO」**（无括号、无「版」字）。是否要统一成 `坚祥数字助理GO`？若要，我改 `web\index.html:577` 等 7 处即可（一行命令）。

### C `AGENT.md:51` 的「软著」
`（软著全自主实现 + 单文件交付）` 属「软件著作权」含义，已按规则保留。是否改写为更明确的 `（软著申请全自主实现 + 单文件交付）`？

### D 历史副本/缓存是否顺手清理
`默认工作区\.jxai\tmp\backup-20260928-013759\{index.html,app-core.js}`、`data\testrun*\web\*`、`data\stage-commit\style.css`、`data\desktop.log.old`、`.回收站\*`、`插件兼容层\回收站\dsh-speech\client.js` 等仍含旧字样。它们是历史备份/测试副本/运行日志，改它们等于篡改历史；**默认不动**，你要「清干净」的话我可以按清单逐项处理（建议直接删除备份类目录而不是改字）。

### E 构建产物重新生成
需要你挑一个时机退出正在运行的实例，我才能覆盖 `坚祥数字助理GO.exe` 并（可选）重打 `打包\输出\坚祥数字助理GO-安装包-0.1.7.exe`。当前运行中的程序在界面「关于」页仍会显示旧文案，属预期。

---

## 六、第二轮复核（2026-09-29 02:45，服务重启后收尾）

1. **改动是否还在位**：是。`git diff -U0` 逐行确认，7 处替换（`web\index.html:577`、`web\app-core.js:4`、`web\style.css:1`、`AGENT.md:1`、`消息类型规范.md:1`、`main.go:1`、`main.go:192`）全部仍在，无被并发覆盖。
2. **有没有半成品状态**：没有。本次全程**未重命名任何文件或目录**，所以不存在「改了引用没改文件」或反向的情况。专门做了断链检查：全库检索 `GO版数字助理`，唯一命中是本报告第 157 行的**建议方案**本身；`D:\Desktop\童浩\智能体工作目录\GO版数字助理` **不存在**（`Test-Path=False`），旧目录存在（`True`）。
3. **残留清单（重扫，UTF-8/UTF-16 全库 + GBK 字节兜底 0 命中）**：与上轮一致，另外新增 3 处**由并行会话新建的文件**里的真实路径/历史说明，同样属保留项：
   - `…\主对话气泡与折叠-改动说明.md` 第 44 行（工作目录路径）、第 313 行（提到「全项目『软著版 → GO 版』更名」这一并行任务）
   - `…\打包\输出\wait-stable.sh` 第 5 行（`cd "D:\Desktop\童浩\智能体工作目录\软著版数字助理"`）
   - 「软著板」错别字仍为 0 命中。
4. **并发情况**：复核期间并行会话在改 `web\app-chat.js`、`web\style.css`、`web\index.html`、`AGENT.md`、`server.go` 等。我的编辑全部是**单行唯一字符串**替换（edit 精确匹配），未回滚、未整段重写他人内容；另发现 `…\.jxai\tmp\voicetest\web-probe-wv\{index.html,app-core.js,style.css}` 是并行会话从 `web\` 拷出的前端探针副本，内容已是 **GO版** → 反证我的改动已随拷贝生效。
5. **构建复核（在多方并发改动后的当前树上）**：`go build -o .jxai\tmp\rename\recheck\ ./...` → **EXIT=0**，产出 `helper.exe`(2,502,144 B) 与 `jxassistant.exe`(35,723,264 B)。字节校验：`坚祥数字助理（GO版）` = 4 命中（`web/index.html`、`web/app-core.js`、`web/style.css`、`main.go` 启动日志的内嵌副本），「软著版」的 66 命中**全部是编译机源码绝对路径**（该次构建未加 `-trimpath`；加 `-trimpath` 后为 0，见第三节）。验证用的缓存与 exe 已删除，仅留脚本与命中清单。
6. **「软著」本义误改排查**：**没有误改**。全库属于「软件著作权」本义的只有 `AGENT.md:51` 一处，我**未改动**（保留原文，是否改写为「软著申请全自主实现」见「待确认事项 C」）。

---

## 附：本次用到的清单/脚本（临时件，均在 `.jxai/tmp/rename/`）

- `scan.ps1`：全库文本扫描（UTF-8/UTF-16 自动判定），输出 `report.tsv`
- `filter.ps1`：过滤缓存噪音，输出 `report2.tsv`（本次报告的盘点依据）
- `gbkscan.ps1`：GBK 字节兜底扫描，输出 `gbk_hits.tsv`
