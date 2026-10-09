//go:build windows

package main

// 桌面壳（Windows）：用系统自带的 WebView2 运行时开原生窗口承载本地
// 服务页面，替代「起服务 + 弹默认浏览器」的网页形态。
//
// - 运行时预检：Evergreen WebView2 未见时给安装指引（Win10/11 一般自带）；
// - 单实例：命名互斥体，二次启动唤起已有窗口后退出；
// - 窗口图标取自 exe 资源里的 RT_GROUP_ICON（id=1，见 打包/go-winres）。

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	desktopAppTitle  = "坚祥数字助理GO"
	desktopMutexName = `Local\JX-Digital-Assistant-GO-SingleInstance`
	// WebView2 Evergreen 的 EdgeUpdate 客户端键与官方引导器下载地址
	webView2ClientKey       = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	webView2BootstrapperURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"
	webView2ArgsEnv         = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"
)

var (
	desktopUser32          = windows.NewLazySystemDLL("user32.dll")
	procDesktopShowWindow  = desktopUser32.NewProc("ShowWindow")
	procDesktopMessageBoxW = desktopUser32.NewProc("MessageBoxW")
	desktopKernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procGetStdHandle       = desktopKernel32.NewProc("GetStdHandle")
)

const (
	stdOutputHandle = ^uintptr(10) // -11
	invalidHandle   = ^uintptr(0)
)

// stdHandleValid 报告标准输出是否为有效句柄（控制台或管道）。
// -H=windowsgui 构建从资源管理器双击启动时句柄无效。
func stdHandleValid(h uintptr) bool {
	return h != 0 && h != invalidHandle
}

// guiLogRedirect：GUI 子系统构建没有控制台，双击启动时 log 输出全部
// 落空。检测到无有效标准输出句柄时把 log 改落到 data\desktop.log
// （超 5MB 轮转成 .old），诊断信息不丢；带控制台/管道启动（终端调试、
// 脚本调用）保持原样打屏。
func guiLogRedirect() {
	if h, _, _ := procGetStdHandle.Call(stdOutputHandle); stdHandleValid(h) {
		return
	}
	logPath := filepath.Join("data", "desktop.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)
	if st, err := os.Stat(logPath); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(logPath, logPath+".old")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

// attachParentConsole：GUI 子系统 exe 在终端里跑 CLI 子命令（如
// sandbox acl-audit）时，挂到父进程的控制台并重绑标准句柄，让
// fmt/os.Stdout 输出正常回显。已有可用句柄（管道/控制台）则不动。
func attachParentConsole() {
	if h, _, _ := procGetStdHandle.Call(stdOutputHandle); stdHandleValid(h) {
		return
	}
	procAttachConsole := desktopKernel32.NewProc("AttachConsole")
	const attachParentProcess = ^uintptr(0) // -1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return // 父进程没有控制台（双击启动），静默
	}
	if h, _, _ := procGetStdHandle.Call(stdOutputHandle); stdHandleValid(h) {
		os.Stdout = os.NewFile(h, "/dev/stdout")
	}
	if h, _, _ := procGetStdHandle.Call(^uintptr(11)); stdHandleValid(h) { // -12 stderr
		os.Stderr = os.NewFile(h, "/dev/stderr")
	}
}

// webView2RuntimeAvailable 检测系统是否装了 WebView2 运行时：
// 固定版本（环境变量指定目录）> Evergreen（HKLM/HKCU EdgeUpdate 的 pv 值）。
func webView2RuntimeAvailable() bool {
	if dir := os.Getenv("WEBVIEW2_BROWSER_EXECUTABLE_FOLDER"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "msedgewebview2.exe")); err == nil {
			return true
		}
	}
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, subkey := range []string{
			`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webView2ClientKey,
			`SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2ClientKey,
		} {
			k, err := registry.OpenKey(root, subkey, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			pv, _, err := k.GetStringValue("pv")
			k.Close()
			if err == nil && pv != "" && pv != "0.0.0.0" {
				return true
			}
		}
	}
	return false
}

