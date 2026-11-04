package main

// 注册面用户身份 + 验密失败熔断（2026-11-04 注册准入改造的家端半边）。

import (
	"testing"
	"time"
)

// 注册帧必须带 user（发布者姓名），auth 字段保留（协议帧不动，旧网关仍可用）。
func TestRegisterFrameCarriesUser(t *testing.T) {
	cfg := RemoteAccessCfg{MyID: "jx-test01", PairKey: "pk-甲", RegKey: "chat_p2p_v1"}
	f := registerFrame(cfg, "张三")
	if f["t"] != "register" || f["id"] != "jx-test01" || f["user"] != "张三" {
		t.Fatalf("注册帧身份字段不对: %v", f)
	}
	if f["auth"] != hubAuthKey("pk-甲") {
		t.Fatalf("auth 凭据字段应保留派生值: %v", f["auth"])
	}
	// 空身份（存量形态）：字段仍存在但为空，网关按已登记设备放行
	if f2 := registerFrame(cfg, ""); f2["user"] != "" {
		t.Fatalf("空身份应为空串: %v", f2["user"])
	}
}

// 熔断计数：窗口内连败达阈触发并设置冷却；成功清零；窗口过期重开。
func TestAuthFailBreaker(t *testing.T) {
	h := &hubCtx{}
	for i := 1; i < relayAuthFailBurst; i++ {
		if h.noteAuthFail() {
			t.Fatalf("第 %d 次失败不应熔断", i)
		}
	}
	if !h.noteAuthFail() {
		t.Fatal("窗口内连败达阈值应熔断")
	}
	if until := relayCooldownUntil.Load(); until <= time.Now().UnixMilli() {
		t.Fatal("熔断应把冷却截止设在将来")
	}

	// 成功一次清零：差一次达阈时来一条成功，再失败不应熔断
	h2 := &hubCtx{}
	for i := 0; i < relayAuthFailBurst-1; i++ {
		h2.noteAuthFail()
	}
	h2.noteAuthOK()
	if h2.noteAuthFail() {
		t.Fatal("清零后单次失败不应熔断")
	}

	// 窗口过期重开：先累计到差一次达阈，再把窗起点拨到窗口之外——下一条失败
	// 应按新窗口从 1 计起，而不是沿用旧窗累计触发熔断
	h3 := &hubCtx{}
	for i := 0; i < relayAuthFailBurst-1; i++ {
		h3.noteAuthFail()
	}
	h3.authFailMu.Lock()
	h3.authFailAt = time.Now().Add(-2 * relayAuthFailWindow)
	h3.authFailMu.Unlock()
	if h3.noteAuthFail() {
		t.Fatal("窗口过期后计数应重开，不应沿用旧窗累计熔断")
	}
}
