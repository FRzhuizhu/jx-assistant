// 公司接入单测：登记簿（注册/口令/查询）、UDP 发现（匹配才应答）、
// 配置下发（key 复用/模型清单/未配 new-api 降级——用 httptest 假 new-api）。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestRegistryServer(t *testing.T) (*registryStore, *httptest.Server) {
	t.Helper()
	s := newRegistryStore(t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /registry/register", s.handleRegister)
	mux.HandleFunc("GET /registry/lookup", s.handleLookup)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	s, srv := newTestRegistryServer(t)
	// 公司未登记 → 403
	resp, body := postJSON(t, srv.URL+"/registry/register",
		`{"company":"无名公司","key":"whatever","addrs":["192.168.1.10"],"port":PORT}`)
	if resp.StatusCode != 403 {
		t.Fatalf("未登记公司应 403，收到 %d %s", resp.StatusCode, body)
	}
	// company-add 登记（复用 CLI 的存储路径）
	m := map[string]registryCompany{"测试公司": {Key: "k1", AddedAt: "now"}}
	if err := s.saveCompanies(m); err != nil {
		t.Fatal(err)
	}
	// 口令不符 → 403
	resp, _ = postJSON(t, srv.URL+"/registry/register",
		`{"company":"测试公司","key":"wrong","addrs":["192.168.1.10"],"port":PORT}`)
	if resp.StatusCode != 403 {
		t.Fatalf("口令不符应 403，收到 %d", resp.StatusCode)
	}
	// 正确注册 → 200，登记信息可查
	resp, _ = postJSON(t, srv.URL+"/registry/register",
		`{"company":"测试公司","key":"k1","addrs":["192.168.1.10","192.168.2.20"],"port":PORT}`)
	if resp.StatusCode != 200 {
		t.Fatalf("正确注册应 200，收到 %d", resp.StatusCode)
	}
	lr, err := srv.Client().Get(srv.URL + "/registry/lookup?company=" + "测试公司")
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Body.Close()
	var rec registeredServer
	_ = json.NewDecoder(lr.Body).Decode(&rec)
	if rec.Company != "测试公司" || rec.Port != 2228 || len(rec.Addrs) != 2 || rec.Key != "" {
		t.Fatalf("lookup 记录不对（口令不得回查）: %#v", rec)
	}
	// 未登记公司 → 404
	lr2, _ := srv.Client().Get(srv.URL + "/registry/lookup?company=" + "别家公司")
	if lr2.StatusCode != 404 {
		t.Fatalf("未登记应 404，收到 %d", lr2.StatusCode)
	}
	lr2.Body.Close()
	// 心跳刷新不换口令（二次注册仍通过且 updatedAt 更新）
	resp, _ = postJSON(t, srv.URL+"/registry/register",
		`{"company":"测试公司","key":"k1","addrs":["192.168.1.99"],"port":PORT}`)
	if resp.StatusCode != 200 {
		t.Fatalf("心跳注册应 200，收到 %d", resp.StatusCode)
	}
	lr3, _ := srv.Client().Get(srv.URL + "/registry/lookup?company=" + "测试公司")
	var rec3 registeredServer
	_ = json.NewDecoder(lr3.Body).Decode(&rec3)
	lr3.Body.Close()
	if len(rec3.Addrs) != 1 || rec3.Addrs[0] != "192.168.1.99" {
		t.Fatalf("心跳应刷新地址: %#v", rec3.Addrs)
	}
}

func TestAdvertiseAddrs(t *testing.T) {
	if got := advertiseAddrs("10.1.2.3"); len(got) != 1 || got[0] != "10.1.2.3" {
		t.Fatalf("advertise 覆盖不对: %v", got)
	}
	if got := advertiseAddrs("10.1.2.3:9000"); len(got) != 1 || got[0] != "10.1.2.3" {
		t.Fatalf("advertise 带端口形态: %v", got)
	}
	if got := advertiseAddrs("  "); len(got) == 0 {
		t.Fatal("无 advertise 时应枚举本机地址（测试机至少有一块网卡）")
	}
}

