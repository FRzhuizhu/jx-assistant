// 远程访问中继（服务端半边）—— deploy-210 网关 server.js 的 Go 移植，协议逐条对齐：
//
//	/hub   家侧长连：首帧 {t:'register',id,key}（15s 注册窗口，key 不符即断）→
//	       回 {t:'regok'}；同设备号新顶旧（旧连接立即断开）；家侧下线时该设备
//	       全部挂起中继立即失败（浏览器重试拿到 503）。30s 一轮 ping，两轮无
//	       pong 判死链断开（家里客户端另有 20s ping + 75s 僵死看门狗，双保险）。
//	/relay 手机侧 POST：header X-Relay-Peer 路由到设备，body {iv,ct} 不透明
//	       转发（本服务只见密文）；响应 200 + NDJSON 逐行 {iv,ct} 透传家里的
//	       res 帧，done/连接关闭结束。单请求 150s 超时、body 上限 70MB、
//	       每 IP 并发 8 个；浏览器提前断开/超时/家里掉线都向家里发
//	       {t:'abort',rid} 让其销毁桥接请求。
//
// 与 server.js 的差异（有意为之）：不实现 /peerjs 信令——本服务的客户端
// （remoteaccess.go）本就不走 PeerJS 直连，配对页在 handleGate 里被注入
// _forceRelay 直接走中继。
package main

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

//go:embed gate.html
var gateFS embed.FS

const (
	relayRegWindow  = 15 * time.Second // 注册窗口：15s 内未完成注册即断（对齐 server.js / p2p.js auth 窗口）
	relayPingEvery  = 30 * time.Second // 服务端 ping 周期；两轮无 pong 判死链
	relayMaxBodyDef = 70 << 20         // 单中继请求体上限（对齐家里 64MB × base64 膨胀）
	relayPerIPDef   = 8                // 每 IP 并发挂起数（防匿名滥用占满连接）
)

// relayHub 持有设备连接表与挂起中继；全部字段经 mu 保护（alive 等原子量除外）。
type relayHub struct {
	peerKey string

	timeout  time.Duration // 单请求总超时（默认 150s；测试可调短）
	maxBody  int64
	perIPLim int

	mu      sync.Mutex
	conns   map[string]*hubConn      // 设备号 → 连接
	pending map[string]*relayPending // rid → 挂起中继
	perIP   map[string]int           // IP → 并发计数
	seq     int64                    // rid 序号（atomic）
}

// hubConn 一条家侧 /hub 连接。
type hubConn struct {
	id    string
	hub   *relayHub
	conn  net.Conn
	wmu   sync.Mutex // ws 帧写串行化：regok/relay/abort/ping 并发写
	alive atomic.Bool
}

// relayPending 一条挂起的 /relay 请求。ResponseWriter 只归 HTTP 协程所有：
// 家侧 res/done 帧与离线事件经 ch 递给它，写流/收口全在 HTTP 协程做。
type relayPending struct {
	rid, peerID, ip string
	ch              chan hubLine
	done            chan struct{}
	closeOnce       sync.Once
}

// hubLine 家侧通道里的一条事件：加密响应帧或结束标记。
type hubLine struct {
	iv, ct  string
	done    bool // 家里发来 {t:'done'}：响应全部完成
	offline bool // 家里掉线：本 rid 立即失败
}

func newRelayHub(peerKey string) *relayHub {
	h := &relayHub{
		peerKey:  peerKey,
		timeout:  150 * time.Second,
		maxBody:  relayMaxBodyDef,
		perIPLim: relayPerIPDef,
		conns:    map[string]*hubConn{},
		pending:  map[string]*relayPending{},
		perIP:    map[string]int{},
	}
	go h.pingLoop()
	return h
}

