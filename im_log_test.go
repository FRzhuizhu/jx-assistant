package main

// IM 流水（im_log.go / im_messages 表）与面板只读接口的数据层测试：
// 落库与回填、截断、聊天汇总、未读与已读位标、备注持久化、入站/出站挂钩。
// 全部走真库（t.TempDir + NewStore），不联网、不起长轮询。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// waitIMStatus 等某条流水变成期望状态（worker 是异步回填的，最多等 2 秒）。
func waitIMStatus(t *testing.T, a *App, chat string, want string) imLogRow {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err := a.imLogMessages("weixin", chat, 0, 50)
		if err != nil {
			t.Fatalf("读流水失败: %v", err)
		}
		for _, r := range rows {
			if r.Direction == imDirOut && r.Status == want {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等了 2 秒没等到 status=%s 的出站流水，当前：%+v", want, rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIMLogRoundTripAndStatus(t *testing.T) {
	a := newIMTestApp(t)
	id := a.imLogAppend(imLogEntry{
		ChatID: "u1", Direction: imDirOut, Kind: imKindAnswer,
		Text: "第一条答复", Status: imStatusPending, SessionID: "s1", Seq: 7,
	})
	if id <= 0 {
		t.Fatalf("落库应返回行 id，得到 %d", id)
	}
	a.imLogUpdate(id, imStatusFailed, "平台限流 45009")

	rows, err := a.imLogMessages("weixin", "u1", 0, 10)
	if err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应只有 1 条，得到 %d", len(rows))
	}
	r := rows[0]
	if r.Status != imStatusFailed || r.Error != "平台限流 45009" {
		t.Fatalf("状态/失败原因没回填：%+v", r)
	}
	if r.Seq != 7 || r.Kind != imKindAnswer || r.Direction != imDirOut || r.Text != "第一条答复" {
		t.Fatalf("字段不对：%+v", r)
	}
	if r.At == "" {
		t.Fatalf("时间应自动填上：%+v", r)
	}
}

func TestIMLogTruncatesBody(t *testing.T) {
	a := newIMTestApp(t)
	long := strings.Repeat("长", imLogMaxChars+500)
	a.imLogAppend(imLogEntry{ChatID: "u1", Direction: imDirOut, Kind: imKindAnswer, Text: long})
	rows, _ := a.imLogMessages("weixin", "u1", 0, 10)
	if len(rows) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(rows))
	}
	if n := len([]rune(rows[0].Text)); n > imLogMaxChars+20 {
		t.Fatalf("正文应被截到约 %d 字，实际 %d 字", imLogMaxChars, n)
	}
	if !strings.HasSuffix(rows[0].Text, "（已截断）") {
		t.Fatalf("截断后应有提示，实际结尾：%q", rows[0].Text[len(rows[0].Text)-20:])
	}
}

func TestIMChatSummaryAndPaging(t *testing.T) {
	a := newIMTestApp(t)
	// u1：一收一发（发失败），u2：一收
	a.imLogAppend(imLogEntry{ChatID: "u1", Direction: imDirIn, Kind: "text", Text: "你好", Status: imStatusAccepted})
	idOut := a.imLogAppend(imLogEntry{ChatID: "u1", Direction: imDirOut, Kind: imKindAnswer, Text: "在的", Status: imStatusPending})
	a.imLogUpdate(idOut, imStatusFailed, "平台限流")
	a.imLogAppend(imLogEntry{ChatID: "u2", Direction: imDirIn, Kind: "text", Text: "另一个聊天", Status: imStatusAccepted})

	stats := a.imLogChats()
	if len(stats) != 2 {
		t.Fatalf("应有 2 个聊天，得到 %d：%+v", len(stats), stats)
	}
	byChat := map[string]imChatStat{}
	for _, s := range stats {
		byChat[s.ChatID] = s
	}
	u1 := byChat["u1"]
	if u1.Inbound != 1 || u1.Outbound != 1 || u1.Failed != 1 {
		t.Fatalf("u1 计数不对：%+v", u1)
	}
	if u1.LastText != "在的" || u1.LastDir != imDirOut {
		t.Fatalf("u1 最后一条摘要不对：%+v", u1)
	}
	if u1.LastInID <= 0 {
		t.Fatalf("u1 应记下最后一条入站 id（算未读用）：%+v", u1)
	}
	if byChat["u2"].Failed != 0 {
		t.Fatalf("u2 不该有失败：%+v", byChat["u2"])
	}

	// 分页：before 取更早的一页
	rows, _ := a.imLogMessages("weixin", "u1", 0, 1)
	if len(rows) != 1 || rows[0].Text != "在的" {
		t.Fatalf("limit=1 应取最新一条：%+v", rows)
	}
	older, _ := a.imLogMessages("weixin", "u1", rows[0].ID, 10)
	if len(older) != 1 || older[0].Text != "你好" {
		t.Fatalf("before 应取到更早那条：%+v", older)
	}
}

