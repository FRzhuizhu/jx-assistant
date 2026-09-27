package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 交付链路测试脚手架：临时工作区 + 外部「用户上传目录」+ 一个本地会话。
type deliverFixture struct {
	a       *App
	sess    *SessionMeta
	ws      string // 会话工作区
	userDir string // 用户上传目录（工作区之外）
}

func newDeliverFixture(t *testing.T) *deliverFixture {
	t.Helper()
	a := newTestApp(t, "rule", 5, 5)
	ws := t.TempDir()
	userDir := t.TempDir()
	sess := &SessionMeta{ID: "s1", Title: "交付测试", Mode: "task", Workspace: ws, CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	return &deliverFixture{a: a, sess: sess, ws: ws, userDir: userDir}
}

func (f *deliverFixture) writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写 %s 失败: %v", path, err)
	}
}

// 场景一（主链路）：登记交付 → 会话结束执行 → 被替代文件有快照、源文件被移走、
// 执行结果以模型可见注记落库。
func TestDeliverFileReplaceWithSnapshot(t *testing.T) {
	f := newDeliverFixture(t)
	orig := filepath.Join(f.userDir, "报告.docx")
	f.writeFile(t, orig, "旧版正文")
	// 登记上传（模拟桌面上传采集到的原件路径）
	if err := f.a.Store.AddUpload(&Upload{SessionID: "s1", OrigPath: orig, CopyPath: filepath.Join(f.ws, "报告.docx"), Name: "报告.docx", Size: 24, CreatedAt: now()}); err != nil {
		t.Fatalf("登记上传失败: %v", err)
	}
	// 模型改完的成品（工作区内、换了个名字）
	src := filepath.Join(f.ws, "报告-修改版.docx")
	f.writeFile(t, src, "新版正文")

	out, err := runDeliverFile(&toolContext{app: f.a, sess: f.sess, emit: func(Event) {}},
		[]byte(`{"source":"报告-修改版.docx","upload":"u1"}`))
	if err != nil {
		t.Fatalf("登记交付失败: %v", err)
	}
	for _, want := range []string{"本轮结束后执行", "不会结束对话", "u1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("工具回执应包含 %q：%s", want, out)
		}
	}
	if n := f.a.Store.PendingDeliveryCount("s1"); n != 1 {
		t.Fatalf("待交付应为 1 项，实际 %d", n)
	}

	f.a.flushDeliveries("s1") // 模拟本轮结束

	// 原位置已被替换为新内容，且是原子替换后的完整文件
	if b, err := os.ReadFile(orig); err != nil || string(b) != "新版正文" {
		t.Fatalf("目标文件应被替换为新版正文，得到 %q err=%v", string(b), err)
	}
	// 源文件已被移走（移动语义）
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("源文件应已被移动走，stat err=%v", err)
	}
	// 快照备份：工作区 .jxai/delivery-backups/s1/ 下有旧版内容的备份
	backupDir := filepath.Join(f.ws, ".jxai", "delivery-backups", "s1")
	entries, err := os.ReadDir(backupDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("应有一个快照备份文件，err=%v n=%d", err, len(entries))
	}
	if b, err := os.ReadFile(filepath.Join(backupDir, entries[0].Name())); err != nil || string(b) != "旧版正文" {
		t.Fatalf("快照应保存旧版正文，得到 %q err=%v", string(b), err)
	}
	// 执行结果注记已落库且模型可见
	raw, _ := f.a.Store.RawMessagesAll("s1")
	if len(raw) != 1 || !strings.Contains(raw[0].Text(), "【交付完成】") {
		t.Fatalf("应有一条【交付完成】注记，实际 %d 条", len(raw))
	}
	if !strings.Contains(raw[0].Text(), "已把") || !strings.Contains(raw[0].Text(), "移动到") || !strings.Contains(raw[0].Text(), "快照备份") {
		t.Fatalf("注记应说明移动与快照：%s", raw[0].Text())
	}
	seen := f.a.buildContext(f.sess, "sys")
	if len(seen) == 0 || !strings.Contains(seen[len(seen)-1].Text(), "【交付完成】") {
		t.Fatalf("模型下一轮应能看到执行结果注记")
	}
}

