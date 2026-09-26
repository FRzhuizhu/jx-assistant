package main

// browserTarget 统一 browser_* 工具与右栏面板端点的操作面，形态对工具层透明：
//   * chromedp 宿主（browser_host.go）：有头独立窗口（旧版行为），以及网页
//     模式下充当内嵌形态的无头实例（无面板镜像，仅供 AI 工具使用）；
//   * 桌面右栏原生 WebView2 面板（browser_panel_windows.go）：真浏览器嵌入
//     桌面壳窗口，AI 经控件官方 CDP 通道驱动同一控件，人机共操。
// App.browserFor/activeBrowser 按形态取目标并记账（browserActive），
// 其余 browser_* 工具一律作用于最近一次选定的实例。

type browserTarget interface {
	// Navigate 打开 http/https 地址，返回落定后的地址与标题（工具路径）。
	Navigate(raw string) (string, string, error)
	// NavigatePrivileged 同 Navigate 但放宽到 file:///（仅用户点击 open-view 路径）。
	NavigatePrivileged(raw string) (string, string, error)
	// NavAction 后退/前进/刷新（面板地址栏与工具共用）。
	NavAction(action string) error
	// Content 读当前页地址、标题与可见正文（innerText）。
	Content() (url, title, body string, err error)
	// Click 点击匹配 CSS 选择器的第一个元素（先广播 AI 指针标记）。
	Click(selector string) error
	// TypeInto 向匹配选择器的输入框整段注入文本（中文正确路径），clear 先清空。
	TypeInto(selector, text string, clear bool) error
	// Scroll 滚动页面（direction=down/up，amount 1≈100px）。
	Scroll(direction string, amount int) error
	// SetViewport 设定视口（宽 320–3840、高 320–2160；0 值恢复默认）。
	SetViewport(w, h int) error
	// Evaluate 在页面执行 JS 并返回结果（JSON 反序列化后的值）。
	Evaluate(js string) (any, error)
	// Screenshot 截取当前可见区域（PNG 字节）。
	Screenshot() ([]byte, error)
	// State 当前运行态快照（地址/标题/视口）。
	State() BrowserState
	Started() bool
	// Where 给工具回显用的打开位置描述（右栏面板/独立窗口/后台无头）。
	Where() string
	Close()
	// BroadcastPointer 点击/输入前亮出 AI 即将操作的位置（形态各自实现可见化）。
	BroadcastPointer(selector string)
}
