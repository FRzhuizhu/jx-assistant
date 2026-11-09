package main

// IM 阶段 3（审批/提问推微信）的单元测试：
//   · 来源标记的打标与解析、以及「父轮 → 子任务」的继承式传播；
//   · 桌面端发起的那一轮产生审批/提问时，微信一条都收不到；
//   · 微信发起的那一轮：审批/提问推到微信，回「批准/拒绝/回答」能生效；
//   · 安全护栏：白名单外的回复不算数、模糊回复只追问、序号对不上要提示、
//     超时按拒绝（fail closed）。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// newIMPermApp 建一个带真库、白名单里只有 u1、挂了假微信渠道的 App。
func newIMPermApp(t *testing.T) (*App, *fakeIMChannel) {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := &App{Cfg: &Config{DataDir: dir}, Store: st}
	a.initRuntime()
	a.Cfg.IM.Weixin.Allow = []string{"u1"}
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	return a, fake
}

// registerTestRun 登记一轮「正在执行」的会话并打上来源标记（不拉起执行体：
// 审批/提问的推送判定只认 runOriginOf，不需要真的跑模型）。
func registerTestRun(t *testing.T, a *App, sessID, origin string) *sessionRun {
	t.Helper()
	r := &sessionRun{app: a, ctx: context.Background(), done: make(chan struct{}), subs: map[int]chan Event{}}
	r.setOrigin(origin)
	a.runMu.Lock()
	if a.runners == nil {
		a.runners = map[string]*sessionRun{}
	}
	a.runners[sessID] = r
	a.runMu.Unlock()
	t.Cleanup(func() {
		a.runMu.Lock()
		delete(a.runners, sessID)
		a.runMu.Unlock()
	})
	return r
}

