package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 数字助理（assistant 模式）总控半球。设计参考 DSH 版数字助理插件：
// 总控不亲自读写文件、不执行命令，只做「看清需求 → 新建或复用任务会话 →
// 把需求交代清楚 → 等它做完 → 用压缩摘要向用户汇报」。

// ---------- 工具集 ----------

func listSessionsTool() Tool {
	return Tool{
		Spec: toolSpec("list_sessions", "列出最近的任务会话（默认 12 条，最多 40 条）：id、标题、工作区、更新时间、是否在跑、是否由数字助理派生。用来判断新需求与哪个会话相关。",
			objSchema(map[string]any{
				"limit": intProp("返回条数，默认 12，上限 40"),
			})),
		Run: runListSessions,
	}
}

func runListSessions(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Limit int `json:"limit"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 12
	}
	if limit > 40 {
		limit = 40
	}
	list, err := t.app.Store.List()
	if err != nil {
		return "", err
	}
	lines := []string{}
	for _, m := range list {
		if m.Mode == "assistant" || m.ID == t.sess.ID {
			continue
		}
		if len(lines) >= limit {
			break
		}
		running := ""
		if lock := t.app.sessLock(m.ID); lock.TryLock() {
			lock.Unlock()
		} else {
			running = "｜正在运行"
		}
		ws := m.Workspace
		if ws == "" {
			ws = t.app.Cfg.Workspace
		}
		origin := ""
		if m.ParentID != "" || m.DispatchedBy != "" {
			origin = "｜我派生的"
		}
		lines = append(lines, fmt.Sprintf("- %s｜id=%s｜工作区=%s｜更新 %s%s%s",
			m.Title, m.ID, ws, m.UpdatedAt, running, origin))
	}
	if len(lines) == 0 {
		return "（除主对话外还没有别的会话）", nil
	}
	return strings.Join(lines, "\n"), nil
}

func readSummaryTool() Tool {
	return Tool{
		Spec: toolSpec("read_session_summary", "读某个会话做过什么的压缩摘要（轮数与步数、调用过哪些工具、中间与最终正文、改动了哪些文件）。不含工具参数细节。",
			objSchema(map[string]any{
				"session_id": strProp("目标会话 id，来自 list_sessions"),
			}, "session_id")),
		Run: runReadSummary,
	}
}

func runReadSummary(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	sid := strings.TrimSpace(p.SessionID)
	if sid == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	if sid == t.sess.ID {
		return "", fmt.Errorf("不能读主对话自己的摘要")
	}
	m, err := t.app.Store.Get(sid)
	if err != nil {
		return "", fmt.Errorf("会话 %s 不存在，先用 list_sessions 看看 id", sid)
	}
	msgs, err := t.app.Store.Messages(sid)
	if err != nil {
		return "", err
	}
	return renderSummary(t.app, m, msgs), nil
}

func dispatchTaskTool() Tool {
	return Tool{
		Spec: toolSpec("dispatch_task", "新建一个任务会话并把需求交给它后台执行（它拥有读写文件与执行命令的全部工具）。本工具立即返回，任务会话完成后你会自动收到压缩摘要。任务描述必须完整自洽，包含任务会话所需的全部上下文、验收标准与工作目录。",
			objSchema(map[string]any{
				"task":            strProp("完整任务描述，自带全部上下文（目标、验收标准、涉及目录）"),
				"title":           strProp("给新会话起的标题，可省略"),
				"workspace":       strProp("可选：子任务的工作目录（绝对路径），默认沿用当前工作区"),
				"model":           strProp("可选：任务会话使用的模型，格式 providerId/modelId（如 deepseek/deepseek-v4-pro）或纯 modelId；不填沿用默认"),
				"inherit_context": boolProp("可选：是否把本对话（我们的聊天记录）完整继承给任务会话，默认 false。任务需要了解此前讨论的来龙去脉、或需要复用本对话里已经交代过的背景时才设 true；一次性的独立任务保持默认——任务描述写清楚更省 token。"),
			}, "task")),
		Run: runDispatchTask,
	}
}

// launchSub 把子会话登记为后台执行并立刻返回；执行体见 runtime.go 的 runSubAgent。
// note 是附在返回文案里的补充说明（如「已继承主对话上下文 N 条」）。
func (t *toolContext) launchSub(sub *SessionMeta, note string) (string, error) {
	run, ok := t.app.beginRun(sub.ID)
	if !ok {
		return "", fmt.Errorf("目标会话 %s 正在执行其它任务，请稍后再试", sub.ID)
	}
	// 当前主对话的 SSE 若还连着，先给它推一条派发确认
	t.emit(Event{Type: "sub_start", SessionID: sub.ID, Title: sub.Title})
	go t.app.runSubAgent(t.sess, sub, run)
	return fmt.Sprintf("已派发后台执行：会话 %s「%s」。%s本工具立即返回，不要等待；"+
		"被派发的会话完成后系统会自动收到压缩摘要并触发你处理回报。现在可以先回用户一句「已经安排」。", sub.ID, sub.Title, note), nil
}

// dispatchExtras 应用派发工具的可选项：可选模型（providerId/modelId 或纯
// modelId）与远程工作区继承（dispatch_task / dispatch_sub 共用）。
func (t *toolContext) dispatchExtras(sub *SessionMeta, model string) {
	if m := strings.TrimSpace(model); m != "" {
		provider, mdl := "", m
		if i := strings.Index(m, "/"); i > 0 {
			provider, mdl = m[:i], m[i+1:]
		}
		if t.app.providerByID(provider) != nil {
			sub.Provider = provider
		}
		if mdl != "" {
			sub.Model = mdl
		}
	}
	// 远程工作区继承：派发方挂了 SSH 远程工作区时，被派发的会话默认在同一远程环境执行
	if t.sess.Remote != nil {
		r := *t.sess.Remote
		sub.Remote = &r
	}
}

func dispatchSubTool() Tool {
	return Tool{
		Spec: toolSpec("dispatch_sub", "新建一个子对话（挂在本对话下，右栏可见），把一块自洽的子任务交给它后台执行（它拥有读写文件与执行命令的全部工具）。本工具立即返回，子对话完成后结果自动回报到本对话并触发你继续。子对话只有一级，不能再派发。",
			objSchema(map[string]any{
				"task":      strProp("完整子任务描述，自带全部上下文（目标、验收标准、涉及目录）"),
				"title":     strProp("给子对话起的标题，可省略"),
				"workspace": strProp("可选：子对话的工作目录（绝对路径），默认沿用当前工作区"),
				"model":     strProp("可选：子对话使用的模型，格式 providerId/modelId（如 deepseek/deepseek-v4-pro）或纯 modelId；不填沿用默认"),
			}, "task")),
		Run: runDispatchSub,
	}
}

// runDispatchSub 派发子对话：被派发的是挂在当前会话下的 task 会话（右栏抽屉
// 可见），回报链就是 parent_id。主对话与工作任务（task/office）都可派；
// 子对话本身不能再派发（只有一级）。
func runDispatchSub(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Task      string `json:"task"`
		Title     string `json:"title"`
		Workspace string `json:"workspace"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Task) == "" {
		return "", fmt.Errorf("task 不能为空")
	}
	// 工具按模式分发，但人工把会话克隆成子对话后模式照旧、工具还在手里，
	// 这里硬拦，保证子对话只有一级
	if t.sess.ParentID != "" {
		return "", fmt.Errorf("子对话不能再派发子对话（只有一级）；请把这块工作直接做掉")
	}
	ws := t.workspace()
	if strings.TrimSpace(p.Workspace) != "" {
		ws = p.Workspace
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = truncateRunes(p.Task, 20)
	}
	sub := &SessionMeta{
		ID:        newID(),
		Title:     title,
		Mode:      "task",
		Workspace: ws,
		ParentID:  t.sess.ID, // 子对话挂在当前会话下；回报链走 parent_id
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	t.dispatchExtras(sub, p.Model)
	if err := t.app.Store.Create(sub); err != nil {
		return "", err
	}
	t.app.appendMessage(sub, TextMessage("user", "【派发子对话】\n\n"+p.Task))
	return t.launchSub(sub, "")
}