func TestIMUnreadAndMarkRead(t *testing.T) {
	a := newIMTestApp(t)
	a.imBindChat("weixin", "u1")
	a.imLogAppend(imLogEntry{ChatID: "u1", Direction: imDirIn, Kind: "text", Text: "一", Status: imStatusAccepted})
	second := a.imLogAppend(imLogEntry{ChatID: "u1", Direction: imDirIn, Kind: "text", Text: "二", Status: imStatusAccepted})
	if n := a.imLogUnread("weixin", "u1", 0); n != 2 {
		t.Fatalf("未读数应为 2，得到 %d", n)
	}
	a.imMarkRead("weixin", "u1", second)
	if n := a.imLogUnread("weixin", "u1", second); n != 0 {
		t.Fatalf("标记已读后应为 0，得到 %d", n)
	}
	// 已读位标要落盘（换实例读回）
	a2 := &App{Cfg: a.Cfg}
	for _, b := range a2.imBindingList() {
		if b.ChatID == "u1" && b.ReadUpTo != second {
			t.Fatalf("已读位标没落盘：%+v", b)
		}
	}
}

func TestIMRemarkPersists(t *testing.T) {
	a := newIMTestApp(t)
	a.imBindChat("weixin", "u1")
	if _, ok := a.imSetRemark("weixin", "u1", "产品·王工"); !ok {
		t.Fatalf("应能找到刚绑定的聊天")
	}
	a2 := &App{Cfg: a.Cfg}
	found := false
	for _, b := range a2.imBindingList() {
		if b.ChatID == "u1" {
			found = true
			if b.Remark != "产品·王工" {
				t.Fatalf("备注没落盘：%+v", b)
			}
		}
	}
	if !found {
		t.Fatalf("新实例读不到绑定表")
	}
	// 不存在的聊天：不静默新建（备注只能挂在真实聊天上）
	if _, ok := a.imSetRemark("weixin", "nobody", "x"); ok {
		t.Fatalf("不存在的聊天不该能设备注")
	}
}

func TestIMInboundWritesLog(t *testing.T) {
	// 名单外账号：拒收，但要留一条「in / rejected」流水——「发了没反应」全靠它回看。
	// （空白名单不适用：那会走 im_allow.go 的兜底信任，第一条消息反而被受理）
	a := newIMTestApp(t)
	a.Cfg.IM.Weixin.Allow = []string{"owner-1"}
	a.imOnInbound(IMInbound{
		Conv:      IMConversation{Channel: "weixin", ChatID: "u9", ChatType: "dm", UserID: "u9"},
		MessageID: "m1", Kind: "text", Text: "帮我看看磁盘",
	})
	rows, _ := a.imLogMessages("weixin", "u9", 0, 10)
	var in *imLogRow
	for i := range rows {
		if rows[i].Direction == imDirIn {
			in = &rows[i]
		}
	}
	if in == nil {
		t.Fatalf("入站应记一条流水：%+v", rows)
	}
	if in.Status != imStatusRejected || in.Error != "不在白名单" {
		t.Fatalf("拒收的结局不对：%+v", *in)
	}
	if in.Text != "帮我看看磁盘" {
		t.Fatalf("正文应原样留存：%+v", *in)
	}

	// 白名单内的命令：status=command
	b := newIMTestApp(t)
	b.Cfg.IM.Enabled = true
	b.Cfg.IM.Weixin.Allow = []string{"u1"}
	b.imOnInbound(IMInbound{
		Conv:      IMConversation{Channel: "weixin", ChatID: "u1", ChatType: "dm", UserID: "u1"},
		MessageID: "m2", Kind: "text", Text: "/help",
	})
	rows2, _ := b.imLogMessages("weixin", "u1", 0, 10)
	if len(rows2) == 0 || rows2[0].Status != imStatusCommand {
		t.Fatalf("命令应记 command：%+v", rows2)
	}
}

