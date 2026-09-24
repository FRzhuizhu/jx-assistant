package main

// 临时探针（P0-1a 实现前的两个事实判定，完成后删除）：
// A. grantWriteAce 的可继承 ACE 是否传播到「既有」子文件（决定是否需要全树补打）；
// B. 硬链接写穿透在本实现里是否真实存在（决定要不要做多链接跳过）。

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSandboxProbePropagation(t *testing.T) {
	ws := t.TempDir()
	// 既有文件：在授权之前创建
	pre := filepath.Join(ws, "pre.txt")
	if err := os.WriteFile(pre, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	wsSid := sidForPath(ws)
	if err := grantWriteAce(ws, wsSid); err != nil {
		t.Fatalf("grantWriteAce: %v", err)
	}

	// 探针A：既有文件的 DACL 里是否出现了 wsSid 的 ACE
	if !fileHasSidAce(pre, wsSid) {
		t.Logf("探针A结论：ACE 未传播到既有子文件 → 需要全树补打（materialize walk）")
	} else {
		t.Logf("探针A结论：ACE 已传播到既有子文件（系统自动传播）→ 补打可省")
	}

	// 探针A2：沙箱内对既有文件写入是否成功
	r := sandboxRun{Dir: ws, Command: `Add-Content -LiteralPath '` + pre + `' -Value more`,
		Shell: "powershell", Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws}
	out, err := spawnConfinedWindows(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(pre)
	if strings.Contains(string(data), "more") {
		t.Logf("探针A2结论：沙箱可写既有文件 ✓（输出 %q）", strings.TrimSpace(out))
	} else {
		t.Logf("探针A2结论：沙箱不可写既有文件 → 潜在产品缺陷！输出 %q", strings.TrimSpace(out))
	}

	// 探针B：硬链接写穿透
	outside := t.TempDir()
	ext := filepath.Join(outside, "ext.txt")
	if err := os.WriteFile(ext, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "hardlink.txt")
	if err := os.Link(ext, link); err != nil {
		t.Skipf("创建硬链接失败: %v", err)
	}
	r2 := sandboxRun{Dir: ws, Command: `Add-Content -LiteralPath '` + link + `' -Value hacked`,
		Shell: "powershell", Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws}
	out2, err := spawnConfinedWindows(context.Background(), r2)
	if err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(ext)
	if strings.Contains(string(data2), "hacked") {
		t.Logf("探针B结论：⚠ 硬链接写穿透真实存在（输出 %q）", strings.TrimSpace(out2))
	} else {
		t.Logf("探针B结论：硬链接写被拦 ✓（输出 %q）", strings.TrimSpace(out2))
	}
	t.Logf("探针B2：ext 共享 SD 含 wsSid ACE = %v", fileHasSidAce(ext, wsSid))
}

// TestSandboxProbeMIC2 密级关卡补充判定：NUL 重定向、硬链接顺序、Everyone 位置。
func TestSandboxProbeMIC2(t *testing.T) {
	// C) 2>NUL / >NUL 重定向（Low IL 写 NUL 设备——设备对象的密级标签决定）
	ws := t.TempDir()
	r := sandboxRun{Dir: ws, Command: `cmd /c "echo nul-ok > NUL 2> NUL & echo done"`,
		Shell: "powershell", Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws}
	out, err := spawnConfinedWindows(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("探针C：NUL 重定向后命令异常: %q", strings.TrimSpace(out))
	} else {
		t.Logf("探针C结论：NUL 重定向正常 ✓")
	}

	// D) 硬链接顺序2：先建链（指向外部文件）后首次授权——标签传播会不会
	// 把共享 SD 打成 Low、放行写外部？
	ws2 := t.TempDir()
	outside := t.TempDir()
	ext := filepath.Join(outside, "ext2.txt")
	if err := os.WriteFile(ext, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws2, "hl2.txt")
	if err := os.Link(ext, link); err != nil {
		t.Skipf("创建硬链接失败: %v", err)
	}
	r2 := sandboxRun{Dir: ws2, Command: `Add-Content -LiteralPath '` + link + `' -Value hacked`,
		Shell: "powershell", Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws2}
	out2, err := spawnConfinedWindows(context.Background(), r2)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ext)
	if strings.Contains(string(data), "hacked") {
		t.Errorf("探针D：⚠ 先建链后授权顺序硬链接写穿透！输出 %q", strings.TrimSpace(out2))
	} else {
		t.Logf("探针D结论：先建链后授权顺序也被拦 ✓")
	}

	// E) Everyone 可写位置（官方自报缺口1）：给临时目录显式授 Everyone:(M)
	everyoneDir := t.TempDir()
	ev, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := grantWriteAce(everyoneDir, ev); err != nil {
		t.Fatalf("授 Everyone ACE 失败: %v", err)
	}
	probe := filepath.Join(everyoneDir, "ev.txt")
	r3 := sandboxRun{Dir: ws, Command: `Set-Content -LiteralPath '` + probe + `' -Value x`,
		Shell: "powershell", Timeout: 60 * time.Second, Policy: permWorkspace, Workspace: ws}
	out3, err := spawnConfinedWindows(context.Background(), r3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe); err == nil {
		t.Errorf("探针E：⚠ Everyone 可写位置写入成功（官方缺口1 未堵）！输出 %q", strings.TrimSpace(out3))
	} else {
		t.Logf("探针E结论：Everyone 可写位置也被密级岗拦住 ✓")
	}
}
func fileHasSidAce(path string, sid *windows.SID) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	var sd *windows.SECURITY_DESCRIPTOR
	var dacl *windows.ACL
	r1, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInfo,
		0, 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd)))
	if r1 != 0 {
		return false
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	sb, err := aclBytes(dacl)
	if err != nil {
		return false
	}
	sideb, err := sidBytes(sid)
	if err != nil {
		return false
	}
	for i := 8; i+8 <= len(sb); {
		size := int(binary.LittleEndian.Uint16(sb[i+2:]))
		if size < 8 || i+size > len(sb) {
			break
		}
		if sb[i] == accessAllowedACE && string(sb[i+8:i+size]) == string(sideb) {
			return true
		}
		i += size
	}
	return false
}