// acquireSingleInstance 创建命名互斥体；already=true 表示已有一个桌面实例
// 在跑（此时 release 为空函数，调用方应唤起旧窗口后退出）。
func acquireSingleInstance() (release func(), already bool) {
	procCreateMutexW := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW")
	name, err := windows.UTF16PtrFromString(desktopMutexName)
	if err != nil {
		return func() {}, false
	}
	h, _, callErr := procCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if errno, ok := callErr.(syscall.Errno); ok && errno == windows.ERROR_ALREADY_EXISTS {
		return func() {}, true
	}
	if h == 0 {
		// 互斥体创建失败不拦截启动，退化为允许多开
		return func() {}, false
	}
	return func() { _ = windows.CloseHandle(windows.Handle(h)) }, false
}

// activateExistingWindow 按标题找回已有桌面窗口并置前
// （FindWindowW/SetForegroundWindow 复用 pickfolder_windows.go 的声明）。
func activateExistingWindow() {
	title, err := windows.UTF16PtrFromString(desktopAppTitle)
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return
	}
	procDesktopShowWindow.Call(hwnd, 9) // SW_RESTORE
	procSetForegroundWindow.Call(hwnd)
}

func desktopMessageBox(text, caption string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(caption)
	procDesktopMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x30) // MB_ICONWARNING
}

// workspaceFolderName 是默认工作区目录名：安装目录下的「默认工作区」。
// 旧版用的是「工作区」（legacyWorkspaceFolderName），启动时原地改名迁移
// （见 desktopWorkspaceDefault），用户已有的文件一份不搬不丢。
const (
	workspaceFolderName       = "默认工作区"
	legacyWorkspaceFolderName = "工作区"
)

// migrateLegacyWorkspaceDir 把旧默认工作区目录原地改名成新名（数据留在原处、
// 不改路径引用之外的东西）。返回旧目录路径（空串 = 没有旧目录要处理）与是否已
// 落到新目录（新目录本来就在，或改名成功）。旧目录在、改名失败时如实返回 false，
// 由调用方决定是回退旧目录还是继续——不在这里替它做决定。
func migrateLegacyWorkspaceDir(oldDir, newDir string) (legacy string, migrated bool) {
	fi, err := os.Stat(oldDir)
	if err != nil || !fi.IsDir() {
		return "", false
	}
	if _, err := os.Stat(newDir); err == nil {
		return oldDir, true // 新目录已在（装包建的或上次迁移留下的）：旧目录原样保留
	}
	if rerr := os.Rename(oldDir, newDir); rerr != nil {
		log.Printf("默认工作区改名失败（%s → %s）: %v", oldDir, newDir, rerr)
		return oldDir, false
	}
	return oldDir, true
}

// desktopWorkspaceDefault 归置桌面模式的默认工作区：默认工作区不落在安装目录
// 本身，而是安装目录下的「默认工作区」子文件夹——模型的工作区域与软件本体
// （exe/config/data/技能）分离，默认目录同时跟随安装位置。只认默认形态（当前值
// 就是 exe 目录本身，或旧版默认目录「工作区」）；用户自己设过的工作目录一律不动。
// 旧版的「工作区」原地改名为「默认工作区」；改名失败则保持原样、下次启动再试
// （不把用户切到一个可能为空的新目录上）。
// 返回 (legacy, changed)：changed = 是否改写了配置（调用方负责回写，避免设置页
// 与实际不一致）；legacy = 被搬走的旧默认目录，仅 changed 为真时有效，调用方用
// 它把显式挂在旧路径上的历史会话一并改指新目录。
// 安装目录不可写时（系统级位置）回退「文档\坚祥数字助理」。
func desktopWorkspaceDefault(cfg *Config) (legacy string, changed bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	exeDir := filepath.Clean(filepath.Dir(exe))
	ws, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return "", false
	}
	ws = filepath.Clean(ws)
	oldDir := filepath.Join(exeDir, legacyWorkspaceFolderName)
	dir := filepath.Join(exeDir, workspaceFolderName)
	if !stringsEqualFold(ws, exeDir) && !stringsEqualFold(ws, oldDir) {
		return "", false // 用户自选的工作目录：不碰
	}
	if lg, migrated := migrateLegacyWorkspaceDir(oldDir, dir); lg != "" {
		if !migrated && stringsEqualFold(ws, oldDir) {
			return "", false // 旧目录仍是当前工作区：保持原样，下次启动再试
		}
		legacy = lg
	}
	if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
		docs := filepath.Join(os.Getenv("USERPROFILE"), "Documents")
		if kf, kerr := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); kerr == nil {
			docs = kf
		}
		dir = filepath.Join(docs, "坚祥数字助理")
		_ = os.MkdirAll(dir, 0755)
	}
	cfg.Workspace = dir
	return legacy, true
}

