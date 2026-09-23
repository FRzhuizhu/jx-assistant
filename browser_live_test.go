package main

// 浏览器组件的实弹链路测试：真实启动本机浏览器（有头窗口），打开一个
// 内嵌 data: 页面，点击按钮后读回状态。需要本机装有 Chrome/Edge；
// -short 模式跳过。

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestLiveBrowserPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("live test")
	}
	page := `data:text/html,<html><body><button id="b" onclick="this.textContent='clicked'">hi</button></body></html>`
	if _, err := acquireBrowser(); err != nil {
		t.Fatalf("启动浏览器失败: %v", err)
	}
	t.Cleanup(closeBrowser)

	// 打开页面
	if err := browserDo(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Navigate(page))
	}); err != nil {
		t.Fatalf("导航失败: %v", err)
	}
	// 点击按钮
	if err := browserDo(func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.WaitVisible("#b", chromedp.ByQueryAll),
			chromedp.Click("#b", chromedp.ByQueryAll),
		)
	}); err != nil {
		t.Fatalf("点击失败: %v", err)
	}
	// 读回按钮状态
	var text string
	if err := browserDo(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById("b").textContent`, &text))
	}); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if text != "clicked" {
		t.Fatalf("点击未生效，按钮文本=%q", text)
	}
	// 截图链路（复用 browser_screenshot 的核心步骤）
	var buf []byte
	if err := browserDo(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf))
	}); err != nil {
		t.Fatalf("截图失败: %v", err)
	}
	if len(buf) == 0 || sniffImageMediaType(buf) != "image/png" {
		t.Fatalf("截图不是合法 PNG（%d 字节）", len(buf))
	}
	t.Logf("浏览器链路全部通过：点击生效，截图 %d 字节", len(buf))
}
