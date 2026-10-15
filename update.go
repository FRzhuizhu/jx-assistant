// 更新检查：定期拉取发布源（默认 Gitea releases/latest API），tag_name 比本
// 程序版本新就经事件流给所有客户端推「新版本可用」；最新版本信息同时进
// /api/status（客户端刚打开也能看到，弥补 SSE 无回放）。检查结果落
// data/update.json——升级过版本后只提醒一次，重启不复读。
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpdateState 最近一次检查到的发布信息（内存态，/api/status 直接引用）。
type UpdateState struct {
	Available   bool   `json:"available"`             // 最新版本比当前新
	Latest      string `json:"latest,omitempty"`      // 最新版本号（tag_name，去 v 前缀）
	Notes       string `json:"notes,omitempty"`       // 发布说明（body 截断）
	URL         string `json:"url,omitempty"`         // 发布页（html_url）
	DownloadURL string `json:"downloadUrl,omitempty"` // 安装包直链（完整安装路：横幅「确认更新」与设置页按钮共用）
	PublishedAt string `json:"publishedAt,omitempty"`
	Current     string `json:"current"`             // 当前运行版本
	CheckedAt   string `json:"checkedAt,omitempty"` // 最近一次成功检查时间
	LastError   string `json:"lastError,omitempty"` // 最近一次检查/应用失败原因（发布源不可达等）
	// 分块静默更新（update_blocks.go）
	BlocksMode     bool              `json:"blocksMode,omitempty"`     // 发布源带分块清单（走静默分块路）
	PendingRestart bool              `json:"pendingRestart,omitempty"` // engine 已交换，重启后生效
	PendingVersion string            `json:"pendingVersion,omitempty"`
	Applied        map[string]string `json:"applied,omitempty"` // 已静默应用的组件块 id→版本
}

var (
	updateMu       sync.Mutex
	updateState    = UpdateState{Current: appVersion}
	updateStop     chan struct{}
	updateNotified string // 已广播过的版本：同一版本只推一次
	updateCheckMu  sync.Mutex
)

func updateStatePath(a *App) string { return filepath.Join(a.Cfg.DataDir, "update.json") }

// versionCompare 版本号两套体系：新的「日期+V序号」（20261008V1，同日发多版用
// V2 V3）与旧的点分号（0.1.9）。日期体系恒新于旧体系（体系切换只升不降）；同为
// 日期体系先比 8 位日期再比 V 序号（V10 > V9）；同为旧体系按点分数字段比较
// （非数字段按 0 处理，长度不齐补零——"0.2" == "0.2.0"）。
func versionCompare(a, b string) int {
	ad, an, aok := parseDateVersion(a)
	bd, bn, bok := parseDateVersion(b)
	switch {
	case aok && bok:
		if ad != bd {
			if ad > bd {
				return 1
			}
			return -1
		}
		if an != bn {
			if an > bn {
				return 1
			}
			return -1
		}
		return 0
	case aok:
		return 1 // 新体系 > 旧体系
	case bok:
		return -1
	}
	nums := func(s string) [3]int {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		var out [3]int
		for i, p := range strings.Split(s, ".") {
			if i >= 3 {
				break
			}
			n, _ := strconv.Atoi(strings.TrimSpace(p))
			out[i] = n
		}
		return out
	}
	an2, bn2 := nums(a), nums(b)
	for i := 0; i < 3; i++ {
		if an2[i] > bn2[i] {
			return 1
		}
		if an2[i] < bn2[i] {
			return -1
		}
	}
	return 0
}

// parseDateVersion 解析「日期-序号」版本号（20261013-1 → 20261013, 1）；旧写法「日期+V序号」（20261008V10 → 20261008, 10）一并识别——存量已装版本报 V 格式，混比时仍走精确的日期+序号比较。
var dateVersionRe = regexp.MustCompile(`^(\d{8})(?:[Vv]|-)(\d+)$`)

func parseDateVersion(s string) (date, serial int64, ok bool) {
	m := dateVersionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	d, err1 := strconv.ParseInt(m[1], 10, 64)
	n, err2 := strconv.ParseInt(m[2], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return d, n, true
}

// StartUpdateChecker 启动后台轮询：启动 30s 后首查（不拖慢启动），之后每 6h。
func StartUpdateChecker(a *App) {
	url := a.Cfg.UpdateURL
	if url == "" || url == "off" {
		return
	}
	updateStop = make(chan struct{})
	go func() {
		select {
		case <-time.After(30 * time.Second):
		case <-updateStop:
			return
		}
		a.checkUpdateOnce(url)
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				a.checkUpdateOnce(url)
			case <-updateStop:
				return
			}
		}
	}()
}

func StopUpdateChecker() {
	if updateStop != nil {
		close(updateStop)
		updateStop = nil
	}
}

// releaseInfo Gitea releases API（/releases/latest）用到的字段。
type releaseInfo struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	PublishedAt string         `json:"published_at"`
	Assets      []releaseAsset `json:"assets"`
}

