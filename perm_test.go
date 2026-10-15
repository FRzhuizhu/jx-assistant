package main

// 权限中途变更链路的测试：「权限变更」注记文案（permChangeNote）、注记随
// 环境快照落库（stashPermNote + maybeAppendEnvSnapshot，env 差分通道）、面板
// PATCH 暂存注记、运行指针每步合并（syncRunPerms）与热路径回写不覆盖配置列
// （TouchRunMeta）。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPermTestApp 建一个带临时库的 App（测试用完即删，只落本目录）。
func newPermTestApp(t *testing.T, dir string) *App {
	t.Helper()
	os.RemoveAll(dir)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.db.Close(); os.RemoveAll(dir) })
	return &App{Cfg: DefaultConfig(), Store: s}
}

func mustCreateSession(t *testing.T, a *App, sess *SessionMeta) {
	t.Helper()
	if err := a.Store.Create(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
}

func wantAll(t *testing.T, note, ctx string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(note, w) {
			t.Fatalf("%s 缺少 %q：\n%s", ctx, w, note)
		}
	}
}

func lastEnvRow(t *testing.T, a *App, sid string) string {
	t.Helper()
	rows, err := a.Store.RawMessagesAll(sid)
	if err != nil {
		t.Fatalf("读行失败: %v", err)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == kindEnv {
			return rows[i].Text()
		}
	}
	return ""
}

func countEnvRows(t *testing.T, a *App, sid string) int {
	t.Helper()
	rows, err := a.Store.RawMessagesAll(sid)
	if err != nil {
		t.Fatalf("读行失败: %v", err)
	}
	n := 0
	for _, r := range rows {
		if r.Kind == kindEnv {
			n++
		}
	}
	return n
}

// 场景一：升档（工作区内读写 → 完全访问，捆绑能力点亮）。
func TestPermChangeNoteUpgrade(t *testing.T) {
	s := &SessionMeta{Permission: "full",
		Tools: []string{capUnsandboxed, capBrowser, capComputer, capDevice}}
	note := permChangeNote(permSrcPanel, "workspace-write", []string{capBrowser}, s)
	wantAll(t, note, "升档注记",
		"权限变更：",
		"权限模式：工作区内读写 → 完全访问",
		"新增授权：完全访问——文件读写与命令执行不再受沙箱限制",
		"取消授权：工作区内读写——「文件写入仅限工作区",
		"来源："+permSrcPanel+"，立即生效")
	if strings.Contains(note, "\n") {
		t.Fatalf("注记必须是单行（追加进快照尾部的注记行）：\n%s", note)
	}
}

// 场景二：降档（完全访问 → 工作区内读写，捆绑能力随档位取消）。
func TestPermChangeNoteDowngrade(t *testing.T) {
	s := &SessionMeta{Permission: "workspace-write", Tools: []string{capBrowser}}
	note := permChangeNote(permSrcPanel, "full",
		[]string{capUnsandboxed, capBrowser, capComputer, capDevice}, s)
	wantAll(t, note, "降档注记",
		"权限模式：完全访问 → 工作区内读写",
		"取消授权：完全访问——「文件读写与命令执行不再受沙箱限制（助手自身配置/数据仍受保护）」的旧规则不再适用",
		"新增授权：工作区内读写——文件写入仅限工作区")
}

// 场景三：仅能力开关变化（权限模式不变，逐项说新增/取消与效果）。
func TestPermChangeNoteCapsOnly(t *testing.T) {
	s := &SessionMeta{Permission: "workspace-write", Tools: []string{capBrowser, capComputer}}
	note := permChangeNote(permSrcApproval, "workspace-write", []string{capBrowser}, s)
	wantAll(t, note, "能力开关注记",
		"权限模式不变（工作区内读写）",
		"新增能力：「操作电脑」computer_* 工具现在可用")

	rev := permChangeNote(permSrcApproval, "workspace-write", []string{capBrowser, capDevice}, s)
	wantAll(t, rev, "能力取消注记",
		"新增能力：「操作电脑」computer_* 工具现在可用",
		"取消能力：「操作手机」device_* / adb_* 工具调用将被拒绝")
}

