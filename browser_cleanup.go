package main

// 会话浏览器 profile 的按天回收。每会话独立的浏览器配置目录（三类形态：
// data/browser-profiles/<会话>/、data/browser-profiles-headed/<会话>/、
// data/webview-browsers/<会话>/，见 browser_host.go / browser_panel_windows.go）
// 随会话数无限累积（一台活跃机器 125 个目录 15GB+），而旧会话大概率再也不会
// 回来用浏览器。超过保留天数（BrowserProfileDays，默认 3）未活跃的目录整体
// 删除：旧会话下次用浏览器时按需重建 profile（登录态丢失，产品口径已确认
// 可接受）。双保险防误删活跃会话：① 以树内最新 mtime 判活跃（浏览器使用中
// 必然持续落盘，只看顶层目录 mtime 会漏掉子目录里的写入）；② 白名单——当前
// 在跑的会话与内存里活着的浏览器实例/面板，即使超龄也不动。共享目录
//（browser-profile 等旧版单实例目录）不是按会话堆积的，不在扫描范围。

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

// browserProfileRoots 每会话 profile 的根目录名（DataDir 相对）。
var browserProfileRoots = []string{"browser-profiles", "browser-profiles-headed", "webview-browsers"}

// browserProfileDefaultDays 保留天数缺省值（配置未写该键时按它）。
const browserProfileDefaultDays = 3

// browserProfileRetentionDays 解析配置：nil（未配置）= 默认 3；负数归一为 0=不清理。
func (a *App) browserProfileRetentionDays() int {
	if a.Cfg == nil || a.Cfg.BrowserProfileDays == nil {
		return browserProfileDefaultDays
	}
	if *a.Cfg.BrowserProfileDays < 0 {
		return 0
	}
	return *a.Cfg.BrowserProfileDays
}

// browserProfileJanitor 保留策略的周期执行：启动后先跑一轮，之后每天一轮
// （节奏对齐 snapshotJanitor）。
func (a *App) browserProfileJanitor() {
	time.Sleep(time.Minute)
	a.cleanupBrowserProfiles(time.Now())
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		a.cleanupBrowserProfiles(time.Now())
	}
}

// cleanupBrowserProfiles 扫描三类每会话根目录，删除超龄且不在白名单的会话
// 目录。now 由测试注入。有清理动作时落一行日志（清了几个/回收多少）。
func (a *App) cleanupBrowserProfiles(now time.Time) {
	days := a.browserProfileRetentionDays()
	if days <= 0 {
		return // 0=不清理
	}
	cutoff := now.AddDate(0, 0, -days)
	live := a.liveBrowserProfileKeys()
	cleaned, reclaimed := 0, int64(0)
	for _, root := range browserProfileRoots {
		dir := filepath.Join(a.Cfg.DataDir, root)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // 某形态没被用过、目录不存在是常态
		}
		for _, e := range entries {
			if !e.IsDir() || live[e.Name()] {
				continue
			}
			// 目录名必须是合法 profile 键形状（对齐 browserProfileKey 的收白
			// 规则）——防误把手工放置的杂物目录当目标
			if browserProfileKey(e.Name()) != e.Name() {
				continue
			}
			fresh, size := scanProfileDir(filepath.Join(dir, e.Name()), cutoff)
			if fresh {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				log.Printf("[浏览器] 清理超龄 profile %s/%s 失败: %v", root, e.Name(), err)
				continue
			}
			cleaned++
			reclaimed += size
		}
	}
	if cleaned > 0 {
		log.Printf("[浏览器] 清理 %d 个超龄会话 profile（>%d 天未活跃），回收 %.1f MB", cleaned, days, float64(reclaimed)/(1<<20))
	}
}

// liveBrowserProfileKeys 白名单键集（键=browserProfileKey 口径）：正在执行的
// 会话（内存注册表，IsRunning）+ 内存里活着的浏览器实例/面板/最近工具目标
// （browserActive 也算——browser_open 刚选定目标但实例记录尚未落位的窗口期）。
func (a *App) liveBrowserProfileKeys() map[string]bool {
	live := map[string]bool{}
	a.browserMu.Lock()
	for k := range a.browsersEmbedded {
		live[k] = true
	}
	for k := range a.browsersHeaded {
		live[k] = true
	}
	for k := range a.browserActive {
		live[k] = true
	}
	a.browserMu.Unlock()
	a.panelMu.Lock()
	for k := range a.panels {
		live[k] = true
	}
	a.panelMu.Unlock()
	if a.Store != nil {
		if ss, err := a.Store.List(); err == nil {
			for _, s := range ss {
				if a.IsRunning(s.ID) {
					if k := browserProfileKey(s.ID); k != "" {
						live[k] = true
					}
				}
			}
		}
	}
	return live
}

// scanProfileDir 单趟扫描 profile 目录树：fresh=树内存在晚于 cutoff 的 mtime
// （判活跃，浏览器使用中必然持续落盘，子目录的写入也要算）；size=树内文件
// 字节总量（回收量统计）。发现新 mtime 即刻返回——活跃目录不必走完整棵树；
// 有条目读不了同样按活跃处理：宁可这一轮不删，绝不误删。
func scanProfileDir(root string, cutoff time.Time) (fresh bool, size int64) {
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			fresh = true
			return filepath.SkipAll
		}
		info, e := d.Info()
		if e != nil {
			return nil
		}
		if info.ModTime().After(cutoff) {
			fresh = true
			return filepath.SkipAll
		}
		if !d.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return fresh, size
}
