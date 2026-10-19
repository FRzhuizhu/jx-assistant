package main

// 输出速度（吐字速度）统计的测试：audit 行带 outMs 与 usage.completion 时
// contextStats 报最近一次有效输出的 tok/s；错误调用、无 usage、窗口过短、
// 侧路摘要行与旧格式行（无 outMs 字段）都跳过，取更早一次的真实输出。

import (
	"encoding/json"
	"testing"
)

// seedCallAudit 落一条引擎调用审计行（type=call），outMs/usage/scope/error 按参数组合。
func seedCallAudit(t *testing.T, a *App, sid string, outMs int64, completion int, scope, errMsg string) {
	t.Helper()
	p := map[string]any{"type": "call", "step": 1, "msgs": 3, "est": 100, "ms": 5000, "text": 400}
	if outMs > 0 {
		p["outMs"] = outMs
	}
	if scope != "" {
		p["scope"] = scope
	}
	if completion > 0 {
		p["usage"] = auditUsage{Prompt: 1000, Hit: 500, Miss: 500, Completion: completion}
	}
	if errMsg != "" {
		p["error"] = errMsg
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AppendRow(sid, &StoredMessage{
		Message: TextMessage("system", string(b)), Kind: kindAudit, CreatedAt: "2026-01-02 10:00:00",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestContextStatsSpeed(t *testing.T) {
	a := newCompactTestApp(t)
	sess := &SessionMeta{ID: "speed1", Mode: "task"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	// 空会话：无速度数据
	if st := a.contextStats(sess); st.Speed.TokPerSec != 0 || st.Speed.Ms != 0 {
		t.Fatalf("空会话不应有速度数据: %+v", st.Speed)
	}

	// 第一次有效输出：200 tok / 4s = 50 tok/s
	seedCallAudit(t, a, sess.ID, 4000, 200, "", "")
	st := a.contextStats(sess)
	if st.Speed.Tokens != 200 || st.Speed.Ms != 4000 || st.Speed.TokPerSec != 50 {
		t.Fatalf("应取第一次输出 200tok/4s=50tok/s: %+v", st.Speed)
	}

	// 第二次有效输出（更近）：100 tok / 2s = 50 tok/s，只取最近一次
	seedCallAudit(t, a, sess.ID, 2000, 100, "", "")
	st = a.contextStats(sess)
	if st.Speed.Tokens != 100 || st.Speed.Ms != 2000 || st.Speed.TokPerSec != 50 {
		t.Fatalf("应取最近一次输出 100tok/2s: %+v", st.Speed)
	}

	// 最近一条是失败调用（有 error）：跳过，回落到上一次成功输出
	seedCallAudit(t, a, sess.ID, 3000, 300, "", "模型服务返回 429：限流")
	st = a.contextStats(sess)
	if st.Speed.Tokens != 100 {
		t.Fatalf("失败调用应被跳过: %+v", st.Speed)
	}

	// 无效行们：窗口过短 / 侧路摘要 / 无 usage / 旧格式（无 outMs）——全部跳过，
	// 速度仍取 100tok 那次
	seedCallAudit(t, a, sess.ID, 50, 999, "", "")
	seedCallAudit(t, a, sess.ID, 9000, 900, "summarize", "")
	seedCallAudit(t, a, sess.ID, 9000, 0, "", "")
	seedCallAudit(t, a, sess.ID, 0, 800, "", "")
	st = a.contextStats(sess)
	if st.Speed.Tokens != 100 || st.Speed.Ms != 2000 {
		t.Fatalf("无效行应全部跳过，仍取 100tok/2s: %+v", st.Speed)
	}
}
