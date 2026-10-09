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
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	edge "github.com/jchv/go-webview2/pkg/edge"
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
	// 冷启动直达兼容模式：黑屏自检曾落过标记文件（data/webview-compat.flag）
	// 就不再先走硬件渲染吃一次黑屏，直接软件合成进来。参数合并与麦克风
	// 权限说明见 newDesktopFrontend（每轮前端创建时都会做）。
	softwareCompatPath = filepath.Join("data", "webview-compat.flag")
	if _, err := os.Stat(softwareCompatPath); err == nil {
		webviewSoftwareRender = true
		log.Printf("兼容显示模式已启用（软件合成，标记文件 %s 存在）", softwareCompatPath)
	}
	// 前端窗口循环：正常一轮（用户关窗 → 退出循环、进程收尾）；黑屏自检
	// 两级手段仍救不回时（selfCheckEscalate）置 frontendRestartReq 并关窗，
	// 这里按兼容参数（软件合成）原地重建前端——后端（HTTP 服务、会话、
	// 运行中任务）全在 Go 进程里，一个字节都不动。旧前端的 WebView2 控制器
	// 在收尾时 Close（浏览器进程树与 GPU 占用一并释放），新旧环境用不同
	// 数据目录（webview / webview-compat），互不抢 user-data-folder。
	var prevRect *panelRect
	for {
		w := newDesktopFrontend(prevRect)
		if w == nil {
			return fmt.Errorf("创建桌面窗口失败")
		}
		subclassMainWindow(uintptr(w.Window()))
		// 黑屏自检的 UI 线程通道：检测 goroutine 里要动窗口（1px resize）时经
		// Dispatch 投递（COM/窗口操作必须在 UI 线程，与浏览器面板同一纪律）。
		mainWndDispatch = w.Dispatch
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
			// 每一轮前端都各自布防（热重启换了窗口句柄，只布防首轮会让
			// 新窗口永远等不到关窗指令）。
			go func() {
				time.Sleep(time.Duration(secs) * time.Second)
				// Terminate() 的 PostQuitMessage 只作用于调用线程，跨线程
				// 无效；Destroy() 走 PostMessage(WMClose)，跨线程安全。
				w.Destroy()
			}()
		}
		w.Run()
		// 收尾在 UI 线程（Run 返回处即主线程）：关面板与主控的 COM Close，
		// 让两棵浏览器进程树真正退场（GPU/句柄全释放），再拆窗口句柄。
		releaseFrontend(app, w)
		w.Destroy()
		if atomic.LoadInt32(&frontendRestartReq) != 1 {
			break // 正常关窗：走进程退出
		}
		atomic.StoreInt32(&frontendRestartReq, 0)
		prevRect = restartWndRect
		log.Printf("前端热重启：切换兼容显示模式重建界面（后端与任务不受影响）")
	}
	// 消息循环退出 = 主窗口关闭（正常点 ✕，或 WebView2/窗口异常）。落一行日志：
	// 桌面版「闪退」排障时，靠它区分「窗口关闭走正常退出」与「进程被杀/panic」
	// （后者不会留任何痕迹——GUI 子系统 panic 输出无处可去）。
	log.Printf("桌面窗口已关闭，桌面壳退出")
	return nil
}

// newDesktopFrontend 建主窗口（含附加参数合并与兼容模式分流）。兼容模式走
// 独立数据目录 data/webview-compat：Chromium 参数在环境创建时定死、换参数必须
// 换环境，而新旧环境的 user-data-folder 不能相同（旧浏览器进程异步退场，同
// 目录创建会失败）；代价仅是兼容窗的前端本地存储（界面偏好类）从零开始，
// 业务数据都在服务端。prev 非空时新窗原位重开（热重启不跳位）。
func newDesktopFrontend(prev *panelRect) webview2.WebView {
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
	dataDir := filepath.Join("data", "webview")
	if webviewSoftwareRender {
		// 兼容显示模式（软件合成）：黑屏自检升级或标记文件触发，把 GPU 整个
		// 让给与我们抢资源的其他应用（聊天界面很轻，软件合成无压力）。删掉
		// data/webview-compat.flag 即恢复默认硬件加速（热重启本会话内也会切回）。
		if !strings.Contains(merged, "--disable-gpu-compositing") {
			merged = strings.TrimSpace(merged + " --disable-gpu-compositing")
		}
		dataDir = filepath.Join("data", "webview-compat")
		log.Printf("兼容显示模式：软件合成，数据目录 %s", dataDir)
	}
	if merged != prevArgs {
		_ = os.Setenv(webView2ArgsEnv, merged)
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  dataDir,
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
		return nil
	}
	if prev != nil && prev.Left > -30000 {
		procPanelSetWindowPos.Call(uintptr(w.Window()), 0,
			uintptr(int(prev.Left)), uintptr(int(prev.Top)),
			uintptr(int(prev.Right-prev.Left)), uintptr(int(prev.Bottom-prev.Top)),
			swpNoZOrder) // 触发 WM_SIZE，绑定库会跟着 PutBounds
	}
	return w
}

