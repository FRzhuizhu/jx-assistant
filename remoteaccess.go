// 远程接入（公网中继）——家侧桥接端，对标聊天服务 p2p.js 的中继通道。
//
// 架构（RustDesk 式，与 C:\Users\54358\Videos\AI\deploy-210 上已跑通的网关同协议）：
//   - 210 网关只做 ID 注册与密文中转：本端出站长连 ws://<host>:<port>/hub 注册设备号，
//     手机等外部设备打开 http://<host>:<port>/a/<设备号>#pk=<配对密钥> 的通用配对页
//     （gate.html），直连打洞不适用本实现（未接 PeerJS），自动落到 POST /relay 加密中继
//   - 帧内容 AES-256-GCM 端到端加密（key = SHA-256(pairKey)），网关只见密文
//   - 中继请求解密后桥接到本机回环的 HTTP 服务（/api/ 白名单），响应按 16KB 分帧
//     流式回传——SSE 对话流/事件流天然兼容；210 对单请求有 150s 上限，前端已有的
//     「接回执行流」机制（GET /api/sessions/{id}/stream）会在流被掐后自动续上
//
// 帧协议（与 p2p.js / gate.html 保持逐字节兼容）：
//
//	入：{t:'relay', rid, iv, ct} 解密后 {pk, ts, frame:{t:'req',id,method,path,headers,body(base64)}}
//	    {t:'abort', rid}
//	出：{t:'register', id, key} → 期待 {t:'regok'}
//	    响应帧逐帧 seal 后以 {t:'res', rid, iv, ct} 发送，全部结束后发 {t:'done', rid}
package main