func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// runDesktop 打开 WebView2 窗口并阻塞到窗口关闭。
// JX_DESKTOP_AUTOCLOSE 秒数>0 时到点自动关窗（自动化冒烟用）。
// 桌面壳就是主 WebView2 控件（承载 Web UI）；右栏原生浏览器面板
// （browser_panel_windows.go）作为第二个 WebView2 控件惰性创建，挂在
// 本窗口上。URL 带 shell=desktop 参数：前端据此进入桌面形态（右栏
// 浏览器视图 + 矩形上报）。
func runDesktop(app *App, url string) error {
	if !webView2RuntimeAvailable() {
		desktopMessageBox(
			"未检测到 Microsoft WebView2 运行时（Windows 10 / 11 一般自带）。\n\n"+
				"请先安装 WebView2 Runtime 再启动桌面版：\n"+webView2BootstrapperURL+"\n\n"+
				"也可以不带 -desktop 参数运行，用浏览器模式使用。", desktopAppTitle)
		return fmt.Errorf("缺少 WebView2 运行时")
	}
	release, already := acquireSingleInstance()
	defer release()
	if already {
		activateExistingWindow()
		return nil
	}
	// 麦克风权限：桌面端应当「天生具备」，不能每次都弹审批。
	// 绑定库建主控件时只显式放行了剪贴板（jchv/go-webview2 的
	// SetPermission(ClipboardRead)），其余权限按「默认」交给 WebView2：桌面壳里
	// 弹不出可靠的授权气泡，语音输入就会直接以 not-allowed 失败。这里在创建主
	// 控件（＝主环境，只在这一次调用里建）之前，把 Chromium 的「自动接受媒体
	// 权限」开关并进已有的附加参数，建完立刻还原：应用一启动就带着麦克风权限，
	// 整个生命周期一次都不用问、也不用用户去点授权（下次启动同样如此）。
	// 右栏浏览器面板用的是另一个环境、且是惰性创建的，不会被带上（不给面板里
	// 的任意网站自动放行麦克风）；系统层面的麦克风开关（Windows 设置 → 隐私 →
	// 麦克风）仍照旧生效，是唯一的最终闸门。
	const autoGrantMic = "--use-fake-ui-for-media-stream"
	// 黑屏修复的一半：关掉 Chromium 的原生窗口遮挡判定。电脑操作技能执行时
	// 主窗口长时间被目标程序盖住，Chromium 判定「已遮挡」停止合成，解除后
	// 偶发不再续帧＝整个客户区黑屏、执行不受影响（部分机型才复现，
	// WebView2Feedback #5171 同类）。新版运行时或已忽略该开关（无害），
	// 保底唤醒在 subclassMainWindow 的 1px 位移。已带 --disable-features 的
	// 环境把本特性并进去，避免出现第二个同名开关把用户的值顶掉。
	const noOcclusion = "CalculateNativeWinOcclusion"
	prevArgs, hadArgs := os.LookupEnv(webView2ArgsEnv)
	merged := prevArgs
	if !strings.Contains(prevArgs, autoGrantMic) {
		merged = strings.TrimSpace(merged + " " + autoGrantMic)
	}
	if !strings.Contains(prevArgs, noOcclusion) {
		const dfPrefix = "--disable-features="
		if i := strings.Index(prevArgs, dfPrefix); i >= 0 {
			merged = prevArgs[:i+len(dfPrefix)] + noOcclusion + "," + prevArgs[i+len(dfPrefix):]
		} else {
			merged = strings.TrimSpace(merged + " " + dfPrefix + noOcclusion)
		}
	}
	if merged != prevArgs {
		_ = os.Setenv(webView2ArgsEnv, merged)
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join("data", "webview"),
		WindowOptions: webview2.WindowOptions{
			Title:  desktopAppTitle,
			Width:  1280,
			Height: 880,
			IconId: 1, // 打包/go-winres 生成的 RT_GROUP_ICON 资源号
			Center: true,
		},
	})
	if hadArgs {
		_ = os.Setenv(webView2ArgsEnv, prevArgs)
	} else {
		_ = os.Unsetenv(webView2ArgsEnv)
	}
	if w == nil {
		return fmt.Errorf("创建桌面窗口失败")
	}
	subclassMainWindow(uintptr(w.Window()))
	// 右栏原生浏览器面板：COM 调用一律经 Dispatch 投递到本窗口的 UI 线程
	app.nativePanel = newBrowserPanel(app, w.Dispatch, uintptr(w.Window()))
	// 右侧栏截图（panel_screenshot 工具）的通道：shellEval 注入主界面 JS，
	// 探针把右栏矩形 POST /api/shell/panel-view 回到 panelViewCh。
	// ExecuteScript 是 COM 调用，必须发生在 UI 线程——工具侧从任意 goroutine
	// 调 shellEval，这里经 Dispatch 投递到主窗口消息循环再执行。
	app.shellEval = func(js string) { w.Dispatch(func() { w.Eval(js) }) }
	app.shellWnd = uintptr(w.Window())
	app.panelViewCh = make(chan panelViewReport, 1)
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	w.Navigate(url + sep + "shell=desktop")
	if secs, err := strconv.Atoi(os.Getenv("JX_DESKTOP_AUTOCLOSE")); err == nil && secs > 0 {
		go func() {
			time.Sleep(time.Duration(secs) * time.Second)
			// Terminate() 的 PostQuitMessage 只作用于调用线程，跨线程
			// 无效；Destroy() 走 PostMessage(WMClose)，跨线程安全。
			w.Destroy()
		}()
	}
	w.Run()
	w.Destroy()
	return nil
}

