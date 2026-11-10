package main

// 出站「来源收口」与定时任务每日闸的单元测试（im_push.go）：
//   · 桌面端轮次（origin 空）→ 不产生 out 流水、不调用发送；
//   · 微信轮次（origin=im:weixin:<chatId>）→ 正常推送；
//   · 定时任务：带「【推送到微信】」标记 → 推；不带 → 不推；
//   · 同一天第 3 条带标记的推送被拦下并记日志；
//   · 跨天后计数重置。

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newIMPushApp 建一个「有数字经理、有绑定、IM 开着」的 App，出站交给假渠道接住。
func newIMPushApp(t *testing.T) (*App, *fakeIMChannel, *SessionMeta) {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := &App{Cfg: &Config{DataDir: dir}, Store: st, CfgPath: filepath.Join(dir, "config.json")}
	a.initRuntime()
	a.Cfg.IM.Enabled = true
	a.Cfg.IM.Weixin.Enabled = true
	mgr := &SessionMeta{ID: "mgr", Title: "数字经理", Mode: "assistant",
		Workspace: dir, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(mgr); err != nil {
		t.Fatalf("建数字经理会话失败: %v", err)
	}
	if err := a.setCurrentAssistant(mgr.ID); err != nil {
		t.Fatalf("落当前版本指针失败: %v", err)
	}
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	a.imBindChat("weixin", "u1")
	return a, fake, mgr
}

// waitSome 等一小会儿后返回已发出的快照：断言「没有多发」时必须等一个窗口
// （发件走串行队列，是异步的，读一次就下结论会假绿）。
func waitSome(f *fakeIMChannel, d time.Duration) []string {
	time.Sleep(d)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// imOutRows 取某个聊天方向的出站流水（断言「推没推、什么结局」）。
func imOutRows(t *testing.T, a *App, chatID string) []imLogRow {
	t.Helper()
	rows, _ := a.imLogMessages("weixin", chatID, 0, 50)
	out := make([]imLogRow, 0, len(rows))
	for _, r := range rows {
		if r.Direction == imDirOut {
			out = append(out, r)
		}
	}
	return out
}

// 桌面端触发的轮次：不推、不落 out 流水。
func TestIMOnEventDesktopRunNotPushed(t *testing.T) {
	a, fake, mgr := newIMPushApp(t)
	ev := Event{Type: "message", SessionID: mgr.ID, Seq: 1, Kind: kindChat,
		Message: &Message{Role: "assistant", Content: "桌面端问的，别推微信"}}
	a.imOnEvent(ev, "") // 桌面端发起：origin 为空
	if rows := imOutRows(t, a, "u1"); len(rows) != 0 {
		t.Fatalf("桌面端轮次不该产生出站流水：%+v", rows)
	}
	if got := waitSome(fake, 200*time.Millisecond); len(got) != 0 {
		t.Fatalf("桌面端轮次不该调用发送：%#v", got)
	}
}

// 微信触发的轮次：照常推送（且只推给问话的那个聊天）。
func TestIMOnEventWeixinRunPushed(t *testing.T) {
	a, fake, mgr := newIMPushApp(t)
	a.imBindChat("weixin", "u2") // 另一个已绑定聊天，不该收到 u1 的答复
	ev := Event{Type: "message", SessionID: mgr.ID, Seq: 1, Kind: kindChat,
		Message: &Message{Role: "assistant", Content: "微信问的，要推回去"}}
	a.imOnEvent(ev, imOriginTag("weixin", "u1"))
	got := fake.wait(t, 1)
	if len(got) != 1 || !strings.Contains(got[0], "微信问的") || !strings.HasPrefix(got[0], "u1\x00") {
		t.Fatalf("应只推给 u1：%#v", got)
	}
	if extra := waitSome(fake, 200*time.Millisecond); len(extra) != 1 {
		t.Fatalf("别的聊天不该收到别人的答复：%#v", extra)
	}
}

// 定时任务：带标记的推（且每日最多 2 条），不带标记的不推。
func TestIMOnEventTodoPushRespectsMarkAndCap(t *testing.T) {
	a, fake, mgr := newIMPushApp(t)
	mk := func(seq int, text string) Event {
		return Event{Type: "message", SessionID: mgr.ID, Seq: int64(seq), Kind: kindChat,
			Message: &Message{Role: "assistant", Content: text}}
	}
	// 不带标记的定时任务轮次（origin 为空）：不推
	a.imOnEvent(mk(1, "普通定时任务"), "")
	if got := waitSome(fake, 150*time.Millisecond); len(got) != 0 {
		t.Fatalf("没标记的定时任务不该推：%#v", got)
	}
	// 带标记：前两条推，第三条被每日上限拦下
	a.imOnEvent(mk(2, "定时汇报一"), imOriginTodoWeixin)
	a.imOnEvent(mk(3, "定时汇报二"), imOriginTodoWeixin)
	a.imOnEvent(mk(4, "定时汇报三"), imOriginTodoWeixin)
	got := fake.wait(t, 2)
	if len(got) != 2 || !strings.Contains(got[0], "定时汇报一") || !strings.Contains(got[1], "定时汇报二") {
		t.Fatalf("应只推前两条：%#v", got)
	}
	if extra := waitSome(fake, 300*time.Millisecond); len(extra) != 2 {
		t.Fatalf("第三条不该推出去：%#v", extra)
	}
	// 第三条在流水里留一条「跳过 + 原因」
	rows := imOutRows(t, a, "u1")
	var skipped *imLogRow
	for i := range rows {
		if rows[i].Status == imStatusSkipped && strings.Contains(rows[i].Error, "每日上限") {
			skipped = &rows[i]
		}
	}
	if skipped == nil {
		t.Fatalf("超限的那条应留一条跳过流水：%+v", rows)
	}
	if skipped.Text != "定时汇报三" {
		t.Fatalf("跳过的是第三条：%+v", *skipped)
	}
}

// 每日闸本身：同一天 2 条、第 3 条拒绝；跨天清零。
func TestIMTodoPushDailyReset(t *testing.T) {
	a, _, _ := newIMPushApp(t)
	d1 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local)
	if !a.imTodoPushAllowOn("u1", d1) || !a.imTodoPushAllowOn("u1", d1) {
		t.Fatalf("每天前两条应放行")
	}
	if a.imTodoPushAllowOn("u1", d1) {
		t.Fatalf("同一天第三条应被拦下")
	}
	// 另一个聊天各自计数
	if !a.imTodoPushAllowOn("u2", d1) {
		t.Fatalf("计数应按 chatId 分开")
	}
	d2 := d1.Add(24 * time.Hour)
	if !a.imTodoPushAllowOn("u1", d2) {
		t.Fatalf("跨天后应重置")
	}
	if a.imTodoPushAllowOn("u1", d2) && a.imTodoPushAllowOn("u1", d2) {
		t.Fatalf("新的一天同样只有 2 条")
	}
}

// 标记只认描述开头（别用「内容里出现某字符串」这种脆弱判断）。
func TestIMTodoPushWantedMark(t *testing.T) {
	if !imTodoPushWanted("【推送到微信】每天早上 8 点汇报磁盘") {
		t.Fatalf("开头带标记应识别")
	}
	if !imTodoPushWanted("  \n【推送到微信】复盘一下昨天") {
		t.Fatalf("前面有空白也算开头")
	}
	if imTodoPushWanted("每天早上 8 点汇报磁盘【推送到微信】") {
		t.Fatalf("标记不在开头不算")
	}
	if imTodoPushWanted("普通的定时任务") {
		t.Fatalf("没标记不算")
	}
}

// 来源闸的判定口径（纯函数）。
func TestIMPushableOrigin(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"", false},                         // 桌面端
		{imOriginTag("weixin", "u1"), true}, // 微信入站
		{imOriginTodoWeixin, true},          // 明确标记的定时任务
		{"todo", false},                     // 普通定时任务
		{"im:", false},                      // 畸形标记不算
		{"im:weixin", false},                //
		{"desktop", false},                  // 别的东西
	}
	for _, c := range cases {
		if got := imPushableOrigin(c.origin); got != c.want {
			t.Fatalf("imPushableOrigin(%q)=%v，期望 %v", c.origin, got, c.want)
		}
	}
}

// 失败要按平台原始码留痕（`code=-2` 这种），并能读到连续失败次数。
func TestIMPushErrCode(t *testing.T) {
	we := &weixinErr{Code: "weixin-rejected", ProviderCode: "-2", Msg: "微信拒绝发送消息"}
	if got := imPushErrCode(we); got != "-2" {
		t.Fatalf("应取平台原始码 -2，实得 %q", got)
	}
	net := &weixinErr{Code: "network", Msg: "连不上微信服务"}
	if got := imPushErrCode(net); got != "network" {
		t.Fatalf("没有平台码时退回协议码，实得 %q", got)
	}
}
