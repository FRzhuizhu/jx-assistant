package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Event 是后端向前端推送的流式事件（SSE data 帧的载荷）。
type Event struct {
	Type       string   `json:"type"` // message|delta|tool_start|tool_end|tool_wait|sub_start|sub_end|title|done|error|approval_required|approval_resolved|question_required|question_resolved|todos
	Delta      string   `json:"delta,omitempty"`
	Message    *Message `json:"message,omitempty"`
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

写进 dispatch_task / send_to_session 的 task 必须是一段自足的话：目标、验收标准、涉及的目录或文件、你知道的相关背景。别写「照上面说的做」这类指代——任务会话看不到我们的聊天记录。需要指定工作目录时用 workspace 参数给绝对路径。

## 汇报

派发工具立即返回后，先简短告知用户「已经安排」。子会话在后台执行，结束时你会**自动收到**一条【派发回报】消息，内含它的压缩摘要（做了几轮几步、调用了哪些工具、最终答复、改动了哪些文件），系统会随即触发你说一轮面向用户的汇报：用用户听得懂的话讲清楚做成了什么、结果在哪、有没有留下没做完的事。用户想细看，就提示他点开该子会话。

纯聊天、简单问答直接回答，不必派发。用中文与用户交流，语气专业、简洁。`

// envPermBlock 生成「运行环境与权限」段：按会话的本地/远程环境与
// 权限模式动态描述，让模型清楚知道边界在哪、被拒后该怎么引导用户。
func (a *App) envPermBlock(sess *SessionMeta) string {
	b := &strings.Builder{}
	b.WriteString("# 运行环境与权限\n")
	b.WriteString("- 当前时间：" + time.Now().Format("2006-01-02 15:04") + "\n")
	if sess.Remote != nil {
		r := sess.Remote
		b.WriteString("- 运行环境：SSH 远程工作区 " + r.Display() + "（类 Unix；命令经远程 shell 执行，run_command 的 shell 参数忽略，路径为 POSIX 格式）\n")
		b.WriteString("- 工作区目录（远程）：" + r.Dir + "；文件工具经 SFTP 读写远程目录\n")
	} else {
		b.WriteString("- 操作系统：Windows（run_command 支持 powershell 与 cmd）\n")
		b.WriteString("- 工作区目录：" + wsOr(a, sess) + "\n")
	}
	mode := permModeOf(sess)
	switch mode {
	case permReadOnly:
		b.WriteString("- 权限模式：只读——可读文件、检索、抓取网页；命令可在 OS 沙箱内执行但被禁止任何写入（read-only 沙箱）\n")
	case permFull:
		b.WriteString("- 权限模式：完全访问——读写与命令不受限（助手自身配置/数据仍受保护）\n")
	default:
		b.WriteString("- 权限模式：工作区内读写——文件写入仅限工作区；命令在 OS 级沙箱内执行（workspace-write）：只能写工作区与沙箱私有临时目录，联网不限\n")
	}
	b.WriteString("- 沙箱拒绝是正常工作方式：命令/写入直接尝试，被拒会得到 [sandbox: … denied under <mode> mode] 标记。若更宽权限能让它成功，在同一轮内带 sandbox_permissions（够用的最宽档：workspace-write / danger-full-access / 文件写边界外用 full）与一句 justification 重试一次；重试会弹审批由用户批准\n")
	b.WriteString("- 审批卡上用户点「始终允许」会点亮该类动作对应的能力开关（权限面板可见，如「允许工作区外执行命令」「读写工作区外文件」），之后该类动作不再逐次审批\n")
	b.WriteString("- 未经真实拒绝不要请求升级；升级被拒后该动作本轮终局：如实说明并停止，不要换法绕过；其他动作不受影响\n")
	b.WriteString("- 任何模式都禁止访问助手自身的配置与数据目录（含 API Key）；不可逆的破坏性命令会被危险命令护栏拦截（护栏默认开启，用户可在设置中关闭）\n")
	if hasCapability(sess, capBrowser) {
		b.WriteString("- 能力：已开启「操作浏览器」——可用 browser_* 工具打开网页、读取内容、点击、输入、截图\n")
	}
	if hasCapability(sess, capComputer) {
		b.WriteString("- 能力：已开启「操作电脑」——可用 computer_* 工具截屏、点击、输入、切换窗口（点击坐标以 computer_screenshot 的图片为准）\n")
	}
	b.WriteString("- 自带技能：pdf（读 PDF 文档）、ocr（图片文字识别）、office_skill（Word/Excel/PPT 操作指南），需要时直接调用对应工具\n")
	return b.String()
}

func (a *App) systemPrompt(sess *SessionMeta) string {
	base := taskPrompt
	switch sess.Mode {
	case "assistant":
		base = assistantPrompt
	case "aux":
		base = auxPrompt
	}
	prompt := base + "\n\n" + a.envPermBlock(sess)
	// 插件兼容层：已启用适配器可向系统提示词追加段（如语音输入提示）
	if a.Plugins != nil {
		if extra := a.Plugins.PromptExtra(a, sess); extra != "" {
			prompt += "\n" + extra + "\n"
		}
	}
	// 用户技能段追加在系统提示词的最末尾：基础提示词与环境块是稳定前缀，
	// 开关技能只改动尾部，不影响前缀缓存命中率（瞬态注入原则）。
	if sess.Mode == "task" {
		if block := a.userSkillsPromptBlock(); block != "" {
			prompt += "\n" + block + "\n"
		}
	}
	return prompt
}

// buildLLMMessages 把持久化历史转换为 LLM 请求消息序列，
// 并修剪结尾悬空的工具调用（客户端中断可能留下未配对的调用）。
func buildLLMMessages(sys string, history []Message) []Message {
	msgs := make([]Message, 0, len(history)+1)
	msgs = append(msgs, TextMessage("system", sys))
	msgs = append(msgs, history...)
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			msgs = msgs[:len(msgs)-1]
			continue
		}
		break
	}
	return msgs
}

func (a *App) appendMessage(sess *SessionMeta, m Message) {
	_ = a.Store.Append(sess.ID, m)
	sess.UpdatedAt = now()
	_ = a.Store.SaveMeta(sess)
}

// appendMessageThinking 同 appendMessage，但带上该步的思考过程原文。
func (a *App) appendMessageThinking(sess *SessionMeta, m Message, thinking string) {
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: m, Thinking: thinking, CreatedAt: now()})
	sess.UpdatedAt = now()
	_ = a.Store.SaveMeta(sess)
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
	sys := a.systemPrompt(sess)
	target := a.resolveTarget(sess)
	overflowRetried := false
	// 兜底：异常退出/步数耗尽时残留的插话仍要落库，消息永不丢
	defer func() {
		for _, c := range a.drainInterjects(sess.ID) {
			a.appendMessage(sess, TextMessage("user", c))
		}
	}()

	// interjectsBefore 在下一次调用模型前落库插话（不打断当前步骤），
	// 返回是否注入了消息。落库即可——上下文每步从存储重建，自然带上。
	interjectsBefore := func(emit func(Event)) bool {
		got := false
		for _, content := range a.drainInterjects(sess.ID) {
			um := TextMessage("user", content)
			a.appendMessage(sess, um)
			emit(Event{Type: "message", Message: &um})
			got = true
		}
		return got
	}

	for step := 0; step < a.Cfg.MaxSteps; step++ {
		interjectsBefore(emit)
		msgs := a.buildContext(sess, sys)
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
				a.appendMessageThinking(sess, msg, reasoning.String())
				emit(Event{Type: "message", Message: &msg})
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
		a.appendMessageThinking(sess, am, reasoning.String())
		emit(Event{Type: "message", Message: &am})

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
			if run, ok := runs[tc.Function.Name]; ok {
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
			a.appendMessage(sess, tm)
			emit(Event{Type: "message", Message: &tm})
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