import (
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// 外壳内联用的两个资产引用模式（index.html 是受控文件，模式可写死）
var (
	regexpLinkCSS   = regexp.MustCompile(`<link rel="stylesheet" href="/([^"]+)">`)
	regexpScriptSrc = regexp.MustCompile(`<script src="/([^"]+)"></script>`)
)

const (
	relayPingEvery  = 20 * time.Second // 主动 ping 周期（210 侧另有 30s 周期 ping，双向双保险）
	relayDeadAfter  = 75 * time.Second // 连续 ~3 轮无数据/无 pong 判僵死：主动断开重连
	relayMaxBody    = 64 << 20         // 单请求体上限（与 p2p.js / 210 对齐）
	relayChunkSize  = 16 * 1024        // 响应分帧大小（DataChannel/中继链路友好）
	relayBridgeHost = "127.0.0.1"      // 桥接目标固定走本机回环
)

// remoteHubStatus 供设置页展示与编辑的通道状态（含身份字段：设置页的
// 设备号/密钥/网关输入框由此回显）。
type remoteHubStatus struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	LastError string `json:"lastError,omitempty"`
	Entry     string `json:"entry,omitempty"` // 手机入口 URL（含配对密钥 fragment）
	MyID      string `json:"myId"`
	PairKey   string `json:"pairKey"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
}

// hubCtx 一次 hub 连接的全部状态；断线即整体废弃重连。
type hubCtx struct {
	ctx context.Context // 生命周期：取消即断开（设置页关闭开关/改配置触发的热重启）

	app    *App
	cfg    RemoteAccessCfg
	port   int // 本机回环桥接目标端口（实际监听端口，桌面模式回退后也正确）
	client *http.Client

	registered bool // 本次连接是否拿到过 regok（区分「连上过再断」与「从未连上」的退避）

	conn    net.Conn
	writeMu sync.Mutex // ws 帧写串行化：注册/心跳/多请求响应帧并发写

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // rid → 进行中桥接请求的中止句柄

	lastPong atomic.Int64 // unix ms；任何入站数据/pong 都刷新
}

var (
	remoteConnected atomic.Bool
	remoteLastError atomic.Value // string
)

func remoteStatus(cfg RemoteAccessCfg) remoteHubStatus {
	st := remoteHubStatus{
		Enabled:   cfg.Enabled,
		Connected: remoteConnected.Load(),
		MyID:      cfg.MyID,
		PairKey:   cfg.PairKey,
		Host:      cfg.Host,
		Port:      cfg.Port,
	}
	if v, ok := remoteLastError.Load().(string); ok {
		st.LastError = v
	}
	if cfg.MyID != "" {
		st.Entry = fmt.Sprintf("http://%s:%d/a/%s#pk=%s", cfg.Host, cfg.Port, cfg.MyID, cfg.PairKey)
	}
	return st
}

// syncRemoteAccess 按当前配置同步中继连接：关闭开关即断开、开启即连接，
// 运行中改身份/网关则热重启连接——设置页改动即时生效，无需重启进程。
// 由启动流程（main.go）与设置保存（handleSetConfig）调用。
func (a *App) syncRemoteAccess() {
	a.remoteCtlMu.Lock()
	defer a.remoteCtlMu.Unlock()
	if a.remoteCtlCancel != nil { // 先停旧连接（热重启场景）
		a.remoteCtlCancel()
		a.remoteCtlCancel = nil
		remoteConnected.Store(false)
		remoteLastError.Store("")
	}
	cfg := a.Cfg.RemoteAccess
	if !cfg.Enabled {
		log.Printf("[远程] 远程接入已关闭")
		return
	}
	if cfg.Host == "" || cfg.MyID == "" || cfg.PairKey == "" {
		log.Printf("[远程] 未启动：remoteAccess 缺 host/myId/pairKey")
		return
	}
	// 同一设备号只让一个实例注册：别的活实例已经占着这个号时，重复注册只会被网关
	// 互相踢下线（历史 bug：每秒 3-6 次重注册的热循环 + 手机入口反复闪断）
	if pid, other := a.relayOwnedByOther(); other {
		log.Printf("[远程] 中继已由另一个实例（pid %d）持有，同一设备号不重复注册：本实例不启用中继", pid)
		return
	}
	a.claimRelay()
	ctx, cancel := context.WithCancel(context.Background())
	a.remoteCtlCancel = cancel
	log.Printf("[远程] 正在连接网关 %s:%d（设备号 %s）", cfg.Host, cfg.Port, cfg.MyID)
	go runRemoteAccess(ctx, a, a.listenPort, cfg)
}

// runRemoteAccess 顶层循环：连接 210 的 /hub 并守护这条通道。
//
// 注册只在这几种时刻发生：启动、配置变更（syncRemoteAccess 热重启）、**网络环境
// 变化**（watchNetworkChange 通知后立即重连）、以及链路真的断了之后的退避重连。
// 刻意不做周期性重新注册——通道本身有 20s ping + 僵死看门狗保活，「网络换了 /
// 链路死了」这两件事由它们与变更通知覆盖，而代价小得多（本机与网关都省）。
//
// 退避规则：稳定跑过 relayStableAfter 再断 → 快速重连；从未注册成功 → 退避递增；
// **注册成功但很快被断** → 同样按退避递增（那是被网关踢/网关异常的形态，立刻
// 重连就会变成每秒数次的热循环——正是历史 bug）。
func runRemoteAccess(ctx context.Context, app *App, port int, cfg RemoteAccessCfg) {
	if cfg.Host == "" || cfg.PairKey == "" || cfg.MyID == "" {
		log.Printf("[远程] 未启动：config.json remoteAccess 缺 host/myId/pairKey")
		return
	}
	defer func() {
		remoteConnected.Store(false)
		app.releaseRelay()
		log.Printf("[远程] 中继通道已停止（开关关闭或配置变更）")
	}()

	// 网络环境变化 → 立即重新注册（零轮询：没变化时请求挂在系统里，不占资源）
	netChanged := make(chan struct{}, 1)
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	watchNetworkChange(stopWatch, func() {
		select {
		case netChanged <- struct{}{}:
		default:
		}
	})

	delay := relayRetryMin
	for {
		if ctx.Err() != nil {
			return
		}
		h := &hubCtx{
			ctx: ctx, app: app, cfg: cfg, port: port,
			client:  &http.Client{}, // 无总超时：长流式响应靠 abort 帧与请求 ctx 收口
			cancels: map[string]context.CancelFunc{},
		}
		// 兜底 recover：远程通道属增值能力，任何内部错误都不许拖垮主服务
		startedAt := time.Now()
		wasRegistered := func() (reg bool) {
			defer func() {
				if r := recover(); r != nil {
					remoteLastError.Store(fmt.Sprintf("内部错误: %v", r))
					log.Printf("[远程] 中继桥内部错误（已恢复，将重连）: %v", r)
					reg = h.registered
				}
			}()
			return h.run()
		}()
		up := time.Since(startedAt)
		remoteConnected.Store(false)
		switch {
		case wasRegistered && up >= relayStableAfter:
			delay = relayRetryMin // 稳定链路正常断线：快速重连
		case wasRegistered:
			// 注册成功却很快被断：按退避递增（被踢/网关异常的形态，别热循环）
			delay = min(max(delay, relayRetryMin)*2, relayRetryMax)
		default:
			delay = min(delay*2, relayRetryMax)
		}
		log.Printf("[远程] 中继通道断开（本次存活 %s），%s 后重连", up.Round(time.Second), delay)
		select {
		case <-ctx.Done():
			return
		case <-netChanged:
			log.Printf("[远程] 检测到网络环境变化，立即重新注册中继")
			delay = relayRetryMin
		case <-time.After(delay):
		}
	}
}

// run 建立并守护一条 hub 连接，返回「本次连接是否成功注册过」。
func (h *hubCtx) run() bool {
	url := fmt.Sprintf("ws://%s:%d/hub", h.cfg.Host, h.cfg.Port)
	conn, br, _, err := (&ws.Dialer{Timeout: 15 * time.Second}).Dial(context.Background(), url)
	if err != nil {
		remoteLastError.Store(fmt.Sprintf("连接网关失败: %v", err))
		return h.registered
	}
	h.conn = conn
	h.lastPong.Store(time.Now().UnixMilli())
	defer func() {
		h.mu.Lock()
		for rid, cancel := range h.cancels { // 掐断所有进行中的桥接请求
			cancel()
			delete(h.cancels, rid)
		}
		h.mu.Unlock()
		_ = conn.Close()
	}()

	// 外部取消（关闭开关/改配置热重启）：立即关闭连接，打断读阻塞
	watchCtx := h.ctx
	if watchCtx == nil {
		watchCtx = context.Background()
	}
	cancelWatch := make(chan struct{})
	defer close(cancelWatch)
	go func() {
		select {
		case <-watchCtx.Done():
			_ = conn.Close()
		case <-cancelWatch:
		}
	}()

	// 注册并等 regok（15s 超时对齐 p2p.js 的 auth 窗口）
	if err := h.sendJSON(map[string]any{"t": "register", "id": h.cfg.MyID, "key": h.cfg.RegKey}); err != nil {
		remoteLastError.Store(fmt.Sprintf("注册帧发送失败: %v", err))
		return h.registered
	}

	// 心跳：周期 ping + 僵死看门狗（NAT 静默丢弃死链不触发 close，必须主动探测）
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(relayPingEvery)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				if time.Since(time.UnixMilli(h.lastPong.Load())) > relayDeadAfter {
					log.Printf("[远程] 心跳超时，中继通道已僵死，主动断开重连")
					_ = conn.Close()
					return
				}
				h.writeMu.Lock()
				_ = wsutil.WriteClientMessage(conn, ws.OpPing, nil)
				h.writeMu.Unlock()
			}
		}
	}()

	// 握手未预读任何字节时 gobwas 会返回 nil 的 bufio.Reader：回落到裸连接
	var src io.Reader = conn
	if br != nil {
		src = br
	}
	rd := &wsutil.Reader{
		Source: src,
		State:  ws.StateClientSide,
		OnIntermediate: func(hdr ws.Header, r io.Reader) error {
			if hdr.OpCode == ws.OpPong || hdr.OpCode == ws.OpPing {
				h.lastPong.Store(time.Now().UnixMilli())
			}
			if hdr.OpCode == ws.OpPing { // 回 pong（210 正常不 ping 客户端，防御性处理）
				return h.sendRaw(ws.OpPong, nil)
			}
			return nil
		},
	}
	for {
		hdr, err := rd.NextFrame()
		if err != nil {
			remoteLastError.Store(fmt.Sprintf("通道读取失败: %v", err))
			return h.registered
		}
		if hdr.OpCode != ws.OpText {
			// 夹在消息间的控制帧（210 每 30s 一轮 ping）：必须消费掉帧体再继续，
			// 否则下一个帧头会从 ping 载荷中间读起，协议错位；且 ping 必须回 pong——
			// 210 侧靠 isAlive 标记清理死链，两轮无 pong 即 terminate
			payload, err := io.ReadAll(io.LimitReader(rd, 125))
			if err != nil {
				remoteLastError.Store(fmt.Sprintf("通道读取失败: %v", err))
				return h.registered
			}
			if hdr.OpCode == ws.OpPing {
				h.lastPong.Store(time.Now().UnixMilli())
				_ = h.sendRaw(ws.OpPong, payload) // RFC：pong 原样回带 ping 载荷
			}
			if hdr.OpCode == ws.OpClose {
				remoteLastError.Store("网关关闭了连接")
				return h.registered
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rd, 1<<26))
		if err != nil {
			remoteLastError.Store(fmt.Sprintf("通道读取失败: %v", err))
			return h.registered
		}
		h.lastPong.Store(time.Now().UnixMilli())
		var f struct {
			T   string `json:"t"`
			Rid string `json:"rid"`
			Iv  string `json:"iv"`
			Ct  string `json:"ct"`
		}
		if json.Unmarshal(data, &f) != nil || f.T == "" {
			continue
		}
		switch f.T {
		case "regok":
			first := !h.registered
			h.registered = true
			remoteConnected.Store(true)
			remoteLastError.Store("")
			// 日志节流：重连风暴时按分钟记一次，别把日志刷爆（历史上一夜两万行）
			if first || time.Since(time.UnixMilli(relayRegLoggedAt.Load())) > relayRegLogEvery {
				relayRegLoggedAt.Store(time.Now().UnixMilli())
				log.Printf("[远程] 中继通道已注册 %s:%d/hub id=%s（手机入口 %s）",
					h.cfg.Host, h.cfg.Port, h.cfg.MyID,
					fmt.Sprintf("http://%s:%d/a/%s", h.cfg.Host, h.cfg.Port, h.cfg.MyID))
			}
		case "relay":
			if f.Rid != "" && f.Iv != "" && f.Ct != "" {
				rid, iv, ct := f.Rid, f.Iv, f.Ct
				go func() { // 每请求一协程；panic 不外溢
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[远程] %s 桥接协程异常（已恢复）: %v", rid, r)
						}
					}()
					h.handleRelay(rid, iv, ct)
				}()
			}
		case "abort":
			h.mu.Lock()
			if cancel, ok := h.cancels[f.Rid]; ok { // 浏览器断开/超时：销毁桥接请求
				cancel()
				delete(h.cancels, f.Rid)
			}
			h.mu.Unlock()
		}
	}
}

/* ---------- 中继封包：AES-256-GCM，与 p2p.js sealFrame/openPack 逐字节兼容 ---------- */

type relayReqFrame struct {
	T       string            `json:"t"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64，可空
}

