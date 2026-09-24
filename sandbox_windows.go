//go:build windows

package main

// Windows 沙箱后端：移植 DSH 官方 sandbox-windows-acl 的思路
// （受限令牌 + ACL 授权，非 1:1 代码）。
//
// 机制（对齐官方 AclSandbox）：
//   1. 从「路径」派生出两个合成 SID（workspace SID / temp SID）——
//      同一路径永远得到同一 SID，作为长期授权的稳定身份；
//   2. 在工作区目录和本次运行的私有临时目录上写入允许该 SID 写入的
//      ACE（含继承，子文件/子目录同样可写）；工作区 ACE 长期保留
//      （跨次复用，同官方 standing grant），临时目录随目录删除消失；
//   3. 用 CreateRestrictedToken 造出带这两个「仅限写检查」 restricting
//      SID 的受限令牌（并剥离全部特权），再用它启动子进程；
//   4. 之后子进程的每一次写访问都同时过「正常 DACL 检查」和「仅看
//      restricting SID 的检查」：除工作区/临时目录外，任何写都被内核
//      拒绝。读不受限（官方同款语义，WRITE_RESTRICTED）。
//   5. fail closed：任何一步 Win32 失败，子进程宁可不启动。
//
// 已知边界（官方同款，自报 partial）：Everyone 可写的位置与 NTFS 硬
// 链接可以绕路写；只限写，不限读与联网。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	aclRevision      = 2
	accessAllowedACE = 0
	// ACE 继承标志
	objectInherit    = 0x01
	containerInherit = 0x02
	inheritedACE     = 0x10
	// CreateRestrictedToken Flags（官方 win32-abi.ts 同值 0x1|0x4|0x8）：
	// 剥全部特权 + LUA（标准用户态）+ 写受限模式
	restrictedTokenFlags = 0x1 | 0x4 | 0x8
	// 令牌组属性：登录会话 SID
	seGroupLogonId = 0xC0000000
	// 文件完全访问（官方默认 DACL 补授用 FILE_ALL_ACCESS）
	fileAllAccess = 0x1F01FF
	// Get/SetNamedSecurityInfo 的对象类型与信息位
	seFileObject     = 1
	daclSecurityInfo = 0x4
	// DACL 保护位：设置的 DACL 即最终 DACL，不再合并继承
	protectedDaclSecurityInfo = 0x80000000
	// 写授权掩码：官方 GRANT_MASK —— 写/追加/写属性/写EA/DELETE/DELETE_CHILD/
	// 同步，刻意剔除 READ_CONTROL（受限子进程不得改 DACL/夺所有权以逃逸）
	fileGenericWrite = 0x112156
)

var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")
	procGetNamedSecurityInfoW = advapi32.NewProc("GetNamedSecurityInfoW")
	procSetNamedSecurityInfoW = advapi32.NewProc("SetNamedSecurityInfoW")
	procGetTokenInformation   = advapi32.NewProc("GetTokenInformation")
	procSetTokenInformation   = advapi32.NewProc("SetTokenInformation")
)

// sidForPath 从路径派生稳定合成 SID：S-1-80-<h0>-<h1>-<h2>-<h3>。
// 权威值 80 未被 Windows 语义占用，可安全用作私有授权身份。
// windows.SID 不开放构造，这里手工拼 SID 二进制（rev=1, count=4）后转型。
func sidForPath(p string) *windows.SID {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimRight(p, "\\/") + "\\")))
	b := make([]byte, 8+4*4)
	b[0] = 1 // SID_REVISION
	b[1] = 4 // SubAuthorityCount
	// IdentifierAuthority：6 字节大端，值 80
	b[2], b[3], b[4], b[5], b[6], b[7] = 0, 0, 0, 0, 0, 80
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint32(b[8+i*4:], binary.BigEndian.Uint32(sum[i*4:i*4+4]))
	}
	return (*windows.SID)(unsafe.Pointer(&b[0]))
}

