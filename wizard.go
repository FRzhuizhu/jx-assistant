package main

// 首次接入向导（后端半边）：浏览器发不了 UDP——局域网发现与总服务器查询都在这里做，
// 前端（web/app-wizard.js）只负责表单与展示。
//
//	POST /api/wizard/discover {company}            → UDP 广播（2s，公司名=口令）+
//	  registryURL 查询兜底 → 候选服务端列表（去重，lan 来源优先）
//	POST /api/wizard/connect  {server, user}       → 验证 healthz → POST /provision
//	  → 直接写运行配置：内部提供方（company-internal）插到 providers[0] 即默认
//	  （模型清单 models[0]=公司默认模型）、marketURL/updateURL 一键配好、wizardDone 置位
//
// 重跑向导（设置·网络访问·重新运行）会替换 company-internal 并保留其它提供方；
// 服务端未配模型通道时 api 为空：市场/更新照配，前端给出提示。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	wizardDiscoverTimeout = 2 * time.Second
	wizardProviderID      = "company-internal" // 内部提供方的固定 id（重跑即替换）
	wizardUDPProbe        = 2228               // 与服务端 discovery.go 约定的 UDP 端口
)

// wizardServer 一个候选服务端。
type wizardServer struct {
	Server  string `json:"server"`         // 如 http://192.168.1.10:PORT
	Name    string `json:"name,omitempty"` // 服务端市场名
	Company string `json:"company"`        // 应答身份（与探测口令一致）
	Src     string `json:"src"`            // lan=局域网发现 / registry=总服务器查询
}

// provisionPayload /provision 的响应结构（jxaiserver/provision.go 下发）。
type provisionPayload struct {
	Company string `json:"company"`
	User    string `json:"user"`
	Server  string `json:"server"`
	API     *struct {
		BaseURL string `json:"baseURL"`
		Key     string `json:"key"`
		Models  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
		DefaultModel string `json:"defaultModel"`
	} `json:"api"`
	APIError  string `json:"apiError,omitempty"`
	MarketURL string `json:"marketURL"`
	UpdateURL string `json:"updateURL"`
}

var wizardHTTP = &http.Client{Timeout: 15 * time.Second}

/* ---------- 发现 ---------- */

func (a *App) handleWizardDiscover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Company string `json:"company"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	company := strings.TrimSpace(body.Company)
	if company == "" || len([]rune(company)) > 64 {
		writeErr(w, 400, "公司名称需为 1~64 字")
		return
	}
	found := []wizardServer{}
	seen := map[string]bool{}
	add := func(s wizardServer) {
		s.Server = strings.TrimRight(s.Server, "/")
		if s.Server == "" || seen[s.Server] {
			return
		}
		seen[s.Server] = true
		found = append(found, s)
	}
	for _, s := range udpDiscoverServers(company) {
		add(s)
	}
	// 总服务器查询兜底：广播被隔离（访客 Wi-Fi / 跨 VLAN）时按公司名查登记簿
	if reg := strings.TrimSpace(a.Cfg.RegistryURL); reg != "" {
		if servers, err := wizardRegistryLookup(reg, company); err == nil {
			for _, s := range servers {
				add(s)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"servers": found})
}

// udpDiscoverServers 向 255.255.255.255 与各接口子网广播地址发探测，收满超时窗口。
func udpDiscoverServers(company string) []wizardServer {
	pc, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return nil
	}
	defer pc.Close()
	deadline := time.Now().Add(wizardDiscoverTimeout)
	probe, _ := json.Marshal(map[string]any{"jxai": 1, "company": company})
	for _, t := range broadcastTargets() {
		_, _ = pc.WriteTo(probe, t)
	}
	out := []wizardServer{}
	buf := make([]byte, 1024)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		_ = pc.SetReadDeadline(deadline)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			break // 超时窗口到
		}
		var m struct {
			Jxai    int    `json:"jxai"`
			Company string `json:"company"`
			Server  string `json:"server"`
			Name    string `json:"name"`
		}
		if json.Unmarshal(buf[:n], &m) == nil && m.Jxai == 1 && m.Company == company && m.Server != "" {
			out = append(out, wizardServer{Server: m.Server, Name: m.Name, Company: m.Company, Src: "lan"})
		}
	}
	return out
}

// broadcastTargets 广播目标：255.255.255.255 + 各接口的子网定向广播地址
// （后者在部分路由器/AP 屏蔽全网广播时仍可达）。
func broadcastTargets() []*net.UDPAddr {
	targets := []*net.UDPAddr{}
	seen := map[string]bool{}
	appendAddr := func(ip string) {
		if seen[ip] {
			return
		}
		seen[ip] = true
		if addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", ip, wizardUDPProbe)); err == nil {
			targets = append(targets, addr)
		}
	}
	appendAddr("255.255.255.255")
	ifaces, err := net.Interfaces()
	if err != nil {
		return targets
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil || ipNet.IP.IsLoopback() {
				continue
			}
			broadcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				broadcast[i] = ipNet.IP[i] | ^ipNet.Mask[i]
			}
			if !broadcast.Equal(ipNet.IP) { // /32 与点对点网段没有广播地址
				appendAddr(broadcast.String())
			}
		}
	}
	return targets
}

// wizardRegistryLookup 按公司名向总服务器查登记的服务端地址（404=未登记，返回空）。
func wizardRegistryLookup(registryURL, company string) ([]wizardServer, error) {
	u := strings.TrimRight(registryURL, "/") + "/registry/lookup?company=" + url.QueryEscape(company)
	resp, err := wizardHTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("总服务器返回 HTTP %d", resp.StatusCode)
	}
	var rec struct {
		Company string   `json:"company"`
		Addrs   []string `json:"addrs"`
		Port    int      `json:"port"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rec); err != nil {
		return nil, err
	}
	out := []wizardServer{}
	for _, addr := range rec.Addrs {
		out = append(out, wizardServer{
			Server:  fmt.Sprintf("http://%s:%d", strings.Trim(addr, "[]"), rec.Port),
			Company: rec.Company, Src: "registry",
		})
	}
	return out, nil
}

