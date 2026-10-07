package main

// P2P 直连端到端测试：pion 同时扮演手机端（offerer + DataChannel 发帧），
// 走真实 ICE（回环 host 候选，无需外网）验证 offer→answer→auth→req→响应帧全链路，
// 以及鉴权失败、非 /api/ 路径拒绝两条防线。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// dcOfferer 测试侧的「手机端」：建 offer（等候选收集完）并返回 pc 与 DataChannel。
func dcOfferer(t *testing.T) (*webrtc.PeerConnection, *webrtc.DataChannel, string) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("offerer 创建失败: %v", err)
	}
	dc, err := pc.CreateDataChannel("jxapi", nil)
	if err != nil {
		t.Fatalf("DataChannel 创建失败: %v", err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("offer 创建失败: %v", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription 失败: %v", err)
	}
	select {
	case <-webrtc.GatheringCompletePromise(pc):
	case <-time.After(5 * time.Second):
		t.Fatal("offerer 候选收集超时")
	}
	return pc, dc, pc.LocalDescription().SDP
}

func TestWebRTCDirectLoopback(t *testing.T) {
	// 家端回环 HTTP：被 bridgeCore 桥接的目标
	mux := http.NewServeMux()
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"path":"` + r.URL.Path + `"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	port := 0
	if _, err := fmt.Sscanf(srv.URL, "http://127.0.0.1:%d", &port); err != nil {
		t.Fatalf("解析测试端口失败: %v", err)
	}
	app := &App{Cfg: &Config{RemoteAccess: RemoteAccessCfg{PairKey: "pk-test"}}, listenPort: port}

	dcDial := func() (*webrtc.PeerConnection, *webrtc.DataChannel, chan map[string]any) {
		pc, dc, offerSDP := dcOfferer(t)
		w := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"sdp": offerSDP})
		app.handleWebRTCOffer(w, httptest.NewRequest("POST", "/api/webrtc-offer", strings.NewReader(string(body))))
		if w.Code != 200 {
			t.Fatalf("webrtc-offer 失败: HTTP %d %s", w.Code, w.Body.String())
		}
		var resp struct {
			Answer string `json:"answer"`
		}
		if json.Unmarshal(w.Body.Bytes(), &resp) != nil || resp.Answer == "" {
			t.Fatalf("answer 缺失: %s", w.Body.String())
		}
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: resp.Answer,
		}); err != nil {
			t.Fatalf("SetRemoteDescription(answer) 失败: %v", err)
		}
		opened := make(chan struct{})
		dc.OnOpen(func() { close(opened) })
		select {
		case <-opened:
		case <-time.After(10 * time.Second):
			t.Fatal("DataChannel 未打开（10s）")
		}
		frames := make(chan map[string]any, 16)
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if !m.IsString {
				return
			}
			var f map[string]any
			if json.Unmarshal(m.Data, &f) == nil {
				frames <- f
			}
		})
		return pc, dc, frames
	}
	waitFrame := func(frames chan map[string]any, wantT string) map[string]any {
		t.Helper()
		for {
			select {
			case f := <-frames:
				if f["t"] == wantT {
					return f
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("等 %s 帧超时", wantT)
			}
		}
	}

	// ① 错误密钥 → authfail（会话被端侧关闭）
	pc1, dc1, frames1 := dcDial()
	_ = dc1.SendText(`{"t":"auth","pairKey":"wrong"}`)
	if f := waitFrame(frames1, "authfail"); f["t"] != "authfail" {
		t.Fatalf("错误密钥应回 authfail，得到 %v", f)
	}

	// ② 正常链路：auth → authok → 非 API 拒绝 → echo 请求 → head/chunk/end
	pc2, dc2, frames2 := dcDial()
	_ = dc2.SendText(`{"t":"auth","pairKey":"pk-test"}`)
	waitFrame(frames2, "authok")

	_ = dc2.SendText(`{"t":"req","id":"q1","method":"GET","path":"/etc/passwd","headers":{}}`)
	if f := waitFrame(frames2, "err"); !strings.Contains(fmt.Sprint(f["msg"]), "403") {
		t.Fatalf("非 API 路径应回 403 err，得到 %v", f)
	}

	_ = dc2.SendText(`{"t":"req","id":"q2","method":"GET","path":"/api/echo","headers":{}}`)
	if f := waitFrame(frames2, "head"); f["id"] != "q2" || f["status"] != float64(200) {
		t.Fatalf("head 不符: %v", f)
	}
	bodyB64 := ""
	for {
		f := waitFrame2(frames2, []string{"chunk", "end"})
		if f["t"] == "end" {
			break
		}
		bodyB64 += fmt.Sprint(f["data"])
	}
	body, err := base64.StdEncoding.DecodeString(bodyB64)
	if err != nil {
		t.Fatalf("chunk base64 解码失败: %v", err)
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("echo 响应内容不符: %q", string(body))
	}

	_ = pc1.Close()
	_ = pc2.Close()
}

func waitFrame2(frames chan map[string]any, want []string) map[string]any {
	for {
		select {
		case f := <-frames:
			for _, w := range want {
				if f["t"] == w {
					return f
				}
			}
		case <-time.After(5 * time.Second):
			panic("等帧超时: " + strings.Join(want, "/"))
		}
	}
}