func (h *relayHub) healthz(extra map[string]int) string {
	h.mu.Lock()
	n, p := len(h.conns), len(h.pending)
	h.mu.Unlock()
	m := map[string]any{"ok": true, "service": "jxai-server", "hub": n, "pending": p}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

/* ---------- /hub：家侧长连 ---------- */

var wsUpgrader = ws.HTTPUpgrader{Timeout: relayRegWindow}

func (h *relayHub) handleHub(w http.ResponseWriter, r *http.Request) {
	conn, br, _, err := wsUpgrader.Upgrade(r, w)
	if err != nil {
		return // 升级失败已由 upgrader 回了 4xx
	}
	// 握手后 bufio 里可能已有预读字节（客户端注册帧来得快）：Source 必须带上
	var src io.Reader = conn
	if br != nil {
		src = br
	}
	hc := &hubConn{hub: h, conn: conn}
	hc.alive.Store(true)
	_ = conn.SetReadDeadline(time.Now().Add(relayRegWindow)) // 注册窗口：15s 内不注册即断
	defer func() {
		h.dropped(hc)
		_ = conn.Close()
	}()

	rd := &wsutil.Reader{
		Source: src,
		State:  ws.StateServerSide,
		OnIntermediate: func(hdr ws.Header, r io.Reader) error {
			if hdr.OpCode == ws.OpPing || hdr.OpCode == ws.OpPong {
				hc.alive.Store(true)
			}
			if hdr.OpCode == ws.OpPing {
				return hc.writeRaw(ws.OpPong, nil)
			}
			return nil
		},
	}
	// 读一帧控制通道 JSON 文本（沿途的控制帧处理掉：ping 回 pong）；
	// 返回 nil 表示连接已断/超时/协议错位。
	readText := func(v any) bool {
		for {
			hdr, err := rd.NextFrame()
			if err != nil {
				return false
			}
			if hdr.OpCode == ws.OpClose {
				return false
			}
			if hdr.OpCode != ws.OpText {
				// 夹在消息间的控制帧：消费帧体再继续，防下一帧头从载荷中间读起
				payload, err := io.ReadAll(io.LimitReader(rd, 125))
				if err != nil {
					return false
				}
				if hdr.OpCode == ws.OpPing {
					hc.alive.Store(true)
					_ = hc.writeRaw(ws.OpPong, payload)
				}
				if hdr.OpCode == ws.OpPong {
					hc.alive.Store(true)
				}
				continue
			}
			data, err := io.ReadAll(io.LimitReader(rd, 1<<20))
			if err != nil || json.Unmarshal(data, v) != nil {
				return false
			}
			return true
		}
	}

	// 注册：首帧必须是 {t:'register',id,key} 且 key 相符（server.js：否则直接断）
	var reg struct {
		T   string `json:"t"`
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if !readText(&reg) || reg.T != "register" || reg.ID == "" || reg.Key != h.peerKey {
		return
	}
	hc.id = reg.ID
	_ = conn.SetReadDeadline(time.Time{}) // 注册完成，交给 ping 保活
	h.register(hc)
	log.Printf("[中继] + %s", reg.ID)
	defer log.Printf("[中继] - %s", reg.ID)
	if hc.writeJSON(map[string]any{"t": "regok"}) != nil {
		return
	}

	// 消息循环：res/done 路由到挂起中继；重复 register 帧忽略（对齐 server.js）
	for {
		var f struct {
			T   string `json:"t"`
			Rid string `json:"rid"`
			Iv  string `json:"iv"`
			Ct  string `json:"ct"`
		}
		if !readText(&f) || f.T == "" {
			return
		}
		switch f.T {
		case "res":
			if f.Rid != "" && f.Iv != "" && f.Ct != "" {
				if p := h.pendingOf(f.Rid); p != nil {
					p.deliver(hubLine{iv: f.Iv, ct: f.Ct})
				}
			}
		case "done":
			if f.Rid != "" {
				if p := h.take(f.Rid); p != nil {
					p.deliver(hubLine{done: true})
				}
			}
		}
	}
}

// register 登记 conn；同设备号旧连接立即断开（新顶旧）。
func (h *relayHub) register(hc *hubConn) {
	h.mu.Lock()
	old := h.conns[hc.id]
	h.conns[hc.id] = hc
	h.mu.Unlock()
	if old != nil && old != hc {
		_ = old.conn.Close() // terminate 语义：不等关闭握手
	}
}

// dropped 连接退出时的收口：从连接表摘除（仅当还是当前持有人），
// 该设备全部挂起中继立即以 offline 失败（新顶旧时旧连接退出不动新连接）。
func (h *relayHub) dropped(hc *hubConn) {
	if hc.id == "" {
		return // 未完成注册的连接：连接表里没有它
	}
	h.mu.Lock()
	if h.conns[hc.id] == hc {
		delete(h.conns, hc.id)
	}
	stale := []*relayPending{}
	for _, p := range h.pending {
		if p.peerID == hc.id {
			stale = append(stale, p)
		}
	}
	h.mu.Unlock()
	for _, p := range stale {
		p.deliver(hubLine{offline: true})
	}
}

// pingLoop 周期保活：每轮对全部连接发 ping；上一轮没收到任何 pong 的判死链断开。
func (h *relayHub) pingLoop() {
	for range time.Tick(relayPingEvery) {
		h.mu.Lock()
		conns := make([]*hubConn, 0, len(h.conns))
		for _, hc := range h.conns {
			conns = append(conns, hc)
		}
		h.mu.Unlock()
		for _, hc := range conns {
			if !hc.alive.Swap(false) {
				_ = hc.conn.Close()
				continue
			}
			_ = hc.writeRaw(ws.OpPing, nil)
		}
	}
}

func (hc *hubConn) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	hc.wmu.Lock()
	defer hc.wmu.Unlock()
	return wsutil.WriteServerMessage(hc.conn, ws.OpText, data)
}

func (hc *hubConn) writeRaw(op ws.OpCode, data []byte) error {
	hc.wmu.Lock()
	defer hc.wmu.Unlock()
	return wsutil.WriteServerMessage(hc.conn, op, data)
}

/* ---------- /relay：手机侧加密中继 ---------- */

func (h *relayHub) connOf(peerID string) *hubConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[peerID]
}