// newTestSession 建一个真会话（留痕要往它落 kind=audit 行）。
func newTestSession(t *testing.T, a *App, id, title string) *SessionMeta {
	t.Helper()
	m := &SessionMeta{ID: id, Title: title, Mode: "task", Workspace: t.TempDir(), CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(m); err != nil {
		t.Fatalf("建会话 %s 失败: %v", id, err)
	}
	return m
}

// waitApprovals 等到待审批条数达到 n（最多 2 秒）。
func waitApprovals(t *testing.T, a *App, n int) []*Approval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := a.pendingApprovals(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("等了 2 秒只登记了 %d 条审批（期望 %d）", len(a.pendingApprovals()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitQuestions 等到待回答提问条数达到 n。
func waitQuestions(t *testing.T, a *App, n int) []*UserQuestion {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := a.pendingQuestions(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("等了 2 秒只登记了 %d 条提问（期望 %d）", len(a.pendingQuestions()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertNoPush 断言这段时间内微信一条都没收到（桌面端任务的外发防线）。
func assertNoPush(t *testing.T, f *fakeIMChannel, d time.Duration) {
	t.Helper()
	time.Sleep(d)
	f.mu.Lock()
	got := append([]string(nil), f.sent...)
	f.mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("不该推送，却发出了 %d 条：%#v", len(got), got)
	}
}

// joinSent 把已发出的文本拼起来（断言文案用）。
func joinSent(f *fakeIMChannel) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sent))
	for _, s := range f.sent {
		if i := strings.Index(s, "\x00"); i >= 0 {
			out = append(out, s[i+1:])
		}
	}
	return strings.Join(out, "\n---\n")
}

// waitSentContains 等到已发出的消息里出现某段文字（发件走串行队列，是异步的：
// 断言必须等它落地，不能读一次就下结论）。
func waitSentContains(t *testing.T, f *fakeIMChannel, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := joinSent(f)
		if strings.Contains(got, substr) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等了 2 秒，发出的消息里没有 %q：%q", substr, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func weixinConv(userID string) IMConversation {
	return IMConversation{Channel: "weixin", ChatID: userID, ChatType: "dm", UserID: userID}
}

// 来源标记：打标/解析往返，非 IM 形态一律不算。
func TestIMOriginTagParse(t *testing.T) {
	tag := imOriginTag("weixin", "o9cq806-x@im.wechat")
	if tag != "im:weixin:o9cq806-x@im.wechat" {
		t.Fatalf("标记形态不对：%q", tag)
	}
	ch, chat, ok := imParseOrigin(tag)
	if !ok || ch != "weixin" || chat != "o9cq806-x@im.wechat" {
		t.Fatalf("解析不对：%q %q %v", ch, chat, ok)
	}
	for _, bad := range []string{"", "im", "im:", "im:weixin", "im:weixin:", "im::x", "human", "desktop"} {
		if _, _, ok := imParseOrigin(bad); ok {
			t.Fatalf("%q 不该被当成 IM 来源", bad)
		}
	}
	if got := imOriginTag("weixin", "  "); got != "" {
		t.Fatalf("缺 chatId 不该拼出标记：%q", got)
	}
}

// 传播链路：父轮带 IM 标记 → 派发出去的子轮继承 → 孙轮再继承（继承式，不是只看一层）。
// 桌面端的父轮（无标记）派发出去的子轮也没有标记。
func TestIMRunOriginPropagates(t *testing.T) {
	a, _ := newIMPermApp(t)
	tag := imOriginTag("weixin", "u1")
	registerTestRun(t, a, "main", tag)

	child := &sessionRun{}
	a.imInheritRunOrigin("main", child)
	if got := child.originOf(); got != tag {
		t.Fatalf("子任务该继承父轮的来源标记，得到 %q", got)
	}
	// 孙子任务：子轮自己也在跑，派发时从它身上继承
	registerTestRun(t, a, "child", child.originOf())
	grand := &sessionRun{}
	a.imInheritRunOrigin("child", grand)
	if got := grand.originOf(); got != tag {
		t.Fatalf("孙任务该继续继承同一个 chatId，得到 %q", got)
	}

	// 桌面端发起的那一轮：没有标记，派发出去的子任务同样没有
	registerTestRun(t, a, "desk", "")
	deskChild := &sessionRun{}
	a.imInheritRunOrigin("desk", deskChild)
	if got := deskChild.originOf(); got != "" {
		t.Fatalf("桌面端的子任务不该带 IM 标记，得到 %q", got)
	}
	// 没在跑的会话（定时任务等）同样不传播
	lonely := &sessionRun{}
	a.imInheritRunOrigin("nobody", lonely)
	if got := lonely.originOf(); got != "" {
		t.Fatalf("无来源会话不该传播标记，得到 %q", got)
	}
}

// 桌面端发起的任务：审批不推微信（一条都不发），且照旧走桌面端审批队列。
func TestIMApprovalNotPushedForDesktopRun(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-desk", "桌面任务")
	registerTestRun(t, a, sess.ID, "") // 桌面端发起：无来源标记

	done := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), sess, "run_command", "删除临时文件", "需要写权限")
		done <- approved
	}()
	aps := waitApprovals(t, a, 1)
	assertNoPush(t, fake, 150*time.Millisecond)
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("桌面端的审批不该登记成微信待办，得到 %d 条", n)
	}
	if err := a.resolveApproval(aps[0].ID, true, false); err != nil {
		t.Fatalf("桌面端审批应能正常处理：%v", err)
	}
	if approved := <-done; !approved {
		t.Fatalf("桌面端批准应生效")
	}
	assertNoPush(t, fake, 100*time.Millisecond)
}

