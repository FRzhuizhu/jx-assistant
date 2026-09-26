//go:build windows

package main

// canonicalNative 用 NTFS 对象管理器视角解析路径（GetFinalPathNameByHandle，
// 等价于 Node 的 realpathSync.native——DSH fs-sandbox 同款）：junction/
// 符号链接/subst 盘符全部落到最终物理路径。Go 的 filepath.EvalSymlinks 在
// Windows 上不解析 junction（go1.26 实测返回原路径不报错），围栏判断必须
// 走原生。目标不存在时解析其父目录再拼回末段；两级都失败返回 false
// （调用方回退 EvalSymlinks/Clean 兜底）。

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func canonicalNative(p string) (string, bool) {
	if fp, err := finalPathName(p); err == nil {
		return fp, true
	}
	if fp, err := finalPathName(filepath.Dir(p)); err == nil {
		return filepath.Join(fp, filepath.Base(p)), true
	}
	return "", false
}

// finalPathName 打开对象（文件或目录，FILE_FLAG_BACKUP_SEMANTICS）后查
// 最终路径。0 desired access：只查名字，不需要读写权。
func finalPathName(p string) (string, error) {
	p16, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p16, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	if n > uint32(len(buf)) { // 缓冲不足：返回值是含结尾 NUL 的所需长度
		buf = make([]uint16, n)
		if n, err = windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0); err != nil {
			return "", err
		}
	}
	s := windows.UTF16ToString(buf[:n])
	// \\?\C:\… → C:\…；\\?\UNC\server\share → \\server\share
	if strings.HasPrefix(s, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(s, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(s, `\\?\`), nil
}
