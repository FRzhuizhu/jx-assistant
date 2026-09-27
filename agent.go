package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Event 是后端向前端推送的流式事件（SSE data 帧的载荷）。
type Event struct {
	Type       string   `json:"type"` // message|delta|tool_start|tool_end|tool_wait|sub_start|sub_end|title|done|stopped|error|pending|approval_required|approval_resolved|question_required|question_resolved|todos
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
	QuestionID string `json:"questionId,omitempty"`
	// Kind 语义按 Type 分域（契约见《消息类型规范.md》）：message 事件=存储行 kind；
	// question_required 事件=ask | plan
	Kind    string   `json:"kind,omitempty"`
	Origin  string   `json:"origin,omitempty"` // message 事件附带 report/summary 的 origin 子类
	Plan    string   `json:"plan,omitempty"`
	Options []string `json:"options,omitempty"`
	// 待生效用户消息快照（pending 事件：插话/排队的最新列表，多窗口与刷新后一致）
	Pending *PendingState `json:"pending,omitempty"`
	// 内嵌浏览器（browser_panel_windows.go 桌面原生面板 / browser_host.go 网页形态）：
	// browser_nav=地址与标题（右栏工具栏跟随），browser_resize=AI 的视口宽度请求
	// （X/Y 复用为宽高，前端让对话区让位调宽面板）
	URL string  `json:"url,omitempty"`
	X   float64 `json:"x,omitempty"`
	Y   float64 `json:"y,omitempty"`
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
- dispatch_task：新建一个**工作任务**（用户任务列表里的顶层会话），把需求交给它**后台**执行；工具立即返回，不阻塞。
- dispatch_sub：新建一个**子对话**（挂在本对话下，右栏抽屉里可见），把一块独立的小工作交给它后台执行；完成后结果自动回报到这里。expert 参数可按用户预设的**子对话专家**派发（可用名单先用 list_experts 查询，名字逐字一致），预设自带追加提示词、默认模型与上下文继承。
- send_to_session：把新需求追加给已有会话并后台执行；同样立即返回。

## 怎么选目标

1. 新需求与某个已有会话明显相关、且不复杂 → 用 send_to_session 复用那个会话（上下文连续，效率最高）。
2. 不相关、或事情复杂、或那个会话已经很长 → 用 dispatch_task 新建会话。
3. 整件事值得单独立项（用户会想在任务列表里单独查看、单独复用）→ dispatch_task 开新工作任务；只是当前这件事里一块可并行的子工作、不必单独立项 → dispatch_sub 派子对话。
4. 一次可以派多个互不相干的需求（它们会并行执行）；同一件事别重复派。拿不准就问用户一句。

## 怎么交代需求

写进 dispatch_task / dispatch_sub / send_to_session 的 task 默认必须是一段自足的话：目标、验收标准、涉及的目录或文件、你知道的相关背景。别写「照上面说的做」这类指代——被派发的会话默认看不到我们的聊天记录。需要指定工作目录时用 workspace 参数给绝对路径。

上下文的分寸：**工作任务（dispatch_task）永远不带上下文**——你就像用户一样只给它发一段自足的消息，它和我们的聊天记录完全隔离。**子对话（dispatch_sub）按专家预设决定**：这件事的来龙去脉本来就长在我们现在的对话里（用户前面说的要求、我们讨论定的方案、已经写在聊天里的背景），把整段历史继承过去比重新写一遍更省事也更准确——这时用 expert=继承上下文子对话 派发，子对话会完整继承我们此前的聊天记录（含系统提示词，因此也命中缓存，成本很低），task 就只用写「接下来要做什么」。一次性的独立活儿按默认（标准子对话）派，别为了省几句话把无关的历史都塞过去。

你派发的是用户任务列表里的**工作任务**（顶层会话，用户在左侧列表能看到、能点开）；右栏抽屉里的「子对话」是用户手工创建的，与派发无关。子对话只有一级：子会话不能再派发子会话，你手上的编排工具只在主对话可用。

## 汇报

派发工具立即返回后，先简短告知用户「已经安排」。任务会话在后台执行，结束时你会**自动收到**一条【派发回报】消息，内含它的压缩摘要（做了几轮几步、调用了哪些工具、最终答复、改动了哪些文件），系统会随即触发你说一轮面向用户的汇报：用用户听得懂的话讲清楚做成了什么、结果在哪、有没有留下没做完的事。用户想细看，就提示他点开该任务。

纯聊天、简单问答直接回答，不必派发。用中文与用户交流，语气专业、简洁。`

// fileToolsPrompt 是有文件写入能力的工作准则补充（task / office 模式共用）：
// 什么时候自动有快照、什么时候必须手动打快照、成果怎么交付。
const fileToolsPrompt = `
## 文件修改与交付规范

文件操作一律优先用文件工具：查看文本文件用 read（而不是 cat 等命令行）；创建文件或整体替换内容用 write（既有文件先 read 读过才能覆盖——观察策略强制，本会话刚创建/刚改过的除外）；对既有文件的局部修改优先用 edit，而不是重写整个文件或用命令行脚本改。

1. write / edit 的修改自动记录历史快照，用户编辑历史消息时可以把文件一并回退，无需你做额外操作。
2. 通过 run_command、外部程序或文档运行库（Office 的 python-docx/openpyxl/python-pptx 等）修改文件无法自动追踪：改动一个已存在的重要文件前，必须先调用 snapshot_file 打快照，否则该文件无法回退；新建文件无需快照。
3. 产出最终交付文件后（无论什么方式生成），调用 deliver_result 登记交付清单——用户界面会展示交付卡片并提供下载，这是用户拿到成果的正式入口；只在回复里贴路径不算交付。每轮可多次调用，后登记的会并列展示。

## 工作区目录约定

- 工作区里的 .jxai/ 是本助手的系统目录：.jxai/snapshots 存文件快照正文（系统管理，禁止读写），.jxai/tmp 是给你用的暂存区。
- 一切临时产物——中间脚本、下载的素材、截图、过程数据——都放到 .jxai/tmp/ 下（可按任务建子目录），不要散落在工作区根目录；只有最终交付文件才放在正式位置。
- .jxai 不进目录检索（list_dir/glob/grep 不扫它），按路径直接 read 访问没有限制。`

// dispatchSubPrompt 是子对话派发工具的使用准则（task / office 模式附加；
// assistant 的派发准则在其专属提示词里）：什么时候把工作分给子对话、
// 怎么写任务描述、回报怎么消化。
const dispatchSubPrompt = `

## 子对话派发

- dispatch_sub：新建一个子对话，把一块**自洽的子任务**交给它后台执行（它有全部实操工具，工作目录默认与你相同）。工具立即返回，你可以先继续手头的其他部分；它结束后你会自动收到一条【派发回报】（含压缩摘要），系统随即让你带着结果继续推进当前任务。
- 上下文由专家预设决定：默认（标准子对话）只收到你写的 task（自足，看不到本对话记录）；这件事的来龙去脉就长在本对话里时用 expert=继承上下文子对话 派发，它会完整继承本对话的聊天记录，task 只用写「接下来要做什么」。
- 适合：相对独立、可以并行的分块（比如「整理 A 目录」交给子对话，你同时处理 B）。不适合：强依赖你当前上下文的小步操作——直接自己做更快。
- task 必须自足（默认模式下）：目标、验收标准、涉及目录写全，别写「照我上面说的做」。
- 子对话专家：用户可在设置中预制定义专家（追加提示词 / 默认模型 / 默认上下文继承），当前可用的专家名要先调用 list_experts 查询（名单是易变的，别凭记忆猜）。需要那种专门视角（如对抗性审查）或专门模型时用 expert=名称 派发，其余参数照常；清单里没有合适的专家就按普通子对话派，别编名字。
- 子对话只有一级：它们不能再派发。同质的批量工作可以一次派多个（并行执行）。`

// systemPrompt 是会话内固定的系统提示词：只有基础提示词 + 软件版本标识。
// 刻意不含任何易变信息（时间/权限/模式/环境）——它们全部进 envPrompt 的
// 环境快照（追加式：快照按落位进历史，变化以短更新消息出现在尾部，前缀
// 稳定命中 KV 缓存）。因此它只随软件版本变化，会话期间永远不变。
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
	// 引用文件覆盖：data/prompts/<mode>.md 存在时替换该模式的基础提示词；
	// 模式附加段（文件规范/派发准则）是产品硬规则，覆盖后仍追加。
	// 版本行记录提示词来源：内置路径文案保持原样（存量会话缓存不受影响），
	// 引用文件则附内容 hash——提示词变更必然改变请求前缀（断缓存），
	// hash 让这种变更在导出时间线上一眼可见。
	tag := ""
	if over := a.promptOverride(sess.Mode); over != "" {
		base = over
		tag = "；系统提示词：引用文件 " + promptHash(base)
	}
	// 文件修改与交付规范只给有写入能力的模式（task / office）；
	// assistant 只编排不亲手改文件，aux 只读，都不需要。
	// 子对话派发准则同样只给 task / office（assistant 的派发准则在其专属提示词里）。
	if sess.Mode == "task" || sess.Mode == "office" {
		base += fileToolsPrompt + dispatchSubPrompt
	}
	// 子对话专家的追加提示词：预设只记名字，这里按名解析拼接在模式基础提示词
	// 之后（会话内稳定——专家固定于派发时；删改专家等同改引用文件，前缀随之变化）
	if sess.Expert != "" {
		if ex := a.expertByName(sess.Expert); ex != nil && strings.TrimSpace(ex.Prompt) != "" {
			base += "\n\n## 子对话专家设定：" + ex.Name + "\n\n" + strings.TrimSpace(ex.Prompt)
		}
	}
	return base + "\n\n（本助手由 坚祥数字助理 v" + appVersion + " 驱动" + tag + "）"
}

// promptHash 取提示词内容的短指纹（版本行/时间线标识用；非安全用途）。
func promptHash(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// envPrompt 生成「运行环境快照」：一切易变信息的唯一归宿。内容有变化才落库
// 新快照（见 maybeAppendEnvSnapshot 的 diff 逻辑）；组装时首条整份进历史、
// 后续按行差分为短「环境更新」（见 buildContext），已发送的前缀绝不改写。
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
	if exe, err := os.Executable(); err == nil {
		b.WriteString("- 引擎程序：" + exe + "（支持子命令，如 `apkinfo <file.apk>` 查看 APK 包名/版本/签名）\n")
	}
	mode := permModeOf(sess)
	switch mode {
	case permReadOnly:
		if isAndroidRuntime {
			b.WriteString("- 权限模式：只读——可读文件、检索内容、抓取网页；文件工具禁止写入。本机命令无 OS 级沙箱（安卓），不要执行会产生写入副作用的命令\n")
		} else {
			b.WriteString("- 权限模式：只读——可读文件、检索内容、抓取网页；命令可在 OS 沙箱内执行但被禁止任何写入（read-only 沙箱）\n")
		}
	case permFull:
		b.WriteString("- 权限模式：完全访问——读写与命令不受限（助手自身配置/数据仍受保护）；已捆绑开启「允许工作区外执行命令」「操作浏览器」「操作电脑」能力\n")
	default:
		if isAndroidRuntime {
			b.WriteString("- 权限模式：工作区内读写——文件写入仅限工作区；本机命令无 OS 级沙箱（安卓内核禁用 Landlock），命令直接执行、由安卓应用沙箱承担隔离（对齐社区方案），联网不限\n")
		} else {
			b.WriteString("- 权限模式：工作区内读写——文件写入仅限工作区；命令在 OS 级沙箱内执行（workspace-write）：只能写工作区与沙箱私有临时目录，联网不限\n")
		}
	}
	b.WriteString("- 沙箱拒绝是正常工作方式：命令/写入直接尝试，被拒会得到 [sandbox: … denied under <mode> mode] 标记。若更宽权限能让它成功，在同一轮内带 sandbox_permissions（够用的最宽档：workspace-write / danger-full-access / 文件写边界外用 full）与一句 justification 重试一次；重试会弹审批由用户批准\n")
	b.WriteString("- 审批卡上用户点「始终允许」= 常设授权：命令升级点亮对应能力开关（如「允许工作区外执行命令」），工作区外写入把会话权限提升为完全访问（捆绑能力一并点亮），之后该类动作不再逐次审批\n")
	b.WriteString("- 未经真实拒绝不要请求升级；升级被拒后该动作本轮终局：如实说明并停止，不要换法绕过；其他动作不受影响\n")
	b.WriteString("- 任何模式都禁止访问助手自身的配置与数据目录（含 API Key）；不可逆的破坏性命令会被危险命令护栏拦截（护栏默认开启，用户可在设置中关闭）\n")
	// 能力开关只对带这些工具的模式（task/office）有意义。工具定义常驻目录
	// （不随开关增删，请求形状稳定、缓存友好），真实状态在这里公告——
	// 开与关都说，变化经环境快照补充在对话尾部，配合调用时的 requireCapability 拦截
	if sess.Mode == "task" || sess.Mode == "office" {
		if hasCapability(sess, capBrowser) {
			b.WriteString("- 能力：已开启「操作浏览器」——可用 browser_* 工具打开网页、读取内容、点击、输入、截图\n")
		} else {
			b.WriteString("- 能力：未开启「操作浏览器」——browser_* 工具的调用会被拒绝；需要时请让用户在权限芯片中勾选后再试\n")
		}
		if hasCapability(sess, capComputer) {
			b.WriteString("- 能力：已开启「操作电脑」——可用 computer_* 工具截屏、点击、输入、切换窗口（点击坐标以 computer_screenshot 的图片为准）\n")
		} else {
			b.WriteString("- 能力：未开启「操作电脑」——computer_* 工具的调用会被拒绝；需要时请让用户在权限芯片中勾选后再试\n")
		}
	}
	if sess.PlanMode {
		if sess.Mode == "assistant" {
			b.WriteString("- 计划模式：已开启。dispatch_task / send_to_session 已暂停（任务会话会实际改动文件）；此阶段只用只读工具调研（list_sessions / read_session_summary / search_sessions / read_session_messages），调研完成后调用 exit_plan_mode 提交计划（目标/步骤/涉及文件/验收标准）供用户批准，批准后恢复派发。\n")
		} else {
			b.WriteString("- 计划模式：已开启。write / edit / run_command / browser_* / computer_* / job_* 工具已暂停；此阶段只做只读探索（read / grep / glob / list_dir / web_fetch 等），调研完成后调用 exit_plan_mode 提交计划（目标/步骤/涉及文件/验收标准）供用户批准，批准后恢复执行。\n")
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
		b.WriteString("- 本会话是子对话（父会话 " + sess.ParentID + "）：开头继承的上下文只是背景资料——其中可能正是父对话进行到一半的工作快照，不要去延续、续做或修改那些未完成的工作（父对话可能仍在执行，两边会冲突）；你只做交给自己的这一件事。子对话只有一级，不配备任何派发工具；需要并行分块时把安排写进你的答复，由父对话或用户来派。\n")
	}
	// 插件兼容层的动态段（如语音听写提示）也归入环境快照——它们随时间变化
	if a.Plugins != nil {
		if extra := a.Plugins.PromptExtra(a, sess); extra != "" {
			b.WriteString(extra + "\n")
		}
	}
	// 自带技能一览（静态）；用户技能段也归入快照——开关技能只影响尾部
	// 快照，系统提示词的稳定前缀不受影响（瞬态注入原则）。
	b.WriteString("- 自带技能：pdf（读 PDF 文档）、ocr（图片文字识别）、office_skill（Word/Excel/PPT 操作指南）、downloader（下载器：aria2c 下载落盘）、apkinfo（APK 包名/版本/签名查看），需要时直接调用对应工具或子命令\n")
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

// detectOSDesc 探测系统描述：给模型「我在哪、有什么、注意什么」——
// Windows 版本 + PowerShell 版本 + 中文编码规则；Linux 发行版与内核；
// 安卓版本/ABI + toybox 边界与常见坑。探测失败逐项降级、缺哪段省哪段；
// 结果由调用方缓存。
func detectOSDesc() string {
	switch runtime.GOOS {
	case "windows":
		return detectOSDescWindows()
	case "linux":
		return detectOSDescLinux()
	case "android":
		return detectOSDescAndroid()
	default:
		return runtime.GOOS + "（类 Unix）"
	}
}

// detectOSDescWindows：Windows 版本（cmd /c ver）+ PowerShell 版本
// （pwsh 优先，回退 powershell），附中文编码规则（沿袭 dsh-env-context
// 插件的 WIN32_NOTES：cmd 默认 GBK 代码页、PowerShell 控制台编码、
// 文件一律 UTF-8 无 BOM）。
func detectOSDescWindows() string {
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
	return desc + " · " + ps + "（run_command 支持 powershell 与 cmd）\n" +
		"- 中文编码：cmd 默认代码页 936（GBK），涉及中文输入输出先执行 chcp 65001；Windows PowerShell 5.1 按控制台编码输出，中文 I/O 前设 [Console]::OutputEncoding 为 UTF-8\n" +
		"- 文件一律 UTF-8 无 BOM 读写；路径、标识符、命令保持原文不翻译（常见 GBK 命令输出引擎已自动解码）"
}

// kernelRelease：uname -r，失败返回空。
func kernelRelease() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// osReleasePrettyName：/etc/os-release 的 PRETTY_NAME，失败返回空。
func osReleasePrettyName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// detectOSDescLinux：发行版 + 内核 + shell 提示。
func detectOSDescLinux() string {
	head := "linux"
	if pretty := osReleasePrettyName(); pretty != "" {
		head += "（" + pretty + "）"
	}
	if kr := kernelRelease(); kr != "" {
		head += " · 内核 " + kr
	}
	return head + " · shell 为 sh（run_command 的 shell 参数用 sh）· 文本默认 UTF-8"
}

// detectOSDescAndroid：安卓版本/ABI/机型/内核 + toybox 命令边界与
// 常见坑（SELinux 禁硬链接、/sdcard noexec、精简版 grep/sed、无
// TLS 命令行下载工具——写清一次，避免每轮现场重新探测）。
func detectOSDescAndroid() string {
	prop := func(name string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "getprop", name).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	rel := prop("ro.build.version.release")
	sdk := prop("ro.build.version.sdk")
	abi := prop("ro.product.cpu.abi")
	model := prop("ro.product.model")
	if rel == "" {
		// 极端环境（连 getprop 都没有）至少如实降级
		head := "android"
		if kr := kernelRelease(); kr != "" {
			head += " · 内核 " + kr
		}
		return head + " · shell 为 sh"
	}
	parts := []string{}
	if sdk != "" {
		parts = append(parts, "API "+sdk)
	}
	if abi != "" {
		parts = append(parts, abi)
	}
	if model != "" {
		parts = append(parts, model)
	}
	head := "Android " + rel
	if len(parts) > 0 {
		head += "（" + strings.Join(parts, "，") + "）"
	}
	if kr := kernelRelease(); kr != "" {
		head += " · 内核 " + kr
	}
	head += " · shell 为 toybox（run_command 的 shell 参数用 sh）"
	return head + "\n" +
		"- 可用命令：pm am dumpsys settings getprop input uiautomator screencap logcat monkey cmd content 及常用 toybox 工具（grep/sed 为精简版，部分 GNU 选项不支持）\n" +
		"- 没有 curl、https 版 wget、openssl、python、aapt2/apksigner/keytool——下载用「下载器」技能（aria2c），APK 校验用 apkinfo 子命令，不要反复探测\n" +
		"- 常见坑：SELinux 禁硬链接（建临时文件用占位+rename，write_file 已内置处理）；/sdcard 多为 noexec 挂载，可执行文件放 /data/local/tmp 或应用 nativeLibraryDir，放 /sdcard 会 Permission denied；文本默认 UTF-8 无编码坑"
}

// maybeAppendEnvSnapshot 环境快照 diff：与最近一条 kind=env 快照比较（比较时
// 剔除历史快照尾部的技能变更行，避免变更说明自我累积、每步重复落快照），
// 内容有变化才落库新的一条（时间戳粒度为分钟，通常每轮一条），并实时推给前端。
// 用户技能清单有增减时，在新快照尾部追加一行「（技能变更：…）」——上文历史
// 快照一律不动。快照按**追加式**进上下文（buildContext 首条整份、后续按行差分
// 为短更新，已发送前缀绝不改写）。返回最终落库的快照内容（Assembly.Env 口径）。
func (a *App) maybeAppendEnvSnapshot(sess *SessionMeta, env string, emit func(Event)) string {
	var prev string
	havePrev := false
	raw, err := a.Store.RawMessagesAll(sess.ID)
	if err == nil {
		for i := len(raw) - 1; i >= 0; i-- {
			if raw[i].Kind == "env" {
				prev, havePrev = raw[i].Text(), true
				break
			}
		}
	}
	if havePrev && stripSkillChangeNote(prev) == env {
		return env
	}
	if havePrev {
		if note := skillChangeNote(stripSkillChangeNote(prev), env); note != "" {
			env = env + "\n（" + note + "）"
		}
	}
	// 上下文材料行（env/摘要/快照）一律存 role=system：它们不是用户发言，
	// 只是入库归档；真实发送时由 buildContext 现场构造（env 走 user 尾消息）
	sm := a.appendRow(sess, StoredMessage{Message: TextMessage("system", env), Kind: kindEnv, CreatedAt: now()})
	if emit != nil {
		emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: kindEnv})
	}
	return env
}

// stripSkillChangeNote 去掉快照尾部的技能变更行，得到用于比较的规范内容。
func stripSkillChangeNote(env string) string {
	i := strings.LastIndex(env, "\n（技能变更：")
	if i < 0 || !strings.HasSuffix(env, "）") {
		return env
	}
	return env[:i]
}

// skillChangeNote 对比新旧环境快照里的用户技能名单，生成一行变更说明；
// 没有变化或首次出现清单（prev 为空）时返回空串。旧格式（全文注入时代的
// 「## 用户技能：名字」段）与新清单格式都识别。
func skillChangeNote(prev, cur string) string {
	if prev == "" {
		return ""
	}
	oldNames := skillNamesInEnv(prev)
	newNames := skillNamesInEnv(cur)
	if len(oldNames) == 0 && len(newNames) == 0 {
		return ""
	}
	var add, del []string
	for n := range newNames {
		if !oldNames[n] {
			add = append(add, n)
		}
	}
	for n := range oldNames {
		if !newNames[n] {
			del = append(del, n)
		}
	}
	sort.Strings(add)
	sort.Strings(del)
	var parts []string
	if len(add) > 0 {
		parts = append(parts, "新增技能："+strings.Join(add, "、"))
	}
	if len(del) > 0 {
		parts = append(parts, "移除技能："+strings.Join(del, "、"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "技能变更：" + strings.Join(parts, "；")
}

// skillNamesInEnv 从环境快照文本里提取用户技能名集合：新清单行
// 「  - 名字（id=…）」与旧全文段的「## 用户技能：名字」都识别；
// 变更说明行自身不是技能，跳过。
func skillNamesInEnv(env string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(env, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "（技能变更") {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			rest := strings.TrimPrefix(line, "- ")
			if i := strings.Index(rest, "（id="); i > 0 {
				out[rest[:i]] = true
				continue
			}
		}
		if strings.HasPrefix(line, "## 用户技能：") {
			out[strings.TrimSpace(strings.TrimPrefix(line, "## 用户技能："))] = true
		}
	}
	return out
}

// planSuspendedTask 是计划模式下暂停的 task 会话工具（目录不变，调用时拦截）。
var planSuspendedTask = map[string]bool{
	"write": true, "edit": true, "run_command": true,
	"job_output": true, "job_list": true, "job_kill": true,
	"deliver_file": true, // 登记即承诺搬运，计划模式下不该落任何实际行动
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
	sm := &StoredMessage{Message: m, Kind: kindChat, CreatedAt: now()}
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
	sm := &StoredMessage{Message: m, Kind: kindChat, Thinking: thinking, CreatedAt: now()}
	_ = a.Store.AppendRow(sess.ID, sm)
	sess.UpdatedAt = now()
	_ = a.Store.SaveMeta(sess)
	return sm
}

// RunAgent 是核心主循环：向上下文引擎取包 → 流式请求模型 → 执行工具 → 回填结果，
// 循环直到模型不再发起工具调用、用户中断或达到步数上限。
// 发给模型的内容一律来自引擎的 Assembly（组装、开轮留痕、测量口径全归引擎），
// 主循环只经手——补 Model/思考档位等调用参数并发送，不碰任何拼装函数。
// 每步的上下文都从存储重建（引擎的组装管线：摘要 + 规则压缩视图 + 近期原文）。
func (a *App) RunAgent(ctx context.Context, sess *SessionMeta, emit func(Event)) error {
	runs := map[string]func(*toolContext, json.RawMessage) (string, error){} // 工具执行注册表（编排用；请求侧的工具目录来自引擎 Assembly）
	for _, tl := range a.toolsFor(sess.Mode) {
		runs[tl.Spec.Function.Name] = tl.Run
	}
	target := a.resolveTarget(sess)
	overflowRetried := false
	modelFallback := false // 专家预设模型不可用 → 切全局默认重试过一次（防循环）

	// interjectsBefore 在下一次调用模型前落库插话（不打断当前步骤），
	// 返回是否注入了消息。落库即可——上下文每步从存储重建，自然带上。
	interjectsBefore := func(emit func(Event)) bool {
		got := false
		for _, pm := range a.drainSteer(sess.ID) {
			um := TextMessage("user", pm.Content)
			sm := a.appendMessage(sess, um)
			emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: kindChat})
			got = true
		}
		return got
	}

	// 开轮留痕：本轮拼装快照（系统提示词/环境快照/工具定义）由上下文引擎
	// 组装并落 kind=prompt 行（OpenRound），主循环不再亲手拼这条行
	a.ctxEngine().OpenRound(sess, emit)
	defer a.requeueSteer(sess.ID) // 正常/异常退出后残留插话转为排队，由执行体接着发

	for step := 0; step < a.Cfg.MaxSteps; step++ {
		interjectsBefore(emit)
		// 上下文引擎组装本步全部请求材料与测量口径（系统提示词/环境快照/工具
		// 目录/消息视图/视觉内联/env 快照留痕/token 估算/前缀指纹都归引擎）
		pack := a.ctxEngine().Assemble(sess, emit)
		msgs := pack.Msgs
		if err := a.maybeAutoCompact(sess, msgs, target.ContextWindow); err != nil {
			emit(Event{Type: "error", Error: "自动压缩失败: " + err.Error()})
			a.auditWrite(sess, map[string]any{"type": "error", "error": "自动压缩失败: " + err.Error()})
		}
		req := ChatRequest{Model: target.Model, Messages: msgs, Tools: pack.Tools}
		req.ApplyThinking(sess.Thinking) // 会话级思考档位（空串=服务端默认）
		var text strings.Builder
		var reasoning strings.Builder
		var calls []ToolCall
		var usage *Usage // 本步流尾用量（审计行用；网关不带 usage 则为 nil）
		callStart := time.Now()
		err := StreamChat(ctx, target, req, StreamCallbacks{
			// 请求发出前捕获全量请求体，经引擎缓存（环形保留最近三轮，「导出原始数据」数据源）
			OnRequest: func(u string, body []byte) {
				a.ctxEngine().RecordRequest(sess, u, target.Model, step+1, len(msgs), pack.EstTokens, pack.SysHash, pack.EnvHash, body)
			},
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
				uu := u
				usage = &uu
				sess.UsagePrompt += int64(u.PromptTokens)
				sess.UsageCacheHit += int64(u.PromptCacheHit)
				sess.UsageOutput += int64(u.CompletionTokens)
				sess.UsageCalls++
				_ = a.Store.SaveMeta(sess)
			},
		})
		// 调用日志（引擎记录）：成败都落——发送规模、前缀指纹、返回、缓存命中、耗时
		a.ctxEngine().RecordCall(sess, step+1, len(msgs), pack.EstTokens, pack.SysHash, pack.EnvHash, time.Since(callStart).Milliseconds(),
			usage, len([]rune(text.String())), toolNamesOf(calls), err)
		if err != nil {
			// 上下文超限兜底：强制压缩一次后重发（DSH 式），每轮最多一次
			if isOverflowError(err) && !overflowRetried {
				if cerr := a.overflowCompact(sess); cerr == nil {
					a.auditWrite(sess, map[string]any{"type": "overflow", "error": err.Error()})
					overflowRetried = true
					continue
				}
			}
			// 专家预设模型不可用兜底：会话带着专家预设的模型（Expert 非空）而
			// 失败明确是「这个模型/提供方用不了」（余额不足/鉴权失败/模型不存在/
			// 连不上），且与全局默认目标不同——切回全局默认（= 主对话的默认模型）
			// 重试本步并落库，后续步不再撞同一个错；调用日志里两次调用都可查。
			// 只兜专家会话：用户在普通会话手选的模型被拒时应如实报错，不静默降级。
			if !modelFallback && sess.Expert != "" && isModelUnavailableErr(err) {
				if def := a.DefaultTarget(); def.Model != target.Model || def.BaseURL != target.BaseURL {
					from := target.Model + " @ " + target.BaseURL
					sess.Provider, sess.Model = "", ""
					_ = a.Store.SaveMeta(sess)
					target = a.resolveTarget(sess)
					modelFallback = true
					a.auditWrite(sess, map[string]any{"type": "model_fallback",
						"expert": sess.Expert, "from_model": from, "to_model": target.Model,
						"error": truncateRunes(err.Error(), 300)})
					emit(Event{Type: "tool_wait", Name: "model-fallback",
						Output: "专家预设的模型不可用（" + truncateRunes(err.Error(), 160) +
							"），已切回默认模型 " + target.Model + " 继续执行"})
					continue
				}
			}
			// 半截正文落盘，历史不留空洞（这是助手自己说过的话，标一下被中断）
			// 说明：异常收尾不往对话里塞“系统交代”，用户可见的报错统一走前端的报错区，
			// 见 executeRun 的终态事件与前端 runErrorBar
			if strings.TrimSpace(text.String()) != "" {
				a.appendMessageThinking(sess, TextMessage("assistant", text.String()+"\n\n（本轮输出中断）"), reasoning.String())
			}
			return err
		}
		if len(calls) == 0 {
			if strings.TrimSpace(text.String()) != "" {
				msg := TextMessage("assistant", text.String())
				sm := a.appendMessageThinking(sess, msg, reasoning.String())
				emit(Event{Type: "message", Message: &sm.Message, Thinking: reasoning.String(), Seq: sm.Seq, Kind: kindChat})
			} else if strings.TrimSpace(reasoning.String()) != "" {
				// 只出了思考没出正文：思考原文落盘（前端折叠展示），本轮按失败收尾
				sm := a.appendMessageThinking(sess, TextMessage("assistant", ""), reasoning.String())
				emit(Event{Type: "message", Message: &sm.Message, Thinking: reasoning.String(), Seq: sm.Seq, Kind: kindChat})
				return errors.New("模型本轮只返回了思考内容，没有给出正文，可点重试")
			} else {
				return errors.New("模型本轮没有返回任何内容，可点重试")
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
		emit(Event{Type: "message", Message: &asm.Message, Thinking: reasoning.String(), Seq: asm.Seq, Kind: kindChat})

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
				tctx := &toolContext{ctx: ctx, app: a, sess: sess, emit: emit,
					seq: asm.Seq, callID: tc.ID, toolName: tc.Function.Name}
				res, rerr := run(tctx, json.RawMessage(tc.Function.Arguments))
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
			emit(Event{Type: "message", Message: &tsm.Message, Seq: tsm.Seq, Kind: kindChat})
			emit(Event{Type: "tool_end", Name: tc.Function.Name, Output: truncateRunes(out, 2000)})
		}
		if interrupted {
			// 用户停止：不往对话里塞交代，也不进报错区（前端只是收掉流式）
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}
	}
	return fmt.Errorf("连续 %d 步仍未完成，已自动停止（可在设置中调大最大步数），可点重试", a.Cfg.MaxSteps)
}