// 微信发起的任务：审批推到微信，回「批准」→ 动作放行，并留痕（kind=audit）。
func TestIMApprovalPushedAndApprovedViaWeixin(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-im", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	done := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), sess, "run_command", "npm install 装依赖", "要联网执行命令")
		done <- approved
	}()
	waitApprovals(t, a, 1)
	got := fake.wait(t, 1)
	if !strings.Contains(got[0], "需要你处理") || !strings.Contains(got[0], "1 批准") {
		t.Fatalf("审批推送文案不对：%q", got[0])
	}
	if !strings.Contains(got[0], "超时未回复按拒绝处理") {
		t.Fatalf("推送里必须写明超时按拒绝：%q", got[0])
	}

	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m1", Kind: "text", Text: "批准"})
	select {
	case approved := <-done:
		if !approved {
			t.Fatalf("微信里回「批准」应放行")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("微信里的「批准」没有生效（审批仍在等）")
	}
	waitSentContains(t, fake, "已批准")
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("处理完待办应清空，还剩 %d 条", n)
	}
	// 留痕：会话里落了一条 type=im_decision 的审计行，带 via 与审批 id
	rows, err := a.Store.MessagesWithSeq(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Kind != kindAudit {
			continue
		}
		txt := r.Text()
		if strings.Contains(txt, "im_decision") && strings.Contains(txt, "im:weixin:u1") && strings.Contains(txt, "已批准") {
			found = true
		}
	}
	if !found {
		t.Fatalf("批准必须留痕（kind=audit / type=im_decision / via=im:weixin:u1）：%d 行", len(rows))
	}
}

// 白名单外的账号回「批准」不算数：审批仍挂着，并回一句未授权。
func TestIMApprovalReplyRejectedForStranger(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-stranger", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	done := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), sess, "run_command", "格式化 U 盘", "危险操作")
		done <- approved
	}()
	aps := waitApprovals(t, a, 1)
	fake.wait(t, 1)

	a.imOnInbound(IMInbound{Conv: weixinConv("u9"), MessageID: "m9", Kind: "text", Text: "批准"})
	waitSentContains(t, fake, "未授权")
	select {
	case <-done:
		t.Fatalf("非白名单账号的「批准」不该生效")
	case <-time.After(150 * time.Millisecond):
	}
	if n := len(a.pendingApprovals()); n != 1 {
		t.Fatalf("审批应仍在等（%d 条）", n)
	}
	if err := a.resolveApproval(aps[0].ID, false, false); err != nil {
		t.Fatal(err)
	}
	<-done
}

// 模糊回复（「嗯」「可以」「行吧」）只追问，绝不默认放行。
func TestIMApprovalAmbiguousReplyAsksAgain(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-amb", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	done := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), sess, "write_file", "写入 D:\\x\\y.txt", "工作区外写入")
		done <- approved
	}()
	aps := waitApprovals(t, a, 1)
	fake.wait(t, 1)

	for _, word := range []string{"嗯", "可以", "行吧", "你看着办"} {
		a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "amb-" + word, Kind: "text", Text: word})
	}
	waitSentContains(t, fake, "不够明确")
	waitSentContains(t, fake, "1 批准")
	select {
	case <-done:
		t.Fatalf("模糊回复不该放行")
	case <-time.After(100 * time.Millisecond):
	}
	if n := len(a.pendingApprovals()); n != 1 {
		t.Fatalf("审批应仍在等（%d 条）", n)
	}
	if err := a.resolveApproval(aps[0].ID, false, false); err != nil {
		t.Fatal(err)
	}
	<-done
}

// 多条待审批并存：按序号选；序号对不上要提示且谁都不执行；一次回复只消费一条。
func TestIMApprovalMultiIndexSelect(t *testing.T) {
	a, fake := newIMPermApp(t)
	s1 := newTestSession(t, a, "s-1", "任务一")
	s2 := newTestSession(t, a, "s-2", "任务二")
	registerTestRun(t, a, s1.ID, imOriginTag("weixin", "u1"))
	registerTestRun(t, a, s2.ID, imOriginTag("weixin", "u1"))

	res1 := make(chan bool, 1)
	res2 := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), s1, "run_command", "第一条命令", "理由一")
		res1 <- approved
	}()
	waitApprovals(t, a, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), s2, "write_file", "第二条写入", "理由二")
		res2 <- approved
	}()
	waitApprovals(t, a, 2)

	// 没带序号的「批准」：两项并存，必须追问序号
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m-a", Kind: "text", Text: "批准"})
	waitSentContains(t, fake, "带上序号")
	// 序号越界：提示且不执行
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m-b", Kind: "text", Text: "3 批准"})
	waitSentContains(t, fake, "没有第 3 项")
	// 选第 2 项批准：只有它放行
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m-c", Kind: "text", Text: "2 批准"})
	select {
	case approved := <-res2:
		if !approved {
			t.Fatalf("第 2 项应被批准")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("按序号「2 批准」没有生效")
	}
	select {
	case <-res1:
		t.Fatalf("一次回复只该消费一条：第 1 项不该被动")
	case <-time.After(100 * time.Millisecond):
	}
	// 选第 1 项拒绝
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m-d", Kind: "text", Text: "1 拒绝"})
	select {
	case approved := <-res1:
		if approved {
			t.Fatalf("第 1 项应被拒绝")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("按序号「1 拒绝」没有生效")
	}
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("两项都处理完，待办应清空，还剩 %d 条", n)
	}
}