func TestIMOutboundWritesLog(t *testing.T) {
	a := newIMTestApp(t)
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	a.imSay(IMConversation{Channel: "weixin", ChatID: "u1", ChatType: "dm", UserID: "u1"}, "回执一句")
	fake.wait(t, 1)
	r := waitIMStatus(t, a, "u1", imStatusSent)
	if r.Kind != imKindNotice {
		t.Fatalf("imSay 的类别应是 notice：%+v", r)
	}
	if r.Text != "回执一句" {
		t.Fatalf("正文不对：%+v", r)
	}
}

func TestIMSkippedCountsAsFailure(t *testing.T) {
	// 桌面在线静默跳过：不算「发出去」，面板要能一眼看出（计入 failed 计数）
	a := newIMTestApp(t)
	a.imLogAppend(imLogEntry{
		ChatID: "u1", Direction: imDirOut, Kind: imKindAnswer, Text: "没推的答复",
		Status: imStatusSkipped, Error: "桌面端在线，按「静默」设置未推送",
	})
	stats := a.imLogChats()
	if len(stats) != 1 || stats[0].Failed != 1 {
		t.Fatalf("静默跳过应计入失败数：%+v", stats)
	}
}

// TestIMSessionMarks 钉住 C 的数据来源：按会话查 IM 流水（入站补 session_id 后
// 也能查到），且只回 seq/方向/状态，不回正文。
func TestIMSessionMarks(t *testing.T) {
	a := newIMTestApp(t)
	a.imBindChat("weixin", "u1")
	a.imSetRemark("weixin", "u1", "产品·王工")
	inID := a.imLogAppend(imLogEntry{
		ChatID: "u1", Direction: imDirIn, Kind: "text", Text: "在吗", Status: imStatusAccepted,
	})
	a.imLogSetSession(inID, "s1") // 入站落库时还不知道投给谁，拿到会话后回填
	a.imLogAppend(imLogEntry{
		ChatID: "u1", Direction: imDirOut, Kind: imKindAnswer, Text: "在", Status: imStatusSent,
		SessionID: "s1", Seq: 12,
	})
	rows, err := a.imLogBySession("s1", 0)
	if err != nil {
		t.Fatalf("按会话读流水失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有 2 条（一收一发），得到 %d：%+v", len(rows), rows)
	}
	if rows[0].Direction != imDirIn || rows[0].Seq != 0 {
		t.Fatalf("入站那条的字段不对：%+v", rows[0])
	}
	if rows[1].Direction != imDirOut || rows[1].Seq != 12 || rows[1].Status != imStatusSent {
		t.Fatalf("出站那条的字段不对：%+v", rows[1])
	}
	// 别的会话查不到
	if other, _ := a.imLogBySession("s2", 0); len(other) != 0 {
		t.Fatalf("别的会话不该有流水：%+v", other)
	}
}

// TestIMRequireUIAction 是「界面动作闸」本身：remark/read 与扫码/解绑/重连/测试
// 五个写接口共用它。（名字带 TestIM 前缀，才会被 `go test -run TestIM` 的回归
// 一起跑到——漏了它等于漏了整道闸。）
func TestIMRequireUIAction(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		// 空 Origin = curl / AI 命令行；跨域 = 本机恶意网页的 CSRF 面；同源 = 界面
		{"", false},
		{"http://evil.example", false},
		{"http://example.com", true}, // httptest.NewRequest 的默认 Host
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/im/x", strings.NewReader("{}"))
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		if got := requireUIAction(w, req); got != c.want {
			t.Fatalf("origin=%q 期望 %v，得到 %v", c.origin, c.want, got)
		}
		if !c.want && w.Code != http.StatusForbidden {
			t.Fatalf("origin=%q 应回 403，得到 %d", c.origin, w.Code)
		}
	}
}

