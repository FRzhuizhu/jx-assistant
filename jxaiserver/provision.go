// 配置下发（/provision）+ 模型通道（基于 new-api 的受控发令牌）。
//
// 客户端向导发现本服务端后 POST /provision {user:"张三"}，一次拿齐：
//
//	api:    new-api 受控生成的令牌（名字 jxai-<人名>，明文只发给本人）+ 模型清单
//	        + 默认模型——用量统计直接用 new-api 自带的按令牌日志（令牌名=人名）
//	marketURL / updateURL: 本服务端的市场与更新地址
//
// 未配置 new-api（set-api 未跑）时 api=null，向导降级只配市场/更新。
//
// new-api 集成（API 形状按 v1.0.0-rc.41 实测）：
//
//	POST /api/user/login {username,password} → {data:{access_token}}（管理 API 鉴权）
//	GET  /api/token/search?keyword=… → {data:{items:[{id,name,…}]}}（key 打码）
//	POST /api/token/ {name,unlimited_quota,expired_time:-1} 建令牌（响应无 data）
//	POST /api/token/:id/key → {data:{key}} 取明文 ★
//	GET  /v1/models（Bearer sk-<key>）→ OpenAI 格式模型清单（按令牌可见渠道）
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// apiConfig 模型通道配置（data/api.json，set-api CLI 维护）。
type apiConfig struct {
	NewapiURL    string   `json:"newapiURL"` // new-api 根地址（如 http://127.0.0.1:3000）
	AdminUser    string   `json:"adminUser"`
	AdminPass    string   `json:"adminPass"`
	PublicBase   string   `json:"publicBase,omitempty"`   // 客户端可达的 new-api 地址（缺省按请求 Host 换主机名）
	DefaultModel string   `json:"defaultModel,omitempty"` // 公司选定的默认模型（缺省取清单第一个）
	AllowModels  []string `json:"allowModels,omitempty"`  // 允许下发的模型（空=不限制）
}

// provisionUser 已发过令牌的用户（data/provision.json：同人复用同一把 key）。
type provisionUser struct {
	Key       string `json:"key"` // 完整令牌（含 sk- 前缀）
	TokenName string `json:"tokenName"`
	CreatedAt string `json:"createdAt"`
}

type provisionStore struct {
	dir     string // 数据目录
	company string // 本服务端身份（-company；同时是发现口令）
	apiMu   sync.Mutex
	apiCfg  *apiConfig // 惰性加载的模型通道配置（nil = 未配置）
}

func newProvisionStore(dir, company string) *provisionStore {
	return &provisionStore{dir: dir, company: company}
}

func (s *provisionStore) apiPath() string   { return filepath.Join(s.dir, "api.json") }
func (s *provisionStore) usersPath() string { return filepath.Join(s.dir, "provision.json") }

func (s *provisionStore) loadAPI() *apiConfig {
	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	if s.apiCfg == nil {
		cfg := &apiConfig{}
		if data, err := os.ReadFile(s.apiPath()); err == nil {
			_ = json.Unmarshal(data, cfg)
		}
		s.apiCfg = cfg
	}
	if strings.TrimSpace(s.apiCfg.NewapiURL) == "" || strings.TrimSpace(s.apiCfg.AdminUser) == "" {
		return nil
	}
	return s.apiCfg
}

func (s *provisionStore) saveAPI(cfg *apiConfig) error {
	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	if err := writeFileAtomic(s.apiPath(), mustJSONIndent(cfg), 0644); err != nil {
		return err
	}
	s.apiCfg = cfg
	return nil
}

func (s *provisionStore) loadUsers() map[string]provisionUser {
	out := map[string]provisionUser{}
	data, err := os.ReadFile(s.usersPath())
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (s *provisionStore) saveUsers(m map[string]provisionUser) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	return writeFileAtomic(s.usersPath(), mustJSONIndent(m), 0644)
}

/* ---------- new-api 客户端 ---------- */

var newapiHTTP = &http.Client{Timeout: 20 * time.Second}

