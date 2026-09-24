package main

// 沙箱统一入口：按会话策略档位（Grant.Policy）把命令送进对应的
// OS 级沙箱执行。移植自 DSH 官方的分层：
//
//	local Windows → 受限令牌 + ACL（sandbox_windows.go，对齐 sandbox-windows-acl）
//	local Linux   → Landlock（经 sandboxhelper 自限制后 exec，对齐 sandbox-local）
//	SSH remote    → 上传同一 helper 到远端执行（对齐 sandbox-ssh：
//	                「远端主机选择它安装的本地后端来执行策略」）
//	policy = full → 不受限（对齐 danger-full-access：消费者直接绕过 confine）
//
// 拒绝驱动的升级（对齐官方 bash 工具）：confined 执行的输出里检测到
// 访问被拒的标志后，附 [sandbox: …] 标记与升级提示；模型同轮带
// sandbox_permissions + justification 重试一次，由审批卡决定。

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"jxassistant/sandboxhelper"
)

// sandboxIntegrityLow 控制受限子进程是否降为低完整性级别（密级关卡）。
// 默认开：配合工作区/临时目录的 Low 标签，把「写/删工作区外对象」在内核
// 密级岗路线无关地拒掉（判定实验已证实父目录 FILE_DELETE_CHILD 删除路线
// 绕过 WRITE_RESTRICTED 双检——官方同款盲区）。config sandbox.integrityLevel
// = off 可回退官方同款行为（仅双检）。全局开关，启动时从 Config 初始化。
var sandboxIntegrityLow = true

// sandboxRun 是一次沙箱内执行的全部输入。
type sandboxRun struct {
	Dir       string // 子进程工作目录（cwd 本身不是边界）
	Command   string // 完整命令
	Shell     string // powershell | cmd（本地 Windows 用；远端忽略）
	Timeout   time.Duration
	Policy    string       // read-only | workspace-write（full 不进沙箱）
	Workspace string       // 写授权目录（工作区）
	Remote    *SessionMeta // 非nil = SSH 远程执行（取 Remote 连接信息）
}

// runConfined 按 policy 与会话环境选择沙箱后端执行命令。
// policy 为 full 时不应调用本函数（调用方直接走不受限路径）。
func (t *toolContext) runConfined(r sandboxRun) (string, error) {
	if r.Policy == permFull {
		return "", fmt.Errorf("沙箱内部错误：full 策略不应进入 runConfined")
	}
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return "", fmt.Errorf("sandbox unavailable: %w", err)
		}
		out, err := t.app.runRemoteConfined(t.ctx, rc, r)
		if err != nil {
			return "", err
		}
		return out + "\n[远程执行于 " + t.sess.Remote.Display() + "｜沙箱 " + r.Policy + "]", nil
	}
	return spawnConfinedLocal(t.ctx, r)
}

// spawnConfinedLocal 本地沙箱分派：Windows 受限令牌 / Linux Landlock。
func spawnConfinedLocal(ctx context.Context, r sandboxRun) (string, error) {
	switch {
	case isWindowsRuntime:
		wsAbs, err := filepath.Abs(r.Workspace)
		if err != nil {
			return "", fmt.Errorf("sandbox unavailable: %w", err)
		}
		r.Workspace = wsAbs
		return spawnConfinedWindows(ctx, r)
	case isLinuxRuntime:
		return spawnConfinedLinuxReexec(r)
	default:
		return "", fmt.Errorf("sandbox unavailable: 当前平台暂无沙箱后端；如确需执行，可带 sandbox_permissions=danger-full-access 与 justification 申请升级（将弹审批）")
	}
}

// startConfinedAsync 在沙箱内启动一个后台作业风格的进程：输出增量写入 w，
// ctx 取消时终止进程。返回一个等待 goroutine 已启动的标记（作业侧关 done）。
func startConfinedAsync(ctx context.Context, r sandboxRun, w io.Writer) (func() bool, error) {
	switch {
	case isWindowsRuntime:
		wsAbs, err := filepath.Abs(r.Workspace)
		if err != nil {
			return nil, fmt.Errorf("sandbox unavailable: %w", err)
		}
		r.Workspace = wsAbs
		child, err := startConfinedWindows(r)
		if err != nil {
			return nil, err
		}
		go child.drain(w)
		waitDone := make(chan struct{}, 1)
		go func() {
			child.wait(ctx, 0) // 0 = 不限时；终止只经 ctx（job_kill）
			child.cleanup()
			close(waitDone)
		}()
		wait := func() bool { <-waitDone; return true }
		return wait, nil
	case isLinuxRuntime:
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("sandbox unavailable: %w", err)
		}
		tempDir, err := os.MkdirTemp("", "jx-sbx-")
		if err != nil {
			return nil, fmt.Errorf("sandbox unavailable: 创建私有临时目录失败: %w", err)
		}
		wrapped := "cd " + shellQuote(r.Dir) + " && { " + r.Command + " ;}"
		args := []string{"__jx-sandbox-restrict", "restrict", "--mode", r.Policy,
			"--workspace", r.Workspace, "--temp", tempDir, "--", "/bin/sh", "-c", wrapped}
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Dir = r.Dir
		cmd.Env = sandboxEnv(false)
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Start(); err != nil {
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("sandbox unavailable: %w", err)
		}
		waitDone := make(chan struct{}, 1)
		go func() { _ = cmd.Wait(); os.RemoveAll(tempDir); close(waitDone) }()
		return func() bool { <-waitDone; return true }, nil
	default:
		return nil, fmt.Errorf("sandbox unavailable: 当前平台暂无沙箱后端")
	}
}

