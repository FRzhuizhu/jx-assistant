// 更新分发（两级拉取式）：总服务端发布 → 客户服务端定期拉上游并镜像 → 客户端
// updateURL 指向本服务端的 /updates/latest。
//
//	总服务端（公司总部）  jxai-server publish-update 安装包.exe -version 20261008V1
//	                       └─ GET /updates/latest（给下级服务端与直连客户端）
//	客户公司服务端        jxai-server -update-upstream http://总服务端:PORT
//	                       └─ 启动后 10s 首拉、每小时对账：上游版本更新则下载镜像
//	客户端（数字助理）    config.updateURL = http://本服务端:PORT/updates/latest
//	                       └─ 现有轮询/横幅/确认更新 全部复用（见下）
//
// /updates/latest 下发 Gitea releases/latest 兼容 JSON（tag_name + assets 直链），
// 客户端（update.go）零协议改动；附件直链按请求方到达的 Host 生成——127.0.0.1、
// 内网 IP、公网域名随请求自动正确，不需要服务端配置自己的对外地址。
// 未发布过任何版本时返回 404（与 Gitea 无 release 同语义，客户端静默视为无更新）。
//
// 数据布局：data/updates/latest.json + data/updates/files/<版本>/<安装包名>.exe。
// 版本号体系：日期+V序号（20261008V1、同日 V2），恒新于旧点分号（0.1.9）。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// updateInfo 本服务端当前持有的最新版本（latest.json 的结构）。
type updateInfo struct {
	Version     string `json:"version"`         // 日期+V序号（20261008V1）
	Notes       string `json:"notes,omitempty"` // 发布说明（横幅里截断展示）
	Filename    string `json:"filename"`        // 安装包文件名
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256,omitempty"`
	PublishedAt string `json:"publishedAt"`      // RFC3339
	Source      string `json:"source,omitempty"` // 镜像来源（上游地址；总服务端发布时为空）
}

type updateStore struct {
	dir      string // 数据目录；latest 在 dir/updates/latest.json，包在 dir/updates/files/
	upstream string // 总服务端地址（空 = 自己就是源头，不拉取）
	mu       chan struct{}
}

func newUpdateStore(dir, upstream string) *updateStore {
	return &updateStore{dir: dir, upstream: strings.TrimRight(strings.TrimSpace(upstream), "/"), mu: make(chan struct{}, 1)}
}

func (s *updateStore) latestPath() string { return filepath.Join(s.dir, "updates", "latest.json") }
func (s *updateStore) filesDir() string   { return filepath.Join(s.dir, "updates", "files") }

