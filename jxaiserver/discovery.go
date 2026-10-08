// 局域网自动发现（UDP 2228）：公司名即配对口令。
//
// 客户端（向导）向 255.255.255.255:PORT/udp 及各接口子网广播地址发
//
//	{"jxai":1,"company":"公司名"}
//
// 本服务端（配置了 -company 才应答）公司名精确匹配 → 单播回
//
//	{"jxai":1,"company":"公司名","server":"http://<收包源IP>:PORT","name":"..."}
//
// 回应地址用收包源地址：客户端用什么网络到达，就回它能路由回去的 IP，服务端零配置。
// 不匹配/未配置 -company 一律沉默（不泄露公司是否存在于本网）。
package main

import (
	"encoding/json"
	"log"
	"net"
	"strconv"
	"strings"
)

const discoveryPort = 2228 // 与 TCP 服务同号（UDP 独立协议栈，互不冲突）

type discoveryProbe struct {
	Jxai    int    `json:"jxai"`
	Company string `json:"company"`
}

// startDiscovery 监听 UDP 2228 应答发现探测（company 为空 = 不启用发现）。
func startDiscovery(company, serverName string, port int) {
	if strings.TrimSpace(company) == "" {
		return
	}
	company = strings.TrimSpace(company)
	pc, err := net.ListenPacket("udp4", ":PORT")
	if err != nil {
		log.Printf("[发现] UDP %d 监听失败（局域网自动发现不可用）: %v", discoveryPort, err)
		return
	}
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p discoveryProbe
			if json.Unmarshal(buf[:n], &p) != nil || p.Jxai != 1 || p.Company != company {
				continue // 不是我们的探测、或口令（公司名）不匹配：沉默
			}
			host, _, err := net.SplitHostPort(addr.String())
			if err != nil {
				host = addr.String()
			}
			resp, _ := json.Marshal(map[string]any{
				"jxai": 1, "company": company, "name": serverName,
				"server": "http://" + host + ":" + strconv.Itoa(port),
			})
			if _, err := pc.WriteTo(resp, addr); err != nil {
				continue
			}
			log.Printf("[发现] 应答 %s 的探测（公司匹配）", host)
		}
	}()
	log.Printf("[发现] 局域网自动发现已启用（UDP %d，公司口令=%s）", discoveryPort, company)
}
