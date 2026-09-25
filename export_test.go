package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// newExportApp 建一个带临时库的 App（测试用完即删，只落本目录）。
func newExportApp(t *testing.T, dir string) *App {
	t.Helper()
	os.RemoveAll(dir)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.db.Close(); os.RemoveAll(dir) })
	a := &App{Cfg: DefaultConfig(), Store: s}
	return a
}

func appendTestRow(t *testing.T, a *App, sid string, sm *StoredMessage) {
	t.Helper()
	sm.CreatedAt = "2026-09-24 12:00:0" + itoa(int(sm.Seq%10))
	if err := a.Store.AppendRow(sid, sm); err != nil {
		t.Fatalf("写行失败: %v", err)
	}
}

// 时间线运行日志：上下文组装与变更、模型调用与缓存命中、报错、工具结果配对。
func TestExportTimeline(t *testing.T) {
	a := newExportApp(t, "data-export-test")
	sess := &SessionMeta{ID: "s1", Title: "导出测试", Mode: "task",
		Provider: "deepseek", Model: "deepseek-chat", Permission: "workspace-write",
		CreatedAt: "2026-09-24 12:00:00"}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	tools := []ToolDef{{Type: "function", Function: ToolSpec{Name: "write_file",
		Description: "写文件", Parameters: map[string]any{"type": "object"}}}}
	pj, _ := json.Marshal(map[string]any{"sys": "SYS-A", "env": "ENV-1（时间 12:00）", "tools": tools})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("system", string(pj)), Kind: "prompt"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("user", "帮我创建 demo.txt"), Kind: "chat"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("system",
		`{"type":"call","step":1,"msgs":3,"est":100,"ms":800,"tools":["write_file"],"usage":{"prompt":100,"hit":0,"miss":100,"completion":50}}`), Kind: "audit"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID: "c1", Type: "function", Function: ToolCallFunc{Name: "write_file", Arguments: `{"path":"demo.txt"}`}}}},
		Thinking: "先想一下路径", Kind: "chat"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: Message{Role: "tool", Content: "已写入 demo.txt（2 字节）", ToolCallID: "c1"}, Kind: "chat"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("system",
		`{"type":"call","step":2,"msgs":5,"est":160,"ms":600,"text":12,"usage":{"prompt":160,"hit":100,"miss":60,"completion":30}}`), Kind: "audit"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("assistant", "全部完成"), Kind: "chat"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("system", "ENV-2（时间 12:01）"), Kind: "env"})
	tools2 := append(append([]ToolDef{}, tools...), ToolDef{Type: "function",
		Function: ToolSpec{Name: "run_command", Description: "执行命令", Parameters: map[string]any{"type": "object"}}})
	pj2, _ := json.Marshal(map[string]any{"sys": "SYS-B", "env": "ENV-2（时间 12:01）", "tools": tools2})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("system", string(pj2)), Kind: "prompt"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("system", `{"type":"error","error":"模型服务返回 429：限流"}`), Kind: "audit"})
	appendTestRow(t, a, "s1", &StoredMessage{Message: TextMessage("system", `{"type":"compact","rounds":2,"from":3,"to":6}`), Kind: "audit"})

	md := a.ctxEngine().ExportTimeline(sess)
	for _, want := range []string{
		"# 运行日志：导出测试",
		"模型调用与缓存命中",
		"| 1 |", "| 2 |",
		"上下文组装（第 1 轮）：系统提示词 5 字",
		"系统提示词全文（请求里的第一条 system 消息）",
		"工具定义完整 JSON（请求体里与消息平级的 tools 数组）",
		"用户消息",
		"> 帮我创建 demo.txt",
		"模型调用 #1（本轮第 1 步）：发送 3 条消息 · 约 100 tok",
		"缓存命中 0（0.0%）",
		"模型调用 #2（本轮第 2 步）：发送 5 条消息 · 约 160 tok",
		"缓存命中 100（62.5%）",
		"模型输出：思考 6 字 · 工具调用 write_file",
		"模型输出：正文 4 字",
		"工具结果 write_file（18 字）",
		"环境快照更新（固定作为尾部 user 消息随下次请求发送）",
		"- ENV-1（时间 12:00）",
		"+ ENV-2（时间 12:01）",
		"上下文组装（第 2 轮）",
		"系统提示词发生变化（前缀改变——此前已缓存的前缀将失效）",
		"工具定义变化：新增 run_command",
		"报错（未发给模型）：模型服务返回 429：限流",
		"规则压缩生效：折叠第 3–6 条（2 轮）",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("运行日志缺少 %q\n--- 渲染结果 ---\n%s", want, md)
		}
	}
}

