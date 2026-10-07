// headedprobe: 复现 browser_host.go 有头 chromedp 启动路径的最小探针。
// 用法: headedprobe [headless|headed]；打印启动耗时与结果，25 秒看门狗。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"
)

func findBrowserExe() string {
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		os.Getenv("LOCALAPPDATA") + `\Microsoft\Edge\Application\msedge.exe`,
		os.Getenv("LOCALAPPDATA") + `\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func main() {
	mode := "headed"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	profile, _ := filepath.Abs(filepath.Join("data", "browser-profile-probe-"+mode))
	_ = os.MkdirAll(profile, 0o755)
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("user-data-dir", profile),
	)
	if mode == "headed" {
		opts = append(opts,
			chromedp.Flag("headless", false),
			chromedp.Flag("disable-gpu", false),
		)
	}
	if exe := findBrowserExe(); exe != "" {
		opts = append(opts, chromedp.ExecPath(exe))
	}
	fmt.Printf("mode=%s exe=%s\n", mode, findBrowserExe())
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			fmt.Printf("START FAILED after %v: %v\n", time.Since(start), err)
			os.Exit(1)
		}
		fmt.Printf("START OK after %v\n", time.Since(start))
	case <-time.After(25 * time.Second):
		fmt.Printf("START HUNG (watchdog 25s) — 同 browser_host.go 看门狗场景\n")
		os.Exit(2)
	}
	// 启动成功则试导航
	navStart := time.Now()
	runCtx, navCancel := context.WithTimeout(ctx, 30*time.Second)
	defer navCancel()
	var url, title string
	err := chromedp.Run(runCtx,
		chromedp.Navigate("https://example.com"),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Location(&url), chromedp.Title(&title))
	if err != nil {
		fmt.Printf("NAVIGATE FAILED after %v: %v\n", time.Since(navStart), err)
		os.Exit(3)
	}
	fmt.Printf("NAVIGATE OK after %v: %s (%s)\n", time.Since(navStart), url, title)
	cancel()
	allocCancel()
	time.Sleep(500 * time.Millisecond)
	fmt.Println("DONE")
}
