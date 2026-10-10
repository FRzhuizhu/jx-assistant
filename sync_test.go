package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// applyAnnounce 差异应用：公司名、模型清单（服务端元数据优先+兜底猜测）、
// 市场/更新/服务端地址；已选模型被下线时回落默认；空模型清单不动模型。
func TestSyncApplyAnnounce(t *testing.T) {
	a := newWizardTestApp(t)
	a.Cfg.WizardCompany = "旧公司"
	a.Cfg.WizardServer = "http://old:PORT"
	a.Cfg.MarketURL = "http://old:PORT/market/manifest.json"
	a.Cfg.UpdateURL = "http://old:PORT/updates/latest"
	a.Cfg.Model = "removed-model"
	a.Cfg.Providers = []ProviderCfg{
		{ID: wizardProviderID, Name: "旧公司·内部通道", BaseURL: "http://old:3000/v1", APIKey: "sk-x",
			Models: []ModelCfg{{ID: "removed-model"}}},
	}

	ann := &announcePayload{
		Jxai: 1, Company: "新公司", Server: "http://new:PORT", Rev: "abc123def456",
		MarketURL: "http://new:PORT/market/manifest.json", UpdateURL: "http://new:PORT/updates/latest",
		API: &struct {
			BaseURL      string            `json:"baseURL"`
			Models       []wizardModelInfo `json:"models"`
			DefaultModel string            `json:"defaultModel"`
		}{BaseURL: "http://new:3000/v1", Models: []wizardModelInfo{{ID: "deepseek-flash"}}, DefaultModel: "deepseek-flash"},
	}
	applyAnnounce(a, ann, "http://new:PORT")

	p := a.Cfg.Providers[0]
	if a.Cfg.WizardCompany != "新公司" || p.Name != "新公司·内部通道" {
		t.Fatalf("公司名未跟随: %q %q", a.Cfg.WizardCompany, p.Name)
	}
	if p.BaseURL != "http://new:3000/v1" || len(p.Models) != 1 || p.Models[0].ID != "deepseek-flash" ||
		p.Models[0].ContextWindow != 1048576 || !p.Models[0].SupportsImages {
		t.Fatalf("模型清单未应用: %#v", p.Models)
	}
	if p.APIKey != "sk-x" {
		t.Fatalf("key 不应被动: %q", p.APIKey)
	}
	if a.Cfg.Model != "deepseek-flash" { // 已选模型被下线 → 回默认
		t.Fatalf("已选模型应回落默认: %q", a.Cfg.Model)
	}
	if a.Cfg.MarketURL != ann.MarketURL || a.Cfg.UpdateURL != ann.UpdateURL || a.Cfg.WizardServer != "http://new:PORT" {
		t.Fatalf("地址未跟随: %v %v %v", a.Cfg.MarketURL, a.Cfg.UpdateURL, a.Cfg.WizardServer)
	}

	// 空模型清单（服务端拉取失败的防御）：模型不被清空
	annEmpty := *ann
	annEmpty.API = &struct {
		BaseURL      string            `json:"baseURL"`
		Models       []wizardModelInfo `json:"models"`
		DefaultModel string            `json:"defaultModel"`
	}{BaseURL: "http://new:3000/v1"}
	applyAnnounce(a, &annEmpty, "http://new:PORT")
	if len(a.Cfg.Providers[0].Models) != 1 {
		t.Fatalf("空清单不应清空模型: %#v", a.Cfg.Providers[0].Models)
	}
}

// 失联迁移：当前服务端不可达 → 按公司名重新发现新地址 → 拉公告应用，
// 市场/更新地址随服务端地址整体迁移。
func TestSyncMigrateOnFailover(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 当前服务端已下线

	newSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/announce" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"jxai":1,"company":"测试公司","server":"` + "http://" + r.Host + `",
			"rev":"f1e2d3c4b5a6","marketURL":"http://` + r.Host + `/market/manifest.json",
			"updateURL":"http://` + r.Host + `/updates/latest",
			"api":{"baseURL":"http://127.0.0.1:3000/v1","models":[{"id":"deepseek-flash","contextWindow":1048576}],
			       "defaultModel":"deepseek-flash"}}`))
	}))
	t.Cleanup(newSrv.Close)

	a := newWizardTestApp(t)
	a.Cfg.WizardCompany = "测试公司"
	a.Cfg.WizardServer = deadURL
	a.Cfg.MarketURL = deadURL + "/market/manifest.json"
	a.Cfg.UpdateURL = deadURL + "/updates/latest"
	a.Cfg.Providers = []ProviderCfg{{ID: wizardProviderID, Name: "测试公司·内部通道",
		BaseURL: "http://old:3000/v1", APIKey: "sk-x", Models: []ModelCfg{{ID: "old-m"}}}}

	// 发现函数打桩：按公司名发现到新服务端
	oldDiscover := udpDiscoverFn
	udpDiscoverFn = func(company string) []wizardServer {
		if company != "测试公司" {
			return nil
		}
		return []wizardServer{{Server: newSrv.URL, Company: company}}
	}
	t.Cleanup(func() { udpDiscoverFn = oldDiscover })

	companySyncOnce(a, "")

	if a.Cfg.WizardServer != newSrv.URL {
		t.Fatalf("服务端地址应迁移到新发现地址: %q", a.Cfg.WizardServer)
	}
	if !strings.HasPrefix(a.Cfg.MarketURL, newSrv.URL+"/") || !strings.HasPrefix(a.Cfg.UpdateURL, newSrv.URL+"/") {
		t.Fatalf("市场/更新地址应随服务端迁移: %v %v", a.Cfg.MarketURL, a.Cfg.UpdateURL)
	}
	if ms := a.Cfg.Providers[0].Models; len(ms) != 1 || ms[0].ID != "deepseek-flash" {
		t.Fatalf("模型清单未应用: %#v", ms)
	}
}