func (s *updateStore) load() (*updateInfo, error) {
	data, err := os.ReadFile(s.latestPath())
	if err != nil {
		return nil, err
	}
	var u updateInfo
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

/* ---------- 版本号体系：日期+V序号（新）与点分号（旧） ---------- */

var dateVerRe = regexp.MustCompile(`^(\d{8})[Vv](\d+)$`)

// dateVer 解析「日期+V序号」（20261008V10 → 20261008, 10）。
func dateVer(s string) (date, serial int64, ok bool) {
	m := dateVerRe.FindStringSubmatch(strings.TrimSpace(s))
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

// versionCompare 双体系比较：日期体系恒新于旧点分体系（体系切换只升不降）；
// 同为日期体系比日期再比 V 序号（V10 > V9）；同为旧体系按点分数字段。
func versionCompare(a, b string) int {
	ad, an, aok := dateVer(a)
	bd, bn, bok := dateVer(b)
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
	// 旧点分体系（与主程序 update.go 的旧逻辑同语义）
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

/* ---------- 发布（总服务端） ---------- */

// publishUpdate 登记一个新版本：安装包落 files/<版本>/，latest.json 原子换新。
func (s *updateStore) publishUpdate(src, version, notes string) (*updateInfo, error) {
	if _, _, ok := dateVer(version); !ok {
		return nil, fmt.Errorf("版本号需为「日期+V序号」（如 20261008V1），收到 %q", version)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("读取安装包失败: %w", err)
	}
	name := filepath.Base(src)
	dst := filepath.Join(s.filesDir(), version, name)
	if err := writeFileAtomic(dst, data, 0644); err != nil {
		return nil, fmt.Errorf("写包失败: %w", err)
	}
	sum := sha256.Sum256(data)
	info := &updateInfo{
		Version: version, Notes: notes, Filename: name,
		Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
		PublishedAt: time.Now().Format(time.RFC3339),
	}
	if err := writeFileAtomic(s.latestPath(), mustJSONIndent(info), 0644); err != nil {
		return nil, fmt.Errorf("写 latest.json 失败: %w", err)
	}
	// 旧版本包不再被引用：清掉（与市场同款策略，存储不随版本累积）
	if entries, err := os.ReadDir(s.filesDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != version {
				_ = os.RemoveAll(filepath.Join(s.filesDir(), e.Name()))
			}
		}
	}
	return info, nil
}

/* ---------- HTTP：给客户端与下级服务端 ---------- */

// handleLatest GET /updates/latest：Gitea releases/latest 兼容 JSON；附件直链按
// 请求 Host 现场生成（请求方用什么地址到达，下载地址就是什么地址）。未发布过
// 任何版本 → 404（客户端静默视为无更新）。
func (s *updateStore) handleLatest(w http.ResponseWriter, r *http.Request) {
	info, err := s.load()
	if err != nil || info.Version == "" || info.Filename == "" {
		http.NotFound(w, r)
		return
	}
	base := schemeOf(r) + "://" + r.Host
	dl := base + "/updates/file/" + url.PathEscape(info.Version) + "/" + url.PathEscape(info.Filename)
	page := info.Source
	if page == "" {
		page = base + "/updates/latest"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tag_name":     info.Version,
		"name":         info.Version,
		"body":         info.Notes,
		"html_url":     page,
		"published_at": info.PublishedAt,
		"assets": []map[string]any{{
			"name":                 info.Filename,
			"browser_download_url": dl,
			"size":                 info.Size,
		}},
	})
}

// handleUpdateFile GET /updates/file/<版本>/<安装包名>：安装包本体（ServeFile
// 天然支持 Range，浏览器下载可续传）。逐段校验防穿越。
func (s *updateStore) handleUpdateFile(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/updates/file/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `\/`) {
			http.NotFound(w, r)
			return
		}
	}
	info, err := s.load()
	if err != nil || parts[0] != info.Version || parts[1] != info.Filename {
		http.NotFound(w, r) // 只发当前版本：旧版本包已清理，直链随之失效
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filepath.Join(s.filesDir(), info.Version, info.Filename))
}