// sidBytes 把 *windows.SID 序列化为自包含字节（用于拼 ACE）。
func sidBytes(sid *windows.SID) ([]byte, error) {
	n := windows.GetLengthSid(sid)
	if n == 0 || n > 68 {
		return nil, fmt.Errorf("非法 SID 长度 %d", n)
	}
	b := make([]byte, n)
	if err := windows.CopySid(uint32(n), (*windows.SID)(unsafe.Pointer(&b[0])), sid); err != nil {
		return nil, err
	}
	return b, nil
}

// grantWriteAce 把「允许 writeSID 写入」的 ACE 合入目录 DACL（幂等）。
// 任何失败都返回错误——调用方必须 fail closed。
func grantWriteAce(dir string, writeSID *windows.SID) error {
	dirPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	var sd *windows.SECURITY_DESCRIPTOR
	var dacl *windows.ACL
	r1, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(dirPtr)), seFileObject, daclSecurityInfo,
		0, 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd)))
	if r1 != 0 {
		return fmt.Errorf("GetNamedSecurityInfo(%s) 失败: %d", dir, r1)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))

	sb, err := aclBytes(dacl)
	if err != nil {
		return err
	}
	sideb, err := sidBytes(writeSID)
	if err != nil {
		return err
	}
	// 已存在同 SID 的 allow ACE 则幂等返回（ACE: type0 flags mask sid）
	for i := 0; i+8+len(sideb) <= len(sb); {
		size := int(binary.LittleEndian.Uint16(sb[i+2:]))
		if size < 8 || i+size > len(sb) {
			break
		}
		if sb[i] == accessAllowedACE && binary.LittleEndian.Uint32(sb[i+4:]) == fileGenericWrite &&
			string(sb[i+8:i+size]) == string(sideb) {
			return nil
		}
		i += size
	}
	// 新 ACE：type(1) flags(1) size(2) mask(4) sid
	ace := make([]byte, 8+len(sideb))
	ace[0] = accessAllowedACE
	ace[1] = objectInherit | containerInherit
	binary.LittleEndian.PutUint16(ace[2:], uint16(len(ace)))
	binary.LittleEndian.PutUint32(ace[4:], fileGenericWrite)
	copy(ace[8:], sideb)

	// 插入位置：第一个继承 ACE 之前（保持显式 ACE 在前），否则末尾
	pos := len(sb)
	for i := 8; i+8 <= len(sb); {
		size := int(binary.LittleEndian.Uint16(sb[i+2:]))
		if size < 8 || i+size > len(sb) {
			pos = len(sb)
			break
		}
		if sb[i+1]&inheritedACE != 0 {
			pos = i
			break
		}
		i += size
	}
	count := int(binary.LittleEndian.Uint16(sb[4:]))
	nacl := make([]byte, 8+len(sb)-8+len(ace))
	nacl[0], nacl[1] = aclRevision, 0
	binary.LittleEndian.PutUint16(nacl[2:], uint16(len(nacl)))
	binary.LittleEndian.PutUint16(nacl[4:], uint16(count+1))
	copy(nacl[8:], sb[8:pos])
	copy(nacl[8+pos-8:], ace)
	copy(nacl[8+pos-8+len(ace):], sb[pos:])
	// 对齐到 DWORD
	if pad := (4 - len(nacl)%4) % 4; pad > 0 {
		nacl = append(nacl, make([]byte, pad)...)
		binary.LittleEndian.PutUint16(nacl[2:], uint16(len(nacl)))
	}

	nptr := (*windows.ACL)(unsafe.Pointer(&nacl[0]))
	r1, _, _ = procSetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(dirPtr)), seFileObject,
		daclSecurityInfo|protectedDaclSecurityInfo,
		0, 0, uintptr(unsafe.Pointer(nptr)), 0)
	if r1 != 0 {
		return fmt.Errorf("SetNamedSecurityInfo(%s) 失败: %d", dir, r1)
	}
	return nil
}

// aclHeader 读 ACL 头部 8 字节（rev, sbz1, size u16, count u16, sbz2）。
// x/sys 的 windows.ACL 字段未导出，只能按内存布局读。
func aclHeader(acl *windows.ACL) *[8]byte { return (*[8]byte)(unsafe.Pointer(acl)) }

