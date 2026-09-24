//go:build linux

package sandboxhelper

// Landlock 自限制器（Linux）：移植 DSH 官方「远端沙箱后端」的思路。
// 独立成小工具，同一段代码有两个用途：
//  1. 编译为静态 helper（jx-sandbox-linux-*）随主程序内嵌，首次需要时
//     经 SFTP 上传到 SSH 远程主机，命令在远端沙箱内执行（官方
//     sandbox-ssh：远端选择本地后端执行策略）；
//  2. 本地是 Linux 时，主程序以隐藏参数自重入本逻辑，再 exec 目标命令。
//
// 机制（Landlock，非特权、内核 5.13+）：
//   - prctl(PR_SET_NO_NEW_PRIVS) 禁止借 setuid 提权；
//   - 创建 ruleset，「handled」全部写类文件访问（写/删/建/截断/改名）；
//   - read-only：不加任何规则 → 所有写被内核拒绝；
//     workspace-write：给工作区与私有临时目录加 path-beneath 规则；
//   - landlock_restrict_self 后 exec 目标命令，限制随进程生死。
//   读与联网不在 Landlock 本套 handled 之内（对齐官方文件效果语义）。

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// Landlock 访问位（uapi/linux/landlock.h）
const (
	landlockAccessWriteFile  = 1 << 1
	landlockAccessRemoveDir  = 1 << 4
	landlockAccessRemoveFile = 1 << 5
	landlockAccessMakeChar   = 1 << 6
	landlockAccessMakeDir    = 1 << 7
	landlockAccessMakeReg    = 1 << 8
	landlockAccessMakeSock   = 1 << 9
	landlockAccessMakeFifo   = 1 << 10
	landlockAccessMakeSym    = 1 << 11
	landlockAccessRefer      = 1 << 12 // ABI 2+
	landlockAccessTruncate   = 1 << 13 // ABI 3+

	landlockCreateRuleset = 444
	landlockAddRule       = 445
	landlockRestrictSelf  = 446

	// O_PATH 在 Go 的 syscall 包各架构名不一致，直接用 Linux 常量（0x200000）
	oPath = 0x200000

	landlockRulePathBeneath = 1
	landlockCreateVersion   = 1 << 0

	prctlSetNoNewPrivs = 38
	sysPrctl           = 157
)

// Main 是 helper 的入口：
//   - jx-sandbox version：打印版本号退出（ensureRemoteHelper 校验用）
//   - jx-sandbox restrict --mode M --workspace W --temp T|auto -- cmd…
//
// 返回进程退出码。
func Main(args []string) int {
	if len(args) > 0 && args[0] == "version" {
		fmt.Println(Version)
		return 0
	}
	mode, ws, temp, cmd, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "用法: jx-sandbox restrict --mode read-only|workspace-write --workspace DIR --temp DIR|auto -- cmd…")
		fmt.Fprintln(os.Stderr, "      "+err.Error())
		return 2
	}
	child, err := Restrict(mode, ws, temp, cmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "JX_SANDBOX_UNAVAILABLE: "+err.Error())
		return 126
	}
	return unixExec(child)
}

// parseArgs 解析 restrict 子命令参数。
func parseArgs(args []string) (mode, ws, temp string, cmd []string, err error) {
	if len(args) == 0 || args[0] != "restrict" {
		return "", "", "", nil, fmt.Errorf("需要 restrict 子命令")
	}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--mode":
			i++
			if i < len(rest) {
				mode = rest[i]
			}
		case "--workspace":
			i++
			if i < len(rest) {
				ws = rest[i]
			}
		case "--temp":
			i++
			if i < len(rest) {
				temp = rest[i]
			}
		case "--":
			cmd = rest[i+1:]
			i = len(rest)
		}
	}
	if mode != "read-only" && mode != "workspace-write" {
		return "", "", "", nil, fmt.Errorf("非法 --mode: %q", mode)
	}
	if ws == "" {
		return "", "", "", nil, fmt.Errorf("缺少 --workspace")
	}
	if len(cmd) == 0 {
		return "", "", "", nil, fmt.Errorf("-- 之后缺少要执行的命令")
	}
	return mode, ws, temp, cmd, nil
}