// ---------- 主窗口渲染唤醒（电脑操作时黑屏修复） ----------

// 现象：执行电脑操作技能期间，主窗口被目标程序遮挡或整体最小化（模型常会
// 激活别的窗口、点任务栏），WebView2 合成器停帧；恢复可见后偶发不再续帧——
// 整个客户区黑屏、标题栏正常、执行不受影响，部分机型才复现（WebView2
// Feedback #5171 / #2983 同类，官方未修）。右栏面板已踩过同族问题并验证了
// 解法（browser_panel_windows.go applyRectOnUI：同矩形重发不重绘，真实位移
// 1px 立即唤醒），这里把同款机制装到主窗口：
//   - 吞掉最小化时的 WM_SIZE——绑定库会拿退化矩形 PutBounds，是停帧源之一；
//   - 激活/还原/纯 Z 序变化（遮挡解除）时，1px 往返位移逼合成器出新帧，
//     两步在同一次消息处理内完成，中间不发生绘制，肉眼不可见；
//     （去抖 150ms，WM_ACTIVATE 与 WM_SIZE 连发时只唤醒一次）
//   - 自己发出的位移事件带真实坐标（无 NOSIZE/NOMOVE 标志），被
//     WM_WINDOWPOSCHANGED 分支的条件天然滤掉，不会递归。
const (
	wmSize             = 0x0005
	sizeMinimized      = 1
	wmActivate         = 0x0006
	wmWindowPosChanged = 0x0047
	swpNoSizeFlag      = 0x0001
	swpNoMoveFlag      = 0x0002
	waInactive         = 0
	gwlpWndProc        = ^uintptr(3) // GWLP_WNDPROC(-4)：按位取反，^3 == -4
)

