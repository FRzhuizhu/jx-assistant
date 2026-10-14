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
		Spec: toolSpec("dispatch_task", "新建一个工作任务会话并把需求交给它后台执行（它拥有读写文件与执行命令的全部工具）。本工具立即返回，任务完成后你会自动收到压缩摘要。工作任务与我们的聊天记录完全隔离——你就像用户一样只给它发一段自足的消息，task 必须写全目标、验收标准、涉及目录等全部上下文。可选参数与人工在界面新建工作任务一致：mode 选工作模式、plan_mode 让它以计划模式启动、attachments 给它带上现成文件、permission 调它的权限档（只能同等或降权，绝不允许增权）。",
			objSchema(map[string]any{
				"task":        strProp("完整任务描述，自带全部上下文（目标、验收标准、涉及目录）——工作任务看不到本对话的任何记录"),
				"title":       strProp("给新任务起的标题，可省略"),
				"workspace":   strProp("可选：工作任务的工作目录（绝对路径），默认沿用当前工作区"),
				"model":       strProp("可选：任务会话使用的模型，格式 providerId/modelId（如 deepseek/deepseek-v4-pro）或纯 modelId；不填沿用默认"),
				"mode":        strProp("可选：工作模式，取值与人工新建会话的模式下拉同一份清单（" + chatModesDoc() + "）；按任务性质选——办公文档/表格/资料选 office，改代码/编译排障选 coding，用户不懂编程的编程需求选 beginner-coding，把做法沉淀成技能教程选 skill-create。不填＝通用工作任务（task，与改动前一致）"),
				"plan_mode":   boolProp("可选：true＝让新任务以计划模式启动（与人工建会话勾选「计划模式」一致）：它先只读调研、提交计划等用户批准后才动手；适合改动面大、跨多文件、有风险的任务"),
				"attachments": arrayProp("可选：附件文件路径列表（本机绝对路径，如 D:\\资料\\需求.docx）。文件会复制进新任务的工作区并登记为上传（交付时可用 deliver_file 交回用户原处），与人工建会话上传附件完全一致；路径不存在、是目录，或新任务是远程工作区时，整个派发被拒绝并报错", strProp("本机文件绝对路径，如 D:\\资料\\需求.docx")),
				"permission":  strProp("可选：新任务的权限档，取值 read-only（只读）/ workspace-write（工作区内读写）/ full（完全访问），与权限面板同一套档位。只能**同等或降权**——不得高于本会话（数字助理）当前权限（见环境快照的「权限模式」行），超出即拒绝并报错（不静默钳制）。不填＝工作区内读写（本会话权限更窄时取本会话权限）。要改文件、跑命令的任务别降档；只读调研/查看可降为 read-only"),
			}, "task")),
		Run: runDispatchTask,
	}
}