// schemeOf 反代后 X-Forwarded-Proto 优先，否则按 TLS 连接判断（本服务多为明文，
// 默认 http）。
func schemeOf(r *http.Request) string {
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

/* ---------- 上游同步（客户服务端） ---------- */

// giteaRelease 与主程序 update.go 同形（tag_name + assets），从总服务端拉的就是
// 它自己下发的 /updates/latest。
type giteaRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

var updateHTTP = &http.Client{Timeout: 30 * time.Second}
var updateDownload = &http.Client{Timeout: 10 * time.Minute}

// startUpdateSyncer 启动即 10s 首拉、此后每小时对账；失败只记日志不动现有版本。
func (s *updateStore) startUpdateSyncer() {
	if s.upstream == "" {
		return
	}
	go func() {
		select {
		case <-time.After(10 * time.Second):
		case <-s.mu: // 关停（当前实现随进程退出，实际用不到，保结构对称）
			return
		}
		s.syncOnce()
		for range time.Tick(time.Hour) {
			s.syncOnce()
		}
	}()
}

// syncOnce 拉一次上游：版本更新才镜像（下载安装包 + 换 latest.json）。
func (s *updateStore) syncOnce() {
	rel, err := fetchRelease(s.upstream + "/updates/latest")
	if err != nil {
		log.Printf("[更新] 上游 %s 拉取失败（保留现有版本）: %v", s.upstream, err)
		return
	}
	if rel.TagName == "" {
		return
	}
	cur, _ := s.load()
	if cur != nil && versionCompare(rel.TagName, cur.Version) <= 0 {
		return // 上游不比本地新
	}
	var assetURL, assetName string
	for _, a := range rel.Assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".exe") {
			assetURL, assetName = a.BrowserDownloadURL, a.Name
			if strings.Contains(a.Name, "安装包") || strings.Contains(n, "setup") || strings.Contains(n, "installer") {
				break
			}
		}
	}
	if assetURL == "" {
		log.Printf("[更新] 上游 %s 的 %s 没有可下载的安装包附件，跳过", s.upstream, rel.TagName)
		return
	}
	if !strings.HasPrefix(assetURL, "http://") && !strings.HasPrefix(assetURL, "https://") {
		assetURL = s.upstream + assetURL // 相对路径按上游补全
	}
	data, err := downloadUpdate(assetURL)
	if err != nil {
		log.Printf("[更新] 安装包下载失败（保留现有版本）: %v", err)
		return
	}
	sum := sha256.Sum256(data)
	info := &updateInfo{
		Version: rel.TagName, Notes: rel.Body, Filename: assetName,
		Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
		PublishedAt: rel.PublishedAt, Source: rel.HTMLURL,
	}
	if info.PublishedAt == "" {
		info.PublishedAt = time.Now().Format(time.RFC3339)
	}
	dst := filepath.Join(s.filesDir(), info.Version, info.Filename)
	if err := writeFileAtomic(dst, data, 0644); err != nil {
		log.Printf("[更新] 安装包落盘失败: %v", err)
		return
	}
	if err := writeFileAtomic(s.latestPath(), mustJSONIndent(info), 0644); err != nil {
		log.Printf("[更新] latest.json 落盘失败: %v", err)
		return
	}
	// 清旧版本包（与发布同款策略）
	if entries, err := os.ReadDir(s.filesDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != info.Version {
				_ = os.RemoveAll(filepath.Join(s.filesDir(), e.Name()))
			}
		}
	}
	log.Printf("[更新] 已从上游同步 %s（%.1f MB，包 %s）", info.Version, float64(info.Size)/1048576, info.Filename)
}

func fetchRelease(url string) (*giteaRelease, error) {
	resp, err := updateHTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &giteaRelease{}, nil // 上游还没发过版：静默
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var rel giteaRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("响应不是 releases/latest 格式: %w", err)
	}
	return &rel, nil
}

// downloadUpdate 下载安装包到内存（安装包百兆级，2GB 上限足够且避免落盘半包）。
func downloadUpdate(url string) ([]byte, error) {
	resp, err := updateDownload.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<30))
}

/* ---------- 子命令 ---------- */

func runPublishUpdate(args []string) int {
	fs := flag.NewFlagSet("publish-update", flag.ExitOnError)
	data := fs.String("data", "data", "数据目录")
	version := fs.String("version", "", "版本号（日期+V序号，如 20261008V1；必填）")
	notes := fs.String("notes", "", "发布说明（横幅里展示一句话）")
	_ = fs.Parse(flagsFirst(args))
	rest := fs.Args()
	if len(rest) != 1 || strings.TrimSpace(*version) == "" {
		fmt.Fprintln(os.Stderr, "用法: jxai-server publish-update <安装包.exe> -version 20261008V1 [-notes 说明] [-data data]")
		return 2
	}
	s := newUpdateStore(*data, "")
	info, err := s.publishUpdate(rest[0], strings.TrimSpace(*version), *notes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "发布失败:", err)
		return 1
	}
	fmt.Printf("已发布更新 %s（%.1f MB，包 %s）\n", info.Version, float64(info.Size)/1048576, info.Filename)
	fmt.Printf("客户端检查地址：%s/updates/latest（updateURL 指到这里）\n", "http://<本服务地址:端口>")
	return 0
}