// 超时未回复：按拒绝处理（fail closed），并在微信里明说「已按拒绝处理」。
func TestIMApprovalTimeoutFailsClosed(t *testing.T) {
	oldTTL := approvalTTL
	approvalTTL = 300 * time.Millisecond
	defer func() { approvalTTL = oldTTL }()

	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-to", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	type result struct{ approved, timedOut bool }
	done := make(chan result, 1)
	go func() {
		approved, _, timedOut := a.requestApproval(context.Background(), sess, "run_command", "长时间等待的命令", "理由")
		done <- result{approved, timedOut}
	}()
	waitApprovals(t, a, 1)
	fake.wait(t, 1)

	select {
	case r := <-done:
		if r.approved {
			t.Fatalf("超时必须按拒绝处理，不能默认放行")
		}
		if !r.timedOut {
			t.Fatalf("应标记为超时（timedOut=true）")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("审批没有在等待上限到点时结束")
	}
	got := fake.wait(t, 2)
	if !strings.Contains(got[1], "按拒绝处理") {
		t.Fatalf("超时后应推送「按拒绝处理」：%q", got[1])
	}
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("超时的待办应撤下，还剩 %d 条", n)
	}
	// 事后再回「批准」：没有待办，明确拒绝并说明
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m-late", Kind: "text", Text: "批准"})
	waitSentContains(t, fake, "没有等你审批")
}

// 微信发起的任务产生提问：推到微信，微信里回的正文就是答案。
func TestIMQuestionPushedAndAnsweredViaWeixin(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-q", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	ansCh := make(chan string, 1)
	go func() {
		ans, ok := a.requestUserAnswer(context.Background(), sess, &UserQuestion{
			Kind: askKindAsk, Question: "要覆盖旧文件吗？", Options: []string{"覆盖", "改名"}, AllowFree: true, TimeoutSec: 300,
		})
		if !ok {
			ansCh <- "__no_answer__"
			return
		}
		ansCh <- ans
	}()
	waitQuestions(t, a, 1)
	got := fake.wait(t, 1)
	if !strings.Contains(got[0], "提问") || !strings.Contains(got[0], "覆盖旧文件") {
		t.Fatalf("提问推送文案不对：%q", got[0])
	}

	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m-q1", Kind: "text", Text: "改名吧，别覆盖"})
	select {
	case ans := <-ansCh:
		if ans != "改名吧，别覆盖" {
			t.Fatalf("微信里的回答应原样作为答案，得到 %q", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("微信里的回答没有生效")
	}
	waitSentContains(t, fake, "已收到回答")
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("回答完待办应清空，还剩 %d 条", n)
	}
}

// 桌面端发起的任务产生提问：微信一条都不推（提问同样不外发）。
func TestIMQuestionNotPushedForDesktopRun(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-q-desk", "桌面任务")
	registerTestRun(t, a, sess.ID, "")

	done := make(chan string, 1)
	go func() {
		ans, _ := a.requestUserAnswer(context.Background(), sess, &UserQuestion{
			Kind: askKindAsk, Question: "桌面上的问题？", AllowFree: true, TimeoutSec: 300,
		})
		done <- ans
	}()
	qs := waitQuestions(t, a, 1)
	assertNoPush(t, fake, 150*time.Millisecond)
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("桌面端提问不该登记成微信待办，得到 %d 条", n)
	}
	if err := a.resolveQuestion(qs[0].ID, "桌面回答"); err != nil {
		t.Fatal(err)
	}
	if ans := <-done; ans != "桌面回答" {
		t.Fatalf("桌面回答应生效，得到 %q", ans)
	}
	assertNoPush(t, fake, 100*time.Millisecond)
}