// aclBytes 把 *windows.ACL 指向的内存读成字节切片。
func aclBytes(acl *windows.ACL) ([]byte, error) {
	if acl == nil {
		b := make([]byte, 8)
		b[0] = aclRevision
		binary.LittleEndian.PutUint16(b[2:], 8)
		return b, nil
	}
	size := binary.LittleEndian.Uint16(aclHeader(acl)[2:4])
	if size < 8 {
		b := make([]byte, 8)
		b[0] = aclRevision
		binary.LittleEndian.PutUint16(b[2:], 8)
		return b, nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(acl)), size), nil
}

// confinedChild 是一个已启动的受限子进程（前台与后台作业共用）。
type confinedChild struct {
	proc, thread windows.Handle
	stdoutR      *os.File
	stderrR      *os.File
	tempDir      string
}

// kill 终止子进程（job_kill / 超时用）。
func (c *confinedChild) kill() { windows.TerminateProcess(c.proc, 1) }

// cleanup 释放句柄并删除私有临时目录（授权 ACE 随目录消失）。
func (c *confinedChild) cleanup() {
	windows.CloseHandle(c.proc)
	windows.CloseHandle(c.thread)
	c.stdoutR.Close()
	c.stderrR.Close()
	os.RemoveAll(c.tempDir)
}