// UDP 发现：本地起监听（随机端口形态的匹配逻辑测试直接走 discovery 应答核心：
// 用同机双 socket 模拟客户端与服务端交互，走 startDiscovery 会在真实 2228 端口上
// 监听——测试里改为直接驱动一个独立端口实例）。
func TestDiscoveryMatch(t *testing.T) {
	company := "发现测试公司"
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go discoveryServe(pc, company, "测试市场", 2228)

	client, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	srvAddr, _ := net.ResolveUDPAddr("udp4", pc.LocalAddr().String())

	probe := func(comp string) (string, bool) {
		data, _ := json.Marshal(discoveryProbe{Jxai: 1, Company: comp})
		if _, err := client.WriteTo(data, srvAddr); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 1024)
		_ = client.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
		n, _, err := client.ReadFrom(buf)
		if err != nil {
			return "", false
		}
		return string(buf[:n]), true
	}
	// 公司名匹配 → 应答，地址是客户端源 IP
	resp, ok := probe(company)
	if !ok {
		t.Fatal("公司名匹配应收到应答")
	}
	var m map[string]any
	if json.Unmarshal([]byte(resp), &m) != nil || m["company"] != company || !strings.HasPrefix(m["server"].(string), "http://127.0.0.1:PORT") {
		t.Fatalf("应答内容不对: %s", resp)
	}
	// 公司名不匹配 → 沉默
	if _, ok := probe("别的公司"); ok {
		t.Fatal("公司名不匹配不应应答")
	}
	// 非 jxai 探测 → 沉默
	raw, _ := json.Marshal(map[string]any{"jxai": 2, "company": company})
	_, _ = client.WriteTo(raw, srvAddr)
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1024)
	if _, _, err := client.ReadFrom(buf); err == nil {
		t.Fatal("非 jxai 探测不应应答")
	}
}

// discoveryServe 把 startDiscovery 的应答循环抽出来（监听 socket 由调用方给，
// 测试用随机端口，生产用 :PORT）。
func discoveryServe(pc net.PacketConn, company, serverName string, port int) {
	buf := make([]byte, 1024)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		var p discoveryProbe
		if json.Unmarshal(buf[:n], &p) != nil || p.Jxai != 1 || p.Company != company {
			continue
		}
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			host = addr.String()
		}
		resp, _ := json.Marshal(map[string]any{
			"jxai": 1, "company": company, "name": serverName,
			"server": fmt.Sprintf("http://%s:%d", host, port),
		})
		_, _ = pc.WriteTo(resp, addr)
	}
}

/* ---------- provision（假 new-api） ---------- */

// fakeNewAPI 最小实现本集成用到的四个端点。
type fakeNewAPI struct {
	srv       *httptest.Server
	tokens    map[string]string // name → full key（无 sk- 前缀）
	nextID    int
	loginHits int
	tokenHits map[string]int // name → 取明文次数
}

