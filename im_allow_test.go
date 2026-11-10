package main

// 白名单「绑定即放行」的单元测试（im_allow.go）：
//   · 绑定成功 → owner 自动进白名单，且落盘、幂等；
//   · 白名单为空 + 首条入站 → 兜底 TOFU 放行并回一句让人知情；
//   · 白名单已有条目 → 启动补齐与 TOFU 都不动它（用户手写的信任边界不覆盖）；
//   · 第二个人再来 → 照旧拒绝（fail closed）；
//   · owner 已知而发送者不是他 → TOFU 不触发，直接拒。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newIMAllowApp 建一个带真库、空白名单、配了 CfgPath（能落盘）、挂假微信渠道的 App。
func newIMAllowApp(t *testing.T) (*App, *fakeIMChannel) {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := &App{Cfg: &Config{DataDir: dir}, Store: st, CfgPath: filepath.Join(dir, "config.json")}
	a.initRuntime()
	fake := &fakeIMChannel{}
	a.imSetChannel("weixin", fake)
	return a, fake
}

// ① 扫码绑定成功 → owner 自动写进白名单（并落盘、幂等）。
func TestIMAllowOwnerOnBind(t *testing.T) {
	a, _ := newIMAllowApp(t)
	if a.Cfg.imAllowCount() != 0 {
		t.Fatalf("起步白名单应为空")
	}
	if !a.imAllowOwnerOnBind("owner-1") {
		t.Fatalf("绑定后应把 owner 写进白名单")
	}
	if !a.Cfg.imAllowWeixin("owner-1") {
		t.Fatalf("owner 应能下指令")
	}
	// 幂等：再调一次不重复追加
	if a.imAllowOwnerOnBind("owner-1") {
		t.Fatalf("已在白名单里不该重复写入")
	}
	if n := a.Cfg.imAllowCount(); n != 1 {
		t.Fatalf("白名单应只有 1 条，实得 %d", n)
	}
	// 落盘：重启后不必重新扫码（凭据里的 owner 直接生效）
	data, err := os.ReadFile(a.CfgPath)
	if err != nil {
		t.Fatalf("配置文件没写出来: %v", err)
	}
	if !strings.Contains(string(data), "owner-1") {
		t.Fatalf("白名单没落盘：%s", string(data))
	}
}

// ③ 白名单已有条目时：启动补齐不动它；绑定 owner 是**追加**而不是覆盖。
func TestIMAllowKeepsManualList(t *testing.T) {
	a, _ := newIMAllowApp(t)
	a.Cfg.IM.Weixin.Allow = []string{"manual-1"}
	// 启动补齐只填空白名单
	if a.imAllowOwnerOnStart("weixin", "owner-9") {
		t.Fatalf("白名单非空时启动补齐不该动手")
	}
	if got := a.Cfg.IM.Weixin.Allow; len(got) != 1 || got[0] != "manual-1" {
		t.Fatalf("用户手写的列表被改了：%#v", got)
	}
	// 刚绑定那一刻追加 owner 本人：保留原条目
	a.imAllowOwnerOnBind("owner-9")
	if got := imNormalizeAllow(a.Cfg.IM.Weixin.Allow); len(got) != 2 || got[0] != "manual-1" || got[1] != "owner-9" {
		t.Fatalf("绑定应追加而不是覆盖：%#v", got)
	}
}

// ② 白名单为空 + 首条入站 → 兜底 TOFU 放行，并回一句「已加入 / 不是你请解绑」。
func TestIMAllowFirstSenderTOFU(t *testing.T) {
	a, fake := newIMAllowApp(t)
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m1", Kind: "text", Text: "/status"})
	if !a.Cfg.imAllowWeixin("u1") {
		t.Fatalf("空白名单下首个发送者应被自动信任")
	}
	got := fake.wait(t, 2) // 「已加入白名单」+ /status 回执
	if !strings.Contains(got[0], "已把你的账号加入白名单") ||
		!strings.Contains(got[0], "请立即在桌面端解绑微信") {
		t.Fatalf("应回一句知情提示：%q", got[0])
	}
}

// ④ 第二个人再来 → 照旧拒绝，且白名单不长出第二条。
func TestIMAllowSecondSenderRejected(t *testing.T) {
	a, fake := newIMAllowApp(t)
	a.imOnInbound(IMInbound{Conv: weixinConv("u1"), MessageID: "m1", Kind: "text", Text: "/status"})
	if !a.Cfg.imAllowWeixin("u1") {
		t.Fatalf("首个发送者应被信任")
	}
	a.imOnInbound(IMInbound{Conv: weixinConv("u2"), MessageID: "m2", Kind: "text", Text: "/status"})
	got := fake.wait(t, 3)
	last := got[len(got)-1]
	if !strings.Contains(last, "未授权") || !strings.Contains(last, "u2") {
		t.Fatalf("第二个账号应被拒并回它的 id：%q", last)
	}
	if a.Cfg.imAllowWeixin("u2") {
		t.Fatalf("第二个账号不该被信任")
	}
	if n := a.Cfg.imAllowCount(); n != 1 {
		t.Fatalf("白名单应仍只有 1 条，实得 %d", n)
	}
}

// owner 已知（扫码绑定的那个号）而发送者是别人：TOFU 不触发，直接拒。
func TestIMAllowTOFUSkippedWhenOwnerKnown(t *testing.T) {
	a, _ := newIMAllowApp(t)
	// 用一个带 owner 的真渠道对象（这里只验判定，不走出站，故不接假发送器）
	a.imSetChannel("weixin", &weixinChannel{app: a, account: &weixinAccount{OwnerID: "owner-1"}})
	if a.imChannelOwner("weixin") != "owner-1" {
		t.Fatalf("应能读到渠道 owner")
	}
	if a.imAllowFirstSender("weixin", "stranger") {
		t.Fatalf("owner 已知时不该把陌生人当首个发送者信任")
	}
	if n := a.Cfg.imAllowCount(); n != 0 {
		t.Fatalf("不该把陌生人写进白名单，实得 %d 条", n)
	}
	// 发送者就是 owner：即使白名单空（启动补齐还没跑到）也认他
	if !a.imAllowFirstSender("weixin", "owner-1") {
		t.Fatalf("发送者就是绑定者时应信任")
	}
	if !a.Cfg.imAllowWeixin("owner-1") {
		t.Fatalf("owner 应能下指令")
	}
}

// 拒绝文案不能再指向不存在的「设置 · IM 接入」界面。
func TestIMRejectTextHasNoDeadEnd(t *testing.T) {
	a, fake := newIMAllowApp(t)
	a.Cfg.IM.Weixin.Allow = []string{"someone-else"}
	a.imOnInbound(IMInbound{Conv: weixinConv("u9"), MessageID: "m1", Kind: "text", Text: "你好"})
	got := fake.wait(t, 1)
	if strings.Contains(got[0], "设置 · IM 接入") || strings.Contains(got[0], "设置·IM") {
		t.Fatalf("还在指一个不存在的界面：%q", got[0])
	}
	if !strings.Contains(got[0], "IM") || !strings.Contains(got[0], "绑定") {
		t.Fatalf("应指向真实可操作的位置（IM 面板 · 绑定抽屉）：%q", got[0])
	}
}