func runDispatchTask(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Task           string `json:"task"`
		Title          string `json:"title"`
		Workspace      string `json:"workspace"`
		Model          string `json:"model"`
		InheritContext bool   `json:"inherit_context"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Task) == "" {
		return "", fmt.Errorf("task 不能为空")
	}
	// 子对话不能再派发子对话：编排工具只发给总控，但人工把总控克隆成子对话后，
	// 那个子会话手里也有这套工具，这里硬拦，保证只有一级子对话。
	if t.sess.ParentID != "" {
		return "", fmt.Errorf("子对话不能再派发子对话（本会话已是子对话，只有一级）；请把需求直接做掉，或让用户在主对话里派发")
	}
	ws := t.workspace()
	if strings.TrimSpace(p.Workspace) != "" {
		ws = p.Workspace
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = truncateRunes(p.Task, 20)
	}
	sub := &SessionMeta{
		ID:           newID(),
		Title:        title,
		Mode:         "task",
		Workspace:    ws,
		DispatchedBy: t.sess.ID, // 派发的是顶层工作任务（左侧列表可见），不是子对话；
		// 回报链（中断回报/「我派生的」标记）走 dispatched_by，不走 parent_id
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	t.dispatchExtras(sub, p.Model)
	if err := t.app.Store.Create(sub); err != nil {
		return "", err
	}
	// 继承上下文：把主对话的完整历史复制进任务会话（前端折叠成一条「来自主对话」）
	inherited := 0
	if p.InheritContext {
		n, err := t.app.copyInheritedContext(t.sess.ID, sub.ID)
		if err != nil {
			return "", fmt.Errorf("继承主对话上下文失败：%w", err)
		}
		inherited = n
	}
	// 派发需求在继承块之后落库：任务会话读到的最后一条用户消息是自己的任务，
	// 而不是主对话里某句旧话
	t.app.appendMessage(sub, TextMessage("user", "【数字助理派发】\n\n"+p.Task))
	hint := ""
	if inherited > 0 {
		hint = fmt.Sprintf("（已继承主对话上下文 %d 条，任务会话能看到我们此前的聊天记录）", inherited)
	}
	return t.launchSub(sub, hint)
}

func sendToSessionTool() Tool {
	return Tool{
		Spec: toolSpec("send_to_session", "把新需求追加给一个已有会话并后台执行（新需求与该会话明显相关且不复杂时优先用它，上下文连续效率最高）。本工具立即返回，该会话完成后你会自动收到压缩摘要。",
			objSchema(map[string]any{
				"session_id": strProp("目标会话 id，来自 list_sessions"),
				"task":       strProp("完整、自足的一段需求"),
			}, "session_id", "task")),
		Run: runSendToSession,
	}
}

func runSendToSession(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Task      string `json:"task"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	sid := strings.TrimSpace(p.SessionID)
	if sid == "" || strings.TrimSpace(p.Task) == "" {
		return "", fmt.Errorf("session_id 与 task 不能为空")
	}
	if sid == t.sess.ID {
		return "", fmt.Errorf("不能把任务发回主对话自己")
	}
	m, err := t.app.Store.Get(sid)
	if err != nil {
		return "", fmt.Errorf("会话 %s 不存在，先用 list_sessions 看看 id", sid)
	}
	if m.Mode == "assistant" {
		return "", fmt.Errorf("目标会话是另一个总控会话，不能作为派发目标")
	}
	t.app.appendMessage(m, TextMessage("user", "【数字助理转达】\n\n"+p.Task))
	return t.launchSub(m, "")
}

