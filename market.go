package main

// 技能/组件/插件市场（客户端半边）：从市场服务器（jxaiserver，见 jxaiserver/）拉
// 清单，一键安装/更新/卸载，按类型路由到三套既有体系：
//
//	skill     → addUserSkill / deleteUserSkill（技能/user/<id>/；更新=先删再装）
//	component → 组件/<id>/ 落盘 + Components.Rescan（前端随后 CX.refresh 热加载）
//	plugin    → ccplugin 体系的 ccInstallFromPath / Uninstall（市场归属名 jx-market）
//
// 安装记账 data/market_installed.json：技能与组件本体没有可靠的版本来源，
// 安装时记账是「已装版本 / 可更新」判定的唯一事实；插件的版本以 ccplugin
// 安装记录为准。市场地址在 config.marketURL（设置页「网络」分区填写），
// 未配置时返回建议值（与中继网关同 host 时市场就在同一台 jxai-server 上）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// marketPluginHome 市场安装的 Claude 插件归属的市场名（安装键 name@jx-market；
// 服务端发布插件时条目 id 即 ccSanitizeName(plugin 名)，两端键对齐）。
const marketPluginHome = "jx-market"

type marketEntryIn struct { // 服务器清单条目
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Zip         string   `json:"zip"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
}

type marketManifestIn struct {
	Name       string          `json:"name"`
	UpdatedAt  string          `json:"updatedAt"`
	Skills     []marketEntryIn `json:"skills"`
	Components []marketEntryIn `json:"components"`
	Plugins    []marketEntryIn `json:"plugins"`
}

// marketEntryOut 清单条目 + 本机安装态（前端渲染用）。
type marketEntryOut struct {
	marketEntryIn
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Update           bool   `json:"update"`
}

type marketView struct {
	Configured bool             `json:"configured"`
	URL        string           `json:"url,omitempty"`
	Name       string           `json:"name,omitempty"`
	Suggested  string           `json:"suggested,omitempty"` // 未配置时的建议值（中继网关同 host）
	Error      string           `json:"error,omitempty"`
	UpdatedAt  string           `json:"updatedAt,omitempty"`
	Skills     []marketEntryOut `json:"skills"`
	Components []marketEntryOut `json:"components"`
	Plugins    []marketEntryOut `json:"plugins"`
}

type marketInstallRec struct {
	Version     string `json:"version"`
	Key         string `json:"key,omitempty"` // plugin: name@market；component: 组件目录名；skill: 技能 id
	InstalledAt string `json:"installedAt"`
}

type marketCacheEntry struct {
	url string
	at  time.Time
	man *marketManifestIn
}

var (
	marketHTTPClient     = &http.Client{Timeout: 15 * time.Second}
	marketDownloadClient = &http.Client{Timeout: 5 * time.Minute}
)

func (a *App) marketRecsPath() string { return filepath.Join(a.Cfg.DataDir, "market_installed.json") }

func (a *App) loadMarketRecs() map[string]marketInstallRec {
	a.marketMu.Lock()
	defer a.marketMu.Unlock()
	recs := map[string]marketInstallRec{}
	data, err := os.ReadFile(a.marketRecsPath())
	if err == nil {
		_ = json.Unmarshal(data, &recs)
	}
	return recs
}

func (a *App) saveMarketRec(typ, id string, rec marketInstallRec) error {
	a.marketMu.Lock()
	defer a.marketMu.Unlock()
	recs := map[string]marketInstallRec{}
	data, err := os.ReadFile(a.marketRecsPath())
	if err == nil {
		_ = json.Unmarshal(data, &recs)
	}
	recs[typ+":"+id] = rec
	out, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.Cfg.DataDir, 0755); err != nil {
		return err
	}
	return writeFileAtomic(a.marketRecsPath(), out)
}

func (a *App) removeMarketRec(typ, id string) {
	a.marketMu.Lock()
	defer a.marketMu.Unlock()
	recs := map[string]marketInstallRec{}
	data, err := os.ReadFile(a.marketRecsPath())
	if err == nil {
		_ = json.Unmarshal(data, &recs)
	}
	if _, ok := recs[typ+":"+id]; !ok {
		return
	}
	delete(recs, typ+":"+id)
	if out, err := json.MarshalIndent(recs, "", "  "); err == nil {
		_ = writeFileAtomic(a.marketRecsPath(), out)
	}
}

// fetchMarketManifest 拉市场清单（60s 缓存；force 用于安装前取最新）。
func (a *App) fetchMarketManifest(rawURL string, force bool) (*marketManifestIn, error) {
	a.marketMu.Lock()
	if !force && a.marketCache.man != nil && a.marketCache.url == rawURL &&
		time.Since(a.marketCache.at) < time.Minute {
		m := a.marketCache.man
		a.marketMu.Unlock()
		return m, nil
	}
	a.marketMu.Unlock()
	resp, err := marketHTTPClient.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	var m marketManifestIn
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("清单不是合法 JSON: %w", err)
	}
	a.marketMu.Lock()
	a.marketCache = marketCacheEntry{url: rawURL, at: time.Now(), man: &m}
	a.marketMu.Unlock()
	return &m, nil
}

// marketZipURL 清单里的 zip 路径是绝对路径（/market/items/...），拼上清单 URL
// 的 origin；也兼容清单直接写完整 URL 的情况。
func marketZipURL(baseURL, zipPath string) (string, error) {
	if strings.HasPrefix(zipPath, "http://") || strings.HasPrefix(zipPath, "https://") {
		return zipPath, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("市场地址不合法: %s", baseURL)
	}
	return u.Scheme + "://" + u.Host + zipPath, nil
}

func marketEntriesOf(m *marketManifestIn, typ string) []marketEntryIn {
	switch typ {
	case "skill":
		return m.Skills
	case "component":
		return m.Components
	case "plugin":
		return m.Plugins
	}
	return nil
}

func marketTypeOK(t string) bool { return t == "skill" || t == "component" || t == "plugin" }

/* ---------- 视图：清单 + 本机安装态 ---------- */

func (a *App) buildMarketView() marketView {
	v := marketView{Skills: []marketEntryOut{}, Components: []marketEntryOut{}, Plugins: []marketEntryOut{}}
	raw := strings.TrimSpace(a.Cfg.MarketURL)
	if raw == "" {
		if a.Cfg.RemoteAccess.Host != "" && a.Cfg.RemoteAccess.Port > 0 {
			v.Suggested = fmt.Sprintf("http://%s:%d/market/manifest.json",
				a.Cfg.RemoteAccess.Host, a.Cfg.RemoteAccess.Port)
		}
		return v
	}
	v.Configured = true
	v.URL = raw
	man, err := a.fetchMarketManifest(raw, false)
	if err != nil {
		v.Error = fmt.Sprintf("市场清单获取失败: %v", err)
		return v
	}
	v.Name, v.UpdatedAt = man.Name, man.UpdatedAt
	recs := a.loadMarketRecs()
	v.Skills = a.decorateEntries("skill", man.Skills, recs)
	v.Components = a.decorateEntries("component", man.Components, recs)
	v.Plugins = a.decorateEntries("plugin", man.Plugins, recs)
	return v
}

func (a *App) decorateEntries(typ string, in []marketEntryIn, recs map[string]marketInstallRec) []marketEntryOut {
	out := make([]marketEntryOut, 0, len(in))
	for _, e := range in {
		o := marketEntryOut{marketEntryIn: e}
		rec, hasRec := recs[typ+":"+e.ID]
		switch typ {
		case "skill":
			if userDir, err := a.userSkillsDir(); err == nil {
				o.Installed = dirExists(filepath.Join(userDir, sanitizeSkillID(e.ID)))
			}
			if o.Installed {
				o.InstalledVersion = rec.Version
			}
		case "component":
			_, ok := a.Components.Manifest(e.ID)
			if !ok && hasRec {
				_, ok = a.Components.Manifest(rec.Key) // 发布方 -id 与组件清单 id 不一致时按记账找
			}
			o.Installed = ok
			if o.Installed {
				o.InstalledVersion = rec.Version
			}
		case "plugin":
			st, ok := a.CCPlugins.stateOf(e.ID + "@" + marketPluginHome)
			if !ok && hasRec {
				st, ok = a.CCPlugins.stateOf(rec.Key)
			}
			o.Installed = ok && st != nil && st.Version != ""
			if o.Installed && st != nil {
				o.InstalledVersion = st.Version
				if !hasRec { // 手动装到 jx-market 名下的边角：没有记账时以实际版本兜底
					rec.Version = st.Version
				}
			}
		}
		o.Update = o.Installed && hasRec && rec.Version != "" && rec.Version != e.Version
		out = append(out, o)
	}
	return out
}

/* ---------- 安装 ---------- */

// marketDownloadZip 下载包体到临时文件并校验 sha256；调用方用 cleanup 清理。
func marketDownloadZip(zipURL string, e marketEntryIn) (zipPath string, cleanup func(), err error) {
	resp, err := marketDownloadClient.Get(zipURL)
	if err != nil {
		return "", nil, fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", nil, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.MkdirTemp("", "jxmkd-")
	if err != nil {
		return "", nil, err
	}
	fail := func(e error) (string, func(), error) {
		os.RemoveAll(tmp)
		return "", nil, e
	}
	zipPath = filepath.Join(tmp, "pkg.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return fail(err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 500<<20))
	f.Close()
	if err != nil {
		return fail(fmt.Errorf("下载写入失败: %w", err))
	}
	if e.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), e.SHA256) {
		return fail(fmt.Errorf("包校验失败（sha256 不符，下载可能不完整或被篡改）"))
	}
	return zipPath, func() { os.RemoveAll(tmp) }, nil
}

// findRootWith 在解压目录里找包含目标文件的根（根上或唯一子目录）。
func findRootWith(dir, file string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	subs := []string{}
	for _, en := range entries {
		if en.IsDir() && !strings.HasPrefix(en.Name(), ".") {
			subs = append(subs, en.Name())
		}
	}
	if len(subs) == 1 {
		sub := filepath.Join(dir, subs[0])
		if _, err := os.Stat(filepath.Join(sub, file)); err == nil {
			return sub, nil
		}
	}
	return "", fmt.Errorf("压缩包里找不到 %s（需在根部或其唯一子目录里）", file)
}

// installMarketComponent 把组件目录装进 组件/<id>/ 并重扫；id 以组件自己的
// manifest.id 为准（缺省用条目 id），返回记账。
func (a *App) installMarketComponent(e marketEntryIn, root string) (marketInstallRec, error) {
	id := ""
	var m ComponentManifest
	if data, err := os.ReadFile(filepath.Join(root, "manifest.json")); err == nil && json.Unmarshal(data, &m) == nil {
		id = strings.TrimSpace(m.ID)
	}
	if id == "" {
		id = e.ID
	}
	if strings.ContainsAny(id, `\/:*?"<>|`) || id == "." || id == ".." {
		return marketInstallRec{}, fmt.Errorf("组件 id 不能当目录名: %s", id)
	}
	dst := filepath.Join(a.Components.Dir(), id)
	if err := os.RemoveAll(dst); err != nil {
		return marketInstallRec{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return marketInstallRec{}, err
	}
	if err := copyDir(root, dst); err != nil {
		return marketInstallRec{}, fmt.Errorf("复制组件文件失败: %w", err)
	}
	a.Components.Rescan()
	if _, ok := a.Components.Manifest(id); !ok {
		return marketInstallRec{}, fmt.Errorf("组件安装后未被识别（manifest.json 异常）")
	}
	return marketInstallRec{Version: e.Version, Key: id, InstalledAt: time.Now().Format(time.RFC3339)}, nil
}

