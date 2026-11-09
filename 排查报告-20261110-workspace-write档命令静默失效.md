# 排查报告：workspace-write 档命令「静默失效」（2026-11-10 00:22 派发）

**现场**：会话权限档为「修改工作区」时，`run_command` / `run_python` 任何命令都返回
「（命令执行完成，无输出）」、退出码看着是 0，却完全没有副作用（echo/whoami/dir/
Out-File 写文件/删除自建文件/执行不存在的命令，全部无输出、无效果、无拒绝标记）；
带 `sandbox_permissions=danger-full-access` 重试或提到完全访问就正常。

---

## 结论速览

| 问题 | 结论 |
| --- | --- |
| 直接原因 | 受限子进程**根本没启动**：`CreateProcessAsUser` 返回成功，子进程在 2ms 内以 `0xC0000142`（STATUS_DLL_INIT_FAILED）退出，一行命令都没执行 |
| 为什么起不来 | 控制台子系统的新进程只能「继承父进程控制台」或「由系统新建控制台」；**受限令牌下新建控制台必败**。桌面形态的引擎是 GUI 进程、自己没有控制台 → 系统只能为子进程新建 → 秒退。`CREATE_NO_WINDOW`（新建隐藏控制台）与 `CREATE_NEW_CONSOLE`（新建可见控制台）同属「新建」，一律失败；实测换任何 `restricting SID` 组合、任何令牌创建标志都救不回来 |
| 为什么用户看到「成功」 | 执行层从不校验子进程退出码：`confinedChild.wait` 只返回是否超时，`spawnConfinedWindows` 见输出为空就填「（命令执行完成，无输出）」→ 启动即死被渲染成「成功但没输出」。输出还可能在搬运 goroutine 收尾前被取走，进一步丢字 |
| 为什么「部分命令被拒」是显式报错 | `Get-CimInstance Win32_LogicalDisk` / `Get-PSDrive` 那类是被受限令牌**真正执行后**拒绝访问，属正常沙箱语义；与「进程没起来」是两回事 |
| 修复 | ① 引擎在首次受限执行前给自己分配一个**隐藏控制台**（`AllocConsole` + `SW_HIDE`），供子进程继承；② 受限子进程**不再传任何控制台创建标志**，一律走继承（引擎有控制台就继承，没有就继承第①步补的隐藏控制台——两种情况都不会冒窗口）；③ 退出码如实上报，启动即死明确报错，不再假装成功 |

安全语义没有削弱：受限令牌、restricting SID、写受限模式、ACL 授权、Job 对象全部照旧；
回归测试里专门校验「工作区外写入仍被拒」。

---

## 一、定位过程（实测矩阵，非推断）

临时诊断测试直接调 `startConfinedWindows`，取子进程真实退出码：

| 实验 | 结果 |
| --- | --- |
| 现状（`CREATE_NO_WINDOW`，go test 有控制台） | 2ms，`exit=0xC0000142`，输出空 |
| 只去掉 `CREATE_NO_WINDOW`（有控制台可继承） | 158ms，`exit=0`，输出 `hi-ok` ✅ |
| 只留 `CREATE_NO_WINDOW` | 2ms，`0xC0000142` ❌ |
| Job 对象 / 挂起+恢复 / 自建环境块 各自单独开 | 均正常（排除） |
| 换 `exec.Cmd{SysProcAttr.Token}` 起 PowerShell | 能起，`whoami` 报 Access denied（证明令牌本身可用） |
| **DETACHED_PROCESS 重拉自己得到「无控制台」父进程**（= 生产里的 GUI 引擎） | 任何标志（含 flags=0）都 2ms `0xC0000142` ❌ |
| 同上 + 先 `AllocConsole` 并 `ShowWindow(SW_HIDE)` | 161ms，`exit=1`（whoami 被拒），输出 `hi-ok` ✅ |
| 附加 SID（ConsoleLogon S-1-2-1 / Interactive / AuthenticatedUsers / BuiltinUsers / LocalService / LocalSystem）、令牌标志 0x1 / 0x4 / 0x8 全组合、`lpDesktop=winsta0\default` | 全部 `0xC0000142`（无解，确认只能靠「继承控制台」） |

## 二、修复内容

`sandbox_windows.go`
- 新增 `ensureSandboxConsole()`（`sync.Once`）：当前进程没有控制台就 `AllocConsole` 并
  `ShowWindow(SW_HIDE)` 藏起窗口；紧接着 `SetConsoleCtrlHandler(NULL, TRUE)` 让引擎忽略
  CTRL+C——这个控制台与受限子进程共享，不能让沙箱里的命令借 `GenerateConsoleCtrlEvent`
  广播 Ctrl+C 打断引擎。
- 受限子进程创建标志去掉 `CREATE_NO_WINDOW`（只保留 `CREATE_UNICODE_ENVIRONMENT |
  CREATE_SUSPENDED`），并在注释里写清「受限令牌建不出控制台，只能继承」这一前提。
  代价：受限档下 `visible:true` 无法真给一个独立窗口（工具说明已同步）。
- `confinedChild.wait` 改为返回 `(exitCode, timedOut)`；`spawnConfinedWindows` 等输出搬运
  goroutine 收尾后再取缓冲，非零退出追加 `[进程退出异常: exit code N (0x…)`，
  `0xC0000142` 直接返回 `sandbox unavailable: 受限子进程启动即失败…`（命令没运行，不许装成功）。

`sandbox.go` / `child.go` / `jobs.go`
- 后台作业路径同样带回真实退出码：进度面板与作业面板不再写死 `exit 0`，
  「启动后秒退」的作业会显示异常结局并留日志。

## 三、验证

新回归测试 `sandbox_confined_console_test.go`（`//go:build windows`）——关键是它
**先 `DETACHED_PROCESS` 重拉自己，在「真正没有控制台」的进程里**跑完整执行路径，
否则有控制台的 `go test` 复现不出这个 bug：

```
OK 普通命令输出与副作用正常      （Write-Output + Set-Content 落盘）
OK 非零退出码如实上报            （exit 3 → [进程退出异常: exit code 3]）
OK [echo] -> "hi-echo"
OK [whoami] -> "ERROR: Access is denied.…[进程退出异常: exit code 1]"
OK [dir] / [out-file] -> True
OK [delete] -> GONE
OK [nonexistent] -> thiscmd_does_not_exist_xyz : The term '…' is not recognized…
OK cmd 路径可用
OK 工作区外写入仍被拒（沙箱语义未削弱）
```

`go build ./...`、`go vet .` 通过；`TestJobStopSettles` / `TestShutdownJobs*` 通过。

**生效方式**：改动在 Go 侧，需重编译并重启引擎（`rebuild-engine.ps1` 或关掉重开数字助理）。
