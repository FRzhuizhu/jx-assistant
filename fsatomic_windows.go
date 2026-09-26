//go:build windows

package main

// copySecurityDescriptorBestEffort 把 src 的属主/组/DACL/完整性标签复制到
// dst（对齐 DSH win32.ts 的 copyFileDaclWin32：原子替换不该丢原文件的显式
// ACL——工作区文件大多继承目录 ACL，少数带显式授权的文件替换后应保持）。
// 尽力而为：任一步失败静默放弃（dst 保持目录继承的默认安全描述符）。

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ownerSecurityInfo = 0x1
	groupSecurityInfo = 0x2
)

func copySecurityDescriptorBestEffort(src, dst string) {
	sp, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return
	}
	var sd *windows.SECURITY_DESCRIPTOR
	var owner, group *windows.SID
	var dacl *windows.ACL
	r1, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(sp)), seFileObject,
		ownerSecurityInfo|groupSecurityInfo|daclSecurityInfo|labelSecurityInfo,
		uintptr(unsafe.Pointer(&owner)), uintptr(unsafe.Pointer(&group)),
		uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd)))
	if r1 != 0 {
		return
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	dp, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return
	}
	procSetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(dp)), seFileObject,
		ownerSecurityInfo|groupSecurityInfo|daclSecurityInfo|labelSecurityInfo,
		uintptr(unsafe.Pointer(owner)), uintptr(unsafe.Pointer(group)),
		uintptr(unsafe.Pointer(dacl)), 0, 0)
}