/* ---------- 连接与配置应用 ---------- */

func (a *App) handleWizardConnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Server string `json:"server"`
		User   string `json:"user"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	server := strings.TrimRight(strings.TrimSpace(body.Server), "/")
	user := strings.TrimSpace(body.User)
	if server == "" || user == "" || len([]rune(user)) > 32 {
		writeErr(w, 400, "需要服务器地址与本人称呼（称呼 1~32 字）")
		return
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "http://" + server // 容错：裸地址自动补协议
	}

	// ① 验证是 jxai-server（healthz）
	resp, err := wizardHTTP.Get(server + "/healthz")
	if err != nil {
		writeErr(w, 400, "连不上 "+server+"："+err.Error())
		return
	}
	var hz struct {
		Service string `json:"service"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&hz)
	resp.Body.Close()
	if resp.StatusCode != 200 || hz.Service != "jxai-server" {
		writeErr(w, 400, server+" 不是 jxai-server（地址或端口对吗？）")
		return
	}

	// ② 取配置包
	pd, err := json.Marshal(map[string]string{"user": user})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp2, err := wizardHTTP.Post(server+"/provision", "application/json", strings.NewReader(string(pd)))
	if err != nil {
		writeErr(w, 502, "取配置失败："+err.Error())
		return
	}
	defer resp2.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp2.Body, 4<<20))
	if resp2.StatusCode != 200 {
		writeErr(w, 502, "服务端拒绝下发："+string(raw))
		return
	}
	var payload provisionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeErr(w, 502, "配置包格式不对："+err.Error())
		return
	}

	// ③ 应用到运行配置：内部提供方置顶（即默认），市场/更新一键配好
	respBody := map[string]any{
		"ok": 1, "company": payload.Company, "server": server, "user": user,
		"api": payload.API != nil, "apiError": payload.APIError,
		"marketURL": payload.MarketURL, "updateURL": payload.UpdateURL,
	}
	if payload.API != nil && len(payload.API.Models) > 0 {
		models := make([]ModelCfg, 0, len(payload.API.Models))
		for _, m := range payload.API.Models {
			if m.ID != "" {
				models = append(models, ModelCfg{ID: m.ID, Name: m.Name}) // ContextWindow 0=未知，走默认兜底
			}
		}
		if dm := payload.API.DefaultModel; dm != "" { // 默认模型置顶（models[0] 即默认）
			for i := range models {
				if models[i].ID == dm {
					models = append([]ModelCfg{models[i]}, append(models[:i:i], models[i+1:]...)...)
					break
				}
			}
		}
		name := payload.Company
		if name == "" {
			name = "公司"
		}
		internal := ProviderCfg{
			ID: wizardProviderID, Name: name + "·内部通道",
			BaseURL: payload.API.BaseURL, APIKey: payload.API.Key, Models: models,
		}
		providers := []ProviderCfg{internal}
		for _, p := range a.Cfg.Providers { // 重跑向导：替换旧的内部提供方，保留其它
			if p.ID != wizardProviderID {
				providers = append(providers, p)
			}
		}
		a.Cfg.Providers = providers
		respBody["defaultModel"] = firstModelID(models)
		respBody["modelCount"] = len(models)
	}
	if payload.MarketURL != "" {
		a.Cfg.MarketURL = payload.MarketURL
		a.marketMu.Lock()
		a.marketCache = marketCacheEntry{} // 市场缓存作废
		a.marketMu.Unlock()
	}
	if payload.UpdateURL != "" {
		a.Cfg.UpdateURL = payload.UpdateURL
	}
	a.Cfg.WizardDone = true
	a.Cfg.syncLegacyFields()
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		writeErr(w, 500, "配置保存失败："+err.Error())
		return
	}
	log.Printf("[向导] 已接入 %s（%s，用户「%s」；模型通道=%v）", server, payload.Company, user, payload.API != nil)
	writeJSON(w, 200, respBody)
}

func firstModelID(models []ModelCfg) string {
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}