// Restrict 对当前进程施加 Landlock 限制，返回要 exec 的命令。
// temp=auto 时自动创建私有临时目录并把它设为子进程的 TMPDIR。
func Restrict(mode, ws, temp string, cmd []string) ([]string, error) {
	_, handled := writeHandledSet()
	if handled == 0 {
		return nil, fmt.Errorf("内核不支持 Landlock（需要 Linux 5.13+）")
	}
	if mode == "workspace-write" && temp == "auto" {
		td, err := os.MkdirTemp("", "jx-sbx-")
		if err != nil {
			return nil, fmt.Errorf("创建私有临时目录失败: %w", err)
		}
		temp = td
	}
	// 显式或 auto 给出的临时目录都注入 TMPDIR：否则子进程往默认 /tmp 写
	// 临时文件会被 Landlock 拒掉（TMPDIR 指向授权目录才可写）。
	if mode == "workspace-write" && temp != "" {
		_ = os.Setenv("TMPDIR", temp)
	}
	if err := noNewPrivs(); err != nil {
		return nil, fmt.Errorf("PR_SET_NO_NEW_PRIVS 失败: %w", err)
	}
	fd, err := createRuleset(handled)
	if err != nil {
		return nil, fmt.Errorf("创建 Landlock ruleset 失败: %w", err)
	}
	defer syscall.Close(fd)
	if mode == "workspace-write" {
		for _, dir := range []string{ws, temp} {
			if dir == "" {
				continue
			}
			if err := addPathBeneath(fd, dir, handled); err != nil {
				return nil, fmt.Errorf("添加写授权规则 %s 失败: %w", dir, err)
			}
		}
	}
	// /dev/null sink（官方 POSIX 后端同款）：shell 与大量工具的
	// >/dev/null、2>/dev/null 重定向依赖它，两种模式都放行写。
	// 字符设备规则只接受 WRITE_FILE 单独使用（ABI5 实测：含 TRUNCATE/
	// READ/EXECUTE 任意组合都 EINVAL；O_TRUNC 对字符设备是 no-op 不需要位）。
	devNullBits := uint64(landlockAccessWriteFile)
	if err := addPathBeneath(fd, "/dev/null", devNullBits); err != nil {
		return nil, fmt.Errorf("放行 /dev/null sink 失败: %w", err)
	}
	if err := restrictSelf(fd); err != nil {
		return nil, fmt.Errorf("landlock_restrict_self 失败: %w", err)
	}
	return cmd, nil
}

// writeHandledSet 返回（内核 ABI 版本，支持的「写类」访问集合）；
// 不支持时返回 (0, 0)。
func writeHandledSet() (int, uint64) {
	ver, err := rulesetVersion()
	if err != nil || ver < 1 {
		return 0, 0
	}
	set := uint64(landlockAccessWriteFile | landlockAccessRemoveDir | landlockAccessRemoveFile |
		landlockAccessMakeChar | landlockAccessMakeDir | landlockAccessMakeReg |
		landlockAccessMakeSock | landlockAccessMakeFifo | landlockAccessMakeSym)
	if ver >= 2 {
		set |= landlockAccessRefer
	}
	if ver >= 3 {
		set |= landlockAccessTruncate
	}
	return ver, set
}

func rulesetVersion() (int, error) {
	v, _, errno := syscall.Syscall6(landlockCreateRuleset, 0, 0, landlockCreateVersion, 0, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(v), nil
}

func createRuleset(handled uint64) (int, error) {
	attr := handled
	fd, _, errno := syscall.Syscall(landlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func addPathBeneath(rulesetFd int, dir string, allowed uint64) error {
	fd, err := syscall.Open(dir, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var attr struct {
		allowedAccess uint64
		parentFd      int32
		pad           uint32
	}
	attr.allowedAccess = allowed
	attr.parentFd = int32(fd)
	_, _, errno := syscall.Syscall(landlockAddRule, uintptr(rulesetFd), landlockRulePathBeneath, uintptr(unsafe.Pointer(&attr)))
	if errno != 0 {
		// REFER/TRUNCATE 等新位在老 ABI ruleset 上已裁剪；目录级失败则原样报错
		return errno
	}
	return nil
}

func restrictSelf(rulesetFd int) error {
	if _, _, errno := syscall.Syscall(landlockRestrictSelf, uintptr(rulesetFd), 0, 0); errno != 0 {
		return errno
	}
	return nil
}

func noNewPrivs() error {
	if _, _, errno := syscall.Syscall6(sysPrctl, prctlSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

func unixExec(cmd []string) int {
	path := cmd[0]
	look, err := lookPath(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "JX_SANDBOX: 找不到命令 "+path)
		return 127
	}
	if err := syscall.Exec(look, cmd, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "JX_SANDBOX: exec 失败: "+err.Error())
		return 127
	}
	return 127
}

// lookPath 按 PATH 查找可执行文件（避免依赖 os/exec 的完整实现）。
func lookPath(file string) (string, error) {
	if strings.Contains(file, "/") {
		if err := syscall.Access(file, 1); err == nil {
			return file, nil
		}
		return "", fmt.Errorf("不可执行: %s", file)
	}
	path := os.Getenv("PATH")
	for _, dir := range strings.Split(path, ":") {
		if dir == "" {
			dir = "."
		}
		p := dir + "/" + file
		if err := syscall.Access(p, 1); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("PATH 中找不到 %s", file)
}
