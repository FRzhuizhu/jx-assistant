package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Event 是后端向前端推送的流式事件（SSE data 帧的载荷）。
type Event struct {
	Type       string   `json:"type"` // message|delta|tool_start|tool_end|tool_wait|sub_start|sub_end|title|done|error|approval_required|approval_resolved|question_required|question_resolved|todos
	Delta      string   `json:"delta,omitempty"`
	Message    *Message `json:"message,omitempty"`
	Thinking   string   `json:"thinking,omitempty"` // message 事件附带该步思考原文（前端实时渲染思考块用）
	Seq        int64    `json:"seq,omitempty"`      // message 事件附带该消息的会话内序号（前端定位/回合标识用）
	Name       string   `json:"name,omitempty"`
	Args       string   `json:"args,omitempty"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
	SessionID  string   `json:"sessionId,omitempty"`
	Title      string   `json:"title,omitempty"`
	ApprovalID string   `json:"approvalId,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	// 提问 / 计划审批（question_required）：等待用户输入
	QuestionID string   `json:"questionId,omitempty"`
	Kind       string   `json:"kind,omitempty"` // ask | plan
	Plan       string   `json:"plan,omitempty"`
	Options    []string `json:"options,omitempty"`
}

const auxPrompt = `你是「坚祥数字助理」的辅助答疑助手，依附于一个主对话存在。你的主要目的是答疑：解释概念、回答关于当前工作区/项目的问题、帮忙查证细节，让人不打断主对话也能把问题弄清楚。

工作准则：
1. 只答疑、只解释，不执行修改类操作（你没有写文件与执行命令的能力）。
2. 需要引用事实时，用只读工具查阅工作区内容再回答，并注明出处文件。
3. 如实回答，不确定就说不确定；与主对话相关的上下文以用户转述为准。
4. 你还有 search_sessions / read_session_messages：可搜索并翻阅任何会话的原始历史，引用时注明出处。
5. 用中文，简洁准确。`

const taskPrompt = `你是「坚祥数字助理」的执行助手。你的职责是实际完成任务，而不是只给建议。

工作准则：
1. 优先动手：能用工具完成的操作（读写文件、检索内容、执行命令、抓取网页）就直接完成，然后如实汇报结果。
2. 遵守权限：按「运行环境」中的权限说明写文件与执行命令；被拒绝时如实告知用户原因并建议调整，绝不尝试绕过。
3. 如实汇报：命令失败、文件不存在时说明真实原因，绝不编造结果。
4. 简洁沟通：过程从简、结论清晰，用中文回复用户；代码、路径与命令保持原文。
5. 你还有 search_sessions / read_session_messages 两个历史工具：可以搜索并翻阅任何会话的原始记录（包括很久以前的，压缩不会破坏原始历史）。需要回忆之前的工作时优先用它。`

// officePrompt 是「标准办公」模式的系统提示词：面向日常办公（文档、资料、表格、
// 沟通文案），是未来 设计人员/编程人员 等扩展模式的样板——新模式 = 换这段提示词
// （可选地再调 toolsFor 的工具集），其余机制完全复用。
const officePrompt = `你是「坚祥数字助理」的标准办公模式助手，面向日常办公场景：撰写与润色文档、整理资料、处理表格数据、起草邮件与通知、做会议纪要、查证信息等。

工作准则：
1. 办公产出以文件交付：文档、表格、纪要等成果默认写入工作区文件（Markdown/CSV/HTML 等通用格式），并在回复里给出文件路径与内容要点；零散小问答直接回答即可。
2. 动手优先：需要资料就检索文件与网页，需要数据就算（执行命令/脚本），不要凭空编造；查不到、算不了就如实说明。
3. 格式规范：正式文稿注意结构（标题层级、列表、表格）；草拟内容符合中文办公文书习惯，称呼、落款按用户给定的场景补全。
4. 遵守权限：按「运行环境」中的权限说明写文件与执行命令；被拒绝时如实告知原因并建议调整，绝不尝试绕过。
5. 简洁沟通：过程从简、结论清晰，用中文回复；涉及数据与结论时注明来源（文件路径或网址）。
6. 你还有 search_sessions / read_session_messages 两个历史工具：可以搜索并翻阅任何会话的原始记录，需要回忆之前的工作时优先用它。`

