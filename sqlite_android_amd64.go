//go:build android && amd64

package main

// 安卓（x86_64）的 modernc sqlite 兼容补丁。
//
// 背景：安卓内核给应用进程装的 seccomp 白名单封杀了老式 stat 家族系统
// 调用（SYS_stat=4 / SYS_lstat=6；bionic 只走 newfstatat=262）。modernc
// sqlite 的 unixFullPathname 在解析数据库路径时会逐级 lstat（见
// _appendOnePathElement → Xlstat → _fstatat_kstat 的 SYS_lstat 捷径），
// 引擎第一次打开 jx.db 即被 SIGSYS 杀死。arm64 无此问题（该架构没有
// 旧式 stat 调用，musl 直接走 fstatat），桌面 Linux 的 seccmp 政策不同
// 也不受影响——所以本补丁只在 android && amd64 编译。
//
// 机制：sqlite 的 _aSyscall 表（未导出）每项 {zName, pCurrent, pDefault}
// 三个 uintptr，运行期经 pCurrent 间接调用系统调用。照 lib 包
// PatchIssue199 的先例（getpagesize 项的运行时替换），这里用
// go:linkname 绑定整张表，把名字为 lstat / stat 的表项换成 Go 实现——
// Go 的 syscall.Stat/Lstat 在 amd64 上恰好走 newfstatat，合法通过。
//
// 调用时机：main() 里首次打开数据库之前（引擎启动最早期）。

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	_ "unsafe"

	"modernc.org/libc"
)

//go:linkname sqliteASyscall modernc.org/sqlite/lib._aSyscall
var sqliteASyscall [29][3]uintptr // Tunix_syscall = {zName, pCurrent, pDefault}

// patchSqliteSyscallsForAndroid 替换被封杀的表项（幂等）。
//
// 封杀面（模拟器实测 + bionic 走 *_at 推断）分两类：
//  1. 旧式 stat 族：lstat=6 / stat=4（实测 SIGSYS）——bionic 走 newfstatat=262
//  2. 其余 legacy：unlink=87（实测 SIGSYS）等——bionic 走 unlinkat=263；
//     同类（有 *_at 替代而 policy 封旧号）一并预置：open/access/readlink/
//     rename/mkdir/symlink/fstat。
// 实测通过的（fcntl/read/write/ftruncate/mmap 等无 *_at 替代）不动。
func patchSqliteSyscallsForAndroid() {
	// 诊断：把表内全部可替换系统调用名打一次到 stderr（随引擎日志捕获）
	names := make([]string, 0, len(sqliteASyscall))
	for i := range sqliteASyscall {
		names = append(names, libc.GoString(sqliteASyscall[i][0]))
	}
	fmt.Fprintf(os.Stderr, "[jxpatch] sqlite aSyscall: %s\n", strings.Join(names, ","))

	patch := func(name, impl uintptr) {
		for i := range sqliteASyscall {
			if sqliteASyscall[i][0] == name {
				sqliteASyscall[i][1] = impl
				sqliteASyscall[i][2] = impl // 防止 restore-defaults 路径清回被禁实现
			}
		}
	}
	patch(jxNamePtr("lstat"), jxFuncPtr(jxLstatImpl))
	patch(jxNamePtr("stat"), jxFuncPtr(jxStatImpl))
	patch(jxNamePtr("fstat"), jxFuncPtr(jxFstatImpl))
	patch(jxNamePtr("access"), jxFuncPtr(jxAccessImpl))
	patch(jxNamePtr("open"), jxFuncPtr(jxOpenImpl))
	patch(jxNamePtr("unlink"), jxFuncPtr(jxUnlinkImpl))
	patch(jxNamePtr("readlink"), jxFuncPtr(jxReadlinkImpl))
	patch(jxNamePtr("rename"), jxFuncPtr(jxRenameImpl))
	patch(jxNamePtr("mkdir"), jxFuncPtr(jxMkdirImpl))
	patch(jxNamePtr("symlink"), jxFuncPtr(jxSymlinkImpl))
}

// jxNamePtr 在表内按名字取 FzName 指针（找不到返回 0，patch 自然跳过）。
func jxNamePtr(want string) uintptr {
	for i := range sqliteASyscall {
		if libc.GoString(sqliteASyscall[i][0]) == want {
			return sqliteASyscall[i][0]
		}
	}
	return 0
}

// jxFuncPtr 拿到任意无捕获函数首地址（ libc.TLS 首参约定与转译代码一致）。
func jxFuncPtr[T any](f T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&f))
}