// 桌面端先处理掉微信里挂着的待办：撤下那条待办并回一句「已在桌面端处理」，
// 用户在微信里再回也不会重复执行。
func TestIMPendingDroppedWhenResolvedOnDesktop(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-both", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	done := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), sess, "run_command", "桌面与微信都看得见的命令", "理由")
		done <- approved
	}()
	aps := waitApprovals(t, a, 1)
	fake.wait(t, 1)

	// 桌面端点「批准」
	if err := a.resolveApproval(aps[0].ID, true, false); err != nil {
		t.Fatal(err)
	}
	if approved := <-done; !approved {
		t.Fatalf("桌面端批准应生效")
	}
	got := fake.wait(t, 2)
	if !strings.Contains(got[1], "已在桌面端处理") {
		t.Fatalf("应通知微信侧已处理：%q", got[1])
	}
	// 微信里再回「批准」：待办已撤，不会重复执行
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m-dup", Kind: "text", Text: "批准"})
	waitSentContains(t, fake, "没有等你审批")
}

// 只有审批在等时，普通任务消息照旧投给数字经理：不能被审批流程吞掉
// （用户可能一边等审批、一边问别的事）。
func TestIMPlainTaskNotSwallowedByPendingApproval(t *testing.T) {
	a, _ := newIMPermApp(t)
	sess := newTestSession(t, a, "s-plain", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	done := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), sess, "run_command", "等审批的命令", "理由")
		done <- approved
	}()
	aps := waitApprovals(t, a, 1)
	if a.imHandleInboundDecision(weixinConv("u1"), "帮我看看磁盘还剩多少") {
		t.Fatalf("审批在等时，普通任务消息不该被决策处理消费")
	}
	if a.imHandleInboundDecision(weixinConv("u1"), "/status") {
		t.Fatalf("命令不该被决策处理消费（命令在更前面处理）")
	}
	if err := a.resolveApproval(aps[0].ID, false, false); err != nil {
		t.Fatal(err)
	}
	<-done
}

