package main

// 浏览器操作组件：经 CDP（chromedp，纯 Go）驱动系统自带的 Edge（兜底 Chrome）。
// 设计要点：
//   * 进程级单例浏览器：首次调用懒启动，后续工具复用同一窗口，
//     browser_close 显式关闭；助手进程退出由 chromedp 兜底清理。
//   * 有头模式启动（用户能看到浏览器在被操作，与 DSH 坚祥版的取舍一致）。
//   * 所有工具要求会话开启「操作浏览器」能力（perm.go 能力门控，
//     权限芯片里的 browser 复选框），fail closed。
//   * browser_screenshot 的截图入库为附件：用户直接看，视觉模型可内联。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

var (
	browserMu     sync.Mutex
	browserCtx    context.Context
	browserCancel context.CancelFunc
)

// findBrowserExe 与 browserTimeout 常量由 browser_host.go 统一提供
// （内嵌浏览器宿主与工具探测同一份 Edge/Chrome 位置清单，两处定义逐字相同）。

// acquireBrowser 返回可复用的浏览器执行上下文；必要时启动浏览器。
func acquireBrowser() (context.Context, error) {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserCtx != nil {
		return browserCtx, nil
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false), // 有头模式：用户可见，操作可信
		chromedp.Flag("disable-gpu", false),
	)
	if exe := findBrowserExe(); exe != "" {
		opts = append(opts, chromedp.ExecPath(exe))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	// 预热：真正拉起浏览器进程，把启动失败暴露在第一次调用上
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		allocCancel()
		browserCtx = nil
		return nil, fmt.Errorf("启动浏览器失败（需要系统自带的 Edge，或已安装的 Chrome）: %w", err)
	}
	browserCtx = ctx
	browserCancel = func() {
		cancel()
		allocCancel()
	}
	return browserCtx, nil
}

func closeBrowser() {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserCancel != nil {
		browserCancel()
		browserCtx = nil
		browserCancel = nil
	}
}

// requireCapability 是能力门控的统一提示；能力清单与开关在 perm.go / 权限芯片。
func requireCapability(t *toolContext, capID, title string) error {
	if !hasCapability(t.sess, capID) {
		return fmt.Errorf("需要「%s」能力：请让用户在权限芯片中勾选后再试", title)
	}
	return nil
}

// browserDo 在浏览器上下文里执行一段操作（统一超时与锁）。
func browserDo(action func(ctx context.Context) error) error {
	browserMu.Lock()
	ctx := browserCtx
	browserMu.Unlock()
	if ctx == nil {
		return fmt.Errorf("浏览器尚未打开：先调用 browser_open")
	}
	runCtx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()
	return chromedp.Run(runCtx, chromedp.ActionFunc(action))
}

func browserOpenTool() Tool {
	return Tool{
		Spec: toolSpec("browser_open",
			"打开或导航到指定网页（复用同一个可见浏览器窗口）。只接受 http/https 地址。返回页面标题与最终地址。",
			objSchema(map[string]any{"url": strProp("完整网址，http 或 https")}, "url")),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器"); err != nil {
				return "", err
			}
			var p struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			u := strings.TrimSpace(p.URL)
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				return "", fmt.Errorf("只支持 http/https 地址，收到: %s", u)
			}
			if _, err := acquireBrowser(); err != nil {
				return "", err
			}
			var title string
			if err := browserDo(func(ctx context.Context) error {
				if err := chromedp.Run(ctx, chromedp.Navigate(u)); err != nil {
					return err
				}
				if err := chromedp.Run(ctx, chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
					return err
				}
				return chromedp.Run(ctx, chromedp.Title(&title))
			}); err != nil {
				return "", err
			}
			return fmt.Sprintf("已打开：%s（标题：%s）", u, title), nil
		},
	}
}

func browserContentTool() Tool {
	return Tool{
		Spec: toolSpec("browser_content",
			"读取当前网页的地址、标题与可见正文文本（截断到 2 万字符）。读不到正文时可用 browser_evaluate 自行提取。",
			objSchema(map[string]any{}),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器"); err != nil {
				return "", err
			}
			var url, title, body string
			if err := browserDo(func(ctx context.Context) error {
				return chromedp.Run(ctx,
					chromedp.Location(&url),
					chromedp.Title(&title),
					chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &body),
				)
			}); err != nil {
				return "", err
			}
			if len([]rune(body)) > 20000 {
				body = string([]rune(body)[:20000]) + "\n…（正文过长已截断，可用 browser_evaluate 分段提取）"
			}
			return fmt.Sprintf("地址：%s\n标题：%s\n\n%s", url, title, body), nil
		},
	}
}