// startConfinedWindows 用受限令牌在沙箱内启动命令（官方 AclSandbox 思路）。
// 任何准备步骤失败都返回错误且绝不以无沙箱状态放行子进程（fail closed）。
func startConfinedWindows(r sandboxRun) (*confinedChild, error) {
	// 1) 私有临时目录（本次运行独占，官方为每会话；此处每运行一次，等价更强）
	tempDir, err := os.MkdirTemp("", "jx-sbx-")
	if err != nil {
		return nil, fmt.Errorf("sandbox unavailable: 创建私有临时目录失败: %w", err)
	}

	// 2) 写授权：工作区（长期 ACE，跨次复用）+ 私有临时目录（随目录消失）
	wsSid := sidForPath(r.Workspace)
	tempSid := sidForPath(tempDir)
	if err := grantWriteAce(r.Workspace, wsSid); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: 授权工作区失败: %w", err)
	}
	if err := grantWriteAce(tempDir, tempSid); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: 授权临时目录失败: %w", err)
	}

	// 3) 受限令牌（官方 token.ts 配方）：
	//   OpenProcessToken 要含 TOKEN_ASSIGN_PRIMARY —— CreateRestrictedToken
	//   返回句柄继承源句柄的访问权，缺了它 CreateProcessAsUser 直接拒绝访问。
	//   restricting 列表按档位：
	//     read-only:       [logon SID, Everyone]
	//     workspace-write: [logon SID, Everyone, 工作区SID, 临时目录SID]
	//   logon SID 保住 WinSta0/桌面等每登录对象（缺了子进程 0xC0000142 崩）；
	//   Everyone 保住系统目录的读/执行；写只认我们授了 ACE 的合成 SID，
	//   Everyone 可写的位置就是官方文档承认的 partial 缺口。
	worldSid, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: 构造 Everyone SID 失败: %w", err)
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|
			windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_SESSIONID, &tok); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: 打开进程令牌失败: %w", err)
	}
	defer tok.Close()
	logonSid, err := tokenLogonSid(tok)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: %w", err)
	}
	restricting := []windows.SIDAndAttributes{
		{Sid: logonSid, Attributes: 0},
		{Sid: worldSid, Attributes: 0},
	}
	if r.Policy == permWorkspace {
		restricting = append(restricting,
			windows.SIDAndAttributes{Sid: wsSid, Attributes: 0},
			windows.SIDAndAttributes{Sid: tempSid, Attributes: 0})
	}
	var restricted windows.Token
	r1, _, callErr := procCreateRestrictedToken.Call(
		uintptr(tok), restrictedTokenFlags, 0, 0, 0, 0,
		uintptr(len(restricting)), uintptr(unsafe.Pointer(&restricting[0])), uintptr(unsafe.Pointer(&restricted)))
	if r1 == 0 {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: CreateRestrictedToken 失败: %v", callErr)
	}
	defer restricted.Close()

	// 3b) 令牌默认 DACL 补授（官方 setTokenDefaultDaclGrant）：受限令牌创建的
	// 新对象（管道、临时文件）默认 DACL 里没有任何 restricting SID 的 ACE，
	// pass-2 检查会在创建时就拒绝。给默认 DACL 并入一条该 SID 的完全访问 ACE，
	// 对象创建改由父容器的 DACL 把关（工作区外的文件依旧创建不出来）。
	grantSid := worldSid
	if r.Policy == permWorkspace {
		grantSid = wsSid
	}
	if err := grantTokenDefaultDacl(restricted, grantSid); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: %w", err)
	}

	// 4) 解析 shell 与命令行（父进程解析，子进程环境可能不全）
	app, cmdLine, err := buildShellCmdline(r.Shell, r.Command)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: %w", err)
	}
	appPtr, err := windows.UTF16PtrFromString(app)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("app UTF16: %w", err)
	}
	cmdPtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("cmdline UTF16: %w", err)
	}
	dirPtr, err := windows.UTF16PtrFromString(r.Dir)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("dir UTF16: %w", err)
	}
	env := sandboxEnv(false)
	env = append(env, "TEMP="+tempDir, "TMP="+tempDir)
	envBlock, err := envBlockPtr(env)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("env block: %w", err)
	}

	// 5) stdio：NUL 输入 + 两根继承管道
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		os.RemoveAll(tempDir)
		return nil, err
	}
	if err := windows.SetHandleInformation(windows.Handle(stdoutW.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		stdoutR.Close()
		stderrR.Close()
		os.RemoveAll(tempDir)
		return nil, err
	}
	if err := windows.SetHandleInformation(windows.Handle(stderrW.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		stdoutR.Close()
		stderrR.Close()
		os.RemoveAll(tempDir)
		return nil, err
	}
	nul, err := os.Open(os.DevNull)
	if err != nil {
		stdoutR.Close()
		stderrR.Close()
		os.RemoveAll(tempDir)
		return nil, err
	}
	defer nul.Close()
	si := &windows.StartupInfo{
		StdInput:  windows.Handle(nul.Fd()),
		StdOutput: windows.Handle(stdoutW.Fd()),
		StdErr:    windows.Handle(stderrW.Fd()),
		Flags:     windows.STARTF_USESTDHANDLES, // 必须显式声明使用这三个句柄
	}
	var pi windows.ProcessInformation

	// 6) CreateProcessAsUser 启动受限子进程（官方同款）。所需主令牌特权
	// 来自源句柄的 TOKEN_ASSIGN_PRIMARY（见上面 OpenProcessToken）。
	err = windows.CreateProcessAsUser(restricted, appPtr, cmdPtr, nil, nil, true,
		windows.CREATE_UNICODE_ENVIRONMENT, envBlock, dirPtr, si, &pi)
	// 父进程侧的写端立即关闭，避免读不完
	stdoutW.Close()
	stderrW.Close()
	if err != nil {
		stdoutR.Close()
		stderrR.Close()
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sandbox unavailable: CreateProcess(受限令牌) 失败: %w", err)
	}
	return &confinedChild{proc: pi.Process, thread: pi.Thread, stdoutR: stdoutR, stderrR: stderrR, tempDir: tempDir}, nil
}

// drain 把两路输出合并写入 w（阻塞到流关闭，适合放 goroutine）。
func (c *confinedChild) drain(w io.Writer) {
	var wg sync.WaitGroup
	wg.Add(2)
	for _, f := range []*os.File{c.stdoutR, c.stderrR} {
		go func(f *os.File) {
			defer wg.Done()
			_, _ = io.Copy(w, f)
		}(f)
	}
	wg.Wait()
}

// wait 阻塞等待子进程退出；timeout 到点或 ctx 取消时终止它。
// timeout<=0 表示不限时（后台作业），终止只经 ctx（job_kill）。
func (c *confinedChild) wait(ctx context.Context, timeout time.Duration) (timedOut bool) {
	waitDone := make(chan struct{}, 1)
	go func() {
		_, _ = windows.WaitForSingleObject(c.proc, windows.INFINITE)
		close(waitDone)
	}()
	var timeoutCh <-chan time.Time // 不限时（timeout<=0）时保持 nil：从 nil 通道接收永久阻塞
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}
	select {
	case <-waitDone:
	case <-ctx.Done():
		c.kill()
		<-waitDone
		timedOut = true
	case <-timeoutCh:
		c.kill()
		<-waitDone
		timedOut = true
	}
	return timedOut
}

