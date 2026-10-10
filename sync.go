// 公司接入自动同步：向导接入后，客户端持续跟随服务端的变化，员工零操作。
//
// 服务端（jxaiserver announce.go）两级通知：
//  1. GET /announce 公告（公司名、模型清单含能力元数据、市场/更新地址、内容哈希
//     rev）——本文件每 5 分钟拉一次（启动 20 秒后首轮），差异驱动应用；
//  2. 配置一变就向局域网 UDP 2230 广播——客户端开着一个 UDP 监听（端口被占则
//     静默退化为纯轮询），收到本公司名的广播立即触发一轮同步（秒级跟上）。
//
// 服务端搬家（换端口/换机器，如 2228→2227）：当前地址失联时按保存的公司名重新
// 局域网发现（discovery 应答自带新地址）自动迁移；广播消息里的 server 地址也作为
// 迁移候选优先一试（旧地址已死、新地址广播可达的场景）。
//
// 应用范围：公司名（内部提供方改名）、模型清单与元数据（增删/上下文/图片能力）、
// new-api 地址（baseURL）、市场/更新地址、服务端地址本身。API key 不在此轮换
// （key 由 provision 按人发放；失效重跑向导即可）。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	syncInterval    = 5 * time.Minute  // 常规轮询间隔
	syncFirstDelay  = 20 * time.Second // 启动后首轮
	syncUDPDebounce = 15 * time.Second // 广播触发的去抖窗口
	syncAnnounceUDP = 2230             // 服务端变更广播端口（jxaiserver announce.go）
)

// udpDiscoverFn 发现函数的可测试替换点。
var udpDiscoverFn = udpDiscoverServers

// announcePayload GET /announce 的载荷（与 jxaiserver announce.go 对齐）。
type announcePayload struct {
	Jxai      int    `json:"jxai"`
	Company   string `json:"company"`
	Server    string `json:"server"`
	Rev       string `json:"rev"`
	MarketURL string `json:"marketURL"`
	UpdateURL string `json:"updateURL"`
	API       *struct {
		BaseURL      string            `json:"baseURL"`
		Models       []wizardModelInfo `json:"models"`
		DefaultModel string            `json:"defaultModel"`
	} `json:"api"`
}

// startCompanySyncer 后台同步循环 + UDP 广播监听（NewApp 起一次）。
// 向导完成前（WizardServer 空）各轮空转，接入完成后无需重启即开始同步。
func startCompanySyncer(a *App) {
	go func() {
		time.Sleep(syncFirstDelay)
		for {
			if a.Cfg.WizardServer != "" {
				companySyncOnce(a, "")
			}
			time.Sleep(syncInterval)
		}
	}()
	go listenAnnounceUDP(a)
}

// companySyncOnce 一轮同步：hint 非空时优先试广播带来的地址（服务端搬家、旧地址
// 已死）；全部失联再按公司名重新局域网发现。拿到公告后差异驱动应用。
func companySyncOnce(a *App, hintServer string) {
	server := strings.TrimRight(a.Cfg.WizardServer, "/")
	if server == "" {
		return
	}
	candidates := []string{}
	if h := strings.TrimRight(strings.TrimSpace(hintServer), "/"); h != "" && h != server {
		candidates = append(candidates, h)
	}
	candidates = append(candidates, server)

	var ann *announcePayload
	for _, c := range candidates {
		if p := fetchAnnounce(c); p != nil {
			ann = p
			if c != server { // 广播地址即是新家：市场/更新/服务端地址整体迁移
				migrateServerBase(a, server, c)
				log.Printf("[公司同步] 服务端已迁移：%s → %s", server, c)
				server = c
			}
			break
		}
	}
	if ann == nil && a.Cfg.WizardCompany != "" { // 失联：按公司名重新发现（换端口/换机器）
		for _, ws := range udpDiscoverFn(a.Cfg.WizardCompany) {
			if c := strings.TrimRight(ws.Server, "/"); c != "" && c != server {
				if p := fetchAnnounce(c); p != nil {
					ann = p
					migrateServerBase(a, server, c)
					log.Printf("[公司同步] 失联后重新发现，服务端迁移：%s → %s", server, c)
					server = c
					break
				}
			}
		}
	}
	if ann == nil {
		return
	}
	applyAnnounce(a, ann, server)
}

// fetchAnnounce 拉一份公告；任何失败都返回 nil（同步器把 nil 当失联处理）。
func fetchAnnounce(server string) *announcePayload {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(strings.TrimRight(server, "/") + "/announce")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var p announcePayload
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p) != nil || p.Jxai != 1 {
		return nil
	}
	return &p
}

