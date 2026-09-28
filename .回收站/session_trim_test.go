package main

import (
	"encoding/json"
	"testing"
)

// 下发瘦身（trimMessagesForUI）行为契约：库内原样、只削响应副本——
// 审计工件（request/audit）正文不下发；prompt 快照与上一条 sys+tools
// 相同换空壳 {}、变化行完整保留；比对基于原始正文（A→A→B 不误判）。
func TestTrimMessagesForUI(t *testing.T) {
	prompt := func(sys, env string, tools any) string {
		b, _ := json.Marshal(map[string]any{"sys": sys, "env": env, "tools": tools})
		return string(b)
	}
	toolsA := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}}
	toolsB := []map[string]any{{"type": "function", "function": map[string]any{"name": "write_file"}}}

	msgs := []*StoredMessage{
		{Message: TextMessage("system", prompt("S1", "env-1", toolsA)), Kind: kindPrompt, Seq: 1}, // 首条完整
		{Message: TextMessage("system", prompt("S1", "env-2", toolsA)), Kind: kindPrompt, Seq: 2}, // 同 sys/tools、env 变 → 空壳
		{Message: TextMessage("system", prompt("S2", "env-3", toolsA)), Kind: kindPrompt, Seq: 3}, // sys 变 → 完整
		{Message: TextMessage("system", prompt("S2", "env-4", toolsB)), Kind: kindPrompt, Seq: 4}, // tools 变 → 完整
		{Message: TextMessage("system", prompt("S2", "env-5", toolsB)), Kind: kindPrompt, Seq: 5}, // 又同 → 空壳
		{Message: TextMessage("system", `{bad json`), Kind: kindPrompt, Seq: 6},                    // 坏行原样下发
		{Message: TextMessage("assistant", "完整请求体"), Kind: kindRequest, Seq: 7},
		{Message: TextMessage("system", "审计用量"), Kind: kindAudit, Seq: 8},
		{Message: TextMessage("user", "你好"), Kind: kindChat, Seq: 9},
	}
	trimMessagesForUI(msgs)

	if msgs[0].Text() == "{}" || msgs[0].Text() == "" {
		t.Errorf("首条 prompt 行应完整保留，实际 %q", msgs[0].Text())
	}
	if msgs[1].Text() != "{}" {
		t.Errorf("未变化的 prompt 行应为空壳 {}，实际 %q", msgs[1].Text())
	}
	if msgs[2].Text() == "{}" || msgs[2].Text() == "" {
		t.Errorf("sys 变化的 prompt 行应完整保留，实际 %q", msgs[2].Text())
	}
	if msgs[3].Text() == "{}" {
		t.Errorf("tools 变化的 prompt 行应完整保留")
	}
	if msgs[4].Text() != "{}" {
		t.Errorf("未变化的 prompt 行应为空壳 {}，实际 %q", msgs[4].Text())
	}
	if msgs[5].Text() != `{bad json` {
		t.Errorf("坏行应原样下发，实际 %q", msgs[5].Text())
	}
	if msgs[6].Text() != "" || msgs[6].Kind != kindRequest {
		t.Errorf("request 行正文应置空、kind 保留，实际 content=%q kind=%q", msgs[6].Text(), msgs[6].Kind)
	}
	if msgs[7].Text() != "" {
		t.Errorf("audit 行正文应置空，实际 %q", msgs[7].Text())
	}
	if msgs[8].Text() != "你好" {
		t.Errorf("chat 行不应被改动，实际 %q", msgs[8].Text())
	}
}
