// jx-server —— 一体化服务端（210 网关的 Go 版：远程访问中继 + 技能/组件/插件市场）。
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
//     SSH 使用）：jx-server publish / unpublish，见 market.go。
//
// 部署见 部署/（setup.sh + jx-server.service + README）；三期预留：模型中转
// （OpenAI 兼容反代）另起子命令挂入，本期不实现。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
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
		case "version":
			fmt.Println("jx-server " + serverVersion)
			return
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	os.Exit(runServer(os.Args[1:]))
}

func usage() {
	fmt.Println(`jx-server —— 远程访问中继 + 技能/组件/插件市场（单二进制）

用法：
  jx-server [flags]                      启动服务（默认）
  jx-server publish <skill|component|plugin> <目录|zip> [flags]   发布一个条目
  jx-server unpublish <skill|component|plugin> <id> [flags]      下架一个条目
  jx-server version                      版本

服务 flags：
  -addr string        监听地址（默认 ":PORT"）
  -data string        数据目录，存 market.json 与 items/（默认 "data"）
  -peer-key string    /hub 注册准入 key，客户端需一致（默认 "chat_p2p_v1"）
  -market-name string 市场名称，进 manifest.name（默认 "坚祥内部市场"）

publish flags：
  -data string        数据目录（同上）
  -id string          条目 id（缺省从 SKILL.md/manifest/plugin.json 推导）
  -name string        展示名（缺省同上推导）
  -version string     版本号（缺省取组件/插件清单里的 version，再缺省 1.0.0）
  -desc string        一句话描述
  -tags string        逗号分隔标签`)
}

// runServer 启动 HTTP 服务：中继 + 市场 + 配对页。
func runServer(args []string) int {
	fs := flag.NewFlagSet("jx-server", flag.ExitOnError)
	addr := fs.String("addr", ":PORT", "监听地址")
	dataDir := fs.String("data", "data", "数据目录（market.json 与 items/）")
	peerKey := fs.String("peer-key", "chat_p2p_v1", "hub 注册准入 key")
	marketName := fs.String("market-name", "坚祥内部市场", "市场名称")
	_ = fs.Parse(args)

	hub := newRelayHub(*peerKey)
	market := newMarketStore(*dataDir, *marketName)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           buildMux(hub, market),
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("[jx-server] 监听 %s（peer-key=%s 数据目录=%s 市场名=%s）", *addr, *peerKey, *dataDir, *marketName)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("[jx-server] 服务退出: %v", err)
		return 1
	}
	return 0
}

// buildMux 组装全部路由（服务与测试共用同一份路由表）。
func buildMux(hub *relayHub, market *marketStore) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStr(w, 200, hub.healthz(market.counts()))
	})
	mux.HandleFunc("GET /hub", hub.handleHub)
	mux.HandleFunc("POST /relay", hub.handleRelay)
	mux.HandleFunc("GET /market/manifest.json", market.handleManifest)
	mux.Handle("GET /market/items/", http.HandlerFunc(market.handleItem))
	mux.HandleFunc("GET /", hub.handleGate)
	return mux
}
