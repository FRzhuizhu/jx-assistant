// 中继协议单测：以「家侧客户端」的身份拨 /hub、以「手机侧」的身份 POST /relay，
// 逐条验证与 deploy-210 网关 server.js 对齐的语义。
// 帧编解码用 gobwas 客户端模式——与主程序 remoteaccess.go 同一条路。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// newTestServer 起一个完整路由的 httptest 服务。
func newTestServer(t *testing.T, hub *relayHub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(buildMux(hub, newMarketStore(t.TempDir(), "测试市场")))
	t.Cleanup(srv.Close)
	return srv
}

func newTestHub() *relayHub {
	h := newRelayHub("test_key")
	h.timeout = 500 * time.Millisecond // 测试不等 150s
	return h
}

// hubClient 一个测试用家侧客户端：拨 /hub、注册、读文本帧。
type hubClient struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialHub(t *testing.T, srv *httptest.Server, id, key string) *hubClient {
	t.Helper()
	conn, br, _, err := (&ws.Dialer{}).Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/hub")
	if err != nil {
		t.Fatalf("拨 /hub 失败: %v", err)
	}
	c := &hubClient{conn: conn, br: br}
	c.write(map[string]any{"t": "register", "id": id, "key": key})
	return c
}

func (c *hubClient) write(v any) {
	data, _ := json.Marshal(v)
	if err := wsutil.WriteClientMessage(c.conn, ws.OpText, data); err != nil {
		tLogf("写帧失败: %v", err)
	}
}

func (c *hubClient) readText(t *testing.T) map[string]any {
	t.Helper()
	rd := &wsutil.Reader{
		Source: readerOf(c),
		State:  ws.StateClientSide,
		OnIntermediate: func(hdr ws.Header, r io.Reader) error {
			if hdr.OpCode == ws.OpPing { // 回 pong（对齐真实客户端）
				payload, _ := io.ReadAll(io.LimitReader(r, 125))
				return wsutil.WriteClientMessage(c.conn, ws.OpPong, payload)
			}
			return nil
		},
	}
	for {
		hdr, err := rd.NextFrame()
		if err != nil {
			t.Fatalf("读帧失败: %v", err)
		}
		if hdr.OpCode == ws.OpClose {
			t.Fatal("服务端关闭了连接")
		}
		if hdr.OpCode != ws.OpText {
			_, _ = io.Copy(io.Discard, io.LimitReader(rd, 125))
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rd, 1<<20))
		if err != nil {
			t.Fatalf("读帧体失败: %v", err)
		}
		var f map[string]any
		if json.Unmarshal(data, &f) != nil {
			t.Fatalf("帧不是合法 JSON: %s", data)
		}
		return f
	}
}

func readerOf(c *hubClient) io.Reader {
	if c.br != nil {
		return c.br
	}
	return c.conn
}

func tLogf(f string, v ...any) { fmt.Printf("[test] "+f+"\n", v...) }

// postRelay 手机侧发起一次中继请求。
func postRelay(t *testing.T, srv *httptest.Server, peer string, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/relay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Peer", peer)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /relay 失败: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(data)
}

func relayPackJSON(iv, ct string) string {
	b, _ := json.Marshal(map[string]string{"iv": iv, "ct": ct})
	return string(b)
}

/* ---------- 用例 ---------- */