func newFakeNewAPI(t *testing.T, models []string) *fakeNewAPI {
	f := &fakeNewAPI{tokens: map[string]string{}, nextID: 100, tokenHits: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/user/login", func(w http.ResponseWriter, r *http.Request) {
		f.loginHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"fake-admin-token"}}`))
	})
	mux.HandleFunc("GET /api/token/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "fake-admin-token" {
			w.WriteHeader(401)
			return
		}
		keyword := r.URL.Query().Get("keyword")
		items := []map[string]any{}
		for name := range f.tokens {
			if strings.Contains(name, keyword) {
				items = append(items, map[string]any{"id": f.nextIDFor(name), "name": name, "key": "masked"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"items": items}})
	})
	// /api/token 子树统一进一个 handler 内部分发（Go mux 的「方法+子树」模式会
	// 吞掉子路径上的方法路由，必须自己拆）：POST /api/token/ = 建令牌；
	// POST /api/token/<id>/key = 取明文
	mux.HandleFunc("/api/token/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "fake-admin-token" {
			w.WriteHeader(401)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case len(parts) == 2 && r.Method == "POST":
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name == "" {
				_, _ = w.Write([]byte(`{"success":false,"message":"缺名字"}`))
				return
			}
			f.tokens[body.Name] = fmt.Sprintf("key-%s-%d", body.Name, f.nextID)
			f.nextID++
			_, _ = w.Write([]byte(`{"success":true}`))
		case len(parts) == 4 && parts[3] == "key" && r.Method == "POST": // /api/token/<id>/key
			var id int
			_, _ = fmt.Sscanf(parts[2], "%d", &id)
			for name, key := range f.tokens {
				if f.idByName(name) == id {
					f.tokenHits[name]++
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"key": key}})
					return
				}
			}
			w.WriteHeader(404)
		default:
			w.WriteHeader(404)
		}
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer sk-")
		known := false
		for _, k := range f.tokens {
			if k == key {
				known = true
			}
		}
		if !known {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid token"}}`))
			return
		}
		data := []map[string]any{}
		for _, m := range models {
			data = append(data, map[string]any{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNewAPI) idByName(name string) int { return f.nextIDFor(name) }
func (f *fakeNewAPI) nextIDFor(name string) int {
	// 稳定 id：按插入顺序无法回查，用名字散列近似（测试用途足够稳定）
	n := 0
	for _, c := range name {
		n = n*31 + int(c)
	}
	if n < 0 {
		n = -n
	}
	return 100 + n%10000
}

func postJSON(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(data)
}

func TestProvisionFlow(t *testing.T) {
	fake := newFakeNewAPI(t, []string{"model-b", "model-a", "model-c"})
	dir := t.TempDir()
	s := newProvisionStore(dir, "测试公司")
	if err := s.saveAPI(&apiConfig{
		NewapiURL: fake.srv.URL, AdminUser: "root", AdminPass: "x",
		DefaultModel: "model-a",
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /provision", s.handleProvision)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 第一次下发：建令牌 + 模型清单 + 默认模型置顶
	resp, body := postJSON(t, srv.URL+"/provision", `{"user":"张三"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("provision 应 200: %d %s", resp.StatusCode, body)
	}
	var p map[string]any
	if json.Unmarshal([]byte(body), &p) != nil {
		t.Fatal(body)
	}
	if p["company"] != "测试公司" || p["marketURL"] != srv.URL+"/market/manifest.json" || p["updateURL"] != srv.URL+"/updates/latest" {
		t.Fatalf("基础字段不对: %#v", p)
	}
	api := p["api"].(map[string]any)
	if api["baseURL"] != fake.srv.URL+"/v1" { // httptest host 是回环 → 按请求 Host 推导后即自身
		t.Fatalf("baseURL 推导不对: %v", api["baseURL"])
	}
	if !strings.HasPrefix(api["key"].(string), "sk-key-") {
		t.Fatalf("key 应带 sk- 前缀: %v", api["key"])
	}
	models := api["models"].([]any)
	if len(models) != 3 || models[0].(map[string]any)["id"] != "model-a" {
		t.Fatalf("默认模型应置顶: %v", models)
	}
	if api["defaultModel"] != "model-a" {
		t.Fatalf("defaultModel 不对: %v", api["defaultModel"])
	}
	// 第二次下发（同人）：复用 key（取明文次数 +1，但令牌不重建）
	resp2, body2 := postJSON(t, srv.URL+"/provision", `{"user":"张三"}`)
	if resp2.StatusCode != 200 {
		t.Fatal(body2)
	}
	var p2 map[string]any
	_ = json.Unmarshal([]byte(body2), &p2)
	if p2["api"].(map[string]any)["key"] != api["key"] {
		t.Fatalf("同人应复用 key: %v vs %v", p2["api"].(map[string]any)["key"], api["key"])
	}
	if fake.tokenHits["jxai-张三"] != 1 {
		t.Fatalf("复用路径不应再取明文（仅新建一次）: %d", fake.tokenHits["jxai-张三"])
	}
	// 空 user → 400
	resp3, _ := postJSON(t, srv.URL+"/provision", `{"user":""}`)
	if resp3.StatusCode != 400 {
		t.Fatalf("空 user 应 400: %d", resp3.StatusCode)
	}
}

// 未配置 new-api → api=null（向导降级路径），其余字段照发。
func TestProvisionNoAPI(t *testing.T) {
	s := newProvisionStore(t.TempDir(), "测试公司")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /provision", s.handleProvision)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resp, body := postJSON(t, srv.URL+"/provision", `{"user":"李四"}`)
	if resp.StatusCode != 200 {
		t.Fatal(body)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(body), &p)
	if p["api"] != nil {
		t.Fatalf("未配置时 api 应为 null: %v", p["api"])
	}
	if p["marketURL"] == "" || p["updateURL"] == "" {
		t.Fatal("市场与更新地址仍应下发")
	}
}

// allowModels 过滤。
func TestProvisionModelFilter(t *testing.T) {
	fake := newFakeNewAPI(t, []string{"m1", "m2", "m3"})
	dir := t.TempDir()
	s := newProvisionStore(dir, "测试公司")
	_ = s.saveAPI(&apiConfig{NewapiURL: fake.srv.URL, AdminUser: "root", AdminPass: "x", AllowModels: []string{"m2"}})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /provision", s.handleProvision)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, body := postJSON(t, srv.URL+"/provision", `{"user":"王五"}`)
	var p map[string]any
	_ = json.Unmarshal([]byte(body), &p)
	models := p["api"].(map[string]any)["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["id"] != "m2" {
		t.Fatalf("allowModels 过滤不对: %v", models)
	}
}