type relayPack struct {
	Pk    string        `json:"pk"`
	Ts    float64       `json:"ts"`
	Frame relayReqFrame `json:"frame"`
}

func relayKey(pairKey string) []byte {
	k := sha256.Sum256([]byte(pairKey))
	return k[:]
}

func sealJSON(key []byte, v any) (ivB64, ctB64 string, err error) {
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	iv := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(iv); err != nil {
		return
	}
	plain, _ := json.Marshal(v)
	ct := gcm.Seal(nil, iv, plain, nil)
	return base64.StdEncoding.EncodeToString(iv),
		base64.StdEncoding.EncodeToString(ct), nil
}

func openPack(key []byte, ivB64, ctB64 string) (*relayPack, error) {
	iv, err := base64.StdEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if len(data) < gcm.Overhead() || len(iv) != gcm.NonceSize() {
		return nil, fmt.Errorf("bad pack")
	}
	plain, err := gcm.Open(nil, iv, data, nil)
	if err != nil {
		return nil, err
	}
	var p relayPack
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// handleRelay 处理一条中继请求：解密 → 验 pk/ts → 桥接本机服务 → 流式回传。
func (h *hubCtx) handleRelay(rid, ivB64, ctB64 string) {
	key := relayKey(h.cfg.PairKey)
	pack, err := openPack(key, ivB64, ctB64)
	if err != nil {
		// 解不开（密钥不对/篡改）：回空 done，让浏览器侧报「密钥错误」而非挂到超时
		log.Printf("[远程] %s 解密失败（密钥不匹配或包损坏）", rid)
		_ = h.sendJSON(map[string]any{"t": "done", "rid": rid})
		return
	}
	// 防重放：时间窗 ±10 分钟（GCM 保证完整性，此处只需挡被动重放）
	now := time.Now().Unix()
	ts := int64(pack.Ts)
	if pack.Ts == 0 || ts < now-600 || ts > now+600 {
		log.Printf("[远程] %s 时间戳超窗，丢弃", rid)
		_ = h.sendJSON(map[string]any{"t": "done", "rid": rid})
		return
	}
	// 每请求独立验 pairKey（中继无会话，不依赖长连接状态）
	if subtle.ConstantTimeCompare([]byte(pack.Pk), []byte(h.cfg.PairKey)) != 1 {
		log.Printf("[远程] %s 配对密钥错误", rid)
		iv, ct, _ := sealJSON(key, map[string]any{"t": "authfail"})
		_ = h.sendJSON(map[string]any{"t": "res", "rid": rid, "iv": iv, "ct": ct})
		_ = h.sendJSON(map[string]any{"t": "done", "rid": rid})
		return
	}
	fr := pack.Frame
	if fr.T != "req" {
		return // 中继只接受 req 帧
	}
	if !strings.HasPrefix(fr.Path, "/api/") {
		iv, ct, _ := sealJSON(key, map[string]any{"t": "err", "id": fr.ID, "msg": "[403] path not allowed"})
		_ = h.sendJSON(map[string]any{"t": "res", "rid": rid, "iv": iv, "ct": ct})
		_ = h.sendJSON(map[string]any{"t": "done", "rid": rid})
		return
	}
	if len(fr.Body) > relayMaxBody*2 {
		iv, ct, _ := sealJSON(key, map[string]any{"t": "err", "id": fr.ID, "msg": "[413] request too large"})
		_ = h.sendJSON(map[string]any{"t": "res", "rid": rid, "iv": iv, "ct": ct})
		_ = h.sendJSON(map[string]any{"t": "done", "rid": rid})
		return
	}
	h.bridge(rid, fr, key)
}

// bridge 把一条 req 帧转成本机 HTTP 请求并流式回传。sender 失败（连接已死）
// 立即取消请求，避免对空气播。响应拆帧的核心在 bridgeCore（中继/直连共用），
// 本包装负责中继特有部分：每帧 AES 封包裹 res/rid、结束时发 done。
func (h *hubCtx) bridge(rid string, fr relayReqFrame, key []byte) {
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancels[rid] = cancel
	h.mu.Unlock()
	defer func() {
		cancel()
		h.mu.Lock()
		delete(h.cancels, rid)
		h.mu.Unlock()
	}()
	// send：单帧 seal 后经 {t:'res'} 回传；失败即中止整条桥接
	send := func(v map[string]any) bool {
		iv, ct, err := sealJSON(key, v)
		if err != nil {
			return false
		}
		return h.sendJSON(map[string]any{"t": "res", "rid": rid, "iv": iv, "ct": ct}) == nil
	}
	bridgeCore(ctx, h.client, h.port, fr, send)
	_ = h.sendJSON(map[string]any{"t": "done", "rid": rid})
}

// bridgeCore 把一条已验证的 req 帧桥到本机回环 HTTP 并按帧回传响应：
// head（含 gz 标志）→ chunk* → end；任何阶段失败发 err 帧后返回。send 是传输
// 层发送函数——中继版封 AES 后裹 {t:'res',rid}（hubCtx.bridge），直连版明文
// JSON 直接写 DataChannel（DTLS 链路本身加密，p2pdirect.go）。发送失败立即
// 返回，由调用方的 defer cancel() 收掉进行中的 HTTP 请求。
func bridgeCore(ctx context.Context, client *http.Client, port int, fr relayReqFrame, send func(map[string]any) bool) {
	fail := func(msg string) {
		send(map[string]any{"t": "err", "id": fr.ID, "msg": msg})
	}
	var body io.Reader
	if fr.Body != "" {
		raw, err := base64.StdEncoding.DecodeString(fr.Body)
		if err != nil {
			fail("bad base64 body")
			return
		}
		body = strings.NewReader(string(raw))
	}
	method := strings.ToUpper(fr.Method)
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method,
		fmt.Sprintf("http://%s:%d%s", relayBridgeHost, port, fr.Path), body)
	if err != nil {
		fail(err.Error())
		return
	}
	// 透传必要头（host/content-length 由本端重算，其余浏览器头无意义不透传）
	if ct := fr.Headers["content-type"]; ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			fail("bridge: " + err.Error())
		}
		return
	}
	defer resp.Body.Close()

	// 响应体 gzip 仅对一次性响应启用（前端带 x-p2p-gzip 头时）：正文先压
	// 再 base64/传输，膨胀只作用于压缩后字节。除 JSON 外也压 text/css、
	// text/javascript 等组件资产（中继按流量走，文本压缩率高）；SSE/流式响应
	// 必须排除——gzip 内部缓冲不及时吐，会拖走对话流的实时性。
	// 排除判断必须取「媒体类型」部分（去掉 "; charset=utf-8" 等参数后再比对）：
	// 服务端 SSE 端点发的都是 text/event-stream; charset=utf-8，历史实现按
	// 全串精确比对永远不相等 → 对话流/事件流全被套进 gzip，flate 攒着不吐，
	// 远程端整个输出期间收不到帧（发送后「发送中」占位干等数秒、页面毫无
	// 实时更新，直到缓冲灌满才一次性吐出一大块）。
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	wantGz := fr.Headers["x-p2p-gzip"] == "1" && mt != "text/event-stream" &&
		(strings.HasPrefix(mt, "application/json") || strings.HasPrefix(mt, "text/"))
	head := map[string]any{
		"t": "head", "id": fr.ID,
		"status":      resp.StatusCode,
		"contentType": resp.Header.Get("Content-Type"),
	}
	if wantGz {
		head["gz"] = 1
	}
	if !send(head) {
		return
	}
	sendEnd := func() {
		send(map[string]any{"t": "end", "id": fr.ID})
	}
	if wantGz {
		// 流式 gzip：分块都是压缩后的字节（接收端按 head.gz 标志整体解压）
		gz, _ := gzip.NewWriterLevel(&chunkSender{send: send, id: fr.ID}, 6)
		if gz == nil {
			fail("gzip init failed")
			return
		}
		if _, err := io.Copy(gz, resp.Body); err != nil {
			gz.Close()
			fail("gzip: " + err.Error())
			return
		}
		if err := gz.Close(); err != nil {
			fail("gzip: " + err.Error())
			return
		}
		sendEnd()
		return
	}
	buf := make([]byte, relayChunkSize)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if !send(map[string]any{"t": "chunk", "id": fr.ID, "data": base64.StdEncoding.EncodeToString(buf[:n])}) {
				return
			}
		}
		if rerr != nil {
			if rerr != io.EOF && ctx.Err() == nil {
				fail("bridge: " + rerr.Error())
				return
			}
			break
		}
	}
	sendEnd()
}

