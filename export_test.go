package main

import (
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

	md := a.ExportTimeline(sess)
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

// 原始数据导出：无请求时报错；替换式写入只留最新一条；内容与发送逐字节同源。
func TestExportLatestRequest(t *testing.T) {
	a := newExportApp(t, "data-export-req-test")
	sess := &SessionMeta{ID: "s2", Title: "请求导出", Mode: "task", CreatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if _, err := a.ExportLatestRequest(sess); err == nil || !strings.Contains(err.Error(), "还没有") {
		t.Fatalf("无请求时应报「还没有」错误，实际: %v", err)
	}
	body1 := []byte(`{"model":"m1","messages":[{"role":"system","content":"S1"}],"stream":true}`)
	a.recordLastRequest(sess, "http://x/v1/chat/completions", "m1", 1, 2, 10, body1)
	body2 := []byte(`{"model":"m1","messages":[{"role":"system","content":"S1"},{"role":"user","content":"你好"}],"stream":true}`)
	a.recordLastRequest(sess, "http://x/v1/chat/completions", "m1", 2, 3, 20, body2)

	all, _ := a.Store.RawMessagesAll("s2")
	reqRows := 0
	for _, r := range all {
		if r.Kind == "request" {
			reqRows++
		}
	}
	if reqRows != 1 {
		t.Fatalf("替换式写入应只留 1 条 request 行，实际 %d", reqRows)
	}
	out, err := a.ExportLatestRequest(sess)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("导出的不是合法 JSON: %v\n%s", err, out)
	}
	if msgs, _ := got["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("应导出最新一次请求（2 条消息），实际 %d 条: %s", len(msgs), out)
	}
	if !strings.Contains(string(out), "你好") {
		t.Errorf("导出内容不是最新请求:\n%s", out)
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
