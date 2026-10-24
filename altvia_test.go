package main

// 内外网线路状态（llm.go setAltPreferred / companyViaPublic）与发现探测双端口
// （wizard.go broadcastTargets）的单测。

import (
	"net"
	"testing"
)

// 「优先外网」状态翻转要通知（前端徽标刷新钩子），未翻转不打扰。
func TestSetAltPreferredNotifies(t *testing.T) {
	altPreferred.Delete(wizardProviderID)
	notified := 0
	altPrefNotify = func(providerID string, preferAlt bool) { notified++ }
	defer func() { altPrefNotify = nil; altPreferred.Delete(wizardProviderID) }()

	setAltPreferred(wizardProviderID, true)
	if !companyViaPublic() || notified != 1 {
		t.Fatalf("置 wan 应生效并通知: via=%v notified=%d", companyViaPublic(), notified)
	}
	setAltPreferred(wizardProviderID, true) // 同值：不通知
	if notified != 1 {
		t.Fatalf("同值重复写不应通知: %d", notified)
	}
	setAltPreferred(wizardProviderID, false)
	if companyViaPublic() || notified != 2 {
		t.Fatalf("切回 lan 应生效并通知: via=%v notified=%d", companyViaPublic(), notified)
	}
}

// 发现探测必须覆盖新旧约定端口（2227 生产主口 + 2228 历史约定/本机测试实例）。
func TestBroadcastTargetsDualPort(t *testing.T) {
	targets := broadcastTargets()
	ports := map[int]bool{}
	for _, a := range targets {
		if a.IP.String() == "255.255.255.255" {
			ports[a.Port] = true
		}
	}
	if !ports[2227] || !ports[2228] {
		t.Fatalf("全网广播应同时覆盖 2227 与 2228（历史兼容）: %v", targets)
	}
	// 每个目标端口合法
	for _, a := range targets {
		if a.Port <= 0 || a.Port > 65535 {
			t.Fatalf("非法端口: %v", a)
		}
	}
	_ = net.JoinHostPort // 保持 import（端口推导走 net.JoinHostPort）
}