// releaseAsset 发布附件（安装包等）。
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// pickInstaller 从附件里挑安装包：优先名字含「安装包」的 .exe，其次任意 .exe。
// 都没有返回空串——客户端就没有可下载物，横幅只提示不发版。
// sourceURL 是本次检查用的发布源地址：Gitea 的 browser_download_url 由服务端
// ROOT_URL 生成，若哪天被配回局域网地址，这里按发布源的同 scheme/host 改写，
// 保证手机等外网设备拿到的永远是可下载的公网地址。
func pickInstaller(assets []releaseAsset, sourceURL string) string {
	fallback := ""
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if !strings.HasSuffix(n, ".exe") {
			continue
		}
		url := publicizeURL(a.BrowserDownloadURL, sourceURL)
		if strings.Contains(a.Name, "安装包") || strings.Contains(strings.ToLower(a.Name), "setup") || strings.Contains(n, "installer") {
			return url
		}
		if fallback == "" {
			fallback = url
		}
	}
	return fallback
}

// publicizeURL 检测到下载地址指向私网/回环地址时，改用发布源的公网
// scheme+host 重组（路径与查询原样保留）；本来就是公网地址则原样返回。
func publicizeURL(raw, sourceURL string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if !isPrivateHost(u.Hostname()) {
		return raw
	}
	b, err := url.Parse(sourceURL)
	if err != nil || b.Host == "" || isPrivateHost(b.Hostname()) {
		return raw
	}
	u.Scheme = b.Scheme
	u.Host = b.Host
	return u.String()
}

func (a *App) checkUpdateOnce(url string) {
	// 手动检查与 6h 轮询可能同拍触发；已有一轮在跑就跳过（结果共享）
	if !updateCheckMu.TryLock() {
		return
	}
	defer updateCheckMu.Unlock()
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		setUpdateError(err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 404 = 发布源还没有任何 release：静默视为「无更新」，不算错误
		if resp.StatusCode == http.StatusNotFound {
			setUpdateError("")
			return
		}
		setUpdateError("发布源返回 HTTP " + strconv.Itoa(resp.StatusCode))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		setUpdateError(err.Error())
		return
	}
	var rel releaseInfo
	if err := json.Unmarshal(raw, &rel); err != nil || rel.TagName == "" {
		setUpdateError("发布源响应不是 releases/latest 格式")
		return
	}
	setUpdateError("")
	// 分块静默更新（update_blocks.go）：附件里有 blocks.json 时先取清单——
	// 清单覆盖 engine 且自动模式开启则抑制「新版本可用」整包横幅（改走静默
	// 应用 + update_ready 就绪横幅）；清单缺失/失败完全回退整包路
	blocks := fetchUpdateBlocks(rel, url)
	if blocks != nil {
		updateMu.Lock()
		updateState.BlocksMode = true
		updateMu.Unlock()
	}
	silentEngine := blocks != nil && a.silentUpdateEnabled() &&
		blocks.Engine != nil && versionCompare(blocks.Engine.Version, appVersion) > 0
	applyUpdateInfo(a, rel, url, silentEngine)
	if blocks != nil && a.silentUpdateEnabled() {
		a.applyBlocksPlan(blocks, rel, url)
	}
}

// silentUpdateEnabled 更新方式：auto（默认）=分块静默应用；manual=只提醒不动手。
func (a *App) silentUpdateEnabled() bool { return a.Cfg.UpdateMode != "manual" }

func setUpdateError(msg string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	updateState.LastError = msg
	if msg == "" {
		updateState.CheckedAt = time.Now().Format("2006-01-02 15:04")
	}
}

func applyUpdateInfo(a *App, rel releaseInfo, sourceURL string, suppressBanner bool) {
	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	notes := strings.TrimSpace(rel.Body)
	if len(notes) > 500 {
		notes = notes[:500] + "…"
	}
	download := pickInstaller(rel.Assets, sourceURL)
	updateMu.Lock()
	updateState.Latest = latest
	updateState.Notes = notes
	updateState.URL = publicizeURL(rel.HTMLURL, sourceURL)
	updateState.DownloadURL = download
	updateState.PublishedAt = rel.PublishedAt
	updateState.Available = versionCompare(latest, appVersion) > 0
	shouldNotify := updateState.Available && updateNotified != latest && !suppressBanner
	if shouldNotify {
		updateNotified = latest
	}
	updateMu.Unlock()

	// 提醒过一次就落盘记 version：重启后不重复弹（版本再次更新才会重新提醒）。
	// 事件只当「门铃」，前端收到后拉 /api/status 取含 downloadUrl 的全量信息。
	if shouldNotify && !alreadyNotifiedVersion(a, latest) {
		markNotifiedVersion(a, latest)
		log.Printf("[更新] 发现新版本 %s（当前 %s），已通知在线客户端", latest, appVersion)
		a.broadcast(Event{Type: "update_available", Title: latest})
	}
}

func notifiedVersionPath(a *App) string { return filepath.Join(a.Cfg.DataDir, "update.json") }

func alreadyNotifiedVersion(a *App, v string) bool {
	raw, err := os.ReadFile(notifiedVersionPath(a))
	if err != nil {
		return false
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	return m["notified"] == v
}

func markNotifiedVersion(a *App, v string) {
	raw, _ := json.Marshal(map[string]string{"notified": v, "at": time.Now().Format(time.RFC3339)})
	_ = os.MkdirAll(a.Cfg.DataDir, 0755)
	_ = os.WriteFile(notifiedVersionPath(a), raw, 0644)
}
