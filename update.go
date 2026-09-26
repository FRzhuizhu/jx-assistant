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
	DownloadURL string `json:"downloadUrl,omitempty"` // 安装包直链（release 附件，客户端「确认更新」下载用）
	PublishedAt string `json:"publishedAt,omitempty"`
	Current     string `json:"current"`             // 当前运行版本
	CheckedAt   string `json:"checkedAt,omitempty"` // 最近一次成功检查时间
	LastError   string `json:"lastError,omitempty"` // 最近一次检查失败原因（发布源不可达等）
}

var (
	updateMu       sync.Mutex
	updateState    = UpdateState{Current: appVersion}
	updateStop     chan struct{}
	updateNotified string // 已广播过的版本：同一版本只推一次
)

func updateStatePath(a *App) string { return filepath.Join(a.Cfg.DataDir, "update.json") }

// versionCompare 比较点分版本号（忽略 v 前缀与忽略后缀段）：a>b 返 1，a<b 返 -1，相等 0。
// 非数字段按 0 处理，长度不齐补零——"0.2" == "0.2.0"。
func versionCompare(a, b string) int {
	nums := func(s string) []int {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		parts := strings.Split(s, ".")
		out := make([]int, 0, 3)
		for _, p := range parts {
			n, _ := strconv.Atoi(strings.TrimSpace(p))
			out = append(out, n)
		}
		for len(out) < 3 {
			out = append(out, 0)
		}
		return out
	}
	an, bn := nums(a), nums(b)
	for i := 0; i < 3; i++ {
		if an[i] > bn[i] {
			return 1
		}
		if an[i] < bn[i] {
			return -1
		}
	}
	return 0
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
	applyUpdateInfo(a, rel, url)
}

func setUpdateError(msg string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	updateState.LastError = msg
	if msg == "" {
		updateState.CheckedAt = time.Now().Format("2006-01-02 15:04")
	}
}

func applyUpdateInfo(a *App, rel releaseInfo, sourceURL string) {
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
	shouldNotify := updateState.Available && updateNotified != latest
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