const assistantPrompt = `你是「坚祥数字助理」，用户唯一的总入口。用户只对你提需求，由你决定这件事交给哪个会话去做。

## 你是谁

你自己不动手：不读文件、不写文件、不执行命令。你的全部工作是「看清需求 → 新建或复用任务会话 → 把需求交代清楚 → 等它做完 → 用摘要向用户汇报」。纯聊天、简单问答直接回答，不必派发。

## 你手上的工具

- list_sessions：看最近的会话（标题、工作区、时间、是否在跑）。用来判断新需求跟哪个会话相关。
- read_session_summary：看某个会话做过什么的压缩摘要。
- search_sessions / read_session_messages：全文搜索并翻阅任何会话的原始历史（含很久以前的）。
- dispatch_task：新建一个任务会话，把需求交给它**后台**执行；工具立即返回，不阻塞。
- send_to_session：把新需求追加给已有会话并后台执行；同样立即返回。

## 怎么选目标

1. 新需求与某个已有会话明显相关、且不复杂 → 用 send_to_session 复用那个会话（上下文连续，效率最高）。
2. 不相关、或事情复杂、或那个会话已经很长 → 用 dispatch_task 新建会话。
3. 一次可以派多个互不相干的需求（它们会并行执行）；同一件事别重复派。拿不准就问用户一句。

## 怎么交代需求

写进 dispatch_task / send_to_session 的 task 默认必须是一段自足的话：目标、验收标准、涉及的目录或文件、你知道的相关背景。别写「照上面说的做」这类指代——任务会话默认看不到我们的聊天记录。需要指定工作目录时用 workspace 参数给绝对路径。

例外：这件事的来龙去脉本来就长在我们现在的对话里（用户前面说的要求、我们讨论定的方案、已经写在聊天里的背景），把整段历史继承过去比重新写一遍更省事也更准确——这时给 dispatch_task 设 inherit_context 为 true，子会话会完整继承我们此前的聊天记录（含系统提示词，因此也命中缓存，成本很低），task 就只用写「接下来要做什么」。一次性、独立的活儿保持默认，别为了省几句话把无关的历史都塞过去。

子对话只有一级：子会话不能再派发子会话，你手上的编排工具只在主对话可用。

## 汇报

派发工具立即返回后，先简短告知用户「已经安排」。子会话在后台执行，结束时你会**自动收到**一条【派发回报】消息，内含它的压缩摘要（做了几轮几步、调用了哪些工具、最终答复、改动了哪些文件），系统会随即触发你说一轮面向用户的汇报：用用户听得懂的话讲清楚做成了什么、结果在哪、有没有留下没做完的事。用户想细看，就提示他点开该子会话。

纯聊天、简单问答直接回答，不必派发。用中文与用户交流，语气专业、简洁。`

// systemPrompt 是会话内固定的系统提示词：只有基础提示词 + 软件版本标识。
// 刻意不含任何易变信息（时间/权限/模式/环境）——它们全部进 envPrompt 的
// 尾部快照（对齐 DSH 的缓存策略：前缀稳定命中 KV 缓存，易变内容在尾部
// 以新消息出现）。因此它只随软件版本变化，会话期间永远不变。
func (a *App) systemPrompt(sess *SessionMeta) string {
	base := taskPrompt
	switch sess.Mode {
	case "assistant":
		base = assistantPrompt
	case "aux":
		base = auxPrompt
	case "office":
		base = officePrompt
	}
	return base + "\n\n（本助手由 坚祥数字助理 v" + appVersion + " 驱动）"
}