// releaseFrontend 前端收尾（仅 UI 线程调用）：把右栏面板与主界面的 WebView2
// 控制器 Close 掉，浏览器进程树随之退场。绑定库不暴露主控件，这里经反射取
// 其内部 *edge.Chromium（go.mod 已锁定版本；字段变动时 NewAt 抛错由 recover
// 兜住，退化为「进程退出时由 Job Object 回收」，功能不受影响）。
func releaseFrontend(app *App, w webview2.WebView) {
	if bp, ok := app.nativePanel.(*browserPanel); ok {
		bp.releaseAfterMainLoopGone()
	}
	func() {
		defer func() { _ = recover() }()
		rv := reflect.ValueOf(w)
		if rv.Kind() != reflect.Ptr {
			return
		}
		rv = rv.Elem()
		f := rv.FieldByName("browser")
		if !f.IsValid() || (f.Kind() == reflect.Ptr && f.IsNil()) {
			return
		}
		ch, _ := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface().(*edge.Chromium)
		if ch == nil {
			return
		}
		if ctl := ch.GetController(); ctl != nil {
			_, _, _ = ctlVtblOf(ctl).close.Call(uintptr(unsafe.Pointer(ctl)))
		}
	}()
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
	scheduleSelfCheck(hwnd)
}

// ---------- 黑屏自检与自动降级 ----------

var (
	softwareCompatPath    string
	webviewSoftwareRender bool // 本次启动是否已带软件合成
	mainWndDispatch       func(f func())
	selfCheckBusy         int32
	selfCheckLastMs       int64
	selfCheckAlerted      int32      // 弹窗只一次（防打扰/防循环）
	frontendRestartReq    int32      // 黑屏升级：置位后前端窗口循环热重启
	restartWndRect        *panelRect // 热重启时沿用旧窗口位置尺寸
	procPostMessageW      = desktopUser32.NewProc("PostMessageW")
)

// scheduleSelfCheck 唤醒后验证「真的恢复了吗」：500ms 后截自己的客户区做
// 像素判定。黑了 → 升级一档（真实 1px resize 强制重建合成表面）再验；
// 仍黑 → 落兼容模式标记 + 弹窗引导重启。检测在独立 goroutine（截屏像素
// 采样不占 UI 线程），动窗口的动作经 mainWndDispatch 回 UI 线程。
func scheduleSelfCheck(hwnd uintptr) {
	now := time.Now().UnixMilli()
	if !atomic.CompareAndSwapInt32(&selfCheckBusy, 0, 1) {
		return // 已有一轮在跑
	}
	if now-atomic.LoadInt64(&selfCheckLastMs) < 2000 {
		atomic.StoreInt32(&selfCheckBusy, 0)
		return // 2 秒去抖：连番激活只检一次
	}
	atomic.StoreInt64(&selfCheckLastMs, now)
	go func() {
		defer atomic.StoreInt32(&selfCheckBusy, 0)
		time.Sleep(500 * time.Millisecond)
		if !windowIsBlack(hwnd) {
			log.Printf("黑屏自检：通过")
			return
		}
		log.Printf("黑屏自检：发现黑屏，升级手段（1px 真实 resize 重建表面）")
		if mainWndDispatch != nil {
			resizeNudgeDone := make(chan struct{})
			mainWndDispatch(func() {
				defer close(resizeNudgeDone)
				var r panelRect
				if ok, _, _ := procPanelGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
					return
				}
				w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
				if w <= 0 || h <= 0 || r.Left <= -30000 {
					return
				}
				procPanelSetWindowPos.Call(hwnd, 0, uintptr(int(r.Left)), uintptr(int(r.Top)), uintptr(w-1), uintptr(h),
					swpNoZOrder|swpNoActivate)
				procPanelSetWindowPos.Call(hwnd, 0, uintptr(int(r.Left)), uintptr(int(r.Top)), uintptr(w), uintptr(h),
					swpNoZOrder|swpNoActivate)
			})
			select {
			case <-resizeNudgeDone:
			case <-time.After(2 * time.Second):
			}
		}
		time.Sleep(700 * time.Millisecond)
		if !windowIsBlack(hwnd) {
			log.Printf("黑屏自检：升级后已恢复")
			return
		}
		selfCheckEscalate(hwnd)
	}()
}

