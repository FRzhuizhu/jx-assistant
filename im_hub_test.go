package main

// IM 中枢（阶段 2）的单元测试：出站筛选（纯函数）、绑定表往返与持久化、
// 串行发件队列（用假渠道接住，不联网）、入站白名单拒绝与命令回执。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIMChannel 是接住出站文本的假渠道（实现 IMChannel + IMSender）。
type fakeIMChannel struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeIMChannel) Name() string                { return "weixin" }
func (f *fakeIMChannel) Start(context.Context) error { return nil }
func (f *fakeIMChannel) Stop()                       {}
func (f *fakeIMChannel) Status() IMChannelStatus     { return IMChannelStatus{Channel: "weixin", State: "connected"} }

// newIMTestApp 建一个带真库的 App：/status 要读「当前数字经理」，没有库会空指针。
// 库必须在用例结束时关掉——Windows 上 TempDir 清理撞上仍被 SQLite 打开的
// jx.db 会报「文件被另一个进程占用」，用例正文全过却算失败。
func newIMTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &App{Cfg: &Config{DataDir: dir}, Store: st}
}

func (f *fakeIMChannel) SendText(_ context.Context, conv IMConversation, text string) ([]string, error) {
	f.mu.Lock()
	f.sent = append(f.sent, conv.UserID+"\x00"+text)
	f.mu.Unlock()
	return []string{"fake-1"}, nil
}