// uninstallMarketComponent 只允许卸载市场装的组件（按记账），内置组件不动。
func (a *App) uninstallMarketComponent(e marketEntryIn, rec marketInstallRec) error {
	id := rec.Key
	if id == "" {
		id = e.ID
	}
	if _, ok := a.Components.Manifest(id); !ok {
		return nil // 已不在（手动删过）：直接收尾
	}
	if err := os.RemoveAll(filepath.Join(a.Components.Dir(), id)); err != nil {
		return err
	}
	a.Components.Rescan()
	return nil
}

/* ---------- HTTP ---------- */

func (a *App) handleMarketView(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" && strings.TrimSpace(a.Cfg.MarketURL) != "" {
		_, _ = a.fetchMarketManifest(strings.TrimSpace(a.Cfg.MarketURL), true) // 市场页「刷新」：绕过 60s 缓存
	}
	writeJSON(w, 200, a.buildMarketView())
}

func (a *App) handleMarketInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !marketTypeOK(body.Type) || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "需要 type（skill/component/plugin）与 id")
		return
	}
	raw := strings.TrimSpace(a.Cfg.MarketURL)
	if raw == "" {
		writeErr(w, 400, "未配置市场服务器（设置 · 网络访问 · 市场服务器）")
		return
	}
	man, err := a.fetchMarketManifest(raw, true) // 安装前取最新清单
	if err != nil {
		writeErr(w, 502, "市场清单获取失败: "+err.Error())
		return
	}
	var entry marketEntryIn
	found := false
	for _, e := range marketEntriesOf(man, body.Type) {
		if e.ID == body.ID {
			entry, found = e, true
			break
		}
	}
	if !found {
		writeErr(w, 404, fmt.Sprintf("市场里没有 %s %s", body.Type, body.ID))
		return
	}
	zipURL, err := marketZipURL(raw, entry.Zip)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	zipPath, cleanup, err := marketDownloadZip(zipURL, entry)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer cleanup()

	var rec marketInstallRec
	resp := map[string]any{"ok": 1, "type": body.Type, "id": entry.ID, "name": entry.Name, "version": entry.Version}
	switch body.Type {
	case "skill":
		skillID := sanitizeSkillID(entry.ID)
		if userDir, err := a.userSkillsDir(); err == nil && dirExists(filepath.Join(userDir, skillID)) {
			if err := a.deleteUserSkill(skillID); err != nil { // 更新 = 先删再装
				writeErr(w, 500, "卸载旧版本失败: "+err.Error())
				return
			}
		}
		// addUserSkill 按目录名定技能 id：先包一层以条目 id 命名的目录
		root, tmp, err := extractSkillZip(zipPath)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		wrap, err := os.MkdirTemp("", "jxmks-")
		if err != nil {
			os.RemoveAll(tmp)
			writeErr(w, 500, err.Error())
			return
		}
		defer os.RemoveAll(wrap)
		err = func() error { // tmp 的清理独立于 wrap（两个临时目录都要收）
			defer os.RemoveAll(tmp)
			if err := copyDir(root, filepath.Join(wrap, skillID)); err != nil {
				return err
			}
			ent, err := a.addUserSkill(filepath.Join(wrap, skillID))
			if err != nil {
				return err
			}
			rec = marketInstallRec{Version: entry.Version, Key: ent.ID, InstalledAt: time.Now().Format(time.RFC3339)}
			resp["skill"] = ent
			return nil
		}()
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	case "component":
		tmp, err := os.MkdirTemp("", "jxmkc-")
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		defer os.RemoveAll(tmp)
		if err := unzip(zipPath, tmp); err != nil {
			writeErr(w, 400, "解压失败: "+err.Error())
			return
		}
		root, err := findRootWith(tmp, "manifest.json")
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		rec, err = a.installMarketComponent(entry, root)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		resp["components"] = a.Components.List()
	case "plugin":
		key, err := a.CCPlugins.ccInstallFromPath(zipPath, marketPluginHome) // 安装器直接吃 zip
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		rec = marketInstallRec{Version: entry.Version, Key: key, InstalledAt: time.Now().Format(time.RFC3339)}
		resp["plugin"] = key
	}
	if err := a.saveMarketRec(body.Type, entry.ID, rec); err != nil {
		log.Printf("[市场] 安装记账落盘失败（安装本身已成功）: %v", err)
	}
	writeJSON(w, 200, resp)
}

func (a *App) handleMarketUninstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !marketTypeOK(body.Type) || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "需要 type（skill/component/plugin）与 id")
		return
	}
	recs := a.loadMarketRecs()
	rec, hasRec := recs[body.Type+":"+body.ID]
	resp := map[string]any{"ok": 1}
	switch body.Type {
	case "skill":
		if err := a.deleteUserSkill(sanitizeSkillID(body.ID)); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	case "component":
		if !hasRec {
			writeErr(w, 400, "内置或手动放入的组件不通过市场卸载（设置 · 内置组件 里没有删除项，请手动删除目录）")
			return
		}
		if err := a.uninstallMarketComponent(marketEntryIn{ID: body.ID}, rec); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		resp["components"] = a.Components.List()
	case "plugin":
		key := rec.Key
		if key == "" {
			key = body.ID + "@" + marketPluginHome
		}
		if err := a.CCPlugins.Uninstall(key); err != nil && !strings.Contains(err.Error(), "未安装") {
			writeErr(w, 400, err.Error())
			return
		}
	}
	a.removeMarketRec(body.Type, body.ID)
	writeJSON(w, 200, resp)
}