// newapiCall 管理 API 调用：Authorization 用登录换来的 access_token；401 时重登一次再试。
func (s *provisionStore) newapiCall(cfg *apiConfig, method, path string, body io.Reader, out any) error {
	call := func(tok string) ([]byte, int, error) {
		req, _ := http.NewRequest(method, strings.TrimRight(cfg.NewapiURL, "/")+path, body)
		req.Header.Set("Authorization", tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := newapiHTTP.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return raw, resp.StatusCode, nil
	}
	token, err := s.newapiLogin(cfg)
	if err != nil {
		return err
	}
	raw, status, err := call(token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized { // access_token 过期：重登一次
		token2, err2 := s.newapiLoginFresh(cfg)
		if err2 != nil {
			return err2
		}
		raw, status, err = call(token2)
		if err != nil {
			return err
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", status, truncateStr(string(raw), 120))
	}
	var env struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("响应不是 JSON: %s", truncateStr(string(raw), 120))
	}
	if !env.Success {
		return fmt.Errorf("%s", env.Message)
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

var newapiLoginMu sync.Mutex
var newapiLoginCache struct {
	user, token string
}

// newapiLogin 取管理 API 的 access_token（同账号复用缓存）。
func (s *provisionStore) newapiLogin(cfg *apiConfig) (string, error) {
	newapiLoginMu.Lock()
	defer newapiLoginMu.Unlock()
	if newapiLoginCache.user == cfg.AdminUser && newapiLoginCache.token != "" {
		return newapiLoginCache.token, nil
	}
	return s.newapiLoginFreshLocked(cfg)
}

func (s *provisionStore) newapiLoginFresh(cfg *apiConfig) (string, error) {
	newapiLoginMu.Lock()
	defer newapiLoginMu.Unlock()
	return s.newapiLoginFreshLocked(cfg)
}

func (s *provisionStore) newapiLoginFreshLocked(cfg *apiConfig) (string, error) {
	payload, _ := json.Marshal(map[string]string{"username": cfg.AdminUser, "password": cfg.AdminPass})
	resp, err := newapiHTTP.Post(strings.TrimRight(cfg.NewapiURL, "/")+"/api/user/login",
		"application/json", strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("连不上 new-api: %w", err)
	}
	defer resp.Body.Close()
	var env struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil || !env.Success {
		return "", fmt.Errorf("new-api 登录失败: %s", env.Message)
	}
	if env.Data.AccessToken == "" {
		return "", fmt.Errorf("new-api 未返回 access_token")
	}
	newapiLoginCache.user = cfg.AdminUser
	newapiLoginCache.token = env.Data.AccessToken
	return env.Data.AccessToken, nil
}

// ensureUserToken 按人名取（没有则建）new-api 令牌，返回完整 key（含 sk- 前缀）。
// 令牌名 = jxai-<人名>；列表/搜索接口回的 key 是打码的，明文只能经
// POST /api/token/:id/key 取——先查后建、建后再查 id 再取 key。
func (s *provisionStore) ensureUserToken(cfg *apiConfig, user string) (string, string, error) {
	tokenName := "jxai-" + user
	find := func() (int64, bool) {
		var out struct {
			Items []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		}
		if err := s.newapiCall(cfg, "GET", "/api/token/search?keyword="+url.QueryEscape(tokenName), nil, &out); err != nil {
			return 0, false
		}
		for _, t := range out.Items { // keyword 模糊匹配：按名字精确定位
			if t.Name == tokenName {
				return t.ID, true
			}
		}
		return 0, false
	}
	id, ok := find()
	if !ok {
		payload, _ := json.Marshal(map[string]any{
			"name": tokenName, "unlimited_quota": true, "expired_time": -1,
			"remain_quota": 500000, "model_limits_enabled": false, "model_limits": "", "allow_ips": "", "group": "",
		})
		if err := s.newapiCall(cfg, "POST", "/api/token/", strings.NewReader(string(payload)), nil); err != nil {
			return "", "", fmt.Errorf("创建令牌失败: %w", err)
		}
		id, ok = find()
		if !ok {
			return "", "", fmt.Errorf("令牌已创建但查不到（名字 %s）", tokenName)
		}
	}
	var keyOut struct {
		Key string `json:"key"`
	}
	if err := s.newapiCall(cfg, "POST", fmt.Sprintf("/api/token/%d/key", id), nil, &keyOut); err != nil {
		return "", "", fmt.Errorf("取令牌明文失败: %w", err)
	}
	if keyOut.Key == "" {
		return "", "", fmt.Errorf("new-api 返回空 key")
	}
	return "sk-" + keyOut.Key, tokenName, nil
}

// modelsForKey 用用户令牌拉它可见的模型清单（走 OpenAI 格式端点，天然只含可用渠道）。
func (s *provisionStore) modelsForKey(cfg *apiConfig, key string) ([]string, error) {
	req, _ := http.NewRequest("GET", strings.TrimRight(cfg.NewapiURL, "/")+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := newapiHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	allow := map[string]bool{}
	for _, m := range cfg.AllowModels {
		allow[strings.TrimSpace(m)] = true
	}
	ids := []string{}
	for _, m := range out.Data {
		if m.ID != "" && (len(allow) == 0 || allow[m.ID]) {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

/* ---------- /provision 端点 ---------- */

func (s *provisionStore) handleProvision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User string `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil ||
		strings.TrimSpace(body.User) == "" || len([]rune(strings.TrimSpace(body.User))) > 32 {
		writeErrJSON(w, 400, "需要 user（本人称呼，2~32 字）")
		return
	}
	user := strings.TrimSpace(body.User)
	host := requestHostOnly(r)
	serverBase := schemeOf(r) + "://" + r.Host
	resp := map[string]any{
		"company":   s.company,
		"user":      user,
		"server":    serverBase,
		"marketURL": serverBase + "/market/manifest.json",
		"updateURL": serverBase + "/updates/latest",
		"api":       nil,
	}
	if cfg := s.loadAPI(); cfg != nil {
		users := s.loadUsers()
		rec, known := users[user]
		var models []string
		if known {
			models, _ = s.modelsForKey(cfg, rec.Key) // 复用旧 key；顺带校验仍可用
		}
		if !known || len(models) == 0 {
			key, tokenName, err := s.ensureUserToken(cfg, user)
			if err != nil {
				log.Printf("[下发] %s 的令牌获取失败: %v", user, err)
				resp["apiError"] = err.Error()
			} else {
				users[user] = provisionUser{Key: key, TokenName: tokenName, CreatedAt: time.Now().Format(time.RFC3339)}
				_ = s.saveUsers(users)
				rec = users[user]
				models, err = s.modelsForKey(cfg, key)
				if err != nil {
					log.Printf("[下发] %s 的模型清单获取失败: %v", user, err)
					resp["apiError"] = err.Error()
					models = nil
				}
			}
		}
		if rec.Key != "" && len(models) > 0 {
			// 默认模型置顶（客户端侧 models[0] 即默认）
			if cfg.DefaultModel != "" {
				for i, m := range models {
					if m == cfg.DefaultModel {
						models = append([]string{cfg.DefaultModel}, append(models[:i:i], models[i+1:]...)...)
						break
					}
				}
			}
			modelObjs := make([]map[string]any, 0, len(models))
			for _, m := range models {
				modelObjs = append(modelObjs, map[string]any{"id": m, "name": m})
			}
			resp["api"] = map[string]any{
				"baseURL":      apiPublicBase(cfg, host) + "/v1",
				"key":          rec.Key,
				"models":       modelObjs,
				"defaultModel": firstNonEmpty(cfg.DefaultModel, models[0]),
			}
		} else if rec.Key == "" && resp["apiError"] == nil {
			resp["apiError"] = "服务端未配置模型通道（管理员运行 set-api）"
		}
	}
	b, _ := json.Marshal(resp)
	writeJSONStr(w, 200, string(b))
}

// apiPublicBase 客户端可达的 new-api 地址：配置了 PublicBase 用之；否则当 new-api
// 与本服务同机部署（newapiURL 是回环/私网地址）时，把主机名换成请求方到达本服务
// 的主机名、保留 new-api 的端口。
func apiPublicBase(cfg *apiConfig, requestHost string) string {
	if pub := strings.TrimSpace(cfg.PublicBase); pub != "" {
		return strings.TrimRight(pub, "/")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.NewapiURL))
	if err != nil || u.Host == "" {
		return strings.TrimRight(cfg.NewapiURL, "/")
	}
	host := u.Hostname()
	port := u.Port()
	if host == "127.0.0.1" || host == "localhost" || strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "172.") {
		if port != "" && requestHost != "" {
			u.Host = net.JoinHostPort(requestHost, port)
		}
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host, "/")
}

// requestHostOnly 取请求 Host 的主机名部分（不带端口）。
func requestHostOnly(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

/* ---------- CLI：set-api ---------- */

func runSetAPI(args []string) int {
	fs := flag.NewFlagSet("set-api", flag.ExitOnError)
	data := fs.String("data", "data", "数据目录")
	newapi := fs.String("newapi", "", "new-api 根地址（如 http://127.0.0.1:3000）")
	user := fs.String("user", "", "new-api 管理员用户名")
	pass := fs.String("pass", "", "new-api 管理员密码")
	pub := fs.String("public", "", "客户端可达的 new-api 地址（缺省按请求方 Host 自动推导）")
	def := fs.String("default", "", "默认模型 id（缺省取清单第一个）")
	models := fs.String("models", "", "允许下发的模型（逗号分隔，缺省不限制）")
	_ = fs.Parse(flagsFirst(args))

	s := newProvisionStore(*data, "")
	cfg := &apiConfig{}
	if data2, err := os.ReadFile(s.apiPath()); err == nil {
		_ = json.Unmarshal(data2, cfg)
	}
	if *newapi != "" {
		cfg.NewapiURL = strings.TrimRight(*newapi, "/")
	}
	if *user != "" {
		cfg.AdminUser = *user
	}
	if *pass != "" {
		cfg.AdminPass = *pass
	}
	if *pub != "" {
		cfg.PublicBase = strings.TrimRight(*pub, "/")
	}
	if *def != "" {
		cfg.DefaultModel = *def
	}
	if *models != "" {
		cfg.AllowModels = splitTags(*models)
	}
	if cfg.NewapiURL == "" || cfg.AdminUser == "" || cfg.AdminPass == "" {
		fmt.Fprintln(os.Stderr, "用法: jxai-server set-api -newapi http://127.0.0.1:3000 -user root -pass 密码 [-default 模型] [-models a,b] [-public 地址] [-data data]")
		return 2
	}
	if err := s.saveAPI(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "保存失败:", err)
		return 1
	}
	// 顺手验证连通（登录失败只是提醒，不回滚配置）
	if _, err := s.newapiLogin(cfg); err != nil {
		fmt.Printf("已保存，但连通性验证失败：%v（服务启动后 /provision 会再试）\n", err)
		return 0
	}
	fmt.Printf("模型通道已配置（new-api=%s 默认模型=%s 允许=%d 个）\n", cfg.NewapiURL, firstNonEmpty(cfg.DefaultModel, "清单第一个"), len(cfg.AllowModels))
	return 0
}