// 场景二：目标处没有同名文件 → 不产生快照，注记说明未发生替代。
func TestDeliverFileNewNameNoSnapshot(t *testing.T) {
	f := newDeliverFixture(t)
	orig := filepath.Join(f.userDir, "原件.txt")
	f.writeFile(t, orig, "原件")
	_ = f.a.Store.AddUpload(&Upload{SessionID: "s1", OrigPath: orig, CopyPath: "", Name: "原件.txt", Size: 8, CreatedAt: now()})
	src := filepath.Join(f.ws, "成品.txt")
	f.writeFile(t, src, "新文件")
	if _, err := runDeliverFile(&toolContext{app: f.a, sess: f.sess, emit: func(Event) {}},
		[]byte(`{"source":"`+filepath.Base(src)+`","upload":"`+strings.ReplaceAll(orig, `\`, `\\`)+`","filename":"新名字.txt"}`)); err != nil {
		t.Fatalf("登记交付失败: %v", err)
	}
	f.a.flushDeliveries("s1")
	target := filepath.Join(f.userDir, "新名字.txt")
	if b, err := os.ReadFile(target); err != nil || string(b) != "新文件" {
		t.Fatalf("应以新文件名交付，得到 %q err=%v", string(b), err)
	}
	raw, _ := f.a.Store.RawMessagesAll("s1")
	if len(raw) != 1 || !strings.Contains(raw[0].Text(), "未发生替代") {
		t.Fatalf("注记应说明未发生替代：%v", raw)
	}
	if _, err := os.Stat(filepath.Join(f.ws, ".jxai", "delivery-backups", "s1")); !os.IsNotExist(err) {
		t.Fatalf("不应产生快照目录，err=%v", err)
	}
}

// 同目标重复登记：pending 去重为一条，后登记的源文件生效。
func TestAddDeliveryJobDedup(t *testing.T) {
	f := newDeliverFixture(t)
	orig := filepath.Join(f.userDir, "a.txt")
	f.writeFile(t, orig, "x")
	_ = f.a.Store.AddUpload(&Upload{SessionID: "s1", OrigPath: orig, CopyPath: "", Name: "a.txt", Size: 1, CreatedAt: now()})
	f.writeFile(t, filepath.Join(f.ws, "一版.txt"), "1")
	f.writeFile(t, filepath.Join(f.ws, "二版.txt"), "2")
	tc := &toolContext{app: f.a, sess: f.sess, emit: func(Event) {}}
	if _, err := runDeliverFile(tc, []byte(`{"source":"一版.txt","upload":"u1"}`)); err != nil {
		t.Fatalf("首次登记失败: %v", err)
	}
	if _, err := runDeliverFile(tc, []byte(`{"source":"二版.txt","upload":"u1"}`)); err != nil {
		t.Fatalf("二次登记失败: %v", err)
	}
	if n := f.a.Store.PendingDeliveryCount("s1"); n != 1 {
		t.Fatalf("同目标应去重为 1 条 pending，实际 %d", n)
	}
	f.a.flushDeliveries("s1")
	if b, _ := os.ReadFile(orig); string(b) != "2" {
		t.Fatalf("应交付后登记的版本，得到 %q", string(b))
	}
	// 一版仍留在工作区（没被搬走——被二版覆盖了登记）
	if _, err := os.Stat(filepath.Join(f.ws, "一版.txt")); err != nil {
		t.Fatalf("被覆盖登记的源文件不该被移动：%v", err)
	}
}

// 工具校验路径：远程会话、找不到上传记录、文件名带分隔符、工作区外的源。
func TestDeliverFileValidation(t *testing.T) {
	f := newDeliverFixture(t)
	orig := filepath.Join(f.userDir, "a.txt")
	f.writeFile(t, orig, "x")
	_ = f.a.Store.AddUpload(&Upload{SessionID: "s1", OrigPath: orig, CopyPath: "", Name: "a.txt", Size: 1, CreatedAt: now()})
	src := filepath.Join(f.ws, "ok.txt")
	f.writeFile(t, src, "ok")

	remote := &SessionMeta{ID: "s2", Mode: "task", Remote: &RemoteWorkspace{Host: "h", Dir: "/tmp"}}
	if _, err := runDeliverFile(&toolContext{app: f.a, sess: remote, emit: func(Event) {}}, []byte(`{"source":"/tmp/x","upload":"u1"}`)); err == nil || !strings.Contains(err.Error(), "本地会话") {
		t.Fatalf("远程会话应被拒并说明原因，err=%v", err)
	}
	tc := &toolContext{app: f.a, sess: f.sess, emit: func(Event) {}}
	if _, err := runDeliverFile(tc, []byte(`{"source":"ok.txt","upload":"u9"}`)); err == nil || !strings.Contains(err.Error(), "u1") {
		t.Fatalf("未知编号应报错并列出可用编号，err=%v", err)
	}
	if _, err := runDeliverFile(tc, []byte(`{"source":"ok.txt","upload":"u1","filename":"a/b.txt"}`)); err == nil || !strings.Contains(err.Error(), "分隔符") {
		t.Fatalf("文件名带分隔符应被拒，err=%v", err)
	}
	outside := filepath.Join(f.userDir, "外面.txt")
	f.writeFile(t, outside, "外")
	if _, err := runDeliverFile(tc, []byte(`{"source":"`+strings.ReplaceAll(outside, `\`, `\\`)+`","upload":"u1"}`)); err == nil || !strings.Contains(err.Error(), "工作区内") {
		t.Fatalf("工作区外的源应被拒，err=%v", err)
	}
	// 目标目录消失
	_ = os.RemoveAll(f.userDir)
	if _, err := runDeliverFile(tc, []byte(`{"source":"ok.txt","upload":"u1"}`)); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("目标目录消失应在登记时报错，err=%v", err)
	}
}

func TestSanitizeDeliveryName(t *testing.T) {
	if n, err := sanitizeDeliveryName(""); err != nil || n != "" {
		t.Fatalf("空名应通过（表示未指定），得到 %q err=%v", n, err)
	}
	if n, err := sanitizeDeliveryName("  报告.docx "); err != nil || n != "报告.docx" {
		t.Fatalf("应去首尾空白，得到 %q err=%v", n, err)
	}
	for _, bad := range []string{"a/b", `a\b`, "..", "."} {
		if _, err := sanitizeDeliveryName(bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
}

// 启动恢复：卡在 running 的作业复位回 pending 后可被统一执行。
func TestResetStuckJobsAndFlushAll(t *testing.T) {
	f := newDeliverFixture(t)
	orig := filepath.Join(f.userDir, "a.txt")
	f.writeFile(t, orig, "旧")
	_ = f.a.Store.AddUpload(&Upload{SessionID: "s1", OrigPath: orig, CopyPath: "", Name: "a.txt", Size: 3, CreatedAt: now()})
	src := filepath.Join(f.ws, "新.txt")
	f.writeFile(t, src, "新")
	// 先按正常路径登记（pending），再模拟执行到一半崩溃（翻成 running）
	job := &DeliveryJob{SessionID: "s1", SourcePath: src, TargetDir: f.userDir, TargetName: "a.txt", CreatedAt: now()}
	if err := f.a.Store.AddDeliveryJob(job); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	job.Status = "running"
	_ = f.a.Store.UpdateDeliveryJob(job)
	// 模拟崩溃后重启：复位 + 全量 flush（空会话号 = 全部）
	if n := f.a.Store.ResetStuckJobs(); n != 1 {
		t.Fatalf("应复位 1 条 running，实际 %d", n)
	}
	f.a.flushDeliveries("")
	if b, _ := os.ReadFile(orig); string(b) != "新" {
		t.Fatalf("恢复后应完成交付，得到 %q", string(b))
	}
}

// 桌面路径上传（HTTP 层）：登记 uploads 行、注入注记；原件在工作区内时
// 不做自我复制。
func TestHandleUploadByPath(t *testing.T) {
	f := newDeliverFixture(t)
	orig := filepath.Join(f.userDir, "外来的.docx")
	f.writeFile(t, orig, "外部内容")

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/upload", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", "s1")
		rec := httptest.NewRecorder()
		f.a.handleUpload(rec, req)
		return rec
	}
	rec := post(`{"path":"` + strings.ReplaceAll(orig, `\`, `\\`) + `"}`)
	if rec.Code != 200 {
		t.Fatalf("路径上传应成功，得到 %d：%s", rec.Code, rec.Body.String())
	}
	// 工作区副本已就位
	if b, err := os.ReadFile(filepath.Join(f.ws, "外来的.docx")); err != nil || string(b) != "外部内容" {
		t.Fatalf("工作区副本缺失或内容不对，err=%v", err)
	}
	ups, _ := f.a.Store.UploadsFor("s1")
	if len(ups) != 1 || ups[0].UID != "u1" || ups[0].OrigPath != orig {
		t.Fatalf("上传登记不符：%+v", ups)
	}
	raw, _ := f.a.Store.RawMessagesAll("s1")
	if len(raw) != 1 {
		t.Fatalf("应注入一条注记，实际 %d 条", len(raw))
	}
	for _, want := range []string{"【文件上传】", "u1", filepath.ToSlash(orig), "交付目标"} {
		if !strings.Contains(raw[0].Text(), want) {
			t.Fatalf("注记应包含 %q：%s", want, raw[0].Text())
		}
	}

	// 原件就在工作区内：不再复制（内容不被自己覆盖），注记说明位于工作区
	inside := filepath.Join(f.ws, "已在.docx")
	f.writeFile(t, inside, "原地内容")
	rec = post(`{"path":"` + strings.ReplaceAll(inside, `\`, `\\`) + `"}`)
	if rec.Code != 200 {
		t.Fatalf("工作区内原件上传应成功，得到 %d：%s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(inside); string(b) != "原地内容" {
		t.Fatalf("工作区内原件内容不应变化，得到 %q", string(b))
	}
	ups, _ = f.a.Store.UploadsFor("s1")
	if len(ups) != 2 || ups[1].UID != "u2" {
		t.Fatalf("第二次登记应为 u2，实际 %+v", ups)
	}
	raw, _ = f.a.Store.RawMessagesAll("s1")
	if !strings.Contains(raw[1].Text(), "工作区内") {
		t.Fatalf("工作区内原件的注记应说明位于工作区：%s", raw[1].Text())
	}
}
