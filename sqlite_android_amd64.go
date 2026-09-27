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
	"syscall"
	"unsafe"

	_ "unsafe"

	"modernc.org/libc"
)

//go:linkname sqliteASyscall modernc.org/sqlite/lib._aSyscall
var sqliteASyscall [29][3]uintptr // Tunix_syscall = {zName, pCurrent, pDefault}

// patchSqliteSyscallsForAndroid 替换 lstat/stat 表项（幂等）。
func patchSqliteSyscallsForAndroid() {
	for i := range sqliteASyscall {
		name := libc.GoString(sqliteASyscall[i][0])
		switch name {
		case "lstat":
			fp := jxFuncPtr(jxLstatImpl)
			sqliteASyscall[i][1] = fp
			sqliteASyscall[i][2] = fp // 防止 restore-defaults 路径清回被禁实现
		case "stat":
			fp := jxFuncPtr(jxStatImpl)
			sqliteASyscall[i][1] = fp
			sqliteASyscall[i][2] = fp
		}
	}
}

// jxFuncPtr 拿到任意无捕获函数首地址（ libc.TLS 首参约定与转译代码一致）。
func jxFuncPtr(f func(*libc.TLS, uintptr, uintptr) int32) uintptr {
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