// wait 等到至少 n 条发出（最多 2 秒），返回快照。
func (f *fakeIMChannel) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		got := append([]string(nil), f.sent...)
		f.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("等了 2 秒只发出 %d 条（期望 %d）：%#v", len(got), n, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIMShouldPush(t *testing.T) {
	final := Event{Type: "message", SessionID: "s1", Seq: 7, Kind: kindChat,
		Message: &Message{Role: "assistant", Content: "磁盘还剩 42G"}}
	if !imShouldPush(final, "s1", imOriginTag("weixin", "u1")) {
		t.Fatalf("数字经理的最终答复应推送")
	}
	// 带 tool_calls 的中间态不推（过程不是结论）
	toolCall := final
	toolCall.Message = &Message{Role: "assistant", Content: "",
		ToolCalls: []ToolCall{{ID: "t1", Type: "function"}}}
	if imShouldPush(toolCall, "s1", imOriginTag("weixin", "u1")) {
		t.Fatalf("带 tool_calls 的中间态不该推送")
	}
	// 用户消息不推（否则自己的话会被回声一遍）
	userMsg := final
	userMsg.Message = &Message{Role: "user", Content: "你好"}
	if imShouldPush(userMsg, "s1", imOriginTag("weixin", "u1")) {
		t.Fatalf("用户消息不该推送")
	}
	// 别的会话（子会话/任务会话）不推
	if imShouldPush(final, "s2", imOriginTag("weixin", "u1")) {
		t.Fatalf("非当前数字经理会话不该推送")
	}
	// 没有当前数字经理时不推
	if imShouldPush(final, "", imOriginTag("weixin", "u1")) {
		t.Fatalf("没有当前数字经理时不该推送")
	}
	// 非 message 事件、非 chat 行、空正文都不推
	if imShouldPush(Event{Type: "delta", SessionID: "s1"}, "s1", imOriginTag("weixin", "u1")) {
		t.Fatalf("delta 事件不该推送")
	}
	report := final
	report.Kind = kindReport
	if imShouldPush(report, "s1", imOriginTag("weixin", "u1")) {
		t.Fatalf("report 行不该当答复推送")
	}
	empty := final
	empty.Message = &Message{Role: "assistant", Content: "   "}
	if imShouldPush(empty, "s1", imOriginTag("weixin", "u1")) {
		t.Fatalf("空正文不该推送")
	}
}

func TestIMCapPushText(t *testing.T) {
	if got := imCapPushText("短答复"); got != "短答复" {
		t.Fatalf("短文本应原样：%q", got)
	}
	long := strings.Repeat("很长的答复。", 2000) // 12000 字
	got := imCapPushText(long)
	if n := len([]rune(got)); n <= imMaxPushChars || n > imMaxPushChars+80 {
		t.Fatalf("截断长度不对：%d", n)
	}
	if !strings.Contains(got, "完整内容请在桌面端查看") {
		t.Fatalf("截断应带提示：%q", got[len(got)-40:])
	}
	if !strings.HasPrefix(got, "很长的答复。") {
		t.Fatalf("截断应保留开头")
	}
}

func TestIMHubBindings(t *testing.T) {
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	if got := a.imBindingList(); len(got) != 0 {
		t.Fatalf("初始不该有绑定：%#v", got)
	}
	b := a.imBindChat("weixin", "u1")
	if b.Channel != "weixin" || b.ChatID != "u1" || b.UpdatedAt == "" {
		t.Fatalf("绑定内容不对：%+v", b)
	}
	// 重复绑定不产生第二条
	a.imBindChat("weixin", "u1")
	if got := a.imBindingList(); len(got) != 1 {
		t.Fatalf("重复绑定不该新增：%d 条", len(got))
	}
	// lastSeq 只增不减（事件重放不能把序号推回去）
	a.imMarkPushed("weixin", "u1", "s1", 5)
	a.imMarkPushed("weixin", "u1", "s1", 3)
	if got := a.imBindingList()[0].LastSeq; got != 5 {
		t.Fatalf("lastSeq 应保持 5，得到 %d", got)
	}
	// 换数字经理版本：序号重新计数，绑定必须归零并记下新会话
	rb := a.imRebaseBinding("weixin", "u1", "s2")
	if rb.SessionID != "s2" || rb.LastSeq != 0 {
		t.Fatalf("换版本后应重置：%+v", rb)
	}
	// 同一版本重复 rebase 不重置（否则会把已推序号丢掉、重复推送）
	a.imMarkPushed("weixin", "u1", "s2", 9)
	if rb := a.imRebaseBinding("weixin", "u1", "s2"); rb.LastSeq != 9 {
		t.Fatalf("同版本 rebase 不该重置 lastSeq：%+v", rb)
	}
	// 落盘：新实例（同一 DataDir）能读回
	a2 := &App{Cfg: a.Cfg}
	got := a2.imBindingList()
	if len(got) != 1 || got[0].ChatID != "u1" || got[0].LastSeq != 9 || got[0].SessionID != "s2" {
		t.Fatalf("绑定表没落盘或读回不对：%#v", got)
	}
	// 解绑整渠道
	a2.imUnbindChannel("weixin")
	if got := a2.imBindingList(); len(got) != 0 {
		t.Fatalf("解绑后不该还有绑定：%#v", got)
	}
	a3 := &App{Cfg: a.Cfg}
	if got := a3.imBindingList(); len(got) != 0 {
		t.Fatalf("解绑也要落盘：%#v", got)
	}
}

func TestIMSayDeliversThroughQueue(t *testing.T) {
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	a.imSay(IMConversation{Channel: "weixin", ChatID: "u1", ChatType: "dm", UserID: "u1"}, "第一句")
	a.imSay(IMConversation{Channel: "weixin", ChatID: "u1", ChatType: "dm", UserID: "u1"}, "第二句")
	got := fake.wait(t, 2)
	if !strings.HasPrefix(got[0], "u1\x00第一句") || !strings.HasPrefix(got[1], "u1\x00第二句") {
		t.Fatalf("发件顺序/收件人不对：%#v", got)
	}
	// 空内容不发
	a.imSay(IMConversation{Channel: "weixin", ChatID: "u1", UserID: "u1"}, "   ")
	if got := fake.wait(t, 2); len(got) != 2 {
		t.Fatalf("空内容不该发出：%#v", got)
	}
}

func TestIMInboundRejectsStranger(t *testing.T) {
	// 白名单非空（主人已经定了谁能下指令）时，名单外的账号一律拒绝：
	// 空白名单是另一码事——那是「还没有主人的信任记录」，走 im_allow.go 的兜底。
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	a.Cfg.IM.Weixin.Allow = []string{"owner-1"}
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	a.imOnInbound(IMInbound{
		Conv:      IMConversation{Channel: "weixin", ChatID: "u9", ChatType: "dm", UserID: "u9"},
		MessageID: "m1", Kind: "text", Text: "帮我看看磁盘",
	})
	got := fake.wait(t, 1)
	if !strings.Contains(got[0], "未授权") || !strings.Contains(got[0], "u9") {
		t.Fatalf("白名单外应拒绝并回 id：%q", got[0])
	}
	if a.Cfg.imAllowWeixin("u9") {
		t.Fatalf("名单外的账号不该被信任")
	}
}

func TestIMInboundGroupUnsupported(t *testing.T) {
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	a.imOnInbound(IMInbound{
		Conv:      IMConversation{Channel: "weixin", ChatID: "g1", ChatType: "group", UserID: "u1"},
		MessageID: "m2", Kind: "group", Text: "群里说话",
	})
	got := fake.wait(t, 1)
	if !strings.Contains(got[0], "群聊暂不支持") {
		t.Fatalf("群消息应明确拒绝：%q", got[0])
	}
}

func TestIMCommands(t *testing.T) {
	a := newIMTestApp(t)
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	conv := IMConversation{Channel: "weixin", ChatID: "u1", ChatType: "dm", UserID: "u1"}
	if !a.imHandleCommand(conv, "/help") {
		t.Fatalf("/help 应被消费")
	}
	if got := fake.wait(t, 1); !strings.Contains(got[0], "/status") {
		t.Fatalf("/help 文案不对：%q", got[0])
	}
	// 普通文本不是命令
	if a.imHandleCommand(conv, "帮我看看磁盘") {
		t.Fatalf("普通文本不该当命令消费")
	}
	// 未识别的 /xxx 也不消费（交给会话，模型能看懂）
	if a.imHandleCommand(conv, "/unknown") {
		t.Fatalf("未知命令不该消费")
	}
	// /status 回状态正文（含渠道状态行）
	if !a.imHandleCommand(conv, "/status") {
		t.Fatalf("/status 应被消费")
	}
	got := fake.wait(t, 2)
	if !strings.Contains(got[1], "运行中会话") || !strings.Contains(got[1], "白名单") {
		t.Fatalf("/status 正文不对：%q", got[1])
	}
}

func TestIMOutboxSerializes(t *testing.T) {
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	// 并发入队：同一会话仍按入队顺序串行发出（不会交错/丢）
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			a.imEnqueuePush("weixin", "u1", "第"+string(rune('A'+n))+"条", 0, "")
		}(i)
	}
	wg.Wait()
	if got := fake.wait(t, 20); len(got) != 20 {
		t.Fatalf("应发出 20 条，得到 %d", len(got))
	}
}