// deliverSub 把派发需求投递进目标会话——走与用户发消息同一套投递（见 runtime.go
// 的 deliverToSession）：目标空闲=落库并起 runSubAgent；正在执行=按 jx.midrun 偏好排队/插话。
// 落库身份是 kind=aiuser（AI 发出的用户消息）：系统按用户消息同等对待（开新回合、
// 组装时后面跟一条时间提示词），前端是右对齐偏白的「AI 用户」气泡。
func (t *toolContext) deliverSub(sub *SessionMeta, text, note string) string {
	d := t.app.deliverToSession(sub, text, kindAiUser, "task")
	if !d.Started {
		return subQueueNote(sub, d.Mode, note)
	}
	// 当前主对话的 SSE 若还连着，先给它推一条派发确认
	t.emit(Event{Type: "sub_start", SessionID: sub.ID, Title: sub.Title})
	go t.app.runSubAgent(t.sess, sub, d.Run)
	return fmt.Sprintf("已派发后台执行：会话 %s「%s」。%s本工具立即返回，不要等待；"+
		"被派发的会话完成后系统会自动收到压缩摘要并触发你处理回报。现在可以先回用户一句「已经安排」。", sub.ID, sub.Title, note)
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

// resolveDispatchMode 解析 dispatch_task 的 mode 参数：不传＝通用工作任务
// （task，与改动前的默认一致）；传入的值必须落在「人工新建会话」的模式清单里
// （chatModes，见 chatmodes.go）——前端模式下拉与它同一份数据源。清单外的值
// （含 assistant / aux 这类内部模式）一律拒绝并列出可选值，让模型改口重试。
func resolveDispatchMode(req string) (string, error) {
	req = strings.TrimSpace(req)
	if req == "" {
		return "task", nil
	}
	if m := chatModeByValue(req); m != nil {
		return m.Value, nil
	}
	return "", fmt.Errorf("mode=%q 不是可选的工作模式：只能是 %s（与人工新建会话的模式下拉同一份清单）；不传＝通用工作任务（task）", req, chatModesDoc())
}

// dispatchPermission 解析派发工具的 permission 参数（dispatch_task / dispatch_sub
// 共用），返回被派发会话应使用的权限档。规则：只能派发**不高于**本会话权限的
// 会话——可同等、可削减，**绝不允许增权**（数字助理不能借派发给自己或派出去
// 的会话提权）。超出即拒绝并给明确报错：不静默钳制——钳制会让模型以为已按请求
// 生效，报错才能让它改口，或先请用户把本会话权限调宽。
// 档位与权限面板同一套（read-only / workspace-write / full；danger-full-access
// 按别名折成 full，与 request_permission 同口径）。不传＝沿用「工作区内读写」
// 这一既有默认，本会话权限更窄时取本会话权限（只降不升）。
func (t *toolContext) dispatchPermission(req string) (string, error) {
	cur := permModeOf(t.sess)
	req = strings.TrimSpace(req)
	if req == "danger-full-access" {
		req = permFull
	}
	if req == "" {
		if sandboxPolicyRank(cur) < sandboxPolicyRank(permWorkspace) {
			return cur, nil
		}
		return permWorkspace, nil
	}
	if req != permReadOnly && req != permWorkspace && req != permFull {
		return "", fmt.Errorf("permission=%q 不是有效档位：只能是 %s（只读）/ %s（工作区内读写）/ %s（完全访问）", req, permReadOnly, permWorkspace, permFull)
	}
	if sandboxPolicyRank(req) > sandboxPolicyRank(cur) {
		return "", fmt.Errorf("permission=%s 超出本会话当前权限（%s）：派发只能同等或降权，绝不允许增权。请改为「%s」或更窄的档位重试；确需更宽权限时，先请用户把本会话（数字助理）的权限调宽再派发",
			req, permModeLabel(cur), permModeLabel(cur))
	}
	return req, nil
}

// normalizeDispatchAttachments 归一化 dispatch_task 的 attachments 参数：去掉
// 空白项与重复项（同一次派发里同一个文件只登记一次）。只做形态归一——路径
// 是否存在、是不是文件、能不能复制，由 registerUploadPath 在建会话时按桌面端
// 上传同一套校验与文案报错。
func normalizeDispatchAttachments(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// dispatchTaskNote 组织这次派发回给数字助理的一句补充说明（模式、计划模式、
// 附件、权限档——只讲与默认不同的点）。工具结果里带上它，模型才知道这次派发
// 实际按什么档跑起来的；都取默认值时返回空串，不啰嗦。
func dispatchTaskNote(mode, perm, reqPerm string, planMode bool, attached int) string {
	var parts []string
	if m := chatModeByValue(mode); m != nil {
		parts = append(parts, "模式：「"+m.Label+"」（"+mode+"）")
	}
	if planMode {
		parts = append(parts, "以计划模式启动（先调研出计划、经用户批准后再动手）")
	}
	if attached > 0 {
		parts = append(parts, fmt.Sprintf("已带上 %d 个附件（已进新任务工作区并登记为上传）", attached))
	}
	if strings.TrimSpace(reqPerm) != "" {
		parts = append(parts, "权限档：「"+permModeLabel(perm)+"」（"+perm+"）")
	} else if perm != permWorkspace {
		parts = append(parts, "权限档：「"+permModeLabel(perm)+"」（"+perm+"，随本会话权限，只降不升）")
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "；") + "）"
}

// dispatchSubTool 构造 dispatch_sub 工具。描述是**纯静态文案**：内置两种模式
// 写死在说明里，自定义专家名单不嵌进来（嵌了会让 tools 数组随设置增删变化，
// 前缀缓存从最开头全断）——名单走常驻的 list_experts 工具按需查询，返回内容
// 作为工具结果落在上下文尾部。
func (a *App) dispatchSubTool() Tool {
	desc := "新建一个子对话（挂在本对话下，右栏可见），把一块自洽的子任务交给它后台执行（它拥有读写文件与执行命令的全部工具）。本工具立即返回，子对话完成后结果自动回报到本对话并触发你继续。子对话只有一级，不能再派发。专家预设是选模式的唯一方式：不传 expert 等同内置的「标准子对话」（自足任务、不继承上下文）；要继承本对话聊天记录就填内置的「继承上下文子对话」；自定义专家还有哪些是易变信息——先调用 list_experts 查名单，expert 必须与名单里的名字逐字一致。permission 参数可指定子对话的权限档，但只能同等或降权（绝不高于本会话权限）。"
	return Tool{
		Spec: toolSpec("dispatch_sub", desc,
			objSchema(map[string]any{
				"task":       strProp("完整子任务描述。未继承上下文的专家（如「标准子对话」）看不到本对话记录，task 必须自带全部上下文（目标、验收标准、涉及目录）；继承上下文的专家（如「继承上下文子对话」）能看到此前的聊天记录，task 只用写「接下来要做什么」"),
				"expert":     strProp("子对话专家名称：不传等同「标准子对话」；内置另有「继承上下文子对话」；自定义专家的名字与用途先用 list_experts 查询，必须与返回的清单逐字一致"),
				"title":      strProp("给子对话起的标题，可省略"),
				"workspace":  strProp("可选：子对话的工作目录（绝对路径），默认沿用当前工作区"),
				"model":      strProp("可选：覆盖专家预设的模型，格式 providerId/modelId（如 deepseek/deepseek-v4-pro）或纯 modelId；不填用预设默认模型或全局默认"),
				"permission": strProp("可选：子对话的权限档，取值 read-only（只读）/ workspace-write（工作区内读写）/ full（完全访问）。只能**同等或降权**——不得高于本会话（数字助理）当前权限，超出即拒绝并报错（不静默钳制）。不填＝工作区内读写（本会话权限更窄时取本会话权限）"),
			}, "task")),
		Run: runDispatchSub,
	}
}

// runDispatchSub 派发子对话：被派发的是挂在当前会话下的 task 会话（右栏抽屉
// 可见），回报链就是 parent_id。主对话与工作任务（task/office）都可派；
// 子对话本身不能再派发（只有一级）。专家是唯一的模式选择路径：expert=名称
// 按预设派发（继承行为、默认模型、追加提示词都由预设决定），不传 expert
// 等同内置的「标准子对话」（自足任务、不带上下文）；model 参数可换模型。
// 子会话记下专家名（SessionMeta.Expert），提示词构建时按名解析追加。
func runDispatchSub(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Task       string `json:"task"`
		Expert     string `json:"expert"`
		Title      string `json:"title"`
		Workspace  string `json:"workspace"`
		Model      string `json:"model"`
		Permission string `json:"permission"`
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
	// 专家解析：名字即参数，逐字匹配；不存在时把可用名单带回去让模型改
	var ex *ExpertCfg
	if name := strings.TrimSpace(p.Expert); name != "" {
		if ex = t.app.enabledExpertByName(name); ex == nil {
			names := []string{}
			for _, e := range t.app.enabledExperts() {
				names = append(names, "「"+strings.TrimSpace(e.Name)+"」")
			}
			return "", fmt.Errorf("子对话专家「%s」不存在（名称要与 list_experts 返回的清单逐字一致），可用：%s",
				name, strings.Join(names, "、"))
		}
	} else {
		// 不传 expert = 内置「标准子对话」（自足任务、不继承上下文）
		sys := systemExperts()
		ex = &sys[0]
	}
	// 权限档：与 dispatch_task 同一条规则——只能派发不高于本会话权限的子对话
	//（可同等、可降权，绝不允许增权；超出即报错）
	perm, err := t.dispatchPermission(p.Permission)
	if err != nil {
		return "", err
	}
	// 模型默认链：显式参数 > 专家预设 > 空（沿用全局默认）
	model := strings.TrimSpace(p.Model)
	if model == "" {
		model = strings.TrimSpace(ex.Model)
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
		ID:         newID(),
		Title:      title,
		Mode:       "task",
		Workspace:  ws,
		ParentID:   t.sess.ID, // 子对话挂在当前会话下；回报链走 parent_id
		Expert:     strings.TrimSpace(ex.Name),
		Permission: perm, // 只能同等或降权，绝不允许增权（dispatchPermission）
		CreatedAt:  now(),
		UpdatedAt:  now(),
	}
	t.dispatchExtras(sub, model)
	// 派发出来的子对话同样是「新建的会话」：按默认策略补默认能力
	//（工作区内读写默认开启「操作浏览器」，与 HTTP 创建路径同一函数）
	sub.Tools = mergeCapDefaults(sub.Tools, sub.Mode, permModeOf(sub))
	if err := t.app.Store.Create(sub); err != nil {
		return "", err
	}
	// 预设要继承上下文时：把本会话的完整历史复制进子对话，task 排在继承块
	// 之后——子对话读到的最后一条用户消息是自己的任务，而不是本会话里某句旧话
	inherited := 0
	if ex.InheritContext {
		n, err := t.app.copyInheritedContext(t.sess.ID, sub.ID)
		if err != nil {
			return "", fmt.Errorf("继承本会话上下文失败：%w", err)
		}
		inherited = n
	}
	// 需求正文（【派发子对话】+ task）由 deliverSub 走统一投递落库——与用户发消息同路
	var hints []string
	if strings.TrimSpace(p.Permission) != "" {
		hints = append(hints, "权限档：「"+permModeLabel(perm)+"」（"+perm+"，只能同等或降权）")
	}
	if strings.TrimSpace(ex.Prompt) != "" {
		hints = append(hints, fmt.Sprintf("已按专家预设「%s」派发，追加提示词已生效", sub.Expert))
	} else {
		hints = append(hints, fmt.Sprintf("已按「%s」模式派发", sub.Expert))
	}
	if inherited > 0 {
		hints = append(hints, fmt.Sprintf("已继承本会话上下文 %d 条，子对话能看到此前的聊天记录", inherited))
	}
	note := ""
	if len(hints) > 0 {
		note = "（" + strings.Join(hints, "；") + "）"
	}
	return t.deliverSub(sub, "【派发子对话】\n\n"+p.Task, note), nil
}

func runDispatchTask(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Task        string   `json:"task"`
		Title       string   `json:"title"`
		Workspace   string   `json:"workspace"`
		Model       string   `json:"model"`
		Mode        string   `json:"mode"`
		PlanMode    bool     `json:"plan_mode"`
		Attachments []string `json:"attachments"`
		Permission  string   `json:"permission"`
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
	// 三个新参数先全部解析校验（快速失败）：模式 / 权限 / 附件都可能被拒，
	// 拒绝必须发生在建出半个会话之前
	mode, err := resolveDispatchMode(p.Mode)
	if err != nil {
		return "", err
	}
	perm, err := t.dispatchPermission(p.Permission)
	if err != nil {
		return "", err
	}
	attPaths := normalizeDispatchAttachments(p.Attachments)
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
		Mode:         mode, // 与人工新建会话同一份模式清单（resolveDispatchMode）
		Workspace:    ws,
		Permission:   perm,       // 只能同等或降权，绝不允许增权（dispatchPermission）
		PlanMode:     p.PlanMode, // 与人工建会话勾选「计划模式」一致
		DispatchedBy: t.sess.ID,  // 派发的是顶层工作任务（左侧列表可见），不是子对话；
		// 回报链（中断回报/「我派生的」标记）走 dispatched_by，不走 parent_id
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	t.dispatchExtras(sub, p.Model)
	if len(attPaths) > 0 {
		// 附件预检：坏路径（不存在/是目录）与远程工作区一律在建会话之前拒绝，
		// 不留半个空任务
		if sub.Remote != nil {
			return "", fmt.Errorf("附件不能带给远程工作区的新任务（本会话挂了 SSH 远程工作区，新任务默认同环境）：本机文件路径在远程无从复制。请把 workspace 指到本地目录，或改用 send_to_session 让远程会话自己取文件")
		}
		for _, src := range attPaths {
			if _, _, _, err := resolveUploadSource(src); err != nil {
				return "", fmt.Errorf("附件 %s 不可用：%w", src, err)
			}
		}
	}
	// 派发出来的工作任务同样是「新建的会话」：按默认策略补默认能力
	sub.Tools = mergeCapDefaults(sub.Tools, sub.Mode, permModeOf(sub))
	if err := t.app.Store.Create(sub); err != nil {
		return "", err
	}
	// 附件：与桌面端 /api/upload 的 JSON {path} 走同一套登记（registerUploadPath）
	// ——复制进新任务工作区 + 落 uploads 表 + 一条【文件上传】注记。注记先于任务
	// 描述落库（与人工「先传文件、再发消息」的顺序一致），新任务开轮就知道
	// 有哪些现成文件、在哪、能当交付目标。
	attached := 0
	for _, src := range attPaths {
		_, note, err := t.app.registerUploadPath(sub, src)
		if err != nil {
			return "", fmt.Errorf("附件 %s 登记失败：%w", src, err)
		}
		t.app.landUploadNote(sub, note)
		attached++
	}
	// 工作任务永远不带上下文：像用户发消息一样，只收到一段自足的任务描述，
	// 与派发方的聊天记录完全隔离（要上下文的场景用 dispatch_sub 的继承模式）
	// 需求正文（【数字助理派发】+ task）由 deliverSub 走统一投递落库
	return t.deliverSub(sub, "【数字助理派发】\n\n"+p.Task, dispatchTaskNote(mode, perm, p.Permission, p.PlanMode, attached)), nil
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
	// 需求正文（【数字助理转达】+ task）由 deliverSub 走统一投递落库
	return t.deliverSub(m, "【数字助理转达】\n\n"+p.Task, ""), nil
}

// ---------- 压缩摘要（设计对齐 DSH 版 summary.ts，只留本栈有的字段） ----------

type changedFile struct {
	Path string
	Op   string // 新建或覆盖 / 修改
}

var writeTools = map[string]bool{"write": true, "edit": true}

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
				if name == "edit" {
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