// jxLstatImpl musl lstat(const char*, struct stat*) 语义：不跟随符号链接。
func jxLstatImpl(tls *libc.TLS, path, buf uintptr) int32 {
	return jxStatAt(tls, path, buf, _AT_SYMLINK_NOFOLLOW)
}

// jxStatImpl musl stat(const char*, struct stat*) 语义：跟随符号链接。
func jxStatImpl(tls *libc.TLS, path, buf uintptr) int32 {
	return jxStatAt(tls, path, buf, 0)
}

// linux _AT_* 常量未进 syscall 包的 amd64 面，就地取值（与内核 ABI 一致）
const (
	_AT_FDCWD             = -100
	_AT_SYMLINK_NOFOLLOW  = 0x100
)

func jxStatAt(tls *libc.TLS, path, buf uintptr, flags int32) int32 {
	var st syscall.Stat_t
	var err error
	if flags != 0 {
		err = syscall.Lstat(libc.GoString(path), &st)
	} else {
		err = syscall.Stat(libc.GoString(path), &st)
	}
	if err != nil {
		// errno 值与 musl 同一套编号；经 __errno_location 写回
		errnoLoc := (*int32)(unsafe.Pointer(libc.X__errno_location(tls)))
		if eno, ok := err.(syscall.Errno); ok {
			*errnoLoc = int32(eno)
		} else {
			*errnoLoc = 5 // EIO 兜底
		}
		return -1
	}
	// Go syscall.Stat_t 与 musl amd64 struct stat 布局逐字段一致
	// （144 字节，尾部差异为保留填充），整块覆盖安全。
	copy(unsafe.Slice((*byte)(unsafe.Pointer(buf)), 144),
		unsafe.Slice((*byte)(unsafe.Pointer(&st)), 144))
	return 0
}

// ---- 2026-09 模拟器实测扩展：unlink 等 legacy 封杀面 ----

// jxATFDCWD AT_FDCWD=-100 的 uintptr 形态（uintptr(-100) 常量转换会溢出报错）。
var jxATFDCWD = func() uintptr { v := int64(-100); return uintptr(v) }()

// _AT_EMPTY_PATH：fstatat 对 fd 生效（"" 路径 + 该标志）等价旧式 fstat。
const _AT_EMPTY_PATH = 0x1000

// jxEmptyPath 供 fstatat(fd, "", ...) 用。
var jxEmptyPath = [1]byte{0}

// jxSyscallAt 裸调系统调用并回写 errno（musl 同套编号，与 jxStatAt 一致）。
func jxSyscallAt(tls *libc.TLS, nr uintptr, a1, a2, a3, a4 uintptr) int32 {
	r, _, errno := syscall.Syscall6(nr, a1, a2, a3, a4, 0, 0)
	if errno != 0 {
		*(*int32)(unsafe.Pointer(libc.X__errno_location(tls))) = int32(errno)
		return -1
	}
	return int32(r)
}

// 各替换实现：参数个数按 SQLite os_unix.c 调用点，TLS 首参约定与转译代码一致。

func jxFstatImpl(tls *libc.TLS, fd, buf, _, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_NEWFSTATAT, fd, uintptr(unsafe.Pointer(&jxEmptyPath[0])), buf, _AT_EMPTY_PATH)
}

func jxAccessImpl(tls *libc.TLS, path, mode, _, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_FACCESSAT, jxATFDCWD, path, mode, 0)
}

func jxOpenImpl(tls *libc.TLS, path, flags, mode, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_OPENAT, jxATFDCWD, path, flags, mode)
}

func jxUnlinkImpl(tls *libc.TLS, path, _, _, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_UNLINKAT, jxATFDCWD, path, 0, 0)
}

func jxReadlinkImpl(tls *libc.TLS, path, buf, bufsiz, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_READLINKAT, jxATFDCWD, path, buf, bufsiz)
}

func jxRenameImpl(tls *libc.TLS, oldPath, newPath, _, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_RENAMEAT, jxATFDCWD, oldPath, jxATFDCWD, newPath)
}

func jxMkdirImpl(tls *libc.TLS, path, mode, _, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_MKDIRAT, jxATFDCWD, path, mode, 0)
}

func jxSymlinkImpl(tls *libc.TLS, target, linkpath, _, _ uintptr) int32 {
	return jxSyscallAt(tls, syscall.SYS_SYMLINKAT, target, jxATFDCWD, linkpath, 0)
}
