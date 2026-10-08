// 公司登记（两级结构的「总服务器」半边）+ 客户服务端的自动注册循环。
//
// 总服务器：登记簿 data/registry/companies.json（CLI company-add 增删公司），
//
//	POST /registry/register {company,key,addrs[],port}——口令不符 403；
//	GET  /registry/lookup?company=X → {company,addrs,port,updatedAt}（404=未登记；
//	公开可查，但只回已登记条目；客户端按公司名取内网地址的兜底通道）。
//
// 客户公司服务端：-register-to/-company/-company-key 启动 10s 后注册、每 6h 心跳；
//
//	本机地址=非回环 IPv4 枚举（-advertise 覆盖），失败记日志重试不阻断。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// registryCompany 登记簿里的一家公司（口令只存总服务器侧，不下发）。
type registryCompany struct {
	Key     string `json:"key"` // 注册口令（company-add 时生成或指定）
	AddedAt string `json:"addedAt"`
}

// registeredServer 客户公司服务端上报的可达信息（lookup 的返回体）。
type registeredServer struct {
	Company   string   `json:"company"`
	Key       string   `json:"key,omitempty"` // 注册口令（仅入参；落盘前清空，绝不回查）
	Addrs     []string `json:"addrs"`         // 内网 IPv4 列表（客户端按可达性依次尝试）
	Port      int      `json:"port"`
	UpdatedAt string   `json:"updatedAt"`
}

type registryStore struct {
	dir string
	mu  sync.Mutex
}

func newRegistryStore(dir string) *registryStore { return &registryStore{dir: dir} }

func (s *registryStore) companiesPath() string {
	return filepath.Join(s.dir, "registry", "companies.json")
}
func (s *registryStore) serversPath() string { return filepath.Join(s.dir, "registry", "servers.json") }

func (s *registryStore) loadCompanies() map[string]registryCompany {
	out := map[string]registryCompany{}
	data, err := os.ReadFile(s.companiesPath())
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (s *registryStore) saveCompanies(m map[string]registryCompany) error {
	if err := os.MkdirAll(filepath.Dir(s.companiesPath()), 0755); err != nil {
		return err
	}
	return writeFileAtomic(s.companiesPath(), mustJSONIndent(m), 0644)
}

func (s *registryStore) loadServers() map[string]registeredServer {
	out := map[string]registeredServer{}
	data, err := os.ReadFile(s.serversPath())
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (s *registryStore) saveServers(m map[string]registeredServer) error {
	if err := os.MkdirAll(filepath.Dir(s.serversPath()), 0755); err != nil {
		return err
	}
	return writeFileAtomic(s.serversPath(), mustJSONIndent(m), 0644)
}

/* ---------- 总服务器端点 ---------- */

func (s *registryStore) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body registeredServer
	body.Addrs = []string{}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil ||
		strings.TrimSpace(body.Company) == "" || len(body.Addrs) == 0 || body.Port < 1 || body.Port > 65535 {
		writeErrJSON(w, 400, "需要 company、addrs（至少一个内网 IP）与 port")
		return
	}
	body.Company = strings.TrimSpace(body.Company)
	s.mu.Lock()
	companies := s.loadCompanies()
	want, ok := companies[body.Company]
	servers := s.loadServers()
	if ok && want.Key == body.Key {
		body.Key = "" // 登记信息不回存口令
		old := servers[body.Company]
		body.UpdatedAt = time.Now().Format(time.RFC3339)
		servers[body.Company] = body
		err := s.saveServers(servers)
		s.mu.Unlock()
		if err != nil {
			writeErrJSON(w, 500, "登记落盘失败: "+err.Error())
			return
		}
		if old.Port != 0 {
			log.Printf("[登记] %s 心跳刷新（%v:%d）", body.Company, body.Addrs, body.Port)
		} else {
			log.Printf("[登记] %s 首次登记（%v:%d）", body.Company, body.Addrs, body.Port)
		}
		writeJSONStr(w, 200, `{"ok":true}`)
		return
	}
	s.mu.Unlock()
	writeErrJSON(w, 403, "公司未登记或口令不符")
}

func (s *registryStore) handleLookup(w http.ResponseWriter, r *http.Request) {
	company := strings.TrimSpace(r.URL.Query().Get("company"))
	if company == "" {
		writeErrJSON(w, 400, "需要 company 参数")
		return
	}
	s.mu.Lock()
	servers := s.loadServers()
	s.mu.Unlock()
	rec, ok := servers[company]
	if !ok {
		writeErrJSON(w, 404, "该公司尚未登记服务端")
		return
	}
	b, _ := json.Marshal(rec)
	writeJSONStr(w, 200, string(b))
}

/* ---------- 客户服务端：自动注册循环 ---------- */

// startRegistrar 向总服务器注册本机：启动 10s 后首注册，此后每 6h 心跳。
// 地址=非回环 IPv4（-advertise 覆盖，格式 ip 或 ip:port）。
func startRegistrar(hqURL, company, companyKey, advertise string, port int) {
	if hqURL == "" || company == "" {
		return
	}
	go func() {
		for first := true; ; first = false {
			addrs := advertiseAddrs(advertise)
			if len(addrs) == 0 {
				log.Printf("[登记] 找不到可登记的内网地址（可用 -advertise 指定），跳过本轮")
			} else {
				if err := registerOnce(hqURL, company, companyKey, addrs, port); err != nil {
					log.Printf("[登记] 向 %s 注册失败（%s重试）: %v", hqURL, retryDelay(first), err)
				} else if first {
					log.Printf("[登记] 已向 %s 登记为「%s」（%v:%d）", hqURL, company, addrs, port)
				}
			}
			time.Sleep(registryHeartbeat)
		}
	}()
}

const registryHeartbeat = 6 * time.Hour

func retryDelay(first bool) string {
	if first {
		return "1 分钟后"
	}
	return "下个心跳"
}

func registerOnce(hqURL, company, companyKey string, addrs []string, port int) error {
	payload, _ := json.Marshal(registeredServer{Company: company, Key: companyKey, Addrs: addrs, Port: port})
	resp, err := http.Post(strings.TrimRight(hqURL, "/")+"/registry/register", "application/json",
		strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// advertiseAddrs 计算对外登记的地址：-advertise 优先；否则枚举本机非回环 IPv4。
func advertiseAddrs(advertise string) []string {
	if a := strings.TrimSpace(advertise); a != "" {
		if host, port, err := net.SplitHostPort(a); err == nil && port != "" {
			return []string{host}
		}
		return []string{strings.Trim(a, "[]")}
	}
	out := []string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
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
			if !ok || ipNet.IP.To4() == nil || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ipNet.IP.String())
		}
	}
	return out
}

