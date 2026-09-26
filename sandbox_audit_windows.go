//go:build windows

package main

// 沙箱常驻授权审计/清理（P1-4）：工作区 ACE 与密级标签是设计上的常驻授权
// （跨会话复用，官方同款），副作用是换工作区/删会话后目录上留有残留。
// 本文件实现审计扫描与确认后清理：
//   - GRANT 行：DACL 里无法解析为账户名的合成能力 SID allow ACE
//     （本产品 S-1-80-* 与 DSH 官方 S-1-4-* 两代形态都覆盖）；
//   - LABEL 行：强制密级标签为 Low 的对象（低密进程可写的地盘标记）。
// revoke=true 时摘掉合成 ACE 并把标签还原 Medium（与默认等价）。

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// auditSandboxGrants 扫描 roots 下的残留授权；revoke 时一并清理。
// oursOnly=true 只处理本产品 S-1-80-*（自动清理钩子用，DSH 旧残留留给
// 用户确认）；false 连 DSH 官方 S-1-4-* 一起报（acl-audit CLI 用）。
// 返回逐条报告（"GRANT\tpath\tsid" / "LABEL\tpath\tlow"）。
func auditSandboxGrants(roots []string, revoke, oursOnly bool) ([]string, error) {
	var lines []string
	synthetic := map[string]bool{} // sid 字节串 → 是否合成能力 SID（缓存）
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return lines, fmt.Errorf("解析路径 %s: %w", root, err)
		}
		err = filepath.Walk(abs, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // 单个条目读不到跳过
			}
			if info.IsDir() && info.Mode()&os.ModeSymlink != 0 {
				return filepath.SkipDir // 不跟 junction/symlink 越界
			}
			for _, sideb := range allowSyntheticSids(p, synthetic) {
				if oursOnly && !isOurSyntheticSid(sideb) {
					continue
				}
				lines = append(lines, fmt.Sprintf("GRANT\t%s\t%s", p, sidToString(sideb)))
				if revoke {
					if err := stripAllowAceBySid(p, sideb); err != nil {
						lines = append(lines, fmt.Sprintf("ERR\t%s\t%v", p, err))
					}
				}
			}
			if hasLowLabel(p) {
				lines = append(lines, fmt.Sprintf("LABEL\t%s\tlow", p))
				if revoke {
					if err := stripLowLabel(p); err != nil {
						lines = append(lines, fmt.Sprintf("ERR\t%s\t%v", p, err))
					}
				}
			}
			return nil
		})
		if err != nil {
			return lines, err
		}
	}
	return lines, nil
}

// cleanupResidualGrants 清理本产品在工作区树上的残留（S-1-80-* ACE 与 Low
// 标签）。会话删除/工作区切换钩子调用；返回清理条目数。
func cleanupResidualGrants(roots []string) (int, error) {
	lines, err := auditSandboxGrants(roots, true, true)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "ERR\t") {
			n++
		}
	}
	return n, nil
}

// restoreStaleIntegrityLabels 把历史密级沙箱（机制已撤除，见 sandbox_windows.go
// 文件头）打在工作区树上的常驻 Low 完整性标签还原为 Medium。只处理根目录
// 带 Low 标签的树（普通用户工作区不会有根级 Low 标签；浏览器等组件的 Low
// 缓存只标自己的文件，不会标工作区根）；根干净即跳过、零开销。
//
// 还原采用「WO 引导」三步（真实大树实测验证）：直接替换根上带 (OI)(CI) 的
// Low 标签会触发向下自动传播，传播逐子对象改标签需要 WRITE_OWNER，Modify
// 档 ACL 没有 → 整个调用失败（实测 Win32 1299/5），且根上的继承 Low 会经
// 懒传播不断把子文件标签污染回去。故先在根上并入当前用户可继承的
// WRITE_OWNER ACE 引导传播铺权限，再换根标签（一次传播治愈全树），再
// /T 补扫未重同步的残余，最后摘掉引导 ACE。
func restoreStaleIntegrityLabels(roots []string) (int, error) {
	n := 0
	var firstErr error
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			continue
		}
		if !hasLowLabel(abs) {
			continue
		}
		_, _ = grantWriteOwnerAce(abs, true) // 引导 ACE，尽力而为（失败仍尝试换标签）
		_, flipErr := exec.Command("icacls", abs, "/setintegritylevel", "M").CombinedOutput()
		_, sweepErr := exec.Command("icacls", abs,
			"/setintegritylevel", "M", "/T", "/C", "/Q").CombinedOutput()
		_ = stripWriteOwnerAce(abs)
		if flipErr != nil || sweepErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("icacls %s: flip=%v sweep=%v", abs, flipErr, sweepErr)
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// isOurSyntheticSid 只认本产品 sidForPath 形态（S-1-80-<4 子权威>）。
func isOurSyntheticSid(sideb []byte) bool {
	return len(sideb) == 24 && sideb[1] == 4 && sideb[7] == 80
}