func (h *relayHub) pendingOf(rid string) *relayPending {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pending[rid]
}

// take 取走并注销一条挂起（done 路径）：归还 IP 并发额度。
func (h *relayHub) take(rid string) *relayPending {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.pending[rid]
	if p == nil {
		return nil
	}
	delete(h.pending, rid)
	h.releaseIPLocked(p.ip)
	return p
}

func (h *relayHub) releaseIPLocked(ip string) {
	if n := h.perIP[ip]; n <= 1 {
		delete(h.perIP, ip)
	} else {
		h.perIP[ip] = n - 1
	}
}

func (h *relayHub) nextRid() string {
	n := atomic.AddInt64(&h.seq, 1)
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	return fmt.Sprintf("L%s%s%x",
		strconv.FormatInt(time.Now().UnixMilli(), 36),
		strconv.FormatInt(n, 36), rnd)
}

// deliver 把一条事件递给等待中的 HTTP 协程；请求已收口则丢弃。
func (p *relayPending) deliver(l hubLine) {
	select {
	case p.ch <- l:
	case <-p.done:
	}
}

// finish 收口一条挂起（幂等）：注销、归还并发额度、通知家里 abort（连接还在时）。
func (h *relayHub) finish(p *relayPending) {
	p.closeOnce.Do(func() {
		close(p.done)
		h.mu.Lock()
		if h.pending[p.rid] == p {
			delete(h.pending, p.rid)
			h.releaseIPLocked(p.ip)
		}
		peer := h.conns[p.peerID]
		h.mu.Unlock()
		if peer != nil { // 家里还在跑的长请求随 abort 销毁；连接已死则自然丢弃
			_ = peer.writeJSON(map[string]any{"t": "abort", "rid": p.rid})
		}
	})
}

