package main

// deliver_result 合并后的针对性测试（deliver.go）：
//   - 只填 path：只登记交付卡片，无搬运作业；
//   - path + upload：卡片登记 + 轮末搬运作业；flush 执行后卡片路径改写为
//     原件位置——产品核心语义：搬运成功 → 卡片指 B；失败/没搬 → 卡片指 A
//     （源文件不动，卡片照常可打开）；
//   - 搬运失败（目标目录被同名文件占位，MkdirAll 兜底也救不回来）：卡片
//     保持 A、源文件未动；
//   - 计划模式：带 upload 拒绝（原 deliver_file 的硬拦语义收窄到 upload 参数），
//     不带照常登记；
//   - upload 解析：uid / 原件路径 / 不填自动按副本路径反查；解析不了报可读错误。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func deliverFixture(t *testing.T) (*App, *toolContext, *SessionMeta, string) {
	t.Helper()
	a := newCompactTestApp(t)
	ws := t.TempDir()
	a.Cfg.Workspace = ws
	sess := &SessionMeta{ID: "delsess", Title: "交付合并", Mode: "task", Workspace: ws, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	tctx := &toolContext{app: a, sess: sess, seq: 5, emit: func(Event) {}}
	return a, tctx, sess, ws
}

// seedUpload 模拟一次桌面上传：原件在外部目录、工作区副本在 .jxai/uploads。
func seedUpload(t *testing.T, a *App, sess *SessionMeta, ws, name, content string) *Upload {
	t.Helper()
	origDir := t.TempDir()
	orig := filepath.Join(origDir, name)
	if err := os.WriteFile(orig, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(ws, ".jxai", "uploads", name)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	u := &Upload{SessionID: sess.ID, UID: "u1", OrigPath: orig, CopyPath: copyPath, Name: name, Size: int64(len(content)), CreatedAt: now()}
	if err := a.Store.AddUpload(u); err != nil {
		t.Fatalf("登记上传失败: %v", err)
	}
	return u
}

func deliverCall(t *testing.T, tctx *toolContext, files ...map[string]any) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"files": files})
	out, err := deliverResultTool().Run(tctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("deliver_result 调用失败: %v", err)
	}
	return out
}

func deliveryPaths(t *testing.T, a *App, sid string) []string {
	t.Helper()
	dvs, err := a.Store.DeliveriesFor(sid)
	if err != nil {
		t.Fatalf("读交付登记失败: %v", err)
	}
	out := []string{}
	for _, d := range dvs {
		out = append(out, d.Path)
	}
	return out
}