/* ---------- CLI：company-add / company-del / companies ---------- */

func runCompanyAdd(args []string) int {
	fs := flag.NewFlagSet("company-add", flag.ExitOnError)
	data := fs.String("data", "data", "数据目录")
	key := fs.String("key", "", "注册口令（缺省自动生成并打印）")
	_ = fs.Parse(flagsFirst(args))
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(os.Stderr, "用法: jxai-server company-add <公司名> [-key 口令] [-data data]")
		return 2
	}
	company := strings.TrimSpace(rest[0])
	if *key == "" {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		*key = hex.EncodeToString(b)
	}
	s := newRegistryStore(*data)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.loadCompanies()
	m[company] = registryCompany{Key: *key, AddedAt: time.Now().Format(time.RFC3339)}
	if err := s.saveCompanies(m); err != nil {
		fmt.Fprintln(os.Stderr, "保存失败:", err)
		return 1
	}
	fmt.Printf("已登记公司「%s」\n注册口令：%s（交给该公司服务端的 -company-key）\n", company, *key)
	return 0
}

func runCompanyDel(args []string) int {
	fs := flag.NewFlagSet("company-del", flag.ExitOnError)
	data := fs.String("data", "data", "数据目录")
	_ = fs.Parse(flagsFirst(args))
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "用法: jxai-server company-del <公司名> [-data data]")
		return 2
	}
	s := newRegistryStore(*data)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.loadCompanies()
	if _, ok := m[rest[0]]; !ok {
		fmt.Fprintln(os.Stderr, "该公司未登记")
		return 1
	}
	delete(m, rest[0])
	servers := s.loadServers()
	delete(servers, rest[0])
	if err := s.saveCompanies(m); err != nil {
		fmt.Fprintln(os.Stderr, "保存失败:", err)
		return 1
	}
	_ = s.saveServers(servers)
	fmt.Printf("已删除公司「%s」（登记信息一并清除）\n", rest[0])
	return 0
}