// chunkSender 把 gzip 输出切成传输帧（中继/直连共用）。
type chunkSender struct {
	send func(map[string]any) bool
	id   string
}

func (c *chunkSender) Write(p []byte) (int, error) {
	if !c.send(map[string]any{"t": "chunk", "id": c.id, "data": base64.StdEncoding.EncodeToString(p)}) {
		return 0, fmt.Errorf("relay send failed")
	}
	return len(p), nil
}

func (h *hubCtx) replyErr(rid, id string, key []byte, msg string) {
	iv, ct, _ := sealJSON(key, map[string]any{"t": "err", "id": id, "msg": msg})
	_ = h.sendJSON(map[string]any{"t": "res", "rid": rid, "iv": iv, "ct": ct})
	_ = h.sendJSON(map[string]any{"t": "done", "rid": rid})
}

/* ---------- ws 底层发送 ---------- */

func (h *hubCtx) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if h.conn == nil {
		return fmt.Errorf("hub not connected")
	}
	return wsutil.WriteClientMessage(h.conn, ws.OpText, data)
}

func (h *hubCtx) sendRaw(op ws.OpCode, data []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if h.conn == nil {
		return fmt.Errorf("hub not connected")
	}
	return wsutil.WriteClientMessage(h.conn, op, data)
}