// 原始数据导出（引擎）：无请求时报错；环形保留最近三轮；导出含全部缓存请求
// 与两两对比（消息条数变化、工具目录未变、首条变化消息定位、指纹变化）。
func TestEngineExportRawRequests(t *testing.T) {
	a := newExportApp(t, "data-export-req-test")
	sess := &SessionMeta{ID: "s2", Title: "请求导出", Mode: "task", CreatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if _, err := a.ctxEngine().ExportRawRequests(sess); err == nil || !strings.Contains(err.Error(), "还没有") {
		t.Fatalf("无请求时应报「还没有」错误，实际: %v", err)
	}
	e := a.ctxEngine()
	tools := `"tools":[{"type":"function","function":{"name":"read_file"}}]`
	body1 := []byte(`{"model":"m1","messages":[{"role":"system","content":"S1"}],` + tools + `}`)
	body2 := []byte(`{"model":"m1","messages":[{"role":"system","content":"S1"},{"role":"user","content":"你好"}],` + tools + `}`)
	body3 := []byte(`{"model":"m1","messages":[{"role":"system","content":"S1"},{"role":"user","content":"你好"},{"role":"assistant","content":"好的"}],` + tools + `}`)
	body4 := []byte(`{"model":"m1","messages":[{"role":"system","content":"S1"},{"role":"user","content":"你好"},{"role":"assistant","content":"好的"},{"role":"user","content":"再来"}],` + tools + `}`)
	e.RecordRequest(sess, "http://x/v1", "m1", 1, 2, 10, "aa", "bb", body1)
	e.RecordRequest(sess, "http://x/v1", "m1", 2, 3, 20, "aa", "cc", body2)
	e.RecordRequest(sess, "http://x/v1", "m1", 3, 4, 30, "aa", "dd", body3)
	e.RecordRequest(sess, "http://x/v1", "m1", 4, 5, 40, "aa", "dd", body4)

	if recs, _ := e.RecentRequests(sess, keepRequests); len(recs) != keepRequests {
		t.Fatalf("环形保留应只剩最近 %d 条，实际 %d", keepRequests, len(recs))
	}
	out, err := e.ExportRawRequests(sess)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	var doc rawRequestExport
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("导出的不是合法 JSON: %v\n%s", err, out)
	}
	if doc.Count != keepRequests || len(doc.Requests) != keepRequests || len(doc.Diffs) != keepRequests-1 {
		t.Fatalf("应导出 %d 条请求与 %d 条对比，实际 %d/%d/%d", keepRequests, keepRequests-1,
			doc.Count, len(doc.Requests), len(doc.Diffs))
	}
	// 环形：最早一条（body1）已被裁掉，留存的旧→新为 body2/3/4
	if !bytes.Contains(doc.Requests[0].Body, []byte("你好")) || bytes.Contains(doc.Requests[0].Body, []byte(`{"role":"assistant","content":"好的"}`)) {
		t.Fatalf("环形裁剪后最旧一条应是 body2: %s", doc.Requests[0].Body)
	}
	// 对比 #1→#2（body2→body3）：工具未变、sys 未变、env 指纹变、追加式增长（第 2 条起变化）
	d1 := doc.Diffs[0]
	if d1.ToolsChanged || !d1.EnvHashChanged || d1.SysHashChanged || d1.FirstDiffMessage != 2 || d1.MsgsDelta != 1 {
		t.Fatalf("对比 #1→#2 不符: %+v", d1)
	}
	// 对比 #2→#3（body3→body4）：指纹全未变，前缀从第 3 条起变化
	d2 := doc.Diffs[1]
	if d2.ToolsChanged || d2.SysHashChanged || d2.EnvHashChanged || d2.FirstDiffMessage != 3 {
		t.Fatalf("对比 #2→#3 不符: %+v", d2)
	}
	if !bytes.Contains(out, []byte("再来")) {
		t.Fatal("导出应内嵌最新请求的原始消息内容")
	}
}

// 组装管线跳过审计行与请求快照行：不进模型上下文。
func TestBuildContextSkipsAuditAndRequest(t *testing.T) {
	a := newExportApp(t, "data-export-ctx-test")
	sess := &SessionMeta{ID: "s3", Title: "组装", Mode: "task", CreatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	appendTestRow(t, a, "s3", &StoredMessage{Message: TextMessage("user", "你好"), Kind: "chat"})
	appendTestRow(t, a, "s3", &StoredMessage{Message: TextMessage("system", `{"type":"call","step":1}`), Kind: "audit"})
	rec, _ := json.Marshal(lastRequestRec{URL: "http://x", Model: "m", Body: json.RawMessage(`{"messages":[]}`)})
	appendTestRow(t, a, "s3", &StoredMessage{Message: TextMessage("system", string(rec)), Kind: "request"})

	msgs := a.buildContext(sess, "SYS", "ENV")
	joined, _ := json.Marshal(msgs)
	if strings.Contains(string(joined), "audit") || strings.Contains(string(joined), "lastRequestRec") {
		t.Errorf("审计/请求行混进了模型上下文: %s", joined)
	}
	if len(msgs) != 3 { // system SYS + user 你好 + user ENV
		t.Fatalf("组装结果应为 3 条，实际 %d: %s", len(msgs), joined)
	}
}