// TestIMWeixinWriteRoutesRequireUI 钉住扫码/解绑/重连/测试这四个裸路由也过闸：
// 它们不登记 apiMeta（AI 目录里看不到），但 withCORS 放行无 Origin 的 curl，
// 所以 handler 自己必须挡。这里只测「无 Origin → 403」这一侧——闸是第一句，
// 不会触发任何副作用（不生成二维码、不写凭据、不发消息）。
func TestIMWeixinWriteRoutesRequireUI(t *testing.T) {
	a := newIMTestApp(t)
	routes := []struct {
		name string
		h    http.HandlerFunc
	}{
		{"qr", a.handleIMWeixinQr},
		{"logout", a.handleIMWeixinLogout},
		{"reconnect", a.handleIMWeixinReconnect},
		{"test", a.handleIMWeixinTest},
	}
	for _, rt := range routes {
		req := httptest.NewRequest(http.MethodPost, "/api/im/weixin/"+rt.name, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		rt.h(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s：无 Origin 应 403，得到 %d（body=%s）", rt.name, w.Code, w.Body.String())
		}
	}
	// 扫码**轮询**是 GET，浏览器同源 GET 不带 Origin——刻意不加闸（加了扫码进度
	// 会停更）。它的边界由 TestIMWeixinQrPollNotGated 单独钉住。
}

// TestIMWeixinQrPollNotGated 钉住「扫码轮询不加 Origin 闸」这条边界：
// 界面每 1.5 秒 GET 一次进度，浏览器对同源 GET **不带** Origin；一旦给这条路由
// 也套 requireUIAction，扫码进度会被 403 打死（二维码永远停在「等待扫码」）。
// 这里没有进行中的扫码，期望 404（找不到扫码会话）——关键是**不能是 403**。
func TestIMWeixinQrPollNotGated(t *testing.T) {
	a := newIMTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/im/weixin/qr?id=x", nil) // 刻意不带 Origin
	w := httptest.NewRecorder()
	a.handleIMWeixinQrPoll(w, req)
	if w.Code == http.StatusForbidden {
		t.Fatalf("扫码轮询被 Origin 闸拦住了（界面轮询会 403、扫码进度停更）")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("没有进行中的扫码应回 404，得到 %d：%s", w.Code, w.Body.String())
	}
}

// TestIMUIOnlyEndpointsRejectNonBrowser 钉住「备注/已读只能在界面里点」这道闸：
// 不登记 apiMeta 只挡 call_api，挡不住 AI 用 run_command 发 curl（withCORS 放行
// 无 Origin 的非浏览器客户端），所以 handler 自己要求同源 Origin。
func TestIMUIOnlyEndpointsRejectNonBrowser(t *testing.T) {
	a := newIMTestApp(t)
	a.imBindChat("weixin", "u1")

	post := func(h http.HandlerFunc, body, origin string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/im/x", strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h(w, req)
		return w.Code
	}
	const sameOriginHdr = "http://example.com" // httptest.NewRequest 的默认 Host

	remarkBody := `{"chat":"u1","remark":"产品·王工"}`
	if code := post(a.handleIMRemark, remarkBody, ""); code != http.StatusForbidden {
		t.Fatalf("无 Origin（curl/AI 命令行）应 403，得到 %d", code)
	}
	if code := post(a.handleIMRemark, remarkBody, "http://evil.example"); code != http.StatusForbidden {
		t.Fatalf("跨域 Origin 应 403，得到 %d", code)
	}
	if code := post(a.handleIMRemark, remarkBody, sameOriginHdr); code != http.StatusOK {
		t.Fatalf("同源 Origin 应 200，得到 %d", code)
	}
	if b, _ := a.imSetRemark("weixin", "u1", ""); b.Remark != "" {
		t.Fatalf("备注应已写入并能在清空后读回：%+v", b)
	}

	readBody := `{"chat":"u1","upTo":1}`
	if code := post(a.handleIMRead, readBody, ""); code != http.StatusForbidden {
		t.Fatalf("无 Origin 的已读请求应 403，得到 %d", code)
	}
	if code := post(a.handleIMRead, readBody, sameOriginHdr); code != http.StatusOK {
		t.Fatalf("同源已读请求应 200，得到 %d", code)
	}
}

// TestIMReadOnlyRoutesInCatalog 钉住「只读接口对 AI 可见、写接口不可见」：
// 目录是 call_api 的唯一入口，登记漏了等于接口对 AI 隐身。
func TestIMReadOnlyRoutesInCatalog(t *testing.T) {
	a := newIMTestApp(t)
	_ = a.apiMuxOf() // 触发一轮路由注册
	a.apiMetaMu.Lock()
	defer a.apiMetaMu.Unlock()
	for _, p := range []string{"GET /api/im/chats", "GET /api/im/messages"} {
		if _, ok := a.apiMetaByPattern[p]; !ok {
			t.Fatalf("%s 应在 AI 接口目录里（只读，permReadOnly）", p)
		}
	}
	for _, p := range []string{"POST /api/im/remark", "POST /api/im/read", "POST /api/im/weixin/qr"} {
		if _, ok := a.apiMetaByPattern[p]; ok {
			t.Fatalf("%s 不该登记给 AI（界面动作/需人扫）", p)
		}
	}
}