// validRemoteID 设备号格式：2~32 位字母/数字/中划线/下划线（网关按它做
// 入口路径与注册路由，太怪的字符合适挡掉）。
func validRemoteID(id string) bool {
	if len(id) < 2 || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// validRemoteHost 网关地址格式：域名或 IP 字面量（不带协议前缀与端口，
// 端口是独立字段）。
func validRemoteHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if len(h) < 1 || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || !strings.ContainsFunc(l, func(r rune) bool {
			return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
		}) {
			return false
		}
	}
	return true
}

/* ---------- 远程外壳（/api/p2p-shell）：单文件自包含 HTML ---------- */

// buildP2PShell 把多文件前端拼成单文件 HTML：样式/脚本全部内联、logo 转 dataURL、
// 头部注入远程引导脚本。210 的通用配对页（gate.html）连上后经隧道拉这一页换页——
// 网关不寄存任何应用副本，改前端只需重启本服务。结果缓存：拼装不便宜且内容只随
// 二进制（或 dev webdir 文件）变化。
var (
	p2pShellOnce sync.Once
	p2pShellBase string // 外壳基底：内联与引导注入完成（版本脚本除外），按进程缓存
	p2pShellHash string // 基底内容 SHA-256（十六进制）：运行版本 = 哈希 + "." + 代数（shellEpoch）
	p2pShellErr  error
)