// runDeniedMarker 检查 confined 输出是否携带「访问被拒」类标志；
// 命中则返回官方风格的 [sandbox: …] 标记 + 升级提示（同轮重试一次）。
func runDeniedMarker(policy, out string) string {
	if policy == permFull {
		return ""
	}
	lower := strings.ToLower(out)
	sigs := []string{
		"access is denied", "拒绝访问", "required privilege", "unauthorized operation",
		"permission denied", "operation not permitted", "read-only file system",
		"eaccess", "eperm", "erofs", "not permitted to write", "jx_sandbox_unavailable",
	}
	hit := false
	for _, s := range sigs {
		if strings.Contains(lower, s) {
			hit = true
			break
		}
	}
	if !hit {
		return ""
	}
	wider := "danger-full-access"
	if policy == permReadOnly {
		wider = "workspace-write"
	}
	return fmt.Sprintf("\n[sandbox: write denied under %s mode] 若确需更宽权限，可在同一轮内带 sandbox_permissions=%s 与一句 justification 重试同一命令一次（会弹审批由用户决定）；被拒后请停止并说明，不要绕过。", policy, wider)
}

// ---------- SSH 远程沙箱（官方 sandbox-ssh 思路：远端用它的本地后端执行策略） ----------

// remoteSandboxInfo 缓存在 remoteClient 上：远端架构与 helper 路径。
// 定义在 remote.go 的 remoteClient 结构里（arch / helperPath 字段）。

// runRemoteConfined 把命令包进远端 helper 的沙箱执行。
// helper 首次需要时按远端架构上传（~/.jx-assistant/bin/）。
func (a *App) runRemoteConfined(ctx context.Context, rc *remoteClient, r sandboxRun) (string, error) {
	helper, err := a.ensureRemoteHelper(ctx, rc)
	if err != nil {
		return "", fmt.Errorf("sandbox unavailable: %w", err)
	}
	// helper exec /bin/sh -c 'cd dir && { cmd ;}'：cd 在沙箱内执行。
	// 外层 mktemp 建临时目录 + EXIT trap 清理：ssh 会话 shell 退出时必触发
	// （成功/失败/超时被杀都清理），堵住此前 --temp auto 的每命令泄漏。
	wrapped := "cd " + shellQuote(r.Dir) + " && { " + r.Command + " ;}"
	inner := shellQuote(helper) + " restrict --mode " + r.Policy +
		" --workspace " + shellQuote(r.Workspace) + " --temp \"$JX_TMP\" -- /bin/sh -c " + shellQuote(wrapped)
	cmdline := "JX_TMP=$(mktemp -d /tmp/jx-sbx-XXXXXX) && trap 'rm -rf \"$JX_TMP\"' EXIT && " + inner
	return rc.run(ctx, r.Workspace, cmdline, r.Timeout)
}

// ensureRemoteHelper 确保远端装有对应架构的沙箱 helper，返回其绝对路径。
func (a *App) ensureRemoteHelper(ctx context.Context, rc *remoteClient) (string, error) {
	rc.mu.Lock()
	if rc.helperPath != "" {
		defer rc.mu.Unlock()
		return rc.helperPath, nil
	}
	rc.mu.Unlock()

	// 探测远端架构（HOME 复用 remoteClient 的懒探测）
	out, err := rc.run(ctx, "/tmp", "uname -m", 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("探测远端架构失败: %w", err)
	}
	goarch := map[string]string{
		"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64",
	}[strings.TrimSpace(out)]
	if goarch == "" {
		return "", fmt.Errorf("远端架构 %q 无可用沙箱 helper（支持 x86_64/aarch64）", strings.TrimSpace(out))
	}

	bin, ok := helperBinary(goarch)
	if !ok {
		return "", fmt.Errorf("内嵌 helper 缺失（linux-%s）", goarch)
	}
	dest := rc.homeDir() + "/.jx-assistant/bin/jx-sandbox-linux-" + goarch

	sc, err := rc.sftpClient()
	if err != nil {
		return "", fmt.Errorf("sandbox unavailable: %w", err)
	}
	// 远端路径必须用 POSIX 的 path.Dir：filepath.Dir 在 Windows 宿主上会把
	// 分隔符转成反斜杠，SFTP 将其当作字面文件名字符，目录永远建不到位
	if err := rc.mkdirAll(path.Dir(dest)); err != nil {
		return "", fmt.Errorf("远端创建 helper 目录失败: %w", err)
	}
	needUpload := false
	if fi, serr := sc.Stat(dest); serr != nil || fi.Size() != int64(len(bin)) {
		needUpload = true
	} else {
		// 尺寸巧合相同也要校验语义版本：不符即重传（防旧语义 helper）
		out, verr := rc.run(ctx, "/tmp", shellQuote(dest)+" version", 10*time.Second)
		if verr != nil || strings.TrimSpace(out) != sandboxhelper.Version {
			needUpload = true
		}
	}
	if needUpload {
		if err := rc.writeFile(dest, bin); err != nil {
			return "", fmt.Errorf("上传沙箱 helper 失败: %w", err)
		}
		if err := sc.Chmod(dest, 0o755); err != nil {
			return "", fmt.Errorf("设置 helper 执行位失败: %w", err)
		}
		if out, verr := rc.run(ctx, "/tmp", shellQuote(dest)+" version", 10*time.Second); verr != nil ||
			strings.TrimSpace(out) != sandboxhelper.Version {
			return "", fmt.Errorf("sandbox unavailable: 上传后版本校验失败（远端输出 %q，期望 %s）",
				strings.TrimSpace(out), sandboxhelper.Version)
		}
	}

	rc.mu.Lock()
	rc.arch = goarch
	rc.helperPath = dest
	rc.mu.Unlock()
	return dest, nil
}
