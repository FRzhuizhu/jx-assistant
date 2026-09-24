//go:build windows

package main

// DPAPI（crypt32.dll）绑定：CryptProtectData / CryptUnprotectData。
// 用户级绑定（不传 CRYPTPROTECT_LOCAL_MACHINE）：密文只在当前 Windows
// 用户上下文可解。

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	crypt32                = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree          = windows.NewLazySystemDLL("kernel32.dll").NewProc("LocalFree")
)

// dataBlob 对应 Win32 DATA_BLOB { DWORD cbData; BYTE *pbData }。
type dataBlob struct {
	size uint32
	data *byte
}

const cryptProtectUIForbidden = 0x1

// dpapiProtect 加密一段明文（用户级 DPAPI）。
func dpapiProtect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, fmt.Errorf("dpapi: 明文为空")
	}
	in := dataBlob{size: uint32(len(plain)), data: &plain[0]}
	var out dataBlob
	r1, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r1 == 0 {
		return nil, fmt.Errorf("CryptProtectData 失败: %v", callErr)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	return append([]byte(nil), unsafe.Slice(out.data, out.size)...), nil
}

// dpapiUnprotect 解密 DPAPI 密文。
func dpapiUnprotect(cipher []byte) ([]byte, error) {
	if len(cipher) == 0 {
		return nil, fmt.Errorf("dpapi: 密文为空")
	}
	in := dataBlob{size: uint32(len(cipher)), data: &cipher[0]}
	var out dataBlob
	r1, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r1 == 0 {
		return nil, fmt.Errorf("CryptUnprotectData 失败: %v", callErr)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	return append([]byte(nil), unsafe.Slice(out.data, out.size)...), nil
}