// 无实际变化（归一化后同档同能力集合）不产出注记。
func TestPermChangeNoteNoChange(t *testing.T) {
	s := &SessionMeta{Permission: "workspace-write", Tools: []string{capBrowser}}
	if note := permChangeNote(permSrcPanel, "workspace-write", []string{capBrowser}, s); note != "" {
		t.Fatalf("无变化应返回空串，得到：\n%s", note)
	}
	// 官方别名归一化到同档也算无变化
	s2 := &SessionMeta{Permission: "danger-full-access"}
	if note := permChangeNote(permSrcPanel, "full", nil, s2); note != "" {
		t.Fatalf("别名同档应返回空串，得到：\n%s", note)
	}
}

// 注记走 env 通道：暂存的权限变更注记随下一次环境快照落库（即使快照正文
// 没变），落一次即清——不自我累积、不重复记账。
func TestPermNoteRidesEnvSnapshot(t *testing.T) {
	a := newPermTestApp(t, "data-perm-env-test")
	sess := &SessionMeta{ID: "s1", Mode: "task", Permission: "workspace-write",
		Tools: []string{capBrowser}, CreatedAt: "2026-10-08 09:00:00"}
	mustCreateSession(t, a, sess)

	env := "# 运行环境快照\n- 权限模式：工作区内读写\n"
	if got := a.maybeAppendEnvSnapshot(sess, env, nil); got != env {
		t.Fatalf("首份快照应原样落库，得到：\n%s", got)
	}
	if n := countEnvRows(t, a, "s1"); n != 1 {
		t.Fatalf("env 行数 = %d，期望 1", n)
	}

	// 权限升档：暂存注记；快照正文也变了（权限行不同）
	a.stashPermNote("s1", permChangeNote(permSrcPanel, "workspace-write",
		[]string{capBrowser}, &SessionMeta{Permission: "full", Tools: []string{
			capUnsandboxed, capBrowser, capComputer, capDevice}}))
	env2 := "# 运行环境快照\n- 权限模式：完全访问\n"
	got := a.maybeAppendEnvSnapshot(sess, env2, nil)
	wantAll(t, got, "升档后的快照",
		"- 权限模式：完全访问", "（权限变更：权限模式：工作区内读写 → 完全访问")
	if n := countEnvRows(t, a, "s1"); n != 2 {
		t.Fatalf("注记应随新快照落第二条 env 行，行数 = %d", n)
	}

	// 正文与注记都没变：不再落新行
	if got2 := a.maybeAppendEnvSnapshot(sess, env2, nil); got2 != env2 {
		t.Fatalf("无变化不应再落快照，得到：\n%s", got2)
	}
	if n := countEnvRows(t, a, "s1"); n != 2 {
		t.Fatalf("无变化时 env 行数 = %d，期望仍是 2", n)
	}

	// 正文没变、注记待落（如仅点亮「允许工作区外执行命令」这类不进快照正文
	// 的能力开关）：仍落一条「正文相同 + 注记新行」的快照
	a.stashPermNote("s1", "权限变更：新增能力：「允许工作区外执行命令」")
	got3 := a.maybeAppendEnvSnapshot(sess, env2, nil)
	if got3 != env2+"\n（权限变更：新增能力：「允许工作区外执行命令」）" {
		t.Fatalf("注记独落一条的快照内容不符：\n%s", got3)
	}
	if n := countEnvRows(t, a, "s1"); n != 3 {
		t.Fatalf("注记独落时 env 行数 = %d，期望 3", n)
	}

	// 旧注记行参与下次比较时被剔除（stripEnvNotes）：正文回到 env2 不算变化
	if got4 := a.maybeAppendEnvSnapshot(sess, env2, nil); got4 != env2 {
		t.Fatalf("旧注记剔除后正文相同不应落快照，得到：\n%s", got4)
	}
	if lastEnvRow(t, a, "s1") == env2 {
		t.Fatalf("最后一条 env 行应仍是带注记的版本")
	}
}

// stripEnvNotes：剔除快照尾部的技能/权限变更注记行，其他括号行不受影响。
func TestStripEnvNotes(t *testing.T) {
	base := "# 快照\n- 甲：1"
	if got := stripEnvNotes(base + "\n（技能变更：新增技能：X）"); got != base {
		t.Fatalf("技能注记未剔净：%q", got)
	}
	if got := stripEnvNotes(base + "\n（权限变更：权限模式：A → B）\n（技能变更：新增技能：X）"); got != base {
		t.Fatalf("双注记未剔净：%q", got)
	}
	if got := stripEnvNotes(base + "\n（普通括号行）"); got != base+"\n（普通括号行）" {
		t.Fatalf("非注记括号行被误剔：%q", got)
	}
}