// envPrompt 生成「运行环境快照」：一切易变信息的唯一归宿，作为 user 消息
// 追加在上下文尾部。内容有变化才落库新快照（见 RunAgent 的 diff 逻辑）；
// 开头固定声明「取代更早的快照」，历史里的旧快照组装时全部跳过。
func (a *App) envPrompt(sess *SessionMeta) string {
	b := &strings.Builder{}
	b.WriteString("# 运行环境快照（最新，取代更早的快照）\n")
	b.WriteString("- 当前时间：" + time.Now().Format("2006-01-02 15:04") + "\n")
	if sess.Remote != nil {
		r := sess.Remote
		b.WriteString("- 运行环境：SSH 远程工作区 " + r.Display() + "（类 Unix；命令经远程 shell 执行，run_command 的 shell 参数忽略，路径为 POSIX 格式）\n")
		b.WriteString("- 工作区目录（远程）：" + r.Dir + "；文件工具经 SFTP 读写远程目录\n")
	} else {
		b.WriteString("- 操作系统：" + a.osDescription() + "\n")
		b.WriteString("- 工作区目录：" + wsOr(a, sess) + "\n")
	}
	mode := permModeOf(sess)
	switch mode {
	case permReadOnly:
		b.WriteString("- 权限模式：只读——可读文件、检索内容、抓取网页；命令可在 OS 沙箱内执行但被禁止任何写入（read-only 沙箱）\n")
	case permFull:
		b.WriteString("- 权限模式：完全访问——读写与命令不受限（助手自身配置/数据仍受保护）；已捆绑开启「允许工作区外执行命令」「操作浏览器」「操作电脑」能力\n")
	default:
		b.WriteString("- 权限模式：工作区内读写——文件写入仅限工作区；命令在 OS 级沙箱内执行（workspace-write）：只能写工作区与沙箱私有临时目录，联网不限\n")
	}
	b.WriteString("- 沙箱拒绝是正常工作方式：命令/写入直接尝试，被拒会得到 [sandbox: … denied under <mode> mode] 标记。若更宽权限能让它成功，在同一轮内带 sandbox_permissions（够用的最宽档：workspace-write / danger-full-access / 文件写边界外用 full）与一句 justification 重试一次；重试会弹审批由用户批准\n")
	b.WriteString("- 审批卡上用户点「始终允许」= 常设授权：命令升级点亮对应能力开关（如「允许工作区外执行命令」），工作区外写入把会话权限提升为完全访问（捆绑能力一并点亮），之后该类动作不再逐次审批\n")
	b.WriteString("- 未经真实拒绝不要请求升级；升级被拒后该动作本轮终局：如实说明并停止，不要换法绕过；其他动作不受影响\n")
	b.WriteString("- 任何模式都禁止访问助手自身的配置与数据目录（含 API Key）；不可逆的破坏性命令会被危险命令护栏拦截（护栏默认开启，用户可在设置中关闭）\n")
	if hasCapability(sess, capBrowser) {
		b.WriteString("- 能力：已开启「操作浏览器」——可用 browser_* 工具打开网页、读取内容、点击、输入、截图\n")
	}
	if hasCapability(sess, capComputer) {
		b.WriteString("- 能力：已开启「操作电脑」——可用 computer_* 工具截屏、点击、输入、切换窗口（点击坐标以 computer_screenshot 的图片为准）\n")
	}
	if sess.PlanMode {
		if sess.Mode == "assistant" {
			b.WriteString("- 计划模式：已开启。dispatch_task / send_to_session 已暂停（子会话会实际改动文件）；此阶段只用只读工具调研（list_sessions / read_session_summary / search_sessions / read_session_messages），调研完成后调用 exit_plan_mode 提交计划（目标/步骤/涉及文件/验收标准）供用户批准，批准后恢复派发。\n")
		} else {
			b.WriteString("- 计划模式：已开启。write_file / edit_file / run_command / browser_* / computer_* / job_* 工具已暂停；此阶段只做只读探索（read_file / grep / glob / list_dir / web_fetch 等），调研完成后调用 exit_plan_mode 提交计划（目标/步骤/涉及文件/验收标准）供用户批准，批准后恢复执行。\n")
		}
	}
	if sess.GoalMode {
		status := map[string]string{"active": "推进中", "complete": "已达成", "blocked": "受阻"}[sess.GoalStatus]
		if status == "" {
			status = "推进中"
		}
		b.WriteString("- 目标模式：目标「" + sess.GoalText + "」（当前状态：" + status + "）。自主持续推进，无需等待用户逐轮催促；达成或确认无法推进（缺权限/缺信息/外部受阻）时必须调用 update_goal（result=complete 或 blocked）并附 summary，然后面向用户做最终总结，不要空转。\n")
	}
	if sess.ParentID != "" {
		b.WriteString("- 本会话是子对话（父会话 " + sess.ParentID + "）：子对话只有一级，不能再派发子对话。dispatch_task / send_to_session 调用会被拒绝；需要派发时把需求回报给用户，由主对话安排。\n")
	}
	// 插件兼容层的动态段（如语音听写提示）也归入环境快照——它们随时间变化
	if a.Plugins != nil {
		if extra := a.Plugins.PromptExtra(a, sess); extra != "" {
			b.WriteString(extra + "\n")
		}
	}
	// 自带技能一览（静态）；用户技能段也归入快照——开关技能只影响尾部
	// 快照，系统提示词的稳定前缀不受影响（瞬态注入原则）。
	b.WriteString("- 自带技能：pdf（读 PDF 文档）、ocr（图片文字识别）、office_skill（Word/Excel/PPT 操作指南），需要时直接调用对应工具\n")
	if sess.Mode == "task" {
		if block := a.userSkillsPromptBlock(); block != "" {
			b.WriteString(block + "\n")
		}
	}
	return b.String()
}

