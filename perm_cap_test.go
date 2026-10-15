package main

// 能力门控升级通道（requireCapability 的 request_capability + justification）
// 的测试：fail closed 指路、缺理由拒绝、用户拒绝终局、「允许」只放行本次、
// 「始终允许」点亮能力开关并落「权限变更」注记。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// decideWhenRaised 等待审批卡挂出后替用户点按钮（approved/always）。
// 两秒等不到卡返回错误（调用方在阻塞的 requireCapability 返回后收取）。
func decideWhenRaised(a *App, approved, always bool) error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.permMu.Lock()
		var ap *Approval
		for _, v := range a.approvals {
			ap = v
		}
		a.permMu.Unlock()
		if ap != nil {
			return a.resolveApproval(ap.ID, approved, always)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fmt.Errorf("两秒内没等到审批卡")
}

// newCapTestApp 建带 approvals 表的测试 App（newPermTestApp 不初始化它，
// requestApproval 直接写表会 panic）。
func newCapTestApp(t *testing.T, dir string) *App {
	t.Helper()
	a := newPermTestApp(t, dir)
	a.approvals = map[string]*Approval{}
	a.grants = map[string]*Grant{}
	return a
}

func TestRequireCapabilityEscalation(t *testing.T) {
	a := newCapTestApp(t, "data-cap-esc-test")
	sess := &SessionMeta{ID: "s1", Mode: "task", Permission: "workspace-write",
		CreatedAt: "2026-10-09 09:00:00"}
	mustCreateSession(t, a, sess)

	mk := func(args string) (*toolContext, json.RawMessage) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		raw := json.RawMessage(args)
		return &toolContext{ctx: ctx, app: a, sess: sess, toolName: "browser_open"}, raw
	}

	// 1. 能力关、未带升级参数：fail closed，报错指路 request_capability
	tc, raw := mk(`{"url":"https://example.com"}`)
	err := requireCapability(tc, capBrowser, "操作浏览器", raw)
	if err == nil {
		t.Fatalf("能力未开启应 fail closed")
	}
	wantAll(t, err.Error(), "被能力门控拦下", "request_capability", "权限芯片")

	// 2. 带申请但缺 justification
	tc, raw = mk(`{"request_capability":true}`)
	err = requireCapability(tc, capBrowser, "操作浏览器", raw)
	if err == nil || !strings.Contains(err.Error(), "justification") {
		t.Fatalf("缺理由应报错，得到：%v", err)
	}

	// 3. 用户拒绝：终局报错，能力不点亮
	errc := make(chan error, 1)
	go func() { errc <- decideWhenRaised(a, false, false) }()
	tc, raw = mk(`{"request_capability":true,"justification":"需要打开网页核对信息"}`)
	if err := requireCapability(tc, capBrowser, "操作浏览器", raw); err == nil || !strings.Contains(err.Error(), "未批准") {
		t.Fatalf("拒绝应终局报错，得到：%v", err)
	}
	if e := <-errc; e != nil {
		t.Fatalf("替用户点「拒绝」失败: %v", e)
	}
	if hasCapability(sess, capBrowser) {
		t.Fatalf("拒绝不应点亮能力")
	}

	// 4. 「允许」：本次放行（返回 nil），但不点亮开关
	errc = make(chan error, 1)
	go func() { errc <- decideWhenRaised(a, true, false) }()
	tc, raw = mk(`{"request_capability":true,"justification":"需要打开网页核对信息"}`)
	if err := requireCapability(tc, capBrowser, "操作浏览器", raw); err != nil {
		t.Fatalf("「允许」应放行本次，得到：%v", err)
	}
	if e := <-errc; e != nil {
		t.Fatalf("替用户点「允许」失败: %v", e)
	}
	if hasCapability(sess, capBrowser) {
		t.Fatalf("「允许」只放行本次，不应点亮能力开关")
	}

	// 5. 「始终允许」：放行 + 点亮开关 + 落库 + 「权限变更」注记随 env 快照落地
	errc = make(chan error, 1)
	go func() { errc <- decideWhenRaised(a, true, true) }()
	tc, raw = mk(`{"request_capability":true,"justification":"需要打开网页核对信息"}`)
	if err := requireCapability(tc, capBrowser, "操作浏览器", raw); err != nil {
		t.Fatalf("「始终允许」应放行，得到：%v", err)
	}
	if e := <-errc; e != nil {
		t.Fatalf("替用户点「始终允许」失败: %v", e)
	}
	if !hasCapability(sess, capBrowser) {
		t.Fatalf("「始终允许」应点亮能力开关")
	}
	fresh, err := a.Store.Get("s1")
	if err != nil || fresh == nil {
		t.Fatalf("读会话失败: %v", err)
	}
	if !hasCapability(fresh, capBrowser) {
		t.Fatalf("能力开关应已落库（权限芯片刷新的依据）")
	}
	if row := lastEnvRow(t, a, "s1"); !strings.Contains(row, "权限变更") ||
		!strings.Contains(row, "「操作浏览器」") || !strings.Contains(row, permSrcApproval) {
		t.Fatalf("「权限变更」注记应随 env 快照落地：\n%s", row)
	}

	// 6. 能力已开启：直接放行，携带的升级参数被忽略（不弹卡）
	if err := requireCapability(tc, capBrowser, "操作浏览器", raw); err != nil {
		t.Fatalf("能力已开启应直接放行，得到：%v", err)
	}
	if len(a.approvals) != 0 {
		t.Fatalf("能力已开启不应弹审批卡")
	}
}