// 面板 PATCH 改权限：变更瞬间即落 env 快照行（注记随行携带，不落 report，
// 不等下一次组装——落库时机与发送时机分离）。
func TestPatchSessionLandsEnvImmediately(t *testing.T) {
	a := newPermTestApp(t, "data-perm-patch-test")
	mustCreateSession(t, a, &SessionMeta{ID: "s1", Title: "权限注入测试", Mode: "task",
		Permission: "workspace-write", Tools: []string{capBrowser}, CreatedAt: "2026-10-08 09:00:00"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/api/sessions/s1", strings.NewReader(`{"permission":"full"}`))
	r.SetPathValue("id", "s1")
	a.handlePatchSession(w, r)
	if w.Code != 200 {
		t.Fatalf("PATCH 状态码 = %d，正文 %s", w.Code, w.Body.String())
	}

	// 变更瞬间已落第一条 env 快照行，注记随行携带
	if n := countEnvRows(t, a, "s1"); n != 1 {
		t.Fatalf("PATCH 后 env 行数 = %d，期望 1（立即落地）", n)
	}
	row := lastEnvRow(t, a, "s1")
	wantAll(t, row, "PATCH 即落的快照",
		"权限模式：完全访问", "（权限变更：权限模式：工作区内读写 → 完全访问", permSrcPanel)

	// 注记已随行消费，不留暂存；没有 report 行（走 env 通道）
	if note := a.takePermNote("s1"); note != "" {
		t.Fatalf("注记应随快照消费，残留：%s", note)
	}
	rows, _ := a.Store.RawMessagesAll("s1")
	for _, row := range rows {
		if row.Kind == kindReport && row.Origin == "perm" {
			t.Fatalf("权限变更不应落 report 行（走 env 通道），得到：\n%s", row.Text())
		}
	}

	// 下一次组装（同一正文重算，即剥离注记后的内容）等值比较不重复落
	if got := a.maybeAppendEnvSnapshot(
		&SessionMeta{ID: "s1", Permission: "full", Tools: []string{
			capUnsandboxed, capBrowser, capComputer, capDevice}, Mode: "task"},
		stripEnvNotes(row), nil); got != stripEnvNotes(row) {
		t.Fatalf("等值快照不应重复落，得到：\n%s", got)
	}
	if n := countEnvRows(t, a, "s1"); n != 1 {
		t.Fatalf("等值后 env 行数 = %d，期望仍是 1", n)
	}
}

// PATCH 其他环境条目（计划模式）也立即落快照；权限无变化则不携带注记。
func TestPatchSessionLandsEnvForPlanMode(t *testing.T) {
	a := newPermTestApp(t, "data-perm-plan-test")
	mustCreateSession(t, a, &SessionMeta{ID: "s1", Mode: "task",
		Permission: "workspace-write", CreatedAt: "2026-10-08 09:00:00"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/api/sessions/s1", strings.NewReader(`{"planMode":true}`))
	r.SetPathValue("id", "s1")
	a.handlePatchSession(w, r)
	if w.Code != 200 {
		t.Fatalf("PATCH 状态码 = %d", w.Code)
	}
	if n := countEnvRows(t, a, "s1"); n != 1 {
		t.Fatalf("计划模式变更应立即落 env 行，行数 = %d", n)
	}
	if row := lastEnvRow(t, a, "s1"); strings.Contains(row, "权限变更") {
		t.Fatalf("权限无变化不应携带权限注记：\n%s", row)
	}
}

// PATCH 只改标题（权限无变化）不暂存注记。
func TestPatchSessionNoPermNoNote(t *testing.T) {
	a := newPermTestApp(t, "data-perm-notitle-test")
	mustCreateSession(t, a, &SessionMeta{ID: "s1", Mode: "task",
		Permission: "workspace-write", CreatedAt: "2026-10-08 09:00:00"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PATCH", "/api/sessions/s1", strings.NewReader(`{"title":"新标题"}`))
	r.SetPathValue("id", "s1")
	a.handlePatchSession(w, r)
	if w.Code != 200 {
		t.Fatalf("PATCH 状态码 = %d", w.Code)
	}
	if note := a.takePermNote("s1"); note != "" {
		t.Fatalf("权限无变化不应暂存注记，得到：\n%s", note)
	}
}

// syncRunPerms：库里的新权限合并进运行指针，用量等运行态不被库里的旧值覆盖。
func TestSyncRunPerms(t *testing.T) {
	a := newPermTestApp(t, "data-perm-sync-test")
	mustCreateSession(t, a, &SessionMeta{ID: "s1", Mode: "task",
		Permission: "full", Tools: []string{capUnsandboxed, capBrowser, capComputer, capDevice},
		CreatedAt: "2026-10-08 09:00:00"})

	live := &SessionMeta{ID: "s1", Permission: "workspace-write", Tools: []string{capBrowser},
		UsagePrompt: 42, UsageCalls: 3}
	a.syncRunPerms(live)
	if live.Permission != "full" {
		t.Fatalf("运行指针权限 = %s，期望 full", live.Permission)
	}
	if len(live.Tools) != 4 || live.Tools[0] != capUnsandboxed {
		t.Fatalf("运行指针能力表 = %v，期望四项捆绑", live.Tools)
	}
	if live.UsagePrompt != 42 || live.UsageCalls != 3 {
		t.Fatalf("运行态用量被覆盖：prompt=%d calls=%d", live.UsagePrompt, live.UsageCalls)
	}
}

// TouchRunMeta：热路径回写只碰运行列——陈旧指针回写后，库里的权限改动仍在。
func TestTouchRunMetaKeepsPermColumns(t *testing.T) {
	a := newPermTestApp(t, "data-perm-touch-test")
	mustCreateSession(t, a, &SessionMeta{ID: "s1", Mode: "task",
		Permission: "read-only", Tools: []string{}, CreatedAt: "2026-10-08 09:00:00"})

	// 运行体持有陈旧指针（还是旧权限），用户刚把库改成 full
	stale := &SessionMeta{ID: "s1", Permission: "workspace-write", Tools: []string{capBrowser},
		UsagePrompt: 100, UsageCalls: 2}
	if err := a.Store.TouchRunMeta(stale); err != nil {
		t.Fatalf("TouchRunMeta 失败: %v", err)
	}
	fresh, err := a.Store.Get("s1")
	if err != nil {
		t.Fatalf("读会话失败: %v", err)
	}
	if fresh.Permission != "read-only" {
		t.Fatalf("权限列被热路径回写覆盖：=%s，期望 read-only", fresh.Permission)
	}
	if fresh.UsagePrompt != 100 || fresh.UsageCalls != 2 {
		t.Fatalf("用量列未回写：prompt=%d calls=%d", fresh.UsagePrompt, fresh.UsageCalls)
	}
}

// ---------- 只读档提权申请（拒绝驱动的档位选择 + request_permission 工具） ----------

// roTestWS 建一个**不在系统临时目录下**的测试工作区（t.TempDir 落在 %TEMP%
// 里，而写围栏的可写根含系统临时目录——放那里就测不出「围栏外」分支了）。
func roTestWS(t *testing.T) string {
	t.Helper()
	ws, err := os.MkdirTemp(".", "data-ro-ws-")
	if err != nil {
		t.Fatalf("建测试工作区失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ws) })
	return ws
}

func roCtx(t *testing.T, a *App, sess *SessionMeta) *toolContext {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return &toolContext{ctx: ctx, app: a, sess: sess, toolName: "write"}
}

// 只读档写升级：围栏内目标给 workspace-write 档、围栏外给 full；
// 「允许」只按申请档放行本次，「始终允许」把会话切到申请档并落注记。
func TestReadOnlyWriteEscalateWorkspaceTier(t *testing.T) {
	a := newCapTestApp(t, "data-ro-esc-test")
	ws := roTestWS(t)
	sess := &SessionMeta{ID: "s1", Mode: "task", Permission: permReadOnly, Workspace: ws,
		CreatedAt: "2026-10-13 03:00:00"}
	mustCreateSession(t, a, sess)
	inside := filepath.Join(ws, "demo.txt")
	outside, _ := filepath.Abs("ro-outside-demo.txt") // 仓库根：不在工作区也不在临时目录

	// 1. 未带升级参数：围栏内目标提示 workspace-write 档 + request_permission
	tc := roCtx(t, a, sess)
	_, err := tc.authorize(permAction{Op: "write", Tool: "write", Path: inside})
	if err == nil {
		t.Fatalf("只读档写入应被拒")
	}
	wantAll(t, err.Error(), "read-only mode", "sandbox_permissions=workspace-write", "request_permission")

	// 2. 围栏外目标（仍只读）：提示 full 档；带 workspace-write 申请无效、指回 full
	_, err = tc.authorize(permAction{Op: "write", Tool: "write", Path: outside})
	if err == nil || !strings.Contains(err.Error(), "sandbox_permissions=full") {
		t.Fatalf("围栏外应提示 full 档，得到：%v", err)
	}
	_, err = tc.authorize(permAction{Op: "write", Tool: "write", Path: outside,
		EscalateTo: permWorkspace, Justification: "写工作区外"})
	if err == nil || !strings.Contains(err.Error(), "sandbox_permissions=full") {
		t.Fatalf("围栏外的 workspace-write 申请应被指到 full，得到：%v", err)
	}

	// 3. 缺 justification：拒绝
	if _, err := tc.authorize(permAction{Op: "write", Tool: "write", Path: inside,
		EscalateTo: permWorkspace}); err == nil || !strings.Contains(err.Error(), "justification") {
		t.Fatalf("缺理由应报错，得到：%v", err)
	}

	// 4. 「允许」只放行本次：凭证按 workspace-write 签发，会话档位不变
	errc := make(chan error, 1)
	go func() { errc <- decideWhenRaised(a, true, false) }()
	g, err := tc.authorize(permAction{Op: "write", Tool: "write", Path: inside,
		EscalateTo: permWorkspace, Justification: "任务需要落盘脚本"})
	if err != nil {
		t.Fatalf("「允许」应按 workspace-write 放行本次，得到：%v", err)
	}
	if e := <-errc; e != nil {
		t.Fatalf("替用户点「允许」失败: %v", e)
	}
	if g == nil || g.Policy != permWorkspace {
		t.Fatalf("凭证应按 workspace-workpace 签发，得到 %+v", g)
	}
	if permModeOf(sess) != permReadOnly {
		t.Fatalf("「允许」不应改会话档位，仍是 %s", permModeOf(sess))
	}

	// 5. 「始终允许」：会话切到 workspace-write、落库、落「权限变更」注记
	errc = make(chan error, 1)
	go func() { errc <- decideWhenRaised(a, true, true) }()
	if _, err := tc.authorize(permAction{Op: "write", Tool: "write", Path: inside,
		EscalateTo: permWorkspace, Justification: "任务需要落盘脚本"}); err != nil {
		t.Fatalf("「始终允许」应放行，得到：%v", err)
	}
	if e := <-errc; e != nil {
		t.Fatalf("替用户点「始终允许」失败: %v", e)
	}
	if permModeOf(sess) != permWorkspace {
		t.Fatalf("「始终允许」应把会话切到 workspace-write，仍是 %s", permModeOf(sess))
	}
	fresh, err := a.Store.Get("s1")
	if err != nil || fresh == nil || permModeOf(fresh) != permWorkspace {
		t.Fatalf("切档应已落库：err=%v", err)
	}
	if row := lastEnvRow(t, a, "s1"); !strings.Contains(row, "权限变更") {
		t.Fatalf("「权限变更」注记应随 env 快照落地：\n%s", row)
	}
}

// request_permission 工具：参数校验、拒绝终局、「允许」立即切档并落注记。
func TestRequestPermissionTool(t *testing.T) {
	a := newCapTestApp(t, "data-reqperm-test")
	sess := &SessionMeta{ID: "s1", Mode: "task", Permission: permReadOnly,
		Workspace: roTestWS(t), CreatedAt: "2026-10-13 03:00:00"}
	mustCreateSession(t, a, sess)
	tool := requestPermissionTool()

	mk := func(args string) (*toolContext, json.RawMessage) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		return &toolContext{ctx: ctx, app: a, sess: sess, toolName: "request_permission"}, json.RawMessage(args)
	}

	// 1. 非法 target：拒绝（read-only 不在枚举里，同样从这里拦下）
	tc, raw := mk(`{"target":"everything","justification":"x"}`)
	if _, err := tool.Run(tc, raw); err == nil || !strings.Contains(err.Error(), "target 仅支持") {
		t.Fatalf("非法 target 应拒绝，得到：%v", err)
	}

	// 2. 缺 justification：拒绝
	tc, raw = mk(`{"target":"workspace-write"}`)
	if _, err := tool.Run(tc, raw); err == nil || !strings.Contains(err.Error(), "justification") {
		t.Fatalf("缺理由应拒绝，得到：%v", err)
	}

	// 3. 用户拒绝：报错终局，会话保持只读
	errc := make(chan error, 1)
	go func() { errc <- decideWhenRaised(a, false, false) }()
	tc, raw = mk(`{"target":"workspace-write","justification":"需要写工作区脚本"}`)
	if _, err := tool.Run(tc, raw); err == nil || !strings.Contains(err.Error(), "保持") {
		t.Fatalf("拒绝应报错且说明档位保持，得到：%v", err)
	}
	if e := <-errc; e != nil {
		t.Fatalf("替用户点「拒绝」失败: %v", e)
	}
	if permModeOf(sess) != permReadOnly {
		t.Fatalf("拒绝后档位应保持只读")
	}

	// 4. 用户允许：立即切档、落库、落「权限变更」注记
	errc = make(chan error, 1)
	go func() { errc <- decideWhenRaised(a, true, true) }()
	tc, raw = mk(`{"target":"workspace-write","justification":"需要写工作区脚本"}`)
	out, err := tool.Run(tc, raw)
	if err != nil {
		t.Fatalf("「允许」应切档成功，得到：%v", err)
	}
	if e := <-errc; e != nil {
		t.Fatalf("替用户点「允许」失败: %v", e)
	}
	if !strings.Contains(out, "工作区内读写") {
		t.Fatalf("成功文案应说明新档：%s", out)
	}
	if permModeOf(sess) != permWorkspace {
		t.Fatalf("会话应已切到 workspace-write，仍是 %s", permModeOf(sess))
	}
	fresh, err := a.Store.Get("s1")
	if err != nil || fresh == nil || permModeOf(fresh) != permWorkspace {
		t.Fatalf("切档应已落库：err=%v", err)
	}
	if row := lastEnvRow(t, a, "s1"); !strings.Contains(row, "权限变更") {
		t.Fatalf("「权限变更」注记应落地：\n%s", row)
	}

	// 5. 已是目标档再申请（不比当前档宽）：拒绝
	if _, err := tool.Run(tc, json.RawMessage(`{"target":"workspace-write","justification":"再宽点"}`)); err == nil || !strings.Contains(err.Error(), "不比它宽") {
		t.Fatalf("平级 target 应拒绝，得到：%v", err)
	}
}

// requireNotReadOnly：只读档拒绝并指路，非只读放行（MCP/register_skill 门）。
func TestRequireNotReadOnly(t *testing.T) {
	a := newCapTestApp(t, "data-ro-gate-test")
	ro := &SessionMeta{ID: "s1", Mode: "task", Permission: permReadOnly, CreatedAt: "2026-10-13 03:00:00"}
	rw := &SessionMeta{ID: "s2", Mode: "task", Permission: permWorkspace, CreatedAt: "2026-10-13 03:00:00"}
	mustCreateSession(t, a, ro)
	mustCreateSession(t, a, rw)
	tcRW := &toolContext{ctx: context.Background(), app: a, sess: rw, toolName: "mcp_call_tool"}
	if err := requireNotReadOnly(tcRW, "调用 MCP"); err != nil {
		t.Fatalf("非只读档应放行：%v", err)
	}
	tcRO := &toolContext{ctx: context.Background(), app: a, sess: ro, toolName: "mcp_call_tool"}
	err := requireNotReadOnly(tcRO, "调用 MCP")
	if err == nil {
		t.Fatalf("只读档应拒绝")
	}
	wantAll(t, err.Error(), "只读模式", "request_permission", "权限芯片")
}
