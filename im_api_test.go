package main

// IM 出站/状态相关的单元测试：/test 的 context_token 前置门、成功响应的诚实
// 口径、推送回执与 contextToken 在 /api/im/status 里的呈现。
// 不联网——协议联调在隔离实例上用 curl 跑。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestWeixinApp 造一个「已绑定、但还没有任何入站」的微信渠道：这正是真机上
// 点 /test 会踩坑的状态（没 context_token）。
func newTestWeixinApp(t *testing.T) (*App, *weixinChannel) {
	t.Helper()
	app := newIMTestApp(t)
	ch := newWeixinChannel(app)
	ch.account = &weixinAccount{AccountID: "a@im.bot", OwnerID: "owner@im.wechat", Token: "tok"}
	app.imSetChannel("weixin", ch)
	return app, ch
}

// ① 没有 context_token 时必须拒发（409 + 明确文案），不能假装 200。
func TestIMWeixinTestRejectsWithoutContextToken(t *testing.T) {
	app, _ := newTestWeixinApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/im/weixin/test", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://example.com") // 同源：这些写接口只放行界面点（requireUIAction）
	w := httptest.NewRecorder()
	app.handleIMWeixinTest(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("没有 context_token 应回 409，得到 %d：%s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "context_token") {
		t.Fatalf("拒绝文案要说明缺 context_token，得到：%s", body)
	}
}

// ① 有 context_token 时不拒绝（这里只验前置门放行：发送本身要联网，
// 断言落在「没有走到 409 那条拒绝路径」）。
func TestIMWeixinTestPassesGateWithContextToken(t *testing.T) {
	app, ch := newTestWeixinApp(t)
	ch.mu.Lock()
	ch.ctxTokens["owner@im.wechat"] = "ctx-token-abc"
	ch.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/api/im/weixin/test", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://example.com") // 同上：过 Origin 闸才谈得上「前置门放行」
	w := httptest.NewRecorder()
	app.handleIMWeixinTest(w, req)
	if w.Code == http.StatusConflict {
		t.Fatalf("已有 context_token 不该被拒：%s", w.Body.String())
	}
	if w.Code == http.StatusForbidden {
		t.Fatalf("同源请求不该被 Origin 闸拦住：%s", w.Body.String())
	}
}

// ② 成功响应必须把「已提交」与「平台已回执/已送达」分开写清楚。
func TestIMTestReplySemantics(t *testing.T) {
	r := imTestReply([]string{"jx-weixin-abc"})
	if r["ok"] != true || r["submitted"] != true {
		t.Fatalf("成功响应应有 ok/submitted=true：%#v", r)
	}
	if r["platformReceipt"] != false {
		t.Fatalf("平台不回真实消息 id，platformReceipt 必须是 false：%#v", r)
	}
	if r["contextToken"] != true {
		t.Fatalf("能发出去就说明有 context_token：%#v", r)
	}
	note, _ := r["note"].(string)
	for _, want := range []string{"client_id", "平台", "手机"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note 要说清口径（缺 %q）：%s", want, note)
		}
	}
	if ids, _ := r["messageIds"].([]string); len(ids) != 1 || ids[0] != "jx-weixin-abc" {
		t.Fatalf("messageIds 应原样带回：%#v", r["messageIds"])
	}
}

// ③ /api/im/status 要能直接看出「推送成功还是失败」和「有没有 context_token」。
func TestIMStatusPushAndContextToken(t *testing.T) {
	app, ch := newTestWeixinApp(t)
	st := app.imStatuses()[0]
	if st.ContextToken {
		t.Fatalf("还没收到任何入站，不该有 context_token：%+v", st)
	}
	if st.LastPush != nil {
		t.Fatalf("还没推过，LastPush 应为空：%+v", st.LastPush)
	}

	ch.mu.Lock()
	ch.ctxTokens["owner@im.wechat"] = "ctx-token-abc"
	ch.mu.Unlock()
	if !app.imStatuses()[0].ContextToken {
		t.Fatalf("入站带来 context_token 后，status 应显示 contextToken=true")
	}

	app.imNotePush("weixin", 7, false)
	st = app.imStatuses()[0]
	if st.LastPush == nil || st.LastPush.Seq != 7 || st.LastPush.OK {
		t.Fatalf("推送失败要记成 seq=7/ok=false：%+v", st.LastPush)
	}
	app.imNotePush("weixin", 8, true)
	st = app.imStatuses()[0]
	if st.LastPush == nil || st.LastPush.Seq != 8 || !st.LastPush.OK {
		t.Fatalf("推送成功要记成 seq=8/ok=true：%+v", st.LastPush)
	}
	if st.LastPush.At == "" {
		t.Fatalf("推送回执要带时间：%+v", st.LastPush)
	}
}
