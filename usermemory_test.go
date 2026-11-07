package main

// 用户记忆组件（usermemory.go）的自检测试：
//   - md 文档解析/渲染往返（含手工编辑容错）
//   - 长度策略（用户拍板）：存储不截断（md 存全文）、模型侧入口（POST/工具）
//     超长整单报错不写入、注入侧对超长字段截断防快照膨胀
//   - 组件开关：envPrompt 注入生效/关闭即移除、工具挂载/卸载
//   - 专用工具只动「用户身份与喜好」记忆段（称呼/AI 名字/身份不被改）
//   - 经理整文件通道：GET/POST /api/user-memory 在 call_api 目录里、
//     按字段保存语义正确、组件关闭时 404

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// newUserMemoryTestApp 建带临时库的 App，并挂上真实组件清单（把仓库里的
// user-memory manifest 复制进临时组件目录——todo_done_test 同款手法）。
func newUserMemoryTestApp(t *testing.T) *App {
	t.Helper()
	a := newCompactTestApp(t)
	a.initRuntime()
	a.sessLocks = map[string]*sync.Mutex{}
	compDir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("组件", userMemoryComponentID, "manifest.json"))
	if err != nil {
		t.Fatalf("读组件清单失败: %v", err)
	}
	dst := filepath.Join(compDir, userMemoryComponentID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, src, 0644); err != nil {
		t.Fatal(err)
	}
	a.Components = InitComponents(compDir, filepath.Join(t.TempDir(), "components.json"))
	return a
}

func TestUserMemoryDocRoundTrip(t *testing.T) {
	d := UserMemoryData{
		UserName: "张总", Memory: "做制造业；喜欢简洁汇报\n- 周报用表格",
		AIName: "小坚", AIIdentity: "务实的工程助理", BubbleColor: "#dbeafe",
	}
	got := parseUserMemoryDoc(renderUserMemoryDoc(d))
	if got != d {
		t.Fatalf("文档往返不一致:\n%+v\n%+v", d, got)
	}
	// 手工编辑容错：段序打乱、有多余段落与引用行，已知段仍能读出
	manual := "# 用户记忆\n\n> 引用说明\n\n## AI 助理名字\n\n小坚\n\n## 未知段\n\n忽略我\n\n## 用户称呼\n\n张总\n"
	got = parseUserMemoryDoc(manual)
	if got.AIName != "小坚" || got.UserName != "张总" || got.Memory != "" {
		t.Fatalf("手工文档解析不符: %+v", got)
	}
	// 空字段渲染后再读仍为空（空段保留标题）
	got = parseUserMemoryDoc(renderUserMemoryDoc(UserMemoryData{UserName: "x"}))
	if got.Memory != "" || got.UserName != "x" {
		t.Fatalf("空字段往返不符: %+v", got)
	}
}

func TestUserMemoryLengthPolicy(t *testing.T) {
	a := newUserMemoryTestApp(t)

	// 存储层不截断（用户拍板：md 文件存完整内容；约束在写入入口把关）
	long := strings.Repeat("记", 1200) + "🚀尾"
	saved, err := a.saveUserMemory(UserMemoryData{Memory: long})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Memory != long || a.loadUserMemory().Memory != long {
		t.Fatal("saveUserMemory 不应截断（存储完整内容）")
	}

	// 注入侧截断：超长字段进环境快照时截到 1000 字加尾注（快照不膨胀）
	env := a.envPrompt(&SessionMeta{Mode: "task", Permission: permWorkspace, Workspace: a.Cfg.Workspace})
	if !strings.Contains(env, "超长，已截断") {
		t.Fatalf("超长字段的注入行应带截断尾注:\n%s", env)
	}
	for _, ln := range strings.Split(env, "\n") {
		if strings.HasPrefix(ln, "- 用户身份与喜好：") && utf8.RuneCountInString(ln) > userMemoryFieldLimit+40 {
			t.Fatalf("注入行应截到 1000 字+尾注以内，实际 %d 字", utf8.RuneCountInString(ln))
		}
	}

	// 写入入口（模型侧）超长整单报错：call_api 的 POST
	w := httptest.NewRecorder()
	a.handleUserMemorySet(w, httptest.NewRequest(http.MethodPost, "/api/user-memory",
		strings.NewReader(`{"userMemory":"`+strings.Repeat("忆", 1100)+`"}`)))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "上限") {
		t.Fatalf("POST 超长应 400 并说明上限: %d %s", w.Code, w.Body.String())
	}
	if n := len([]rune(a.loadUserMemory().Memory)); n != len([]rune(long)) {
		t.Fatalf("POST 超长报错后不应写入（应保持原值），实际 %d 字", n)
	}

	// 写入入口（工具）超长报错不写入
	tool := a.userMemoryTool()
	tk := &toolContext{ctx: t.Context(), app: a, sess: &SessionMeta{Mode: "task"}}
	if _, err := tool.Run(tk, json.RawMessage(`{"memory":"`+strings.Repeat("忆", 1001)+`"}`)); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("工具超长应报错: %v", err)
	}
	if a.loadUserMemory().Memory != long {
		t.Fatal("工具超长报错后不应写入")
	}
	// 合长（≤1000）写入正常
	if _, err := tool.Run(tk, json.RawMessage(`{"memory":"新记忆"}`)); err != nil {
		t.Fatal(err)
	}
	if a.loadUserMemory().Memory != "新记忆" {
		t.Fatal("合长写入应成功")
	}
}