func (a *App) p2pShell() (string, error) {
	p2pShellOnce.Do(func() {
		idx, err := fs.ReadFile(a.static, "index.html")
		if err != nil {
			p2pShellErr = fmt.Errorf("index.html 读取失败: %w", err)
			return
		}
		s := string(idx)
		// 样式内联：<link rel="stylesheet" href="/x.css">
		s = regexpLinkCSS.ReplaceAllStringFunc(s, func(m string) string {
			sub := regexpLinkCSS.FindStringSubmatch(m)
			css, err := fs.ReadFile(a.static, sub[1])
			if err != nil {
				return m
			}
			return "<style>\n" + string(css) + "\n</style>"
		})
		// 脚本内联：<script src="/x.js"></script>（保留原有属性次序不影响）
		s = regexpScriptSrc.ReplaceAllStringFunc(s, func(m string) string {
			sub := regexpScriptSrc.FindStringSubmatch(m)
			js, err := fs.ReadFile(a.static, sub[1])
			if err != nil {
				return m
			}
			return "<script>\n" + string(js) + "\n</script>"
		})
		// logo 转 dataURL（两处 img）
		if logo, err := fs.ReadFile(a.static, "logo.png"); err == nil {
			s = strings.ReplaceAll(s, `src="/logo.png"`,
				`src="data:image/png;base64,`+base64.StdEncoding.EncodeToString(logo)+`"`)
		}
		// 头部注入远程引导（最先执行：app 脚本加载前劫持好 fetch/EventSource）
		boot, err := fs.ReadFile(a.static, "remote-boot.js")
		if err != nil {
			p2pShellErr = fmt.Errorf("remote-boot.js 读取失败: %w", err)
			return
		}
		s = strings.Replace(s, "<head>", "<head>\n<script>\n"+string(boot)+"\n</script>", 1)
		// 内容哈希只对基底计算（版本脚本不含在内——代数可变，改由服务时注入；
		// 且哈希必须先于自身注入计算，否则含自身无法稳定得出）
		sum := sha256.Sum256([]byte(s))
		p2pShellHash = fmt.Sprintf("%x", sum)
		p2pShellBase = s
	})
	return p2pShellBase, p2pShellErr
}