func browserClickTool() Tool {
	return Tool{
		Spec: toolSpec("browser_click",
			"点击当前网页里匹配 CSS 选择器的元素（第一个匹配项）。选择器示例：#submit、a.nav、button[class='btn primary']。点击后稍等页面反应。",
			objSchema(map[string]any{"selector": strProp("CSS 选择器")}, "selector"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器"); err != nil {
				return "", err
			}
			var p struct {
				Selector string `json:"selector"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			err := browserDo(func(ctx context.Context) error {
				return chromedp.Run(ctx,
					chromedp.WaitVisible(p.Selector, chromedp.ByQueryAll),
					chromedp.Click(p.Selector, chromedp.ByQueryAll),
					chromedp.Sleep(800*time.Millisecond),
				)
			})
			if err != nil {
				return "", fmt.Errorf("点击失败（元素不存在或不可见）: %w", err)
			}
			return "已点击 " + p.Selector, nil
		},
	}
}

func browserTypeTool() Tool {
	return Tool{
		Spec: toolSpec("browser_type",
			"向当前网页里匹配 CSS 选择器的输入框输入文本。clear=true 时先清空已有内容（默认清空）。",
			objSchema(map[string]any{
				"selector": strProp("输入框的 CSS 选择器，如 input[name='q']"),
				"text":     strProp("要输入的文本"),
				"clear":    boolProp("是否先清空，默认 true"),
			}, "selector", "text"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器"); err != nil {
				return "", err
			}
			var p struct {
				Selector string `json:"selector"`
				Text     string `json:"text"`
				Clear    *bool  `json:"clear"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			clear := p.Clear == nil || *p.Clear
			if err := browserDo(func(ctx context.Context) error {
				return chromedp.Run(ctx, chromedp.WaitVisible(p.Selector, chromedp.ByQueryAll))
			}); err != nil {
				return "", fmt.Errorf("输入失败（元素不存在或不可见）: %w", err)
			}
			if clear {
				// Clear 对少数控件（contenteditable 等）会失败，尽力而为
				_ = browserDo(func(ctx context.Context) error {
					return chromedp.Run(ctx, chromedp.Clear(p.Selector, chromedp.ByQueryAll))
				})
			}
			if err := browserDo(func(ctx context.Context) error {
				return chromedp.Run(ctx, chromedp.SendKeys(p.Selector, p.Text, chromedp.ByQueryAll))
			}); err != nil {
				return "", fmt.Errorf("输入失败: %w", err)
			}
			return fmt.Sprintf("已向 %s 输入 %d 个字符", p.Selector, len([]rune(p.Text))), nil
		},
	}
}

func browserEvaluateTool() Tool {
	return Tool{
		Spec: toolSpec("browser_evaluate",
			"在当前网页执行 JavaScript 并返回结果（JSON 序列化）。用于提取浏览器组件覆盖不到的数据，如 document.querySelector(...).href、JSON.stringify(window.__data)。",
			objSchema(map[string]any{"js": strProp("要执行的 JavaScript 表达式或语句")}, "js"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器"); err != nil {
				return "", err
			}
			var p struct {
				JS string `json:"js"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			var res any
			if err := browserDo(func(ctx context.Context) error {
				return chromedp.Run(ctx, chromedp.Evaluate(p.JS, &res))
			}); err != nil {
				return "", err
			}
			out, err := json.Marshal(res)
			if err != nil {
				return fmt.Sprintf("%v", res), nil
			}
			s := string(out)
			if len(s) > 20000 {
				s = s[:20000] + "…"
			}
			return s, nil
		},
	}
}

func browserScreenshotTool() Tool {
	return Tool{
		Spec: toolSpec("browser_screenshot",
			"截取当前网页可见区域，以图片信封返回：用户可在界面查看，支持视觉的模型能直接看到截图。",
			objSchema(map[string]any{}),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器"); err != nil {
				return "", err
			}
			var buf []byte
			if err := browserDo(func(ctx context.Context) error {
				return chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf))
			}); err != nil {
				return "", err
			}
			att, err := t.app.storeAttachment(buf, sniffImageMediaType(buf))
			if err != nil {
				return "", err
			}
			var url, title string
			_ = browserDo(func(ctx context.Context) error {
				return chromedp.Run(ctx, chromedp.Location(&url), chromedp.Title(&title))
			})
			return fmt.Sprintf("%s\n截图完成：%s（标题：%s，%dx%d）",
				imageEnvelope(att, "browser-screenshot.png"), url, title, att.Width, att.Height), nil
		},
	}
}

func browserCloseTool() Tool {
	return Tool{
		Spec: toolSpec("browser_close", "关闭受控的浏览器窗口。下次 browser_open 会重新启动。", objSchema(map[string]any{})),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器"); err != nil {
				return "", err
			}
			closeBrowser()
			return "浏览器已关闭", nil
		},
	}
}

func browserTools() []Tool {
	return []Tool{
		browserOpenTool(), browserContentTool(), browserClickTool(),
		browserTypeTool(), browserEvaluateTool(), browserScreenshotTool(),
		browserCloseTool(),
	}
}