func TestUserMemoryEnvPromptToggle(t *testing.T) {
	a := newUserMemoryTestApp(t)
	sess := &SessionMeta{Mode: "task", Permission: permWorkspace, Workspace: a.Cfg.Workspace}
	if _, err := a.saveUserMemory(UserMemoryData{UserName: "张总", Memory: "做制造业", AIName: "小坚", AIIdentity: "工程助理"}); err != nil {
		t.Fatal(err)
	}
	// 开启：注入记忆块，且不进系统提示词、不进工具定义
	env := a.envPrompt(sess)
	for _, want := range []string{"用户记忆", "张总", "做制造业", "小坚", "工程助理", "user_memory"} {
		if !strings.Contains(env, want) {
			t.Fatalf("组件开启时环境快照应含 %q:\n%s", want, env)
		}
	}
	if strings.Contains(a.systemPrompt(sess), "张总") {
		t.Fatal("用户记忆不得进系统提示词（设计：只进环境提示词）")
	}
	// 工具挂载：执行模式有、aux/assistant 没有（经理走 call_api 通道）
	hasTool := func(mode string) bool {
		for _, tl := range a.toolsFor(mode) {
			if tl.Spec.Function.Name == "user_memory" {
				return true
			}
		}
		return false
	}
	if !hasTool("task") || !hasTool("coding") || !hasTool("office") {
		t.Fatal("执行模式应挂 user_memory 工具")
	}
	if hasTool("aux") || hasTool("assistant") {
		t.Fatal("aux（只读答疑）与 assistant（call_api 整文件通道）不应挂 user_memory 工具")
	}
	// 经理模式的环境快照说明整文件通道与文件路径
	mgrEnv := a.envPrompt(&SessionMeta{Mode: "assistant", Permission: permWorkspace, Workspace: a.Cfg.Workspace})
	for _, want := range []string{"数字经理专属", "GET /api/user-memory", "POST /api/user-memory", "user-memory.md"} {
		if !strings.Contains(mgrEnv, want) {
			t.Fatalf("经理环境快照应含 %q:\n%s", want, mgrEnv)
		}
	}
	// 关闭：注入移除、工具卸载（开关即点即生效——envPrompt/toolsFor 每轮现算）
	if err := a.Components.SetEnabled(userMemoryComponentID, false); err != nil {
		t.Fatal(err)
	}
	if env2 := a.envPrompt(sess); strings.Contains(env2, "张总") || strings.Contains(env2, "用户记忆（") {
		t.Fatalf("组件关闭后环境快照不应再有记忆块:\n%s", env2)
	}
	if hasTool("task") {
		t.Fatal("组件关闭后 user_memory 工具应卸载")
	}
	// 全空记忆：开启也不注入（没内容就没有块）
	if err := a.Components.SetEnabled(userMemoryComponentID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveUserMemory(UserMemoryData{}); err != nil {
		t.Fatal(err)
	}
	if env3 := a.envPrompt(sess); strings.Contains(env3, "# 用户记忆") {
		t.Fatalf("记忆全空时环境快照不应有记忆块:\n%s", env3)
	}
}

func TestUserMemoryToolOnlyMemorySegment(t *testing.T) {
	a := newUserMemoryTestApp(t)
	if _, err := a.saveUserMemory(UserMemoryData{UserName: "张总", Memory: "旧记忆", AIName: "小坚", AIIdentity: "工程助理"}); err != nil {
		t.Fatal(err)
	}
	tool := a.userMemoryTool()
	tk := &toolContext{ctx: t.Context(), app: a, sess: &SessionMeta{Mode: "task"}}

	// 查询：返回全部字段
	out, err := tool.Run(tk, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"张总", "旧记忆", "小坚", "工程助理"} {
		if !strings.Contains(out, want) {
			t.Fatalf("查询应返回字段 %q: %s", want, out)
		}
	}

	// 更新记忆段：只动 userMemory，其余字段原样
	if _, err := tool.Run(tk, json.RawMessage(`{"memory":"新记忆：喜欢表格汇报"}`)); err != nil {
		t.Fatal(err)
	}
	d := a.loadUserMemory()
	if d.Memory != "新记忆：喜欢表格汇报" {
		t.Fatalf("记忆段应已更新: %+v", d)
	}
	if d.UserName != "张总" || d.AIName != "小坚" || d.AIIdentity != "工程助理" {
		t.Fatalf("专用工具不得改动记忆段以外的字段: %+v", d)
	}

	// 超长内容经工具写入：整单报错不写入（长度约束在入口，见 TestUserMemoryLengthPolicy）
	if _, err := tool.Run(tk, json.RawMessage(`{"memory":"`+strings.Repeat("忆", 1100)+`"}`)); err == nil {
		t.Fatal("工具写入超长内容应报错（不静默截断）")
	}
	if a.loadUserMemory().Memory != "新记忆：喜欢表格汇报" {
		t.Fatalf("超长报错后记忆段应保持原值: %+v", a.loadUserMemory())
	}

	// 组件中途关闭：工具调用时报错指路（目录在本轮开轮定型，调用时再核）
	if err := a.Components.SetEnabled(userMemoryComponentID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(tk, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("组件关闭后工具应报未启用: %v", err)
	}
}

func TestUserMemoryAPIAndCatalog(t *testing.T) {
	a := newUserMemoryTestApp(t)
	// 经理通道在 call_api 目录里（apiMuxOf 触发注册）
	_ = a.apiMuxOf()
	found := map[string]bool{}
	a.apiMetaMu.Lock()
	for _, m := range a.apiMetas {
		if m.Path == "/api/user-memory" {
			found[m.Method+" "+m.Path] = true
		}
	}
	a.apiMetaMu.Unlock()
	if !found["GET /api/user-memory"] || !found["POST /api/user-memory"] {
		t.Fatalf("call_api 目录应含 GET/POST /api/user-memory: %v", found)
	}

	// GET：空文档返回空字段
	w := httptest.NewRecorder()
	a.handleUserMemoryGet(w, httptest.NewRequest(http.MethodGet, "/api/user-memory", nil))
	if w.Code != 200 {
		t.Fatalf("GET 应 200: %d %s", w.Code, w.Body.String())
	}
	// POST：按字段保存（只给两个字段，未给的保持）+ 全量读回
	w = httptest.NewRecorder()
	a.handleUserMemorySet(w, httptest.NewRequest(http.MethodPost, "/api/user-memory",
		strings.NewReader(`{"userName":"张总","userMemory":"做制造业"}`)))
	if w.Code != 200 {
		t.Fatalf("POST 应 200: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.handleUserMemorySet(w, httptest.NewRequest(http.MethodPost, "/api/user-memory",
		strings.NewReader(`{"aiName":"小坚"}`)))
	if w.Code != 200 {
		t.Fatalf("POST 二段应 200: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.handleUserMemoryGet(w, httptest.NewRequest(http.MethodGet, "/api/user-memory", nil))
	var got UserMemoryData
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.UserName != "张总" || got.Memory != "做制造业" || got.AIName != "小坚" {
		t.Fatalf("按字段保存语义不符: %+v", got)
	}
	// 组件关闭：两个接口都 404（功能整体随开关停）
	if err := a.Components.SetEnabled(userMemoryComponentID, false); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		w = httptest.NewRecorder()
		if m == http.MethodGet {
			a.handleUserMemoryGet(w, httptest.NewRequest(m, "/api/user-memory", nil))
		} else {
			a.handleUserMemorySet(w, httptest.NewRequest(m, "/api/user-memory", strings.NewReader(`{}`)))
		}
		if w.Code != 404 {
			t.Fatalf("组件关闭后 %s 应 404: %d", m, w.Code)
		}
	}
}