// spawnConfinedWindows 用受限令牌在沙箱内执行一次命令，返回合并输出。
func spawnConfinedWindows(ctx context.Context, r sandboxRun) (string, error) {
	child, err := startConfinedWindows(r)
	if err != nil {
		return "", err
	}
	defer child.cleanup()
	var buf syncBuffer
	go child.drain(&buf)
	timedOut := child.wait(ctx, r.Timeout)
	res := strings.TrimRight(buf.String(), "\r\n")
	if timedOut {
		res += "\n[命令超时或被取消，已终止]"
	}
	if strings.TrimSpace(res) == "" {
		res = "（命令执行完成，无输出）"
	}
	return res, nil
}

// syncBuffer 是并发安全的输出缓冲。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// buildShellCmdline 解析 shell 名并构造 (appName, commandLine)。
func buildShellCmdline(shell, command string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "cmd":
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = `C:\Windows\system32\cmd.exe`
		}
		return comspec, `"` + comspec + `" /C ` + command, nil
	case "pwsh", "powershell":
		exe, err := exec.LookPath(shell)
		if err != nil {
			return "", "", fmt.Errorf("找不到 %s", shell)
		}
		return exe, `"` + exe + `" -NoProfile -NonInteractive -Command ` + syscall.EscapeArg(command), nil
	default:
		return "", "", fmt.Errorf("不支持的 shell: %s", shell)
	}
}

// envBlockPtr 构造 CreateProcessAsUser 的环境变量块（UTF-16，双 NUL 结尾）。
func envBlockPtr(env []string) (*uint16, error) {
	blocks := make([]string, 0, len(env))
	seen := map[string]bool{}
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := strings.ToUpper(kv[:i])
		if seen[k] {
			continue
		}
		seen[k] = true
		blocks = append(blocks, kv)
	}
	// Windows 期望环境块按名排序（不区分大小写）
	sortStrings(blocks)
	s := strings.Join(blocks, "\x00") + "\x00\x00"
	// UTF16PtrFromString 拒绝内部 NUL，环境块必须手工编码 UTF-16
	u := utf16.Encode([]rune(s))
	return &u[0], nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && strings.ToLower(s[j]) < strings.ToLower(s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// tokenLogonSid 从令牌组里提取登录会话 SID（S-1-5-5-x-y）。
// WinSta0/桌面等每登录对象的 DACL 授的是它；缺了它受限子进程启动即
// 0xC0000142 崩溃（官方注释同款结论）。
func tokenLogonSid(tok windows.Token) (*windows.SID, error) {
	tg, err := tok.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("GetTokenGroups 失败: %w", err)
	}
	for _, g := range tg.AllGroups() {
		if g.Attributes&seGroupLogonId == seGroupLogonId {
			n := windows.GetLengthSid(g.Sid)
			b := make([]byte, n)
			if err := windows.CopySid(n, (*windows.SID)(unsafe.Pointer(&b[0])), g.Sid); err != nil {
				return nil, err
			}
			return (*windows.SID)(unsafe.Pointer(&b[0])), nil
		}
	}
	return nil, fmt.Errorf("令牌里没有登录会话 SID")
}