// ---------- 压缩摘要（设计对齐 DSH 版 summary.ts，只留本栈有的字段） ----------

type changedFile struct {
	Path string
	Op   string // 新建或覆盖 / 修改
}

var writeTools = map[string]bool{"write_file": true, "edit_file": true}

var resultFailedPrefixes = []string{"工具执行失败", "未知工具", "（用户中断"}

// summarizeSession 把会话消息流折叠成摘要数据：轮数、步数、工具调用统计、
// 中间/最终正文、成功写入的文件清单。
func summarizeSession(msgs []Message) (rounds, steps int, toolCounts []string, middle, final string, files []changedFile) {
	nameOrder := []string{}
	counts := map[string]int{}
	calls := map[string]string{} // callID → 工具名
	paths := map[string]string{} // callID → 参数里的 path
	texts := []string{}
	fileMap := map[string]string{}
	fileOrder := []string{}

	for _, m := range msgs {
		switch m.Role {
		case "user":
			rounds++
		case "assistant":
			if len(m.ToolCalls) > 0 {
				steps++
			}
			for _, tc := range m.ToolCalls {
				n := tc.Function.Name
				counts[n]++
				if !containsTool(nameOrder, n) {
					nameOrder = append(nameOrder, n)
				}
				calls[tc.ID] = n
				var a struct {
					Path string `json:"path"`
				}
				if json.Unmarshal([]byte(tc.Function.Arguments), &a) == nil && a.Path != "" {
					paths[tc.ID] = a.Path
				}
			}
			if s := m.Text(); strings.TrimSpace(s) != "" {
				texts = append(texts, s)
			}
		case "tool":
			name := calls[m.ToolCallID]
			failed := false
			for _, pre := range resultFailedPrefixes {
				if strings.HasPrefix(strings.TrimSpace(m.Text()), pre) {
					failed = true
					break
				}
			}
			if name != "" && writeTools[name] && !failed && paths[m.ToolCallID] != "" {
				op := "新建或覆盖"
				if name == "edit_file" {
					op = "修改"
				}
				p := paths[m.ToolCallID]
				if _, ok := fileMap[p]; !ok {
					fileOrder = append(fileOrder, p)
				}
				fileMap[p] = op
			}
		}
	}
	for _, n := range nameOrder {
		toolCounts = append(toolCounts, fmt.Sprintf("%s×%d", n, counts[n]))
	}
	middleTexts := []string{}
	if len(texts) > 1 {
		for _, s := range texts[:len(texts)-1] {
			middleTexts = append(middleTexts, clipText(s, 220))
		}
		if len(middleTexts) > 3 {
			middleTexts = middleTexts[len(middleTexts)-3:]
		}
	}
	if len(texts) > 0 {
		final = clipText(texts[len(texts)-1], 900)
	}
	for _, p := range fileOrder {
		files = append(files, changedFile{Path: p, Op: fileMap[p]})
	}
	return rounds, steps, toolCounts, strings.Join(middleTexts, "\n  - "), final, files
}

