// jxai-server —— 一体化服务端（210 网关的 Go 版：远程访问中继 + 技能/组件/插件市场）。
//
// 两个职责：
//  1. 远程访问中继：与 deploy-210 的 Node 网关 server.js 协议逐字节兼容
//     （/hub WebSocket 注册 + POST /relay NDJSON 密文透传 + /a/<设备号> 配对页），
//     现有客户端（remoteaccess.go）与配对页（gate.html）无需任何改动即可切换过来。
//     未移植 PeerJS 信令：Go 客户端本就不走直连，配对页在服务时被注入
//     _forceRelay 标志直接走中继（省去 3 秒等待）；原 Node 网关保持不动，
//     本服务先在新端口并行验证。
//  2. 技能/组件/插件市场：GET /market/manifest.json 下发清单、
//     GET /market/items/... 下发 zip。发布走本二进制的子命令（服务器上经
//     SSH 使用）：jxai-server publish / unpublish，见 market.go。
//
// 部署见 部署/（setup.sh + jxai-server.service + README）；三期预留：模型中转
// （OpenAI 兼容反代）另起子命令挂入，本期不实现。
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const serverVersion = "0.1.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "publish":
			os.Exit(runPublish(os.Args[2:]))
		case "unpublish":
			os.Exit(runUnpublish(os.Args[2:]))
		case "publish-update":
			os.Exit(runPublishUpdate(os.Args[2:]))
		case "company-add":
			os.Exit(runCompanyAdd(os.Args[2:]))
		case "company-del":
			os.Exit(runCompanyDel(os.Args[2:]))
		case "set-api":
			os.Exit(runSetAPI(os.Args[2:]))
		case "version":
			fmt.Println("jxai-server " + serverVersion)
			return
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	os.Exit(runServer(os.Args[1:]))
}

func usage() {
	fmt.Println(`jxai-server —— 远程访问中继 + 技能/组件/插件市场 + 更新分发（单二进制）

用法：
  jxai-server [flags]                      启动服务（默认）
  jxai-server publish <skill|component|plugin> <目录|zip> [flags]   发布一个条目
  jxai-server unpublish <skill|component|plugin> <id> [flags]      下架一个条目
  jxai-server publish-update <安装包.exe> -version 20261008V1 [flags]  发布软件更新（总服务端用）
  jxai-server company-add <公司名> [-key 口令]                       登记客户公司（总服务端用）
  jxai-server company-del <公司名>                                   删除公司登记
  jxai-server set-api -newapi <地址> -user <用户> -pass <密码> [flags] 配置模型通道（接 new-api）
  jxai-server version                      版本

服务 flags：
  -addr string             监听地址（默认 ":PORT"）
  -data string             数据目录，存 market.json / items/ / updates/（默认 "data"）
  -peer-key string         /hub 注册准入 key，客户端需一致（默认 "chat_p2p_v1"）
  -market-name string      市场名称，进 manifest.name（默认 "坚祥内部市场"）
  -update-upstream string  总服务端地址（客户公司服务端用：定期拉上游更新并镜像给
                           本地客户端；空 = 自己是源头，不拉取）
  -company string          本服务端所属公司名（同时是局域网发现的配对口令与下发身份；
                           空 = 不参与发现与下发）
  -company-key string      向总服务器登记用的口令（company-add 生成的那把）
  -register-to string      总服务器地址（启动 10s 后登记、每 6h 心跳；空 = 不登记）
  -advertise string        登记用的对外地址（ip 或 ip:port；缺省枚举本机内网 IPv4）
  -api-public string       new-api 对客户端的可达地址（缺省按请求方 Host 自动推导）

publish flags：
  -data string        数据目录（同上）
  -id string          条目 id（缺省从 SKILL.md/manifest/plugin.json 推导）
  -name string        展示名（缺省同上推导）
  -version string     版本号（缺省取组件/插件清单里的 version，再缺省 1.0.0）
  -desc string        一句话描述
  -tags string        逗号分隔标签

publish-update flags：
  -data string        数据目录（同上）
  -version string     版本号，日期+V序号（如 20261008V1，同日发多版用 V2 V3）
  -notes string       发布说明（客户端横幅里展示）

set-api flags：
  -newapi string      new-api 根地址（如 http://127.0.0.1:3000）
  -user/-pass string  new-api 管理员账号（受控发令牌用）
  -default string     默认模型 id（缺省取清单第一个）
  -models string      允许下发的模型（逗号分隔，缺省不限制）
  -public string      new-api 对客户端的可达地址（缺省按请求 Host 自动推导）`)
}