// migrateServerBase 服务端地址整体迁移：指向旧地址的市场/更新地址换到新地址。
func migrateServerBase(a *App, oldBase, newBase string) {
	if oldBase == "" || oldBase == newBase {
		return
	}
	swap := func(u string) string {
		if u == oldBase || strings.HasPrefix(u, oldBase+"/") {
			return newBase + strings.TrimPrefix(u, oldBase)
		}
		return u
	}
	a.Cfg.MarketURL = swap(a.Cfg.MarketURL)
	a.Cfg.UpdateURL = swap(a.Cfg.UpdateURL)
	a.Cfg.WizardServer = newBase
}

// applyAnnounce 差异驱动应用公告：有变化才写盘；空模型清单不应用（服务端拉取失败
// 的防御，避免误清员工模型）。
func applyAnnounce(a *App, ann *announcePayload, server string) {
	changed := []string{}
	if ann.Company != "" && ann.Company != a.Cfg.WizardCompany {
		a.Cfg.WizardCompany = ann.Company
		changed = append(changed, "公司名→"+ann.Company)
	}
	if p := wizardProvider(a); p != nil {
		if ann.Company != "" {
			p.Name = ann.Company + "·内部通道"
		}
		if ann.API != nil && ann.API.BaseURL != "" && len(ann.API.Models) > 0 {
			p.BaseURL = ann.API.BaseURL
			p.Models = buildWizardModels(ann.API.Models)
			changed = append(changed, fmt.Sprintf("模型×%d", len(ann.API.Models)))
			if m := a.Cfg.Model; m != "" && !providerHasModel(p, m) { // 已选模型被下线：回默认
				a.Cfg.Model = firstModelID(p.Models)
			}
		}
	}
	if ann.MarketURL != "" && ann.MarketURL != a.Cfg.MarketURL {
		a.Cfg.MarketURL = ann.MarketURL
		a.marketMu.Lock()
		a.marketCache = marketCacheEntry{} // 市场缓存作废
		a.marketMu.Unlock()
		changed = append(changed, "市场地址")
	}
	if ann.UpdateURL != "" && ann.UpdateURL != a.Cfg.UpdateURL {
		a.Cfg.UpdateURL = ann.UpdateURL
		changed = append(changed, "更新源")
	}
	if server != "" && server != a.Cfg.WizardServer {
		a.Cfg.WizardServer = server
		changed = append(changed, "服务端→"+server)
	}
	if len(changed) == 0 {
		return
	}
	a.Cfg.WizardRev = ann.Rev
	a.Cfg.syncLegacyFields()
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		log.Printf("[公司同步] 配置保存失败：%v", err)
		return
	}
	log.Printf("[公司同步] 已跟随服务端变化：%s", strings.Join(changed, "、"))
}

func wizardProvider(a *App) *ProviderCfg {
	for i := range a.Cfg.Providers {
		if a.Cfg.Providers[i].ID == wizardProviderID {
			return &a.Cfg.Providers[i]
		}
	}
	return nil
}

func providerHasModel(p *ProviderCfg, id string) bool {
	for _, m := range p.Models {
		if m.ID == id {
			return true
		}
	}
	return false
}

// listenAnnounceUDP best-effort 监听服务端变更广播（UDP 2230）：端口被占（如多开
// 实例）时记一行日志退化为纯轮询。收到本公司名的变更广播即触发即时同步。
func listenAnnounceUDP(a *App) {
	pc, err := net.ListenPacket("udp4", ":"+strconv.Itoa(syncAnnounceUDP))
	if err != nil {
		log.Printf("[公司同步] UDP %d 监听失败（退化为纯轮询）：%v", syncAnnounceUDP, err)
		return
	}
	defer pc.Close()
	log.Printf("[公司同步] 监听 UDP %d（服务端变更广播即时生效）", syncAnnounceUDP)
	var lastTrigger time.Time
	buf := make([]byte, 1024)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		var m struct {
			Jxai    int    `json:"jxai"`
			Type    string `json:"type"`
			Company string `json:"company"`
			Server  string `json:"server"`
		}
		if json.Unmarshal(buf[:n], &m) != nil || m.Jxai != 1 || m.Type != "change" {
			continue
		}
		if a.Cfg.WizardServer == "" || a.Cfg.WizardCompany == "" || m.Company != a.Cfg.WizardCompany {
			continue
		}
		if time.Since(lastTrigger) < syncUDPDebounce {
			continue
		}
		lastTrigger = time.Now()
		go companySyncOnce(a, m.Server)
	}
}