// 完整往返：注册 → regok → 手机 POST → 家收到 relay 帧 → 回 res×2 + done →
// 手机收到 200 + NDJSON 两行。
func TestRelayRoundtrip(t *testing.T) {
	hub := newTestHub()
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-test", "test_key")
	defer hc.conn.Close()

	regok := hc.readText(t)
	if regok["t"] != "regok" {
		t.Fatalf("期待 regok，收到 %#v", regok)
	}

	done := make(chan string, 1)
	go func() {
		resp, body := postRelay(t, srv, "jx-test", relayPackJSON("IV1", "CT1"))
		done <- fmt.Sprintf("%d|%s|%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}()

	relayFrame := hc.readText(t)
	if relayFrame["t"] != "relay" {
		t.Fatalf("期待 relay 帧，收到 %#v", relayFrame)
	}
	rid, _ := relayFrame["rid"].(string)
	if rid == "" || relayFrame["iv"] != "IV1" || relayFrame["ct"] != "CT1" {
		t.Fatalf("relay 帧字段不对: %#v", relayFrame)
	}
	hc.write(map[string]any{"t": "res", "rid": rid, "iv": "RIV1", "ct": "RCT1"})
	hc.write(map[string]any{"t": "res", "rid": rid, "iv": "RIV2", "ct": "RCT2"})
	hc.write(map[string]any{"t": "done", "rid": rid})

	select {
	case got := <-done:
		parts := strings.SplitN(got, "|", 3)
		if parts[0] != "200" {
			t.Fatalf("期待 200，收到 %s（%s）", parts[0], got)
		}
		if !strings.Contains(parts[1], "application/x-ndjson") {
			t.Fatalf("Content-Type 应为 NDJSON: %q", parts[1])
		}
		lines := strings.Split(strings.TrimSpace(parts[2]), "\n")
		if len(lines) != 2 {
			t.Fatalf("期待 2 行 NDJSON，收到 %d 行: %q", len(lines), parts[2])
		}
		var l1, l2 map[string]string
		_ = json.Unmarshal([]byte(lines[0]), &l1)
		_ = json.Unmarshal([]byte(lines[1]), &l2)
		if l1["iv"] != "RIV1" || l1["ct"] != "RCT1" || l2["iv"] != "RIV2" {
			t.Fatalf("NDJSON 内容不对: %v %v", l1, l2)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("中继请求未返回")
	}
}

// 目标设备未注册：503。
func TestRelayOffline(t *testing.T) {
	srv := newTestServer(t, newTestHub())
	resp, body := postRelay(t, srv, "nobody", relayPackJSON("i", "c"))
	if resp.StatusCode != 503 {
		t.Fatalf("期待 503，收到 %d", resp.StatusCode)
	}
	if !strings.Contains(body, "不在线") {
		t.Fatalf("错误文案不对: %s", body)
	}
}

// 封包不合法：400 bad relay pack。
func TestRelayBadPack(t *testing.T) {
	hub := newTestHub()
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-test", "test_key")
	defer hc.conn.Close()
	hc.readText(t) // regok

	resp, body := postRelay(t, srv, "jx-test", `{"iv":""}`)
	if resp.StatusCode != 400 || !strings.Contains(body, "bad relay pack") {
		t.Fatalf("期待 400 bad relay pack，收到 %d %s", resp.StatusCode, body)
	}
}

// 注册 key 不符：连接被断、没有 regok。
func TestRelayWrongKey(t *testing.T) {
	hub := newTestHub()
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-bad", "wrong_key")
	defer hc.conn.Close()
	// 服务端直接断开：后续读帧应报错（而不是收到 regok）
	rd := &wsutil.Reader{Source: readerOf(hc), State: ws.StateClientSide}
	for {
		hdr, err := rd.NextFrame()
		if err != nil {
			return // 预期：连接被关
		}
		if hdr.OpCode == ws.OpText {
			data, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
			t.Fatalf("key 不符不该收到文本帧: %s", data)
		}
		if hdr.OpCode == ws.OpClose {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(rd, 125))
	}
}

// 超时无响应：504 + 家里收到 abort。
func TestRelayTimeout(t *testing.T) {
	hub := newTestHub() // timeout=500ms
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-test", "test_key")
	defer hc.conn.Close()
	hc.readText(t)

	done := make(chan int, 1)
	go func() {
		resp, _ := postRelay(t, srv, "jx-test", relayPackJSON("i", "c"))
		done <- resp.StatusCode
	}()
	f := hc.readText(t)
	if f["t"] != "relay" {
		t.Fatalf("期待 relay 帧: %#v", f)
	}
	// 不回任何 res → 超时
	if code := <-done; code != 504 {
		t.Fatalf("期待 504，收到 %d", code)
	}
	abort := hc.readText(t)
	if abort["t"] != "abort" || abort["rid"] != f["rid"] {
		t.Fatalf("期待 abort 帧: %#v", abort)
	}
}

// done 前没有任何 res：502（密钥错误/目标异常的形态）。
func TestRelayDoneWithoutRes(t *testing.T) {
	hub := newTestHub()
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-test", "test_key")
	defer hc.conn.Close()
	hc.readText(t)

	done := make(chan int, 1)
	go func() {
		resp, _ := postRelay(t, srv, "jx-test", relayPackJSON("i", "c"))
		done <- resp.StatusCode
	}()
	f := hc.readText(t)
	hc.write(map[string]any{"t": "done", "rid": f["rid"]})
	if code := <-done; code != 502 {
		t.Fatalf("期待 502，收到 %d", code)
	}
}

// 同设备号新顶旧：旧连接被立即断开。
func TestRelayNewBeatsOld(t *testing.T) {
	hub := newTestHub()
	srv := newTestServer(t, hub)
	old1 := dialHub(t, srv, "jx-dup", "test_key")
	defer old1.conn.Close()
	if f := old1.readText(t); f["t"] != "regok" {
		t.Fatalf("旧连接应拿到 regok: %#v", f)
	}
	newC := dialHub(t, srv, "jx-dup", "test_key")
	defer newC.conn.Close()
	if f := newC.readText(t); f["t"] != "regok" {
		t.Fatalf("新连接应拿到 regok: %#v", f)
	}
	// 旧连接读到 EOF/错误
	old1.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	if n, err := old1.conn.Read(buf); err == nil {
		t.Fatalf("旧连接应被断开，却读到 %d 字节", n)
	}
	// 新顶旧后家掉线：挂起请求拿 503 而不是挂死（由 dropped 覆盖）——这里只验证
	// 新连接仍是有效注册方：向它发中继还能收到 relay 帧。
	go func() { _, _ = postRelay(t, srv, "jx-dup", relayPackJSON("i", "c")) }()
	if f := newC.readText(t); f["t"] != "relay" {
		t.Fatalf("新连接应接管设备号: %#v", f)
	}
}

// 家掉线：挂起中的请求拿 503。
func TestRelayPeerDropFailsPending(t *testing.T) {
	hub := newTestHub()
	hub.timeout = 3 * time.Second // 别让超时抢先
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-drop", "test_key")
	hc.readText(t)

	done := make(chan int, 1)
	go func() {
		resp, _ := postRelay(t, srv, "jx-drop", relayPackJSON("i", "c"))
		done <- resp.StatusCode
	}()
	if f := hc.readText(t); f["t"] != "relay" {
		t.Fatalf("期待 relay 帧: %#v", f)
	}
	hc.conn.Close() // 家里断线
	select {
	case code := <-done:
		if code != 503 {
			t.Fatalf("期待 503，收到 %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("家里断线后挂起请求没有失败")
	}
}

// 手机提前断开：家里收到 abort。
func TestRelayClientGoneAbort(t *testing.T) {
	hub := newTestHub()
	hub.timeout = 3 * time.Second
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-gone", "test_key")
	defer hc.conn.Close()
	hc.readText(t)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/relay",
		strings.NewReader(relayPackJSON("i", "c")))
	req.Header.Set("X-Relay-Peer", "jx-gone")
	tr := &http.Transport{}
	started := make(chan struct{})
	respCh := make(chan *http.Response, 1)
	go func() {
		close(started)
		resp, err := tr.RoundTrip(req)
		if err == nil {
			respCh <- resp
		}
	}()
	<-started
	f := hc.readText(t)
	if f["t"] != "relay" {
		t.Fatalf("期待 relay 帧: %#v", f)
	}
	cancel()
	abort := hc.readText(t)
	if abort["t"] != "abort" {
		t.Fatalf("期待 abort 帧: %#v", abort)
	}
	select {
	case resp := <-respCh:
		resp.Body.Close()
	default:
	}
}

// 每 IP 并发限流：占满后新请求 429。
func TestRelayPerIPLimit(t *testing.T) {
	hub := newTestHub()
	hub.timeout = 3 * time.Second
	hub.perIPLim = 1
	srv := newTestServer(t, hub)
	hc := dialHub(t, srv, "jx-lim", "test_key")
	defer hc.conn.Close()
	hc.readText(t)

	// 第一个请求挂住不回（流式响应无头可回，Do 在协程里等到超时/断开）
	req1, _ := http.NewRequest("POST", srv.URL+"/relay", strings.NewReader(relayPackJSON("i", "c")))
	req1.Header.Set("X-Relay-Peer", "jx-lim")
	go func() {
		resp1, err := srv.Client().Do(req1)
		if err == nil {
			io.Copy(io.Discard, resp1.Body)
			resp1.Body.Close()
		}
	}()
	if f := hc.readText(t); f["t"] != "relay" {
		t.Fatalf("期待第一个 relay 帧: %#v", f)
	}
	// 第二个：同 IP 超限
	resp2, body := postRelay(t, srv, "jx-lim", relayPackJSON("i", "c"))
	if resp2.StatusCode != 429 {
		t.Fatalf("期待 429，收到 %d %s", resp2.StatusCode, body)
	}
}

// 配对页：注入了强制中继、去掉了 peerjs 引用；/a/<设备号> 与 / 都下发；其余 404。
func TestGatePage(t *testing.T) {
	srv := newTestServer(t, newTestHub())
	for _, p := range []string{"/", "/index.html", "/gate.html", "/a/jx-abc123"} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s := string(body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s 期待 200，收到 %d", p, resp.StatusCode)
		}
		if !strings.Contains(s, "window._forceRelay=true") {
			t.Fatalf("%s 缺少强制中继注入", p)
		}
		if strings.Contains(s, "peerjs.min.js") {
			t.Fatalf("%s 不应再引用 peerjs.min.js", p)
		}
		if !strings.Contains(s, "relayFetchShell") {
			t.Fatalf("%s 不是配对页本体", p)
		}
	}
	resp, _ := srv.Client().Get(srv.URL + "/unknown.js")
	if resp.StatusCode != 404 {
		t.Fatalf("未知路径期待 404，收到 %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// healthz 形状。
func TestHealthz(t *testing.T) {
	srv := newTestServer(t, newTestHub())
	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if m["ok"] != true || m["service"] != "jx-server" {
		t.Fatalf("healthz 形状不对: %v", m)
	}
}