// buildLLMMessages 已废弃删除：真正的组装只走 buildContext。

// osDescription 探测并缓存本机系统描述（Windows 版本 + PowerShell 版本）。
func (a *App) osDescription() string {
	a.osOnce.Do(func() {
		a.osDesc = detectOSDesc()
	})
	return a.osDesc
}

// detectOSDesc 探测系统描述：Windows 版本（cmd /c ver）+ PowerShell 版本
// （pwsh 优先，回退 powershell）。探测失败降级；结果由调用方缓存。
func detectOSDesc() string {
	if runtime.GOOS != "windows" {
		return runtime.GOOS + "（类 Unix）"
	}
	desc := "Windows"
	if out, err := exec.Command("cmd", "/c", "ver").Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if i := strings.Index(s, "Version "); i >= 0 {
			desc = "Windows " + strings.TrimSpace(strings.TrimRight(s[i+len("Version "):], "]"))
		}
	}
	ps := "PowerShell（版本未知）"
	for _, name := range []string{"pwsh", "powershell"} {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(cctx, name, "-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()").Output()
		cancel()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			ps = "PowerShell " + strings.TrimSpace(string(out))
			break
		}
	}
	return desc + " · " + ps + "（run_command 支持 powershell 与 cmd）"
}

// maybeAppendEnvSnapshot 环境快照 diff：与最近一条 kind=env 快照比较，
// 内容有变化才落库新的一条（时间戳粒度为分钟，通常每轮一条），并实时推给前端。
func (a *App) maybeAppendEnvSnapshot(sess *SessionMeta, env string, emit func(Event)) {
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err == nil {
		for i := len(raw) - 1; i >= 0; i-- {
			if raw[i].Kind == "env" {
				if raw[i].Text() == env {
					return
				}
				break
			}
		}
	}
	// 上下文材料行（env/摘要/快照）一律存 role=system：它们不是用户发言，
	// 只是入库归档；真实发送时由 buildContext 现场构造（env 走 user 尾消息）
	sm := a.appendRow(sess, StoredMessage{Message: TextMessage("system", env), Kind: "env", CreatedAt: now()})
	if emit != nil {
		emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: "env"})
	}
}

// planSuspendedTask 是计划模式下暂停的 task 会话工具（目录不变，调用时拦截）。
var planSuspendedTask = map[string]bool{
	"write_file": true, "edit_file": true, "run_command": true,
	"job_output": true, "job_list": true, "job_kill": true,
}

// suspendedInPlan 判断某工具在计划模式下是否被暂停。工具目录跨模式保持
// 不变（请求形状稳定，缓存友好）；约束靠环境快照公告 + 这里调用时硬拦。
func suspendedInPlan(mode, name string) bool {
	if strings.HasPrefix(name, "browser_") || strings.HasPrefix(name, "computer_") {
		return true
	}
	if mode == "assistant" {
		return name == "dispatch_task" || name == "send_to_session"
	}
	return planSuspendedTask[name]
}

// appendMessage 落库并返回带 seq 的记录（seq 供 SSE message 事件回传前端定位用）
func (a *App) appendMessage(sess *SessionMeta, m Message) *StoredMessage {
	sm := &StoredMessage{Message: m, Kind: "chat", CreatedAt: now()}
	_ = a.Store.AppendRow(sess.ID, sm)
	sess.UpdatedAt = now()
	_ = a.Store.SaveMeta(sess)
	return sm
}