// selfCheckEscalate 两级手段（位移 + resize）都救不回来：认定为该机显卡
// 驱动与硬件合成冲突（多为与其他 GPU 重载应用抢资源，如另一个 WebView 应用
// 在前台被电脑操作驱动时）。默认渲染下：落兼容标记（冷启动直达）+ **前端
// 热重启**——当场关掉当前界面窗口，窗口循环按软件合成重建，后端（服务/
// 会话/任务）原样继续，用户视角是「黑窗口自己关了、一两秒后界面回来了」。
// 已在兼容模式下仍黑：不再折腾，提示取证反馈。弹窗每进程至多一次。
func selfCheckEscalate(hwnd uintptr) {
	if !atomic.CompareAndSwapInt32(&selfCheckAlerted, 0, 1) {
		log.Printf("黑屏自检：兼容处置本进程已执行过，跳过")
		return
	}
	if webviewSoftwareRender {
		log.Printf("黑屏自检：兼容模式下仍黑屏（疑似更深层驱动问题），请用户取证反馈")
		go desktopMessageBox(
			"检测到界面在兼容显示模式下仍然黑屏。\n\n"+
				"这通常是显卡驱动层面的问题，请把以下信息反馈给我们：\n"+
				"1. 安装目录 data\\desktop.log（含「黑屏自检」各行）\n"+
				"2. 显卡型号与驱动版本（dxdiag 截图）\n"+
				"3. 事件查看器 → 系统日志中来源为 Display 的 4101 事件记录\n\n"+
				"临时可用：双击安装目录的「重启数字助理.cmd」重启软件。", desktopAppTitle)
		return
	}
	_ = os.MkdirAll(filepath.Dir(softwareCompatPath), 0755)
	if err := os.WriteFile(softwareCompatPath, []byte("black-screen auto fallback: software compositing\n"), 0644); err != nil {
		log.Printf("黑屏自检：兼容标记写入失败: %v", err)
		return
	}
	webviewSoftwareRender = true
	log.Printf("黑屏自检：启用兼容显示模式（%s），前端热重启中（后端不动）", softwareCompatPath)
	go desktopMessageBox(
		"检测到显卡驱动与界面渲染冲突（界面持续黑屏）。\n\n"+
			"已自动切换「兼容显示模式」：界面窗口将自动重开（后台任务与会话不受影响），"+
			"并改用软件渲染，不再与其他应用争抢 GPU。\n\n"+
			"下次启动也将直接使用兼容模式；如需恢复默认渲染，"+
			"删除 data\\webview-compat.flag 后重启软件即可。", desktopAppTitle)
	restartFrontendNow(hwnd)
}

// restartFrontendNow 请求前端热重启：记下旧窗口位置（重开原位）→ 关窗，
// 窗口循环按当前 webviewSoftwareRender 重建前端（黑屏自检升级与设置页
// 「界面渲染」切换共用）。PostMessage 跨线程安全。
func restartFrontendNow(hwnd uintptr) {
	r := &panelRect{}
	if ok, _, _ := procPanelGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(r))); ok != 0 && r.Left > -30000 {
		restartWndRect = r
	}
	atomic.StoreInt32(&frontendRestartReq, 1)
	procPostMessageW.Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
}

// ---------- 渲染模式查询与切换（设置页「界面渲染」） ----------