// runServer 启动 HTTP 服务：中继 + 市场 + 配对页。
func runServer(args []string) int {
	fs := flag.NewFlagSet("jxai-server", flag.ExitOnError)
	addr := fs.String("addr", ":PORT", "监听地址")
	dataDir := fs.String("data", "data", "数据目录（market.json 与 items/）")
	peerKey := fs.String("peer-key", "chat_p2p_v1", "hub 注册准入 key")
	marketName := fs.String("market-name", "坚祥内部市场", "市场名称")
	upstream := fs.String("update-upstream", "", "总服务端地址（定期拉上游更新并镜像给本地客户端；空=自己是源头）")
	company := fs.String("company", "", "公司名（局域网发现口令与下发身份；空=不参与）")
	companyKey := fs.String("company-key", "", "向总服务器登记的口令")
	registerTo := fs.String("register-to", "", "总服务器地址（启动 10s 后登记、每 6h 心跳）")
	advertise := fs.String("advertise", "", "登记用对外地址（缺省枚举本机内网 IPv4）")
	apiPublic := fs.String("api-public", "", "new-api 对客户端的可达地址（缺省按请求 Host 推导）")
	_ = fs.Parse(args)

	port := 2228
	if p := listenPortOf(*addr); p > 0 {
		port = p
	}
	hub := newRelayHub(*peerKey)
	market := newMarketStore(*dataDir, *marketName)
	updates := newUpdateStore(*dataDir, *upstream)
	updates.startUpdateSyncer()
	registry := newRegistryStore(*dataDir)
	provision := newProvisionStore(*dataDir, strings.TrimSpace(*company))
	if pub := strings.TrimSpace(*apiPublic); pub != "" { // -api-public 兜底写进 api.json（flag 与 set-api -public 二选一即可）
		if cfg := provision.loadAPI(); cfg != nil && cfg.PublicBase == "" {
			cfg.PublicBase = pub
			_ = provision.saveAPI(cfg)
		}
	}
	startRegistrar(*registerTo, strings.TrimSpace(*company), *companyKey, *advertise, port)
	startDiscovery(*company, *marketName, port)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           buildMux(hub, market, updates, registry, provision),
		ReadHeaderTimeout: 15 * time.Second,
	}
	upstreamLog := strings.TrimSpace(*upstream)
	if upstreamLog == "" {
		upstreamLog = "无（本机即源头）"
	}
	companyLog := strings.TrimSpace(*company)
	if companyLog == "" {
		companyLog = "未设置（不参与发现与下发）"
	}
	log.Printf("[jxai-server] 监听 %s（peer-key=%s 数据目录=%s 市场名=%s 更新上游=%s 公司=%s）",
		*addr, *peerKey, *dataDir, *marketName, upstreamLog, companyLog)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("[jxai-server] 服务退出: %v", err)
		return 1
	}
	return 0
}

// buildMux 组装全部路由（服务与测试共用同一份路由表）。
func buildMux(hub *relayHub, market *marketStore, updates *updateStore,
	registry *registryStore, provision *provisionStore) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStr(w, 200, hub.healthz(market.counts()))
	})
	mux.HandleFunc("GET /hub", hub.handleHub)
	mux.HandleFunc("POST /relay", hub.handleRelay)
	mux.HandleFunc("GET /market/manifest.json", market.handleManifest)
	mux.Handle("GET /market/items/", http.HandlerFunc(market.handleItem))
	mux.HandleFunc("GET /updates/latest", updates.handleLatest)
	mux.Handle("GET /updates/file/", http.HandlerFunc(updates.handleUpdateFile))
	// 市场管理页（admin.go）：无公开入口、暂无鉴权（用户/角色体系后续接入），
	// 知道网址即达——仅 IT 内部使用
	mux.HandleFunc("GET /admin", market.handleAdminPage)
	mux.HandleFunc("GET /admin/api/list", market.handleAdminList)
	mux.HandleFunc("POST /admin/api/publish", market.handleAdminPublish)
	mux.HandleFunc("POST /admin/api/unpublish", market.handleAdminUnpublish)
	// 公司接入（registry.go / provision.go / discovery.go）：登记簿 + 配置下发
	mux.HandleFunc("POST /registry/register", registry.handleRegister)
	mux.HandleFunc("GET /registry/lookup", registry.handleLookup)
	mux.HandleFunc("POST /provision", provision.handleProvision)
	mux.HandleFunc("GET /", hub.handleGate)
	return mux
}

// listenPortOf 从 -addr 里解析端口（":PORT" / "127.0.0.1:PORT"；解析不出给 0）。
func listenPortOf(addr string) int {
	_, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}