// handleP2PShell 供 210 配对页拉取应用外壳；?enc=gzip 返回整体压缩体
// （中继按流量走，700KB 外壳压到 ~150KB）。
func (a *App) handleP2PShell(w http.ResponseWriter, r *http.Request) {
	base, err := a.p2pShell()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 版本脚本服务时注入：基底按进程缓存，代数（一键清缓存按钮）变了无需重建
	html := strings.Replace(base, "<head>",
		"<head>\n<script>window.JX_SHELL_HASH="+fmt.Sprintf("%q", a.shellVersion())+";</script>", 1)
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("enc") == "gzip" {
		w.Header().Set("Content-Type", "application/gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_, _ = gz.Write([]byte(html))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

// handleShellHash 下发当前外壳运行版本（内容哈希.代数，几十字节）：远程引导
// （remote-boot.js）开机比对，不一致即清手机端外壳缓存并整页重拉——把任何原因
// 的旧外壳（含「关于」页一键清缓存按下的代数递增）压到「下一次打开」就更新。
func (a *App) handleShellHash(w http.ResponseWriter, r *http.Request) {
	if _, err := a.p2pShell(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"hash": a.shellVersion()})
}

/* ---------- 外壳版本代数：「关于」页一键清缓存 ---------- */

// shellVersion 当前外壳运行版本 = 基底内容哈希 + "." + 代数。嵌入式脚本
// （window.JX_SHELL_HASH）与 /api/shell-hash 同源生成，稳态两端一致；代数递增后
// 全部旧外壳「版本不一致」，remote-boot 的自愈（清 p2p_shell_cache_v3 并整页
// 刷新）随即触发——不需要给手机端新下发自毁指令，旧机制天然复用。
func (a *App) shellVersion() string {
	a.shellEpochMu.Lock()
	defer a.shellEpochMu.Unlock()
	return fmt.Sprintf("%s.%d", p2pShellHash, a.shellEpoch)
}

func (a *App) shellEpochPath() string {
	return filepath.Join(a.Cfg.DataDir, "shell-epoch.json")
}

// loadShellEpoch 启动时恢复代数：进程重启若把代数打回去，全部远程端会白白
// 多经历一轮「比对不一致→清缓存→刷新」。
func (a *App) loadShellEpoch() {
	data, err := os.ReadFile(a.shellEpochPath())
	if err != nil {
		return
	}
	var v struct {
		Epoch int64 `json:"epoch"`
	}
	if json.Unmarshal(data, &v) == nil && v.Epoch >= 0 {
		a.shellEpoch = v.Epoch
	}
}

// handleShellBump 「关于」页一键清除远程端缓存：代数 +1 并落盘。按过后，仍在
// 运行的远程页面在下次加载时发现版本不一致，自动清掉本地缓存整页重拉；连还停
// 留在旧版配对页（12 小时 TTL 时代外壳）上的手机也一并治愈——它们的自愈同样
// 比对 /api/shell-hash。日常远程端每次打开本就拉最新外壳，此按钮是异常状态的
// 统一兜底，多按无害（只是多递增几个代数）。
func (a *App) handleShellBump(w http.ResponseWriter, r *http.Request) {
	if _, err := a.p2pShell(); err != nil { // 先确保基底与内容哈希就绪（版本号完整）
		writeErr(w, 500, err.Error())
		return
	}
	a.shellEpochMu.Lock()
	a.shellEpoch++
	ep := a.shellEpoch
	a.shellEpochMu.Unlock()
	data, _ := json.Marshal(map[string]int64{"epoch": ep})
	if err := writeFileAtomic(a.shellEpochPath(), data); err != nil {
		log.Printf("[远程] 外壳代数落盘失败（本次仍生效，重启后回退）: %v", err)
	}
	log.Printf("[远程] 外壳版本已递增至 %s（远程端下次加载自动清缓存重载）", a.shellVersion())
	writeJSON(w, 200, map[string]any{"hash": a.shellVersion()})
}