func containsTool(order []string, name string) bool {
	for _, n := range order {
		if n == name {
			return true
		}
	}
	return false
}

// clipText 压缩空白并按上限截断。
func clipText(s string, limit int) string {
	space := regexp.MustCompile(`\s+`)
	flat := strings.TrimSpace(space.ReplaceAllString(s, " "))
	r := []rune(flat)
	if len(r) <= limit {
		return flat
	}
	return string(r[:limit-1]) + "…"
}

// renderSummary 渲染给数字助理看的中文摘要文本。
func renderSummary(a *App, m *SessionMeta, msgs []Message) string {
	rounds, steps, toolCounts, middle, final, files := summarizeSession(msgs)
	ws := m.Workspace
	if ws == "" {
		ws = a.Cfg.Workspace
	}
	lines := []string{
		fmt.Sprintf("【会话 %s】%s —— 共 %d 轮 / %d 步", m.ID, m.Title, rounds, steps),
		"· 工作区：" + ws,
	}
	if len(toolCounts) == 0 {
		lines = append(lines, "· 工具调用：无")
	} else {
		lines = append(lines, "· 工具调用："+strings.Join(toolCounts, "、"))
	}
	if middle == "" {
		lines = append(lines, "· 中间正文：无")
	} else {
		lines = append(lines, "· 中间正文：\n  - "+middle)
	}
	lines = append(lines, "· 最终答复："+func() string {
		if final == "" {
			return "（没有正文输出）"
		}
		return final
	}())
	if len(files) == 0 {
		lines = append(lines, "· 改动文件：无（未记录到成功的写文件动作）")
	} else {
		parts := []string{}
		for _, f := range files {
			parts = append(parts, fmt.Sprintf("%s（%s）", f.Path, f.Op))
		}
		lines = append(lines, "· 改动文件："+strings.Join(parts, "、"))
	}
	lines = append(lines, "（以上是压缩摘要，不含工具参数细节；需要更多细节可让用户点进该会话查看。）")
	return strings.Join(lines, "\n")
}