func (h *relayHub) handleRelay(w http.ResponseWriter, r *http.Request) {
	peerID := r.Header.Get("X-Relay-Peer")
	hc := h.connOf(peerID)
	if hc == nil {
		writeErrJSON(w, 503, "目标设备不在线（中继通道未连接）")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBody+1))
	if err != nil {
		writeErrJSON(w, 400, "读取请求体失败")
		return
	}
	if int64(len(body)) > h.maxBody {
		writeErrJSON(w, 413, "中继请求体过大")
		return
	}
	var pack struct {
		Iv string `json:"iv"`
		Ct string `json:"ct"`
	}
	if json.Unmarshal(body, &pack) != nil || pack.Iv == "" || pack.Ct == "" {
		writeErrJSON(w, 400, "bad relay pack")
		return
	}
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	h.mu.Lock()
	if h.perIP[ip] >= h.perIPLim {
		h.mu.Unlock()
		writeErrJSON(w, 429, "中继并发过多，请稍后重试")
		return
	}
	h.perIP[ip]++
	h.mu.Unlock()

	p := &relayPending{rid: h.nextRid(), peerID: peerID, ip: ip, ch: make(chan hubLine, 128), done: make(chan struct{})}
	h.mu.Lock()
	h.pending[p.rid] = p
	h.mu.Unlock()
	if hc.writeJSON(map[string]any{"t": "relay", "rid": p.rid, "iv": pack.Iv, "ct": pack.Ct}) != nil {
		h.finish(p)
		writeErrJSON(w, 503, "中继offline")
		return
	}

	headersSent := false
	sendHeaders := func() {
		headersSent = true
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
	}
	timer := time.NewTimer(h.timeout)
	defer timer.Stop()
	for {
		select {
		case line := <-p.ch:
			switch {
			case line.done:
				if !headersSent {
					// 家里没回任何 res 就 done：密钥错误/目标异常的形态
					writeErrJSON(w, 502, "中继无响应（密钥错误或目标异常）")
				}
				return
			case line.offline:
				h.finish(p)
				if !headersSent {
					writeErrJSON(w, 503, "中继offline")
				}
				return
			default:
				if !headersSent {
					sendHeaders()
				}
				b, _ := json.Marshal(map[string]string{"iv": line.iv, "ct": line.ct})
				if _, err := w.Write(append(b, '\n')); err != nil {
					h.finish(p) // 浏览器提前断开
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		case <-timer.C:
			h.finish(p)
			if !headersSent {
				writeErrJSON(w, 504, "中继timeout")
			}
			return
		case <-r.Context().Done():
			h.finish(p) // 页面关闭/网络切换
			return
		}
	}
}

/* ---------- 配对页（gate.html）：内嵌 + 强制中继注入 ---------- */

var (
	gatePageOnce sync.Once
	gatePage     []byte
	gatePageErr  error
)

// gatePageBuild 内嵌 gate.html（与 打包/gate.html 同源，改动需两处同步），
// 做两处替换：去掉 peerjs.min.js 引用、注入 _forceRelay —— 本网关不提供
// /peerjs 信令，直连快试只会白等 3 秒；gate 页语义本就是「直连失败自动回
// 中继」，强制中继不影响协议，只是跳过注定失败的尝试。
func gatePageBuild() ([]byte, error) {
	gatePageOnce.Do(func() {
		raw, err := gateFS.ReadFile("gate.html")
		if err != nil {
			gatePageErr = err
			return
		}
		s := string(raw)
		const peerTag = `<script src="/peerjs.min.js" defer></script>`
		if !strings.Contains(s, peerTag) {
			gatePageErr = fmt.Errorf("gate.html 缺少 peerjs 引用标签，注入逻辑需随页面上游更新")
			return
		}
		s = strings.Replace(s, peerTag, "", 1)
		s = strings.Replace(s, "<body>", "<body><script>window._forceRelay=true</script>", 1)
		gatePage = []byte(s)
	})
	return gatePage, gatePageErr
}

// handleGate 配对页路由：/、/index.html、/gate.html、/a/<设备号>（与 server.js
// 的正则等价）；其余路径 404。no-cache：配对页改动需即时生效。
func (h *relayHub) handleGate(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p != "/" && p != "/index.html" && p != "/gate.html" && !strings.HasPrefix(p, "/a/") {
		http.NotFound(w, r)
		return
	}
	page, err := gatePageBuild()
	if err != nil {
		http.Error(w, "gate.html "+err.Error(), 500)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}