// 审批与提问并存：序号是整份清单的序号，选错了要提示，选对了各自生效。
func TestIMMixedPendingIndexSelect(t *testing.T) {
	a, fake := newIMPermApp(t)
	sess := newTestSession(t, a, "s-mix", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	apDone := make(chan bool, 1)
	go func() {
		approved, _, _ := a.requestApproval(context.Background(), sess, "run_command", "混合场景的命令", "理由")
		apDone <- approved
	}()
	waitApprovals(t, a, 1)
	ansCh := make(chan string, 1)
	go func() {
		ans, _ := a.requestUserAnswer(context.Background(), sess, &UserQuestion{
			Kind: askKindAsk, Question: "用哪个名字？", AllowFree: true, TimeoutSec: 300,
		})
		ansCh <- ans
	}()
	waitQuestions(t, a, 1)
	fake.wait(t, 1) // 两条推送（第二条里已把两项都列出来）

	// 序号 1 是审批：拿它当回答 → 明确提示
	if !a.imHandleInboundDecision(weixinConv("u1"), "1 换个名字") {
		t.Fatalf("序号指向审批时应提示并消费")
	}
	waitSentContains(t, fake, "第 1 项是审批")
	// 序号 2 是提问：回答它
	if !a.imHandleInboundDecision(weixinConv("u1"), "2 用新名字") {
		t.Fatalf("序号指向提问时应作为回答消费")
	}
	select {
	case ans := <-ansCh:
		if ans != "用新名字" {
			t.Fatalf("序号后的正文才是答案，得到 %q", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("按序号回答没有生效")
	}
	// 审批仍挂着，再批它
	if !a.imHandleInboundDecision(weixinConv("u1"), "批准") {
		t.Fatalf("剩下一条审批时，无序号「批准」应生效")
	}
	select {
	case approved := <-apDone:
		if !approved {
			t.Fatalf("应被批准")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("批准没有生效")
	}
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("都处理完应清空，还剩 %d 条", n)
	}
}

// 「始终允许」这类常设授权意图：微信端不认——不执行任何审批动作、也不当普通需求
// 投给数字经理，而是明确回一句「请到桌面端点」并留痕；待审批状态保持不变。
// 单次「允许/批准」的行为不受影响。
func TestIMAlwaysAllowIntentDenied(t *testing.T) {
	a, fake := newIMPermApp(t)
	// 数字经理会话就位：万一这条消息被当普通需求投进去，会在它身上留下消息行
	mgr := &SessionMeta{ID: "mgr", Title: "数字经理", Mode: "assistant",
		Workspace: t.TempDir(), CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(mgr); err != nil {
		t.Fatalf("建数字经理会话失败: %v", err)
	}
	sess := newTestSession(t, a, "s-alw", "微信任务")
	registerTestRun(t, a, sess.ID, imOriginTag("weixin", "u1"))

	done := make(chan bool, 1)
	go func() {
		approved, always, _ := a.requestApproval(context.Background(), sess, "write_file",
			"写入 D:\\x\\y.txt", "工作区外写入")
		if always {
			t.Errorf("微信侧的回复不该产生常设授权（always=true）")
		}
		done <- approved
	}()
	waitApprovals(t, a, 1)
	fake.wait(t, 1) // 审批推送

	variants := []string{"始终允许", "始终允许 1", "永久允许", "always"}
	for _, text := range variants {
		a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "alw-" + text, Kind: "text", Text: text})
	}
	// 每条都要有回话（1 条审批推送 + 4 条提示）
	got := fake.wait(t, 1+len(variants))
	if !strings.Contains(got[len(got)-1], "常设授权（始终允许）请到桌面端点") {
		t.Fatalf("应明确回话「常设授权请到桌面端点」：%q", got[len(got)-1])
	}
	// 待审批状态不变：审批还挂着、微信待办还在
	if n := len(a.pendingApprovals()); n != 1 {
		t.Fatalf("常设授权意图不该改变审批状态，待审批 %d 条", n)
	}
	if n := a.imPendingCount("weixin", "u1"); n != 1 {
		t.Fatalf("微信待办应原样保留，得到 %d 条", n)
	}
	select {
	case <-done:
		t.Fatalf("常设授权意图不该放行审批")
	case <-time.After(100 * time.Millisecond):
	}
	// 不产生普通投递：数字经理会话里没有任何消息行，也没有起轮
	rows, err := a.Store.MessagesWithSeq(mgr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("常设授权意图不该当普通需求投给数字经理，却落了 %d 行", len(rows))
	}
	if a.IsRunning(mgr.ID) {
		t.Fatalf("常设授权意图不该拉起数字经理的一轮")
	}
	// 单次「允许」行为不变：正常放行
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "single-allow", Kind: "text", Text: "允许"})
	select {
	case approved := <-done:
		if !approved {
			t.Fatalf("单次「允许」仍应放行")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("单次「允许」没有生效")
	}
}

// 没有待办时回「始终允许」：同样明确回话（不放行、不投递）。
func TestIMAlwaysAllowWithoutPending(t *testing.T) {
	a, fake := newIMPermApp(t)
	if !a.imHandleInboundDecision(weixinConv("u1"), "始终允许") {
		t.Fatalf("常设授权意图应被消费")
	}
	waitSentContains(t, fake, "常设授权（始终允许）请到桌面端点")
}

// 回复解析：明确词（可带序号）才算；模糊词一律 ambiguous；其余是普通消息。
func TestIMParseDecision(t *testing.T) {
	cases := []struct {
		text string
		kind int
		idx  int
	}{
		{"批准", imDecisionApprove, 0},
		{"同意", imDecisionApprove, 0},
		{"y", imDecisionApprove, 0},
		{"YES", imDecisionApprove, 0},
		{"拒绝", imDecisionReject, 0},
		{"驳回", imDecisionReject, 0},
		{"n", imDecisionReject, 0},
		{"1 批准", imDecisionApprove, 1},
		{"批准 2", imDecisionApprove, 2},
		{"3拒绝", imDecisionReject, 3},
		{"２ 批准", imDecisionApprove, 2}, // 全角数字
		{"批准。", imDecisionApprove, 0},  // 句号不影响
		{"嗯", imDecisionAmbiguous, 0},
		{"可以", imDecisionAmbiguous, 0},
		{"行吧", imDecisionAmbiguous, 0},
		{"你看着办", imDecisionAmbiguous, 0},
		// 常设授权意图：微信端不认，但要识别出来明确回话（见 imSayAlwaysAllowDenied）
		{"始终允许", imDecisionAlwaysAllow, 0},
		{"始终允许 1", imDecisionAlwaysAllow, 1},
		{"永久允许", imDecisionAlwaysAllow, 0},
		{"永远同意", imDecisionAlwaysAllow, 0},
		{"一直允许", imDecisionAlwaysAllow, 0},
		{"总是允许", imDecisionAlwaysAllow, 0},
		{"always", imDecisionAlwaysAllow, 0},
		{"Always Allow", imDecisionAlwaysAllow, 0},
		// 别误伤：「允许」是单次批准；普通句子里的「一直」不算常设意图
		{"允许", imDecisionApprove, 0},
		{"任务一直在跑吗", imDecisionNone, 0},
		{"一直允许吗", imDecisionNone, 0},
		{"帮我看看磁盘还剩多少", imDecisionNone, 0},
		{"批准这个方案并继续执行后面的步骤", imDecisionNone, 0}, // 长句不是明确答复
		{"1 换个名字", imDecisionNone, 1},           // 认不出决定词也要带回序号（对号用）
		{"", imDecisionNone, 0},
	}
	for _, c := range cases {
		got := imParseDecision(c.text)
		if got.Kind != c.kind || got.Index != c.idx {
			t.Fatalf("%q 解析成 kind=%d idx=%d，期望 kind=%d idx=%d", c.text, got.Kind, got.Index, c.kind, c.idx)
		}
	}
}

// 没有待办时：明确决定词回一句「没有待审批」并消费；普通消息照旧交给会话。
func TestIMInboundDecisionWithoutPending(t *testing.T) {
	a, fake := newIMPermApp(t)
	if a.imHandleInboundDecision(weixinConv("u1"), "批准") != true {
		t.Fatalf("没有待办时回「批准」应被消费并说明")
	}
	waitSentContains(t, fake, "没有等你审批")
	if a.imHandleInboundDecision(weixinConv("u1"), "帮我看看磁盘") {
		t.Fatalf("普通任务消息不该被决策处理消费")
	}
	if a.imHandleInboundDecision(weixinConv("u1"), "嗯") {
		t.Fatalf("没有待办时模糊词也不该被消费")
	}
}

// 并发保护：待办表本身是加锁的（并发登记/摘除不丢项、不重复）。
func TestIMPendingTableConcurrent(t *testing.T) {
	a, _ := newIMPermApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			a.imPendingAdd(&imPending{Kind: imPendingApproval, ID: "ap-" + string(rune('a'+n)),
				Channel: "weixin", ChatID: "u1", Summary: "并发"})
		}(i)
	}
	wg.Wait()
	if n := a.imPendingCount("weixin", "u1"); n != 20 {
		t.Fatalf("并发登记应留下 20 条，得到 %d", n)
	}
	taken := 0
	for i := 0; i < 20; i++ {
		if a.imPendingTake(imPendingApproval, "ap-"+string(rune('a'+i))) != nil {
			taken++
		}
	}
	if taken != 20 {
		t.Fatalf("应能逐条摘掉 20 条，摘到 %d 条", taken)
	}
	if n := a.imPendingCount("weixin", "u1"); n != 0 {
		t.Fatalf("摘完应为空，剩 %d 条", n)
	}
}