// displayMode 供 GET /api/display-mode：desktop=桌面壳是否在跑；
// software=当前前端是否软件合成；reason=兼容模式来由（auto=黑屏自动
// 切换 / user=设置页手动开启），非兼容模式下为空串。
func (a *App) displayMode() map[string]any {
	reason := ""
	if webviewSoftwareRender {
		reason = "auto"
		if b, err := os.ReadFile(softwareCompatPath); err == nil && strings.Contains(string(b), "user") {
			reason = "user"
		}
	}
	return map[string]any{
		"desktop":  a.shellWnd != 0,
		"software": webviewSoftwareRender,
		"reason":   reason,
	}
}

// setDisplayMode 供 POST /api/display-mode：写/删兼容标记并前端热重启，
// 当场切到目标模式（后端不动）。标记内容带 user 来由，displayMode 据此
// 在设置页显示「手动开启」还是「黑屏自动切换」。
func (a *App) setDisplayMode(software bool) error {
	if a.shellWnd == 0 {
		return fmt.Errorf("仅桌面版支持渲染模式切换")
	}
	if software == webviewSoftwareRender {
		return nil // 已在目标模式：不动窗口
	}
	if software {
		_ = os.MkdirAll(filepath.Dir(softwareCompatPath), 0755)
		if err := os.WriteFile(softwareCompatPath, []byte("user\n"), 0644); err != nil {
			return fmt.Errorf("兼容标记写入失败: %w", err)
		}
	} else if err := os.Remove(softwareCompatPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("兼容标记删除失败: %w", err)
	}
	webviewSoftwareRender = software
	log.Printf("渲染模式切换（设置页）：%s，前端热重启", map[bool]string{true: "兼容模式（软件合成）", false: "GPU 渲染"}[software])
	// 响应先送达前端再关窗（400ms 缓冲），用户看到界面自动重开即生效
	time.AfterFunc(400*time.Millisecond, func() { restartFrontendNow(a.shellWnd) })
	return nil
}

// windowIsBlack 截本窗口客户区（含边框内整域）做纯黑判定：≥98.5% 像素
// 亮度 <8 才算黑。正常界面有亮色侧栏与卡片，深色网页也到不了纯黑占比，
// 阈值刻意保守——误判的代价（弹窗+标记+要求重启）远高于漏检（下次激活
// 再检）。窗口最小化/矩形异常一律不算黑。
func windowIsBlack(hwnd uintptr) bool {
	var r panelRect
	if ok, _, _ := procPanelGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return false
	}
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w < 64 || h < 64 || r.Left <= -30000 {
		return false
	}
	hScreen, _, _ := procGetDC.Call(0)
	if hScreen == 0 {
		return false
	}
	defer procReleaseDC.Call(0, hScreen)
	hMem, _, _ := procCreateCompatibleDC.Call(hScreen)
	if hMem == 0 {
		return false
	}
	defer procDeleteDC.Call(hMem)
	bmi := make([]byte, 40)
	putU32(bmi[0:4], 40)
	putU32(bmi[4:8], uint32(w))
	putU32(bmi[8:12], uint32(-h))
	putU16(bmi[12:14], 1)
	putU16(bmi[14:16], 32)
	var ppv unsafe.Pointer
	hBmp, _, _ := procCreateDIBSection.Call(hScreen, uintptr(unsafe.Pointer(&bmi[0])), 0,
		uintptr(unsafe.Pointer(&ppv)), 0, 0)
	if hBmp == 0 {
		return false
	}
	defer procDeleteObject.Call(hBmp)
	procSelectObject.Call(hMem, hBmp)
	if r1, _, _ := procBitBlt.Call(hMem, 0, 0, uintptr(w), uintptr(h), hScreen,
		uintptr(int(r.Left)), uintptr(int(r.Top)), uintptr(0x00CC0020)); r1 == 0 {
		return false
	}
	// 步长 16 采样（BGRA），统计纯黑占比
	pixels := unsafe.Slice((*byte)(ppv), w*h*4)
	total, black := 0, 0
	for y := 0; y < h; y += 16 {
		for x := 0; x < w; x += 16 {
			i := (y*w + x) * 4
			if int(pixels[i+0])+int(pixels[i+1])+int(pixels[i+2]) < 24 { // R+G+B<24 ≈ 亮度<8
				black++
			}
			total++
		}
	}
	return total > 0 && black*1000/total >= 985
}
