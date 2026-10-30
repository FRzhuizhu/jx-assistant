package main

// bridgeCore 的流式回归测试：SSE 响应（Content-Type 带 charset 参数）绝不能
// 被套进 gzip——历史 bug 按全串精确比对排除 text/event-stream 失效，flate
// 攒着不吐，远程端整个输出期间收不到帧（「发送中」占位干等数秒、页面无实时
// 更新）；一次性 JSON 响应则应仍走 gzip 省流量。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// bridgePortFromURL 从 httptest 服务地址里取端口（bridgeCore 固定桥到
// 127.0.0.1:<port>）。
func bridgePortFromURL(t *testing.T, u string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(u, "http://"))
	if err != nil {
		t.Fatalf("解析测试服务地址失败: %v", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("端口不是数字: %v", err)
	}
	return n
}

// TestBridgeCoreSSEStreamsLive：SSE 响应（text/event-stream; charset=utf-8，
// 带 x-p2p-gzip 请求头）必须逐帧即时转发、head 不带 gz 标志。判定手法是确定性
// 的：服务端发完第一帧后阻塞等测试方确认「已收到第一帧的 chunk」再发第二帧——
// 若响应被 gzip 套住，第一帧永远到不了，测试在超时处失败，不会偶发。
func TestBridgeCoreSSEStreamsLive(t *testing.T) {
	proceed := make(chan struct{})
	var proceedOnce sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8") // 与 sseHeaders/handleEvents 完全一致
		w.Header().Set("Cache-Control", "no-cache")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"seq\":1}\n\n"))
		flusher.Flush()
		<-proceed // 等测试端确认第一帧已穿透桥接层，再发第二帧
		_, _ = w.Write([]byte("data: {\"seq\":2}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()
	// 任何出口（含 t.Fatal 的 Goexit）都要放开 proceed：服务端 handler 阻塞在它
	// 上面。defer 是 LIFO——本条晚于 ts.Close() 注册，因而先执行，收尾不挂死。
	defer func() { proceedOnce.Do(func() { close(proceed) }) }()

	var mu sync.Mutex
	var frames []map[string]any
	send := func(v map[string]any) bool {
		mu.Lock()
		frames = append(frames, v)
		mu.Unlock()
		return true
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		bridgeCore(context.Background(), ts.Client(), bridgePortFromURL(t, ts.URL), relayReqFrame{
			T:       "req",
			ID:      "s1",
			Method:  "GET",
			Path:    "/api/events",
			Headers: map[string]string{"x-p2p-gzip": "1"}, // 声明可解压：排除逻辑必须顶得住
		}, send)
	}()

	// 第一条事件必须在服务端发出第二帧之前到达
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		var head map[string]any
		firstSeen := false
		for _, f := range frames {
			if f["t"] == "head" && head == nil {
				head = f
			}
			if f["t"] == "chunk" {
				if d, _ := f["data"].(string); d != "" {
					if raw, err := base64.StdEncoding.DecodeString(d); err == nil &&
						strings.Contains(string(raw), `"seq":1`) {
						firstSeen = true
					}
				}
			}
		}
		mu.Unlock()
		if firstSeen {
			if head != nil {
				if gz, _ := head["gz"].(int); gz == 1 {
					proceedOnce.Do(func() { close(proceed) })
					<-done
					t.Fatal("SSE 响应的 head 带 gz=1：流式响应被套进 gzip，实时性会被 flate 缓冲破坏")
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("5 秒内未收到第一条 SSE 事件的 chunk：帧被压缩层扣住（未逐帧即时转发）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	proceedOnce.Do(func() { close(proceed) })
	<-done

	// 收尾校验：两帧 chunk 拼接后是完整明文 SSE 正文
	mu.Lock()
	defer mu.Unlock()
	var sb strings.Builder
	for _, f := range frames {
		if f["t"] != "chunk" {
			continue
		}
		d, _ := f["data"].(string)
		raw, err := base64.StdEncoding.DecodeString(d)
		if err != nil {
			t.Fatalf("chunk base64 解码失败: %v", err)
		}
		sb.Write(raw)
	}
	want := "data: {\"seq\":1}\n\ndata: {\"seq\":2}\n\n"
	if sb.String() != want {
		t.Fatalf("SSE 正文不完整: got %q want %q", sb.String(), want)
	}
}

// TestBridgeCoreJSONStillGzipped：一次性 JSON 响应（application/json; charset=utf-8）
// 仍应整体 gzip（head.gz=1），确保修复没有误伤省流量优化。
func TestBridgeCoreJSONStillGzipped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	var frames []map[string]any
	send := func(v map[string]any) bool {
		frames = append(frames, v)
		return true
	}
	bridgeCore(context.Background(), ts.Client(), bridgePortFromURL(t, ts.URL), relayReqFrame{
		T:       "req",
		ID:      "j1",
		Method:  "GET",
		Path:    "/api/x",
		Headers: map[string]string{"x-p2p-gzip": "1"},
	}, send)

	var head map[string]any
	var raw []byte
	for _, f := range frames {
		switch f["t"] {
		case "head":
			head = f
		case "chunk":
			d, _ := f["data"].(string)
			b, err := base64.StdEncoding.DecodeString(d)
			if err != nil {
				t.Fatalf("chunk base64 解码失败: %v", err)
			}
			raw = append(raw, b...)
		}
	}
	if head == nil {
		t.Fatal("没有收到 head 帧")
	}
	if gz, _ := head["gz"].(int); gz != 1 {
		t.Fatalf("一次性 JSON 响应应仍走 gzip（head.gz=1），got %v", head["gz"])
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("chunk 不是有效 gzip 流: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip 解压失败: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("解压后正文不符: %q", out)
	}
}

// TestBridgeCoreHeadLen：已知大小的响应，head 帧要透传总大小（len 字段）——
// 前端把它还原成 Response 的 Content-Length 头，远程下载的页面内进度条靠它
// 才知道总量（2026-10-30 交付下载体验改造的一部分）。流式响应（chunked、无
// Content-Length）则不下发 len。
func TestBridgeCoreHeadLen(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			// 先 Flush 强制 chunked：响应头里不会有 Content-Length
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("part1"))
			flusher.Flush()
			_, _ = w.Write([]byte("part2"))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "1234")
		_, _ = w.Write(make([]byte, 1234))
	}))
	defer ts.Close()

	run := func(path string) map[string]any {
		var frames []map[string]any
		send := func(v map[string]any) bool {
			frames = append(frames, v)
			return true
		}
		bridgeCore(context.Background(), ts.Client(), bridgePortFromURL(t, ts.URL), relayReqFrame{
			T: "req", ID: "l1", Method: "GET", Path: path,
		}, send)
		for _, f := range frames {
			if f["t"] == "head" {
				return f
			}
		}
		t.Fatalf("%s：没有收到 head 帧", path)
		return nil
	}

	if head := run("/file.bin"); head != nil {
		// len 可能为 int64（本测试直连 send 的原始 map）或 float64（经 JSON 往返）
		ln, _ := head["len"].(float64)
		if li, ok := head["len"].(int64); ok {
			ln = float64(li)
		}
		if int(ln) != 1234 {
			t.Fatalf("已知大小响应的 head.len 应为 1234，got %v（类型 %T）", head["len"], head["len"])
		}
	}
	if head := run("/chunked"); head != nil {
		if _, has := head["len"]; has {
			t.Fatalf("chunked 响应不应带 len 字段，got %v", head["len"])
		}
	}
}
