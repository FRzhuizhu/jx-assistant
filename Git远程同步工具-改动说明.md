# Git 远程同步工具（git_sync）——改动说明

## 一、背景与问题

标准权限（工作区内读写）下命令跑在 OS 级沙箱（受限令牌）里，模型与 git 的交互
需要弄清边界。本次先实测、后动手，给模型补上了沙箱内缺失的远程通道。

## 二、沙箱内 git 实测矩阵（2026-10，Windows 受限令牌沙箱）

一次性测试（跑完已删）用 `spawnConfinedLocal` 把 git 命令送进真实沙箱执行，
宿主侧同命令作对照组（全部成功，排除 fixture 因素）。结论：

| # | 操作 | 结果 | 原因 |
|---|---|---|---|
| ① | init + add + commit | ✅ 成功 | `.git` 在工作区内，写授权覆盖；环境白名单保留 USERPROFILE/HOME（读得到 ~/.gitconfig 的身份配置），TEMP 重定向到沙箱私有临时目录 |
| ② | status / log | ✅ 成功 | 只读 |
| ③ | 读全局配置 | ✅ 成功 | 读不受限（write-restricted 语义） |
| ④ | 写 `%USERPROFILE%` | ❌ 拒绝访问 | 工作区外写入，写边界生效 |
| ⑤⑥⑦ | fetch / push（含**本地路径**远程） | ❌ 全灭 | **与凭据无关的硬死因**：git 的传输子进程（MSYS `sh.exe`）在受限令牌下创建 Cygwin 共享内存被拒——`CreateFileMapping S-1-5-21-…, Win32 error 5`，随后 "Could not read from remote repository" |
| ⑧ | 凭据助手配置 | （读成功） | 本机走系统级 `credential.helper=manager`（GCM） |
| ⑨ | cmdkey 枚举凭据库 | ✅ 成功 | CredRead 走 LSASS 服务侧，**凭据读取本身不被沙箱拦**（可惜远程传输死得更早，用不上） |
| ⑩ | ssh-agent 管道 | 跳过 | 宿主侧 agent 就没运行（多数用户同样）；管道连接是内核对象写检查，预判受限令牌连不上 |
| ⑪ | ls-remote 公共 HTTPS 仓库（无凭据需求） | ❌ 失败 | **第二个硬死因**：`schannel: AcquireCredentialsHandle failed: SEC_E_NO_CREDENTIALS`——受限令牌拿不到 TLS 凭据句柄，HTTPS 通道整个起不来（Git for Windows 默认 schannel 后端） |

**总结论**：本地 git 操作沙箱内一切正常；**远程操作（fetch/pull/push/clone）
在沙箱内必失败**，且死在凭据之前（MSYS 共享内存 + schannel 句柄两道墙）——
比当初"凭据可能失败"的预判更彻底。另注：git hooks 经 sh.exe 执行，
沙箱内跑 hook 同样会死；两处失败的输出都**不含**拒绝方言关键词，不会误触
sandbox 升级标记，模型看到的是普通 git 报错。

## 三、设计（按用户定调）

| 会话权限档位 | git_sync 工具 | 模型怎么完成远程推送 |
|---|---|---|
| 只读 | 在工具清单里，调用被拦（"只读模式下不可用……属于写操作"） | 无通道（只读本就不该推） |
| 工作区内读写（标准） | ✅ 可用（主场景） | 调 git_sync，宿主通道代做，报错原样返回 |
| 完全访问 / 已开「允许工作区外执行命令」 | 可用但没必要 | 工具描述直接告知：用 run_command 命令行（真令牌下凭据可用） |

工具形态：**单工具 + op 参数**（推送/拉取/合并收进同一工具的不同参数，
复用 gitSync 的既有四路）：

- `push`（缺省）：先 fetch 探远程，无新提交才推当前分支 HEAD 到上游
- `pull`：fetch → 快进优先、分叉自动合并，有冲突立即 --abort 恢复原状
- `sync`：fetch → 合并 → 推送一整套
- `fetch`：只更新远程跟踪状态不动任何东西（沙箱内 status 的领先/落后是旧数据）

安全边界（gitSync 既有语义，原样继承）：只推当前分支到已配置上游、不 force、
不推任意 refspec；无上游/分叉冲突/推送被拒等需要决策的情形一律不动手、
git 原话报给模型。

## 四、实现

- **`gittools.go`**（新增）：`gitTools()` 组件门控返回 `git_sync` 工具
  （`todoTools()` 同款先例：组件开关即工具目录增删边界，前缀缓存契约不破）；
  `runGitSync` 调用时依次核：组件在会话中途被关 → 报未启用；SSH 远程工作区
  会话 → 拒绝（本地通道专用）；只读档 → 拦。repo 参数不在 `gitResolve` 候选
  清单 → 报错列候选（gitResolve 本身会静默回退 repos[0]，模型场景必须显式
  报错）。`needHelp` 作为正常结果返回（"无法自动完成：…"），模型读后决定。
- **`tools.go`**：`toolsFor` else 分支（task/office/coding/skill-create 实操
  模式）挂 `a.gitTools()`；assistant/aux 不给。
- **`agent.go`** `envPrompt`：组件启用时公告一行"本地 git 直接 run_command、
  远程操作沙箱内必失败、用 git_sync"（browser_* 的"说明后果与正确出路"同款
  公告；远程会话则公告 git_sync 不可用）。
- **`组件/git-panel/manifest.json`**：描述更新（原描述还写着"不做 push/pull"
  早已过时）+ 提及 git_sync，版本 1.0.0 → 1.1.0。
- **`gittools_test.go`**（新增）：注册门控、只读拦截、远程会话拦截、推送/
  fetch 全链路（真实 git + 本地裸仓库）、repo 守卫与非法 op、无上游 needHelp
  透传，共 5 组。

## 五、点验步骤

1. `go build .`、`go test .` 全绿（已验）。
2. 启动后在任意工作区内读写档任务会话里让模型"提交并推送"：模型应本地
   run_command 提交、调 git_sync 推送；对话区环境快照可见 git 公告行。
3. 同会话切只读档再让推送：工具调用被拦、报错文案可读。
4. 设置 → 内置组件 关掉「Git 面板」：新会话工具清单无 git_sync，
   环境快照公告行消失；重开即恢复。
5. Git 面板「⟲ 同步」按钮行为不变（同一执行缝，未动面板代码）。

## 六、遗留与后续（不做，记录在案）

- 沙箱内 git 远程失败不带 sandbox 拒绝标记（输出不含拒绝方言）——按官方
  收窄方言的既有决策不动；环境快照公告行已把正确出路说清。
- ssh-agent 管道连通性未实测（本机 agent 未运行）；即便管道能通，MSYS 传输
  死因仍在，不改变结论。
- GCM 首次认证弹窗发生在宿主通道（git_sync / 面板同步），用户在场可完成；
  gitSyncTimeout=120s 内完成即可，超时如实报错。