// appendRow 落库一条自定义 kind 的消息（env 快照 / prompt 快照）。
func (a *App) appendRow(sess *SessionMeta, sm StoredMessage) *StoredMessage {
	_ = a.Store.AppendRow(sess.ID, &sm)
	sess.UpdatedAt = now()
	_ = a.Store.SaveMeta(sess)
	return &sm
}

// appendMessageThinking 同 appendMessage，但带上该步的思考过程原文。
func (a *App) appendMessageThinking(sess *SessionMeta, m Message, thinking string) *StoredMessage {
	sm := &StoredMessage{Message: m, Kind: "chat", Thinking: thinking, CreatedAt: now()}
	_ = a.Store.AppendRow(sess.ID, sm)
	sess.UpdatedAt = now()
	_ = a.Store.SaveMeta(sess)
	return sm
}

// RunAgent 是核心主循环：组装上下文 → 流式请求模型 → 执行工具 → 回填结果，
// 循环直到模型不再发起工具调用、用户中断或达到步数上限。
// 每步的上下文都从存储重建（buildContext 管线：摘要 + 规则压缩视图 + 近期原文）。
func (a *App) RunAgent(ctx context.Context, sess *SessionMeta, emit func(Event)) error {
	tools := a.toolsFor(sess.Mode)
	defs := make([]ToolDef, len(tools))
	runs := map[string]func(*toolContext, json.RawMessage) (string, error){}
	for i, tl := range tools {
		defs[i] = tl.Spec
		runs[tl.Spec.Function.Name] = tl.Run
	}
	sys := a.systemPrompt(sess) // 会话内固定：基础提示词 + 版本行
	target := a.resolveTarget(sess)
	overflowRetried := false
	// 兜底：异常退出/步数耗尽时残留的插话仍要落库，消息永不丢
	defer func() {
		for _, c := range a.drainInterjects(sess.ID) {
			a.appendMessage(sess, TextMessage("user", c))
		}
	}()

	// 提示词快照行：每轮记录当轮初始组装的完整上下文材料原文，供前端折叠展示——
	// 系统提示词 + 环境快照 + 工具定义（tools 即请求体里平级发送的独立 JSON 数组）。
	// 环境快照在循环里每步重算（模式中途切换立即反映），以尾部最新快照为准。
	if pj, err := json.Marshal(map[string]any{"sys": sys, "env": a.envPrompt(sess), "tools": defs}); err == nil {
		sm := a.appendRow(sess, StoredMessage{Message: TextMessage("system", string(pj)), Kind: "prompt", CreatedAt: now()})
		emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: "prompt"})
	}

	// interjectsBefore 在下一次调用模型前落库插话（不打断当前步骤），
	// 返回是否注入了消息。落库即可——上下文每步从存储重建，自然带上。
	interjectsBefore := func(emit func(Event)) bool {
		got := false
		for _, content := range a.drainInterjects(sess.ID) {
			um := TextMessage("user", content)
			sm := a.appendMessage(sess, um)
			emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq})
			got = true
		}
		return got
	}

	for step := 0; step < a.Cfg.MaxSteps; step++ {
		interjectsBefore(emit)
		// 环境快照每步重算 + diff：内容（含模式中途变化）有变才落新快照
		env := a.envPrompt(sess)
		a.maybeAppendEnvSnapshot(sess, env, emit)
		msgs := a.buildContext(sess, sys, env)
		// 视觉模型：把工具结果引用的图片内联为内容块；纯文本模型原样透传
		msgs = inlineImagesForRequest(a, msgs, target)
		if err := a.maybeAutoCompact(sess, msgs, target.ContextWindow); err != nil {
			emit(Event{Type: "error", Error: "自动压缩失败: " + err.Error()})
		}
		req := ChatRequest{Model: target.Model, Messages: msgs, Tools: defs}
		req.ApplyThinking(sess.Thinking) // 会话级思考档位（空串=服务端默认）
		var text strings.Builder
		var reasoning strings.Builder
		var calls []ToolCall
		err := StreamChat(ctx, target, req, StreamCallbacks{
			OnText: func(s string) {
				text.WriteString(s)
				emit(Event{Type: "delta", Delta: s})
			},
			OnReasoning: func(s string) {
				reasoning.WriteString(s)
				emit(Event{Type: "reasoning", Delta: s})
			},
		OnToolCall: func(tc ToolCall) { calls = append(calls, tc) },
		OnUsage: func(u Usage) { // 流尾用量按 token 累计落盘（缓存命中率/Token 用量面板数据源）
			sess.UsagePrompt += int64(u.PromptTokens)
			sess.UsageCacheHit += int64(u.PromptCacheHit)
			sess.UsageOutput += int64(u.CompletionTokens)
			sess.UsageCalls++
			_ = a.Store.SaveMeta(sess)
		},
	})
		if err != nil {
			// 上下文超限兜底：强制压缩一次后重发（DSH 式），每轮最多一次
			if isOverflowError(err) && !overflowRetried {
				if cerr := a.overflowCompact(sess); cerr == nil {
					overflowRetried = true
					continue
				}
			}
			// 中断的半截正文也落盘，历史不留空洞
			if strings.TrimSpace(text.String()) != "" {
				a.appendMessageThinking(sess, TextMessage("assistant", text.String()+"\n\n（本轮输出中断）"), reasoning.String())
			}
			if ctx.Err() == nil {
				emit(Event{Type: "error", Error: err.Error()})
			}
			return err
		}
		if len(calls) == 0 {
			if strings.TrimSpace(text.String()) != "" {
				msg := TextMessage("assistant", text.String())
				sm := a.appendMessageThinking(sess, msg, reasoning.String())
				emit(Event{Type: "message", Message: &sm.Message, Thinking: reasoning.String(), Seq: sm.Seq})
			}
			// 本轮将结束时来了插话：不收尾，带着插话继续下一轮
			if interjectsBefore(emit) {
				a.ruleCompactPass(sess)
				continue
			}
			emit(Event{Type: "done"})
			a.ruleCompactPass(sess) // 轮末：规则压缩增量检查
			return nil
		}

		am := Message{Role: "assistant", ToolCalls: calls}
		if strings.TrimSpace(text.String()) != "" {
			am.Content = text.String()
		}
		asm := a.appendMessageThinking(sess, am, reasoning.String())
		emit(Event{Type: "message", Message: &asm.Message, Thinking: reasoning.String(), Seq: asm.Seq})

		interrupted := false
		for _, tc := range calls {
			if ctx.Err() != nil {
				tm := Message{Role: "tool", Content: "（用户中断，未执行）", ToolCallID: tc.ID}
				a.appendMessage(sess, tm)
				interrupted = true
				continue
			}
			emit(Event{Type: "tool_start", Name: tc.Function.Name, Args: tc.Function.Arguments})
			var out string
			if sess.PlanMode && suspendedInPlan(sess.Mode, tc.Function.Name) {
				// 计划模式硬拦：工具目录保持不变（请求形状/缓存稳定），调用时拒绝；
				// exit_plan_mode 批准后 PlanMode 即刻置 false，同一回合内自动放行
				out = "计划模式下该工具已暂停：" + tc.Function.Name + "。当前仅可只读探索；请完成调研后调用 exit_plan_mode 提交计划供用户批准。"
			} else if run, ok := runs[tc.Function.Name]; ok {
				res, rerr := run(&toolContext{ctx: ctx, app: a, sess: sess, emit: emit}, json.RawMessage(tc.Function.Arguments))
				if rerr != nil {
					out = "工具执行失败: " + rerr.Error()
				} else {
					out = res
				}
			} else {
				out = "未知工具: " + tc.Function.Name
			}
			if strings.TrimSpace(out) == "" {
				out = "（工具执行完成，无输出）"
			}
			tm := Message{Role: "tool", Content: out, ToolCallID: tc.ID}
			tsm := a.appendMessage(sess, tm)
			emit(Event{Type: "message", Message: &tsm.Message, Seq: tsm.Seq})
			emit(Event{Type: "tool_end", Name: tc.Function.Name, Output: truncateRunes(out, 2000)})
		}
		if interrupted {
			emit(Event{Type: "done"})
			return ctx.Err()
		}
	}
	emit(Event{Type: "error", Error: fmt.Sprintf("连续 %d 步仍未完成，已停止（可在设置中调大最大步数）", a.Cfg.MaxSteps)})
	return fmt.Errorf("达到最大步数 %d", a.Cfg.MaxSteps)
}
