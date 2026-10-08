package main

// 向导后端单测：广播目标推导、/api/wizard/connect 全链（假 jxai-server 的
// healthz + provision → 配置应用：内部提供方置顶/默认模型置顶/市场更新落位/
// 重跑替换不重复）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCompanyServer 假的公司 jxai-server（healthz + provision 两个端点）。
func fakeCompanyServer(t *testing.T, apiPayload string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"service":"jxai-server"}`))
	})
	mux.HandleFunc("POST /provision", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			User string `json:"user"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.User == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"need user"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Replace(apiPayload, "__USER__", body.User, -1)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newWizardTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a := &App{Cfg: &Config{
		DataDir:   dir,
		Providers: []ProviderCfg{{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", APIKey: "old-key"}},
	}}
	a.CfgPath = filepath.Join(dir, "config.json")
	return a
}

func wizardCall(t *testing.T, h func(w http.ResponseWriter, r *http.Request), body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/wizard/connect", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestWizardConnectAppliesConfig(t *testing.T) {
	fake := fakeCompanyServer(t, `{
		"company": "测试公司", "user": "__USER__", "server": "http://x",
		"api": { "baseURL": "http://127.0.0.1:3000/v1", "key": "sk-abc",
			"models": [{"id":"m-b"},{"id":"m-a"},{"id":"m-c"}], "defaultModel": "m-a" },
		"marketURL": "http://x/market/manifest.json", "updateURL": "http://x/updates/latest" }`)
	a := newWizardTestApp(t)

	code, resp := wizardCall(t, a.handleWizardConnect, `{"server":"`+fake.URL+`","user":"张三"}`)
	if code != 200 {
		t.Fatalf("connect 应 200: %d %v", code, resp)
	}
	if resp["company"] != "测试公司" || resp["defaultModel"] != "m-a" {
		t.Fatalf("回包不对: %#v", resp)
	}
	// 配置应用：内部提供方置顶（默认），旧 deepseek 保留在后且 key 不动
	if len(a.Cfg.Providers) != 2 || a.Cfg.Providers[0].ID != wizardProviderID {
		t.Fatalf("内部提供方应置顶: %#v", a.Cfg.Providers)
	}
	p := a.Cfg.Providers[0]
	if p.BaseURL != "http://127.0.0.1:3000/v1" || p.APIKey != "sk-abc" || p.Name != "测试公司·内部通道" {
		t.Fatalf("提供方内容不对: %#v", p)
	}
	if len(p.Models) != 3 || p.Models[0].ID != "m-a" {
		t.Fatalf("默认模型应置顶: %#v", p.Models)
	}
	if a.Cfg.Providers[1].ID != "deepseek" || a.Cfg.Providers[1].APIKey != "old-key" {
		t.Fatalf("原有提供方应保留: %#v", a.Cfg.Providers[1])
	}
	if a.Cfg.MarketURL != "http://x/market/manifest.json" || a.Cfg.UpdateURL != "http://x/updates/latest" || !a.Cfg.WizardDone {
		t.Fatalf("市场/更新/向导完成未落位: %v %v %v", a.Cfg.MarketURL, a.Cfg.UpdateURL, a.Cfg.WizardDone)
	}

	// 重跑向导：替换内部提供方，不堆积、不动别的
	code, _ = wizardCall(t, a.handleWizardConnect, `{"server":"`+fake.URL+`","user":"李四"}`)
	if code != 200 {
		t.Fatal("重跑失败")
	}
	if len(a.Cfg.Providers) != 2 || a.Cfg.Providers[0].ID != wizardProviderID {
		t.Fatalf("重跑应替换不堆积: %#v", a.Cfg.Providers)
	}
}

// 服务端没配模型通道（api=null）：市场/更新照配，向导完成，回包 apiError 透传。
func TestWizardConnectNoAPI(t *testing.T) {
	fake := fakeCompanyServer(t, `{"company":"测试公司","api":null,"apiError":"服务端未配置模型通道（管理员运行 set-api）",
		"marketURL":"http://x/market/manifest.json","updateURL":"http://x/updates/latest"}`)
	a := newWizardTestApp(t)
	code, resp := wizardCall(t, a.handleWizardConnect, `{"server":"`+fake.URL+`","user":"张三"}`)
	if code != 200 {
		t.Fatalf("connect 应 200: %d %v", code, resp)
	}
	if resp["api"] != false || !strings.Contains(resp["apiError"].(string), "set-api") {
		t.Fatalf("降级回包不对: %#v", resp)
	}
	if a.Cfg.MarketURL == "" || !a.Cfg.WizardDone {
		t.Fatal("市场与完成标志应照常落位")
	}
	if len(a.Cfg.Providers) != 1 || a.Cfg.Providers[0].ID != "deepseek" {
		t.Fatalf("无 api 时不应动提供方: %#v", a.Cfg.Providers)
	}
}

// 地址不是 jxai-server（healthz 形状不符）→ 明确报错。
func TestWizardConnectWrongServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`)) // 没有 service 字段
	}))
	t.Cleanup(srv.Close)
	a := newWizardTestApp(t)
	code, resp := wizardCall(t, a.handleWizardConnect, `{"server":"`+srv.URL+`","user":"张三"}`)
	if code == 200 || !strings.Contains(resp["error"].(string), "jxai-server") {
		t.Fatalf("非 jxai-server 应报错: %d %v", code, resp)
	}
	// 缺 user → 400
	code2, resp2 := wizardCall(t, a.handleWizardConnect, `{"server":"`+srv.URL+`"}`)
	if code2 != 400 {
		t.Fatalf("缺 user 应 400: %d %v", code2, resp2)
	}
}

// 广播目标推导：至少含全网广播，且没有回环/单播地址。
func TestBroadcastTargets(t *testing.T) {
	ts := broadcastTargets()
	if len(ts) == 0 {
		t.Fatal("应至少有 255.255.255.255")
	}
	hasGlobal := false
	for _, a := range ts {
		s := a.String()
		if strings.HasPrefix(s, "255.255.255.255:") {
			hasGlobal = true
		}
		if strings.HasPrefix(s, "127.") {
			t.Fatalf("不应包含回环地址: %s", s)
		}
	}
	if !hasGlobal {
		t.Fatal("应包含全网广播 255.255.255.255")
	}
}