// windowPosMirror 镜像 Win32 WINDOWPOS（x64 布局；只读 flags 字段）。
type windowPosMirror struct {
	hwnd, after  uintptr
	x, y, cx, cy int32
	flags        uint32
}

var (
	procSetWindowLongPtrW = desktopUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = desktopUser32.NewProc("CallWindowProcW")
	procDefWindowProcW    = desktopUser32.NewProc("DefWindowProcW")
	mainWndOrigProc       uintptr
	mainWndNudgeCb        = windows.NewCallback(mainWndSubclassProc)
	lastMainWndNudgeMs    int64
)

// subclassMainWindow 换掉主窗口原 wndproc（安装必须在 UI 线程；此后所有消息
// 先经 mainWndSubclassProc 再转回绑定库原 proc）。失败则保持原样并落日志——
// 用户侧若仍复现黑屏，靠 desktop.log 区分「修复未安装上」与「唤醒无效」，
// 行为退回修复前，不影响功能。
func subclassMainWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	orig, _, callErr := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, mainWndNudgeCb)
	if orig != 0 {
		mainWndOrigProc = orig
	} else {
		log.Printf("主窗口子类化失败：黑屏修复未生效（errno=%v）", callErr)
	}
}

func mainWndSubclassProc(hwnd, msg, wp uintptr, lp unsafe.Pointer) uintptr {
	switch msg {
	case wmSize:
		if wp == sizeMinimized {
			return 0 // 别让绑定库把最小化下的退化矩形 PutBounds 给 WebView2
		}
		nudgeMainWindow(hwnd)
	case wmActivate:
		if wp&0xffff != waInactive {
			nudgeMainWindow(hwnd)
		}
	case wmWindowPosChanged:
		if p := (*windowPosMirror)(lp); p != nil &&
			p.flags&swpNoSizeFlag != 0 && p.flags&swpNoMoveFlag != 0 {
			nudgeMainWindow(hwnd)
		}
	}
	if mainWndOrigProc == 0 {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, uintptr(lp))
		return r
	}
	r, _, _ := procCallWindowProcW.Call(mainWndOrigProc, hwnd, msg, wp, uintptr(lp))
	return r
}

// nudgeMainWindow 真实位移 1px 再移回（复用右栏面板的 procPanelSetWindowPos/
// procPanelGetWindowRect 与 panelRect），给合成器一次必达的窗口位置事件。
// 每次落一行日志：用户侧若仍黑屏，desktop.log 里有「唤醒已发出」的记录，
// 即可排除本机制、指向渲染进程/GPU 死亡的另一族黑屏（那族要靠截图瘦身或
// ProcessFailed 自动重载来修）。
func nudgeMainWindow(hwnd uintptr) {
	now := time.Now().UnixMilli()
	if now-atomic.LoadInt64(&lastMainWndNudgeMs) < 150 {
		return
	}
	atomic.StoreInt64(&lastMainWndNudgeMs, now)
	var r panelRect
	if ok, _, _ := procPanelGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return
	}
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w <= 0 || h <= 0 || r.Left <= -30000 { // 最小化（窗口挪到 -32000）不折腾
		return
	}
	log.Printf("主窗口渲染唤醒：1px 位移（黑屏修复）")
	procPanelSetWindowPos.Call(hwnd, 0, uintptr(int(r.Left)+1), uintptr(int(r.Top)), uintptr(w), uintptr(h),
		swpNoZOrder|swpNoActivate)
	procPanelSetWindowPos.Call(hwnd, 0, uintptr(int(r.Left)), uintptr(int(r.Top)), uintptr(w), uintptr(h),
		swpNoZOrder|swpNoActivate)
}