// grantTokenDefaultDacl 向受限令牌的默认 DACL 并入一条 grantSid 的完全
// 访问 ACE（官方 setTokenDefaultDaclGrant）。默认 DACL 决定令牌持有者
// 新建对象的初始安全描述符——不补授，受限子进程创建管道/临时文件会在
// 受限检查（pass-2）被拒。任何失败都返回错误（fail closed）。
func grantTokenDefaultDacl(tok windows.Token, grantSid *windows.SID) error {
	dacl, err := tokenDefaultDaclBytes(tok)
	if err != nil {
		return err
	}
	sideb, err := sidBytes(grantSid)
	if err != nil {
		return err
	}
	ace := make([]byte, 8+len(sideb))
	ace[0] = accessAllowedACE
	ace[1] = 0 // 对象 DACL 不需要继承标志
	binary.LittleEndian.PutUint16(ace[2:], uint16(len(ace)))
	binary.LittleEndian.PutUint32(ace[4:], fileAllAccess)
	copy(ace[8:], sideb)
	nacl := mergeAce(dacl, ace)

	// SetTokenInformation(TokenDefaultDacl=6, &TOKEN_DEFAULT_DACL{PACL}, 8)
	info := make([]byte, 8)
	binary.LittleEndian.PutUint64(info, uint64(uintptr(unsafe.Pointer(&nacl[0]))))
	r2, _, callErr := procSetTokenInformation.Call(
		uintptr(tok), 6, uintptr(unsafe.Pointer(&info[0])), uintptr(len(info)))
	if r2 == 0 {
		return fmt.Errorf("SetTokenInformation(TokenDefaultDacl) 失败: %v", callErr)
	}
	return nil
}

// tokenDefaultDaclBytes 读令牌默认 DACL 的字节（缺省时给空 ACL 头）。
func tokenDefaultDaclBytes(tok windows.Token) ([]byte, error) {
	var need uint32
	procGetTokenInformation.Call(uintptr(tok), 6, 0, 0, uintptr(unsafe.Pointer(&need)))
	if need == 0 {
		b := make([]byte, 8)
		b[0] = aclRevision
		binary.LittleEndian.PutUint16(b[2:], 8)
		return b, nil
	}
	buf := make([]byte, need)
	r1, _, callErr := procGetTokenInformation.Call(
		uintptr(tok), 6, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&need)))
	if r1 == 0 {
		return nil, fmt.Errorf("GetTokenInformation(TokenDefaultDacl) 失败: %v", callErr)
	}
	// TOKEN_DEFAULT_DACL 结构 = 一个 PACL 指针
	pacl := *(**windows.ACL)(unsafe.Pointer(&buf[0]))
	if pacl == nil {
		b := make([]byte, 8)
		b[0] = aclRevision
		binary.LittleEndian.PutUint16(b[2:], 8)
		return b, nil
	}
	return aclBytes(pacl)
}

// mergeAce 把一条 ACE 插入 ACL 字节（首个继承 ACE 之前），返回新 ACL。
func mergeAce(dacl []byte, ace []byte) []byte {
	pos := len(dacl)
	for i := 8; i+8 <= len(dacl); {
		size := int(binary.LittleEndian.Uint16(dacl[i+2:]))
		if size < 8 || i+size > len(dacl) {
			pos = len(dacl)
			break
		}
		if dacl[i+1]&inheritedACE != 0 {
			pos = i
			break
		}
		i += size
	}
	count := int(binary.LittleEndian.Uint16(dacl[4:]))
	nacl := make([]byte, 0, len(dacl)+len(ace))
	nacl = append(nacl, dacl[:8]...)
	nacl = append(nacl, dacl[8:pos]...)
	nacl = append(nacl, ace...)
	nacl = append(nacl, dacl[pos:]...)
	if pad := (4 - len(nacl)%4) % 4; pad > 0 {
		nacl = append(nacl, make([]byte, pad)...)
	}
	binary.LittleEndian.PutUint16(nacl[2:], uint16(len(nacl)))
	binary.LittleEndian.PutUint16(nacl[4:], uint16(count+1))
	return nacl
}