// allowSyntheticSids 列出对象 DACL 里「合成能力 SID」的 allow ACE（sid 字节）。
// 判定：SID 无法解析为账户名（LookupAccountSid 失败）且不是登录会话 SID。
func allowSyntheticSids(path string, cache map[string]bool) [][]byte {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	var sd *windows.SECURITY_DESCRIPTOR
	var dacl *windows.ACL
	r1, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInfo,
		0, 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd)))
	if r1 != 0 {
		return nil
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	sb, err := aclBytes(dacl)
	if err != nil {
		return nil
	}
	var out [][]byte
	for i := 8; i+8 <= len(sb); {
		size := int(binary.LittleEndian.Uint16(sb[i+2:]))
		if size < 8 || i+size > len(sb) {
			break
		}
		if sb[i] == accessAllowedACE {
			sideb := append([]byte(nil), sb[i+8:i+size]...)
			key := string(sideb)
			isSyn, ok := cache[key]
			if !ok {
				isSyn = isSyntheticSid(sideb)
				cache[key] = isSyn
			}
			if isSyn {
				out = append(out, sideb)
			}
		}
		i += size
	}
	return out
}

// isSyntheticSid 判断是否为沙箱合成能力 SID。只认两种已知形态，避免把
// AppContainer 包能力 SID（S-1-15-3-*，WebView2 等合法使用）等系统 SID
// 误报为残留：
//   - 本产品：S-1-80-<4 子权威>（sidForPath 从路径 SHA-256 派生）
//   - DSH 官方：S-1-4-<2 或 3 子权威>（workspaceWriteSid / tempWriteSid）
func isSyntheticSid(sideb []byte) bool {
	if len(sideb) < 12 {
		return false
	}
	n := int(sideb[1])
	if n < 2 || n > 4 || 8+4*n != len(sideb) {
		return false
	}
	auth := uint64(0)
	for i := 2; i < 8; i++ {
		auth = auth<<8 | uint64(sideb[i])
	}
	switch auth {
	case 80: // 本产品 S-1-80-*（恰 4 子权威）
		return n == 4
	case 4: // DSH 官方 S-1-4-x-y（temp 为 …-1，3 子权威）
		return n == 2 || n == 3
	}
	return false
}

// hasLowLabel 判断对象强制标签是否为 Low（S-1-16-4096）。
func hasLowLabel(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	var sd *windows.SECURITY_DESCRIPTOR
	var sacl *windows.ACL
	r1, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(p)), seFileObject, labelSecurityInfo,
		0, 0, 0, uintptr(unsafe.Pointer(&sacl)), uintptr(unsafe.Pointer(&sd)))
	if r1 != 0 {
		return false
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	sb, err := aclBytes(sacl)
	if err != nil {
		return false
	}
	lowb, err := sidBytes(lowIntegritySid())
	if err != nil {
		return false
	}
	for i := 8; i+8 <= len(sb); {
		size := int(binary.LittleEndian.Uint16(sb[i+2:]))
		if size < 8 || i+size > len(sb) {
			break
		}
		if sb[i] == systemMandatoryLabelAce && string(sb[i+8:i+size]) == string(lowb) {
			return true
		}
		i += size
	}
	return false
}

// stripAllowAceBySid 摘掉对象 DACL 里指定 SID 的全部 allow ACE（不限掩码：
// 本产品 0x112156 与官方 0x110156 两代形态都清）。找不到即视为已净。
func stripAllowAceBySid(path string, sideb []byte) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var sd *windows.SECURITY_DESCRIPTOR
	var dacl *windows.ACL
	r1, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInfo,
		0, 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd)))
	if r1 != 0 {
		return fmt.Errorf("GetNamedSecurityInfo(%s) 失败: %d", path, r1)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	sb, err := aclBytes(dacl)
	if err != nil {
		return err
	}
	// 逐条过滤掉匹配 ACE（可能不止一条）
	out := make([]byte, 0, len(sb))
	out = append(out, sb[:8]...)
	removed := 0
	for i := 8; i+8 <= len(sb); {
		size := int(binary.LittleEndian.Uint16(sb[i+2:]))
		if size < 8 || i+size > len(sb) {
			break
		}
		if sb[i] == accessAllowedACE && string(sb[i+8:i+size]) == string(sideb) {
			removed++
		} else {
			out = append(out, sb[i:i+size]...)
		}
		i += size
	}
	if removed == 0 {
		return nil
	}
	if pad := (4 - len(out)%4) % 4; pad > 0 {
		out = append(out, make([]byte, pad)...)
	}
	binary.LittleEndian.PutUint16(out[2:], uint16(len(out)))
	binary.LittleEndian.PutUint16(out[4:], uint16(int(binary.LittleEndian.Uint16(sb[4:]))-removed))
	nptr := (*windows.ACL)(unsafe.Pointer(&out[0]))
	r1, _, _ = procSetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInfo,
		0, 0, uintptr(unsafe.Pointer(nptr)), 0)
	if r1 != 0 {
		return fmt.Errorf("SetNamedSecurityInfo(摘ACE %s) 失败: Win32 %d", path, r1)
	}
	return nil
}

// sidToString 把 SID 字节格式化为 S-1-… 文本。
func sidToString(sideb []byte) string {
	if len(sideb) < 8 {
		return "?"
	}
	n := int(sideb[1])
	if 8+4*n > len(sideb) {
		return "?"
	}
	auth := uint64(0)
	for i := 2; i < 8; i++ {
		auth = auth<<8 | uint64(sideb[i])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "S-%d-%d", sideb[0], auth)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "-%d", binary.LittleEndian.Uint32(sideb[8+i*4:]))
	}
	return b.String()
}