// reportNote 包含指定子串的交付注记存在（kind=report 的【交付完成】注记）。
func hasReportNote(t *testing.T, a *App, sid, sub string) bool {
	t.Helper()
	rows, err := a.Store.RawMessagesAll(sid)
	if err != nil {
		t.Fatalf("读消息失败: %v", err)
	}
	for _, m := range rows {
		if m.Kind != kindReport {
			continue
		}
		if s, ok := m.Content.(string); ok && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestDeliverCardOnly 只填 path：卡片登记（工作区相对路径形态），不产生搬运作业。
func TestDeliverCardOnly(t *testing.T) {
	a, tctx, sess, ws := deliverFixture(t)
	src := filepath.Join(ws, "成果.md")
	if err := os.WriteFile(src, []byte("成品"), 0644); err != nil {
		t.Fatal(err)
	}
	out := deliverCall(t, tctx, map[string]any{"path": "成果.md"})
	if !strings.Contains(out, "已登记 1 个交付文件") {
		t.Fatalf("回执异常: %s", out)
	}
	if got := deliveryPaths(t, a, sess.ID); len(got) != 1 || got[0] != "成果.md" {
		t.Fatalf("卡片登记异常: %v", got)
	}
	if n := a.Store.PendingDeliveryCount(sess.ID); n != 0 {
		t.Fatalf("不应有搬运作业，实际 %d 项", n)
	}
}

// TestDeliverMoveRewritesCard 核心语义：upload 搬运成功后，卡片路径改写为
// 原件位置；源文件删除、原件位置内容为成品、被替代的旧文件快照备份。
func TestDeliverMoveRewritesCard(t *testing.T) {
	a, tctx, sess, ws := deliverFixture(t)
	u := seedUpload(t, a, sess, ws, "报告.docx", "原稿")
	if err := os.WriteFile(u.CopyPath, []byte("改好的成品"), 0644); err != nil {
		t.Fatal(err)
	}
	out := deliverCall(t, tctx, map[string]any{"path": u.CopyPath, "upload": "u1"})
	if !strings.Contains(out, "已登记 1 个交付文件") || !strings.Contains(out, "已登记轮末搬运") {
		t.Fatalf("回执异常: %s", out)
	}
	// 登记时卡片指向工作区副本（相对路径形态）
	wantRel := filepath.ToSlash(filepath.Join(".jxai", "uploads", "报告.docx"))
	if got := deliveryPaths(t, a, sess.ID); len(got) != 1 || got[0] != wantRel {
		t.Fatalf("登记时卡片应指向 %s: %v", wantRel, got)
	}
	if n := a.Store.PendingDeliveryCount(sess.ID); n != 1 {
		t.Fatalf("应有 1 项搬运作业，实际 %d", n)
	}
	// 轮末执行（flushDeliveries 本体同步跑完作业）
	a.flushDeliveries(sess.ID)
	// 卡片改写为原件位置（正斜杠绝对路径）
	wantTarget := filepath.ToSlash(u.OrigPath)
	if got := deliveryPaths(t, a, sess.ID); len(got) != 1 || got[0] != wantTarget {
		t.Fatalf("搬运成功后卡片应改指 %s: %v", wantTarget, got)
	}
	// 移动语义：原件位置是成品内容，工作区副本删除
	body, err := os.ReadFile(u.OrigPath)
	if err != nil || string(body) != "改好的成品" {
		t.Fatalf("原件位置内容异常: %q %v", body, err)
	}
	if _, err := os.Stat(u.CopyPath); !os.IsNotExist(err) {
		t.Fatalf("工作区副本应已删除: %v", err)
	}
	// 被替代的旧文件（原稿）快照备份到 .jxai/delivery-backups/
	backupDir := filepath.Join(ws, ".jxai", "delivery-backups", sess.ID)
	es, err := os.ReadDir(backupDir)
	if err != nil || len(es) != 1 {
		t.Fatalf("应有 1 份快照备份: %v", err)
	}
	old, err := os.ReadFile(filepath.Join(backupDir, es[0].Name()))
	if err != nil || string(old) != "原稿" {
		t.Fatalf("备份内容异常: %q %v", old, err)
	}
	// 注记落库（下一轮模型可见）
	if !hasReportNote(t, a, sess.ID, "【交付完成】已把") || !hasReportNote(t, a, sess.ID, "快照备份") {
		t.Fatal("缺【交付完成】注记")
	}
}

// TestDeliverMoveFailureKeepsCard 搬运失败：卡片保持 A、源文件未动、注记报失败。
func TestDeliverMoveFailureKeepsCard(t *testing.T) {
	a, tctx, sess, ws := deliverFixture(t)
	u := seedUpload(t, a, sess, ws, "报告.docx", "原稿")
	deliverCall(t, tctx, map[string]any{"path": u.CopyPath, "upload": "u1"})
	// 登记后目标目录被删、原地变同名文件占位：MkdirAll 兜底救不回来
	origDir := filepath.Dir(u.OrigPath)
	if err := os.RemoveAll(origDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(origDir, []byte("占位"), 0644); err != nil {
		t.Fatal(err)
	}
	a.flushDeliveries(sess.ID)
	// 失败：卡片保持登记时的相对路径，源文件还在
	wantRel := filepath.ToSlash(filepath.Join(".jxai", "uploads", "报告.docx"))
	if got := deliveryPaths(t, a, sess.ID); len(got) != 1 || got[0] != wantRel {
		t.Fatalf("失败后卡片应保持 %s: %v", wantRel, got)
	}
	if _, err := os.Stat(u.CopyPath); err != nil {
		t.Fatalf("失败后源文件应未动: %v", err)
	}
	if !hasReportNote(t, a, sess.ID, "交付《报告.docx》失败") {
		t.Fatal("缺失败注记")
	}
}

// TestDeliverPlanModeRejectsUpload 计划模式：带 upload 拒绝（不产生任何登记），
// 不带 upload 照常登记卡片。
func TestDeliverPlanModeRejectsUpload(t *testing.T) {
	a, tctx, sess, ws := deliverFixture(t)
	u := seedUpload(t, a, sess, ws, "报告.docx", "原稿")
	tctx.sess.PlanMode = true
	args, _ := json.Marshal(map[string]any{"files": []map[string]any{{"path": u.CopyPath, "upload": "u1"}}})
	if _, err := deliverResultTool().Run(tctx, json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "计划模式下不能登记搬运") {
		t.Fatalf("计划模式带 upload 应被拒: %v", err)
	}
	if got := deliveryPaths(t, a, sess.ID); len(got) != 0 {
		t.Fatalf("拒绝时不应产生登记: %v", got)
	}
	if n := a.Store.PendingDeliveryCount(sess.ID); n != 0 {
		t.Fatalf("拒绝时不应产生作业: %d", n)
	}
	_ = ws
	deliverCall(t, tctx, map[string]any{"path": u.CopyPath})
	if got := deliveryPaths(t, a, sess.ID); len(got) != 1 {
		t.Fatalf("计划模式不带 upload 应照常登记: %v", got)
	}
}

// TestDeliverUploadRefResolution upload 引用的三条解析路：显式给原件路径、
// 不给（按副本自动反查）、给不存在的编号报可读错误。
func TestDeliverUploadRefResolution(t *testing.T) {
	a, tctx, sess, ws := deliverFixture(t)
	u := seedUpload(t, a, sess, ws, "报告.docx", "原稿")

	// 显式给原件路径
	deliverCall(t, tctx, map[string]any{"path": u.CopyPath, "upload": u.OrigPath})
	if n := a.Store.PendingDeliveryCount(sess.ID); n != 1 {
		t.Fatalf("原件路径应解析成功: %d", n)
	}

	// 不给 upload：按 source（工作区副本）自动反查同一条上传记录
	deliverCall(t, tctx, map[string]any{"path": u.CopyPath})
	if n := a.Store.PendingDeliveryCount(sess.ID); n != 1 { // 同目标 pending 去重覆盖，仍 1
		t.Fatalf("自动反查后同目标作业应去重仍为 1: %d", n)
	}

	// 给不存在的编号：可读错误并列出可用编号
	args, _ := json.Marshal(map[string]any{"files": []map[string]any{{"path": u.CopyPath, "upload": "u9"}}})
	_, err := deliverResultTool().Run(tctx, json.RawMessage(args))
	if err == nil || !strings.Contains(err.Error(), "找不到上传记录 u9") || !strings.Contains(err.Error(), "u1=") {
		t.Fatalf("错误应列出可用编号: %v", err)
	}
}
