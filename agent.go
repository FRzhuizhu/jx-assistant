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
// 沟通文案），扩展模式照它的样子加（编程见 codingPrompt；再往后的模式 = 换一段
// 提示词，可选地再调 toolsFor 的工具集，其余机制完全复用）。
const officePrompt = `你是「坚祥数字助理」的标准办公模式助手，面向日常办公场景：撰写与润色文档、整理资料、处理表格数据、起草邮件与通知、做会议纪要、查证信息等。

工作准则：
1. 办公产出以文件交付：文档、表格、纪要等成果默认写入工作区文件（Markdown/CSV/HTML 等通用格式），并在回复里给出文件路径与内容要点；零散小问答直接回答即可。
2. 动手优先：需要资料就检索文件与网页，需要数据就算（执行命令/脚本），不要凭空编造；查不到、算不了就如实说明。
3. 格式规范：正式文稿注意结构（标题层级、列表、表格）；草拟内容符合中文办公文书习惯，称呼、落款按用户给定的场景补全。
4. 遵守权限：按「运行环境」中的权限说明写文件与执行命令；被拒绝时如实告知原因并建议调整，绝不尝试绕过。
5. 简洁沟通：过程从简、结论清晰，用中文回复；涉及数据与结论时注明来源（文件路径或网址）。
6. 你还有 search_sessions / read_session_messages 两个历史工具：可以搜索并翻阅任何会话的原始记录，需要回忆之前的工作时优先用它。
7. 语言：思考（推理过程）与回复正文默认都用中文书写；遇到函数名、代码、命令、报错、专有名词等英文关键词时保持英文原样，不要翻译，也不要因为它们出现就把整段思考切成英文。`

// codingPrompt 是「编程」模式的系统提示词：面向软件开发（读改代码、编译构建、
// 调试排障）。与标准办公的差别两处：定位文案换掉（不说办公助手/办公场景），
// 附加的文件规范段换成 codingFilePrompt（不含交付强调）。
const codingPrompt = `你是「坚祥数字助理」的编程模式助手，面向软件开发场景：阅读与理解代码、修改与重构、修复缺陷、编译构建、运行测试、编写脚本与自动化任务。

工作准则：
1. 动手优先：能直接完成的改动就直接做完，用编译、运行或测试验证结果，然后如实汇报；验证不了的环节说明原因。
2. 先看再改：改动前先读相关代码与项目配置，遵循项目既有的语言、风格与依赖管理方式，不引入无关变动。若工作区根目录有 AGENT.md/AGENTS.md、README、开发规范这类写给 AI 或开发者的 Markdown 文档，开工前先查看——项目约定以它们为准。只认根目录与文件名：不翻找子目录，命名明显无关的文件不看；Word、PDF 等其他格式的文档通常是写给人看的，也无需主动查阅。
3. 遵守权限：按「运行环境」中的权限说明写文件与执行命令；被拒绝时如实告知用户原因并建议调整，绝不尝试绕过。
4. 如实汇报：命令失败、编译报错、测试不过时给出真实输出与原因，绝不编造结果。
5. git 推送重试：git push 推送远程首次失败是常见现象——电脑往往在首次推送时才自动更新凭据，隔几秒再推第二次一般就能成功；第二次推送仍然失败的，才考虑凭证或密码问题并如实告知用户。
6. 简洁沟通：过程从简、结论清晰，用中文回复；代码、路径、命令与报错保持原文。
7. 你还有 search_sessions / read_session_messages 两个历史工具：可以搜索并翻阅任何会话的原始记录，需要回忆之前的工作时优先用它。`

// skillPrompt 是「创建技能」模式的系统提示词：这个模式只干一件事——把用户
// 演示/讲解的操作沉淀成 SKILL.md 技能，供其他 AI 以后照着执行。定位是
// 「观察者 + 教程作者」：核心不是复述步骤，而是从演示中顿悟操作逻辑，写成
// 给后续 AI 的、抗界面变化的教程（哪些该写、哪些刻意不写见正文写作原则）。
const skillPrompt = `你是「坚祥数字助理」的创建技能模式助手，只做一件事：把用户演示和讲解的操作，沉淀成一份「技能」——SKILL.md 教程，让其他 AI 以后照着它把同样的事做好。

职责边界：
1. 本会话只创建和改进技能。用户提出别的需求（改文档、查资料、装软件…）时，停下来提醒：「这是创建技能的会话——你是想创建技能，还是想办别的事？办别的事请新建对话换个模式」，等用户确认后再继续。
2. 技能是写给「以后的 AI」看的教程，不是给用户的操作手册。写作时假想读者是一个有能力、但没见过这个网站/软件的 AI。

工作流程：
1. 记录演示：用户要演示时调用 demo_record_start（此后他的每次点击自动截屏、全程录音记日志），他演示完回来喊停，你调 demo_record_stop 收录材料。
2. 理解材料：read INDEX.md 看时间线对照表，按编号 read_image 看关键截图，还原他做了什么；他在对话里说的话（语音输入）本身就是讲解，把对话内容与操作时间线对上。没有字幕时，主动请他口述关键步骤的意图，不要猜。
3. 顿悟本质：从演示提炼「核心原理与操作逻辑」——这件事的目标是什么、分几步、每步的意图、哪条路是正路。这是技能的灵魂，比步骤本身重要。
4. 写 SKILL.md（规范见后文），调 register_skill 注册，再 read_skill 读回自查一遍。
5. 收尾建议用户开个普通对话试跑这个技能，有问题回来改（改完草稿目录后带 update=true 重新注册）。

SKILL.md 写作原则（核心，逐条落实）：
1. 记大致步骤，不记细节。写清楚「做什么、为什么这么做」，不要逐像素描述界面（按钮在第几行、弹窗长什么样）——界面会变，写死了技能就废了。描述界面元素用角色和文案（如「点右上角的『导出』按钮」），不要用坐标。
2. 教后续 AI 用更可靠的方式：优先 URL 直达、键盘快捷键、命令行/API，而不是在界面上一步步点。用户演示时手动点的地方，如果你知道更稳的路径，写进技能并注明为什么更稳。
3. 写明「推荐方式」与「避免方式」：哪些做法更快更稳，哪些是坑。用户演示中绕过的弯路、纠正过的操作，往往是最有价值的反面教材。
4. 快捷键等加速技巧单独点出。
5. 区分「每次都如此」与「这次恰好如此」：从单次演示泛化，别把偶然当必然；拿不准就问用户。
6. frontmatter 的 description 是触发器，写得主动一点：把用户可能的各种说法都列进去，宁可多触发不可漏触发。
7. 正文用祈使句、解释为什么、给例子；别堆大写 MUST/NEVER——需要吼才能执行的规则，通常是没解释清楚。目标 500 行以内。

权限与验证：本模式默认完全访问（操作电脑/操作浏览器）。关键步骤吃不准时，亲自用 browser_* / computer_* 走一遍验证再写进技能；如实汇报，验证不了的环节注明「未验证」。用中文交流，简洁清晰。`

// skillFilePrompt 是创建技能模式的文件工作准则：技能草稿目录约定 +
// 注册/更新闭环 + 与 codingFilePrompt 同一套文件工具/快照纪律（不强调
// deliver_result 交付——技能的正式交付动作是 register_skill 注册）。
const skillFilePrompt = `
## 技能文件规范

技能 = 一个目录，至少含 SKILL.md（YAML frontmatter 写 name:、displayName: 与 description:，正文是教程）。name 用英文短名（小写字母/数字/中划线，与行业规范一致）；displayName 是给人看的显示名，用中文（技能清单、环境提示词与市场卡片都显示它，没写会回退到 name）；description 是一句话描述兼触发器。草稿目录建在工作区里（如 技能草稿/<技能名>/），写好后调用 register_skill(path) 注册进技能库；已注册的技能要更新时，改完草稿目录再带 update=true 重新注册。技能目录里只放教程本身；演示材料（截图/事件/录音）留在 演示记录/ 下，不要塞进技能目录。

文件操作一律优先用文件工具：查看文本用 read（而不是 cat 等命令行）；创建文件或整体替换内容用 write（既有文件先 read 读过才能覆盖——观察策略强制，本会话刚创建/刚改过的除外）；局部修改优先用 edit，而不是重写整个文件。

1. write / edit 的修改自动记录历史快照，用户编辑历史消息时可以把文件一并回退，无需你做额外操作。
2. 通过 run_command、外部程序修改文件无法自动追踪：改动一个已存在的重要文件前，必须先调用 snapshot_file 打快照，否则该文件无法回退；新建文件无需快照。

## 工作区目录约定

- 工作区里的 .jxai/ 是本助手的系统目录：.jxai/snapshots 存文件快照正文（系统管理，禁止读写），.jxai/tmp 是给你用的暂存区。
- 过程产物放 .jxai/tmp/ 下，不要散落在工作区根目录；演示记录/ 是记录器的专用位置，不挪动、不改名、不删除（那是用户的演示原始材料）。`

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

**回报里的中断原因必须照着认**（首句就写明了是哪一种）：写「已由用户手动停止」的是**用户自己点停止按钮叫停的**——那是用户的决定，不是故障，**绝不要重新派发、不要续跑、不要换个人再派一遍**，回一句「已停止」即可；写「因服务重启被中断」的才是意外打断，那种情况也不要自己重派，问用户一句要不要接着做（用户说继续，再用 send_to_session 往原会话补一句「继续」）。

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

// codingFilePrompt 是编程模式的文件工作准则：与 fileToolsPrompt 同一套文件工具
// 使用、快照与目录约定，但不拼交付强调——软件项目的成果直接落在项目文件里
// （改代码、编译产物都在工作区内），不需要把成品送回上传目录的交付动作。
// 交付工具（deliver_result / deliver_file）仍在工具目录里，用户明确要交付时
// 模型自然可用；提示词不写「什么情况该用/不该用」的判定规则——边界无法穷举
// （文档、往后的 3D 产物、命令行生成的文件……），只按模式分流：编程模式不强调。
const codingFilePrompt = `
## 文件修改规范

文件操作一律优先用文件工具：查看源码与文本用 read（而不是 cat 等命令行）；创建文件或整体替换内容用 write（既有文件先 read 读过才能覆盖——观察策略强制，本会话刚创建/刚改过的除外）；对既有文件的局部修改优先用 edit，而不是重写整个文件或用命令行脚本改。

1. write / edit 的修改自动记录历史快照，用户编辑历史消息时可以把文件一并回退，无需你做额外操作。
2. 通过 run_command、脚本、代码生成器等途径修改文件无法自动追踪：改动一个已存在的重要文件前，必须先调用 snapshot_file 打快照，否则该文件无法回退；新建文件无需快照。

## 工作区目录约定

- 工作区里的 .jxai/ 是本助手的系统目录：.jxai/snapshots 存文件快照正文（系统管理，禁止读写），.jxai/tmp 是给你用的暂存区。
- 一切临时产物——中间脚本、下载的依赖与素材、日志、过程数据——都放到 .jxai/tmp/ 下（可按任务建子目录），不要散落在项目里；只有属于项目本身的文件才放在正式位置。
- .jxai 不进目录检索（list_dir/glob/grep 不扫它），按路径直接 read 访问没有限制。`

// dispatchSubPrompt 是子对话派发工具的使用准则（task / office / coding 模式附加；
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
	case "coding":
		base = codingPrompt
	case "skill-create":
		base = skillPrompt
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
	// 文件修改与交付规范只给有写入能力的模式：task / office 拼 fileToolsPrompt
	// （含 deliver_result 交付强调——办公向任务的成果文件要送回用户手上）；
	// coding 拼 codingFilePrompt（同一套文件/快照/目录约定，但不强调交付——
	// 软件项目的成果直接长在项目文件里，见其注释）；
	// assistant 只编排不亲手改文件，aux 只读，都不需要。
	// 子对话派发准则给全部三个执行模式（assistant 的派发准则在其专属提示词里）。
	switch sess.Mode {
	case "task", "office":
		base += fileToolsPrompt + dispatchSubPrompt
	case "coding":
		base += codingFilePrompt + dispatchSubPrompt
	case "skill-create":
		base += skillFilePrompt + dispatchSubPrompt
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
//
// 首行的「用户最近发消息时间」= 会话里最新一条**真人用户消息**的落库时刻
// （Store.LastUserMessageAt）：用户在哪一刻说的话，就记哪一刻。它不是挂钟——
// 模型自己执行期间（工具调用之间、目标模式续跑、各种系统注入回合）不会随时间
// 流逝变化，所以环境快照不会再凭空多发「环境更新」；用户真发了新消息，这一行
// 才随新快照更新一次。会话里还没有用户消息（纯派发/待办驱动的会话）时不出这一行。
// 其余条目都是真实状态（工作区/权限/能力/技能/模式），变化时照旧发一条只带
// 变化行的「环境更新」——重发由变化驱动，不由时间驱动。
func (a *App) envPrompt(sess *SessionMeta) string {
	b := &strings.Builder{}
	b.WriteString("# 运行环境快照（最新，取代更早的快照）\n")
	if t, ok := a.Store.LastUserMessageAt(sess.ID); ok {
		b.WriteString("- 用户最近发消息时间：" + t.Format("2006-01-02 15:04") + "\n")
	}
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
	// 安卓：内置 Linux 运行时（Termux 快照）的就绪状态与用法。这是易变状态
	//（首启装配约十几秒），归环境快照正管——就绪前后切换会以「环境更新」送达
	// 模型，未就绪时模型不会误以为有 bash/python 可用。
	if isAndroidRuntime && sess.Remote == nil {
		if prefix := termuxPrefixOrEmpty(); prefix != "" {
			b.WriteString("- 内置 Linux 运行时（Termux）已就绪：run_command 经 bash 执行，python3 / pip / apt / curl 及常用 GNU 工具可用；缺系统包用 `jx-pkg install <包…>`，缺 Python 包用 `jx-pkg pip install <包…>`（镜像已配好、需联网），不要自己拼 apt/dpkg 命令\n")
		} else {
			b.WriteString("- 内置 Linux 运行时未就绪（首启装配约十几秒，若一直未就绪见引擎日志）：当前命令只有系统 toybox（无 curl/pip/python），稍后重试\n")
		}
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
			b.WriteString("- 权限模式：工作区内读写——文件写入仅限工作区；命令在内置 Linux 运行时（Termux）里执行，始终处于安卓应用沙箱内（应用 uid + SELinux；安卓内核禁用 Landlock，无 OS 级命令沙箱，文件边界由文件工具围栏把守），联网不限\n")
		} else {
			b.WriteString("- 权限模式：工作区内读写——文件写入仅限工作区；命令在 OS 级沙箱内执行（workspace-write）：只能写工作区与沙箱私有临时目录，联网不限\n")
		}
	}
	b.WriteString("- 沙箱拒绝是正常工作方式：命令/写入直接尝试，被拒会得到 [sandbox: … denied under <mode> mode] 标记。若更宽权限能让它成功，在同一轮内带 sandbox_permissions（够用的最宽档：workspace-write / danger-full-access / 文件写边界外用 full）与一句 justification 重试一次；重试会弹审批由用户批准\n")
	b.WriteString("- 审批卡上用户点「始终允许」= 常设授权：命令升级点亮对应能力开关（如「允许工作区外执行命令」），工作区外写入把会话权限提升为完全访问（捆绑能力一并点亮），之后该类动作不再逐次审批\n")
	b.WriteString("- 未经真实拒绝不要请求升级；升级被拒后该动作本轮终局：如实说明并停止，不要换法绕过；其他动作不受影响\n")
	b.WriteString("- 任何模式都禁止访问助手自身的配置与数据目录（含 API Key）；不可逆的破坏性命令会被危险命令护栏拦截（护栏默认开启，用户可在设置中关闭）\n")
	// 能力开关只对带这些工具的模式（task/office/coding/skill-create）有意义。工具定义常驻目录
	// （不随开关增删，请求形状稳定、缓存友好），真实状态在这里公告——
	// 开与关都说，变化经环境快照补充在对话尾部，配合调用时的 requireCapability 拦截
	if sess.Mode == "task" || sess.Mode == "office" || sess.Mode == "coding" || sess.Mode == "skill-create" {
		b.WriteString("- 能力开关与命令沙箱是两套门：browser_* / computer_* / device_* 由助手进程直接驱动本机浏览器/桌面/手机，不经过命令沙箱；sandbox_permissions 升级只放宽命令沙箱，不会打开能力开关（要开能力得在被拦后于 browser_* / computer_* 调用里带 request_capability=true 申请）。别用命令行去拉浏览器（沙箱内起不来，见下方能力项）。下面逐项列出当前开关状态：\n")
		if hasCapability(sess, capBrowser) {
			b.WriteString("- 能力：已开启「操作浏览器」——可用 browser_* 工具打开网页、读取内容、点击、输入、截图（助手进程直接驱动本机 Edge/Chrome，不经命令沙箱）\n")
		} else {
			b.WriteString("- 能力：未开启「操作浏览器」——browser_* 工具的调用会被拦下。此能力是沙箱外的独立通道：关着时你在命令沙箱里启动浏览器**一定失败**（受限令牌下报 access denied / 进程直接崩），不要用 shell 去试，也不要申请 sandbox_permissions（升级只放宽命令沙箱，打不开能力开关）。需要看网页时：重试 browser_* 调用并带 request_capability=true 与一句 justification 向用户申请开启（弹审批卡，用户批准后即可用），或请用户在权限芯片里勾选「允许操作浏览器」\n")
		}
		if hasCapability(sess, capComputer) {
			b.WriteString("- 能力：已开启「操作电脑」——可用 computer_* 工具截屏、点击、输入、切换窗口（点击坐标以 computer_screenshot 的图片为准；同样不经命令沙箱）\n")
		} else {
			b.WriteString("- 能力：未开启「操作电脑」——computer_* 工具的调用会被拦下；关着时命令沙箱里的命令也替代不了它（截不了屏、模拟不了键鼠）。需要时：重试 computer_* 调用并带 request_capability=true 与一句 justification 向用户申请开启（弹审批卡），或请用户在权限芯片中勾选「允许操作电脑」\n")
		}
		// 手机操控（仅安卓注册）：device_* 走无障碍桥，adb_* 走引擎内置 ADB
		// 客户端。就绪状态是易变信息（用户开关无障碍/连 ADB），归环境快照正管。
		if isAndroidRuntime {
			if hasCapability(sess, capDevice) {
				line := "- 能力：已开启「操作手机」——可用 device_*（截屏/界面树/点按/滑动/输入/系统键/打开应用）与 adb_*（连接 adbd 后 shell uid 执行命令、底层截屏）工具。"
				if _, svcAlive := deviceBridgeState(); svcAlive {
					line += "无障碍服务已就绪。"
				} else {
					line += "无障碍服务未开启：device_* 需要 设置→无障碍→已下载的应用→数字助理 打开后可用（adb_* 不依赖无障碍，但需要用户开启无线调试并连接）。"
				}
				if s := adbStateText(); s != "未连接" {
					line += "ADB：" + s + "。"
				}
				b.WriteString(line + "\n")
			} else {
				b.WriteString("- 能力：未开启「操作手机」——device_* / adb_* 工具的调用会被拦下；需要时重试调用并带 request_capability=true 与一句 justification 向用户申请开启（弹审批卡），或请用户在权限芯片中勾选「允许操作手机」（沙箱里的命令替代不了它）\n")
			}
		}
	}
	// 创建技能模式的演示记录器公告：工具仅在该模式注册，可用性按平台分叉
	//（Windows 真实现 / 其余平台空桩），转写服务接入状态也一并告知——
	// 模型据此决定是「录演示」还是「让用户口述」。
	if sess.Mode == "skill-create" && sess.Remote == nil {
		if recorderSupported() {
			b.WriteString("- 演示记录器（demo_record_start / demo_record_stop / demo_record_status）可用：开始后用户的每次点击、回车/Tab、窗口切换自动截屏，全程记事件日志与麦克风音频，材料落 工作区/演示记录/ 下，stop 后先读 INDEX.md 对照表再看截图。" +
				map[bool]string{true: "语音讲解会转成 讲解.srt 字幕（转写服务已配置）。", false: "语音讲解暂不转写字幕（转写服务未配置，录音保留）——用户在对话里说的话就是讲解词。"}[transcriberActive()] + "\n")
		} else {
			b.WriteString("- 演示记录器在当前平台不可用（仅 Windows 桌面）：请让用户直接口述操作步骤与意图，必要时用可用的操作能力（browser_* / computer_* / device_*）亲自走关键路径验证\n")
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
	// 自带技能一览（静态，中文为主、括注英文 id）；用户技能段也归入快照——
	// 开关技能只影响尾部快照，系统提示词的稳定前缀不受影响（瞬态注入原则）。
	b.WriteString("- 自带技能：PDF 文档（pdf，读 PDF 文档）、图片文字识别（ocr）、Office 文档（office_skill，Word/Excel/PPT 操作指南）、下载器（downloader，aria2c 下载落盘）、APK 信息（apkinfo，包名/版本/签名查看），需要时直接调用对应工具或子命令\n")
	// 用户技能清单与插件技能清单：环境快照的固定条目，**所有模式一律注入**，
	// 不挂任何模式谓词——与上面的操作系统/权限模式行同级。技能功能晚于
	// office/coding 落地，曾因手写「task/skill-create 才注入」的门控把两个
	// 老模式漏在清单外（实测装了技能、模型却收不到清单更新）；改为无条件
	// 注入后新增模式天然覆盖，启动自检 assertSkillVisibility 再兜一道底。
	// read_skill 工具同样全模式注册（toolsFor 出口统一追加），清单在任何
	// 模式都可读、可行动。
	if block := a.userSkillsPromptBlock(); block != "" {
		b.WriteString(block + "\n")
	}
	// Claude Code 插件技能清单（ccplugin_skills.go）：同为清单先行、按需读取
	if a.CCPlugins != nil {
		if block := a.CCPlugins.ccSkillsPromptBlock(); block != "" {
			b.WriteString(block + "\n")
		}
	}
	// 独立 MCP 连接器清单（mcpservers.go）：与技能同一纪律——清单先行、
	// 内容按需（mcp_read_connector / mcp_call_tool），连接器增删只影响
	// 环境快照尾部，不动工具目录与前缀缓存。只在带 MCP 工具的执行类
	// 模式注入（mcpToolsMode）：总控/答疑模式没有调用入口，不给空头支票。
	if a.MCPServers != nil && mcpToolsMode(sess.Mode) {
		if block := a.MCPServers.PromptBlock(); block != "" {
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
	verCmd := exec.Command("cmd", "/c", "ver")
	hideConsoleWindow(verCmd) // 桌面形态没有控制台可复用，探测也别弹窗
	if out, err := verCmd.Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if i := strings.Index(s, "Version "); i >= 0 {
			desc = "Windows " + strings.TrimSpace(strings.TrimRight(s[i+len("Version "):], "]"))
		}
	}
	ps := "PowerShell（版本未知）"
	for _, name := range []string{"pwsh", "powershell"} {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		psCmd := exec.CommandContext(cctx, name, "-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()")
		hideConsoleWindow(psCmd)
		out, err := psCmd.Output()
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
	head += " · 系统命令层为 toybox（run_command 的 shell 参数在安卓上忽略）"
	return head + "\n" +
		"- 完整工具链（bash / python3 / pip / apt / curl 等）由「内置 Linux 运行时（Termux）」提供，就绪状态与 jx-pkg 用法见环境快照；未就绪前只有下面这些系统层命令\n" +
		"- 系统层可用命令：pm am dumpsys settings getprop input uiautomator screencap logcat monkey cmd content 及常用 toybox 工具（grep/sed 为精简版，部分 GNU 选项不支持）；系统层没有 curl、openssl、python、aapt2/apksigner/keytool，下载用「下载器」技能（aria2c），APK 校验用 apkinfo 子命令，不要反复探测\n" +
		"- 常见坑：SELinux 禁硬链接（建临时文件用占位+rename，write_file 已内置处理）；/sdcard 多为 noexec 挂载，可执行文件放 /data/local/tmp 或应用 nativeLibraryDir，放 /sdcard 会 Permission denied；文本默认 UTF-8 无编码坑"
}

// maybeAppendEnvSnapshot 环境快照 diff：与最近一条 kind=env 快照比较（比较时
// 剔除历史快照尾部的变更注记行——技能/权限变更说明，避免注记自我累积、每步
// 重复落快照），内容有变化才落库新的一条（时间戳粒度为分钟，通常每轮一条），
// 并实时推给前端。**落库时机与发送时机分离**：落库由两类事件驱动——各次组装
// 前的例行检查（这里），以及环境条目变更的瞬间（PATCH 权限/计划/目标/工作区
// 落库后、审批「始终允许」升级后，都直接调用本函数立即落）；「变化驱动」约束
// 的只是发给模型的差分重发（buildContext 按行差分，已发送前缀绝不改写），
// 不约束什么时候可以落库——等值比较保证两条驱动路径不会重复落行。
// 两类事件说明随新快照尾部追加（上文历史快照一律不动）：
//   - 技能清单有增减 → 追加一行「（技能变更：…）」（skillChangeNote 现算）；
//   - 权限/能力开关变更 → 取走变更时暂存的注记（stashPermNote），追加一行
//     「（权限变更：…）」——权限是环境快照正管的条目，同一事实不重复记账，
//     不另发 report 公告。注记待落时即使快照正文没变也落一条（正文相同+注记
//     新行仍是真实变化），模型经「环境更新」差分看到它。
// 返回最终落库的快照内容（Assembly.Env 口径）。
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
	permNote := a.takePermNote(sess.ID)
	if havePrev && stripEnvNotes(prev) == env && permNote == "" {
		return env
	}
	if havePrev {
		if note := skillChangeNote(stripEnvNotes(prev), env); note != "" {
			env = env + "\n（" + note + "）"
		}
	}
	if permNote != "" {
		env = env + "\n（" + permNote + "）"
	}
	// 上下文材料行（env/摘要/快照）一律存 role=system：它们不是用户发言，
	// 只是入库归档；真实发送时由 buildContext 现场构造（env 走 user 尾消息）
	sm := a.appendRow(sess, StoredMessage{Message: TextMessage("system", env), Kind: kindEnv, CreatedAt: now()})
	if emit != nil {
		emit(Event{Type: "message", Message: &sm.Message, Seq: sm.Seq, Kind: kindEnv})
	}
	return env
}

// stripEnvNotes 去掉快照尾部的变更注记行（「（技能变更：…）」「（权限变更：…）」），
// 得到用于比较的规范内容——注记是事件说明，不参与快照等值比较。
func stripEnvNotes(env string) string {
	for {
		i := strings.LastIndex(env, "\n（")
		if i < 0 || !strings.HasSuffix(env, "）") {
			return env
		}
		tail := env[i+1:]
		if !strings.HasPrefix(tail, "（技能变更：") && !strings.HasPrefix(tail, "（权限变更：") {
			return env
		}
		env = env[:i]
	}
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
	// 只有用户输入与模型最终输出刷新「最近更新」时间（口径见 StoredMessage.touchesActivity）：
	// 工具结果、中间步、注入行都不算，否则几个并行的会话会在侧栏来回跳
	if sm.touchesActivity() {
		sess.UpdatedAt = now()
	}
	_ = a.Store.TouchRunMeta(sess) // 只回写运行列（用量/时间），不覆盖并发的权限等配置改动
	return sm
}

// appendRow 落库一条自定义 kind 的消息（env 快照 / prompt 快照）。
func (a *App) appendRow(sess *SessionMeta, sm StoredMessage) *StoredMessage {
	_ = a.Store.AppendRow(sess.ID, &sm)
	// 只有用户输入与模型最终输出刷新「最近更新」时间（口径见 StoredMessage.touchesActivity）：
	// 工具结果、中间步、注入行都不算，否则几个并行的会话会在侧栏来回跳
	if sm.touchesActivity() {
		sess.UpdatedAt = now()
	}
	_ = a.Store.TouchRunMeta(sess) // 只回写运行列（用量/时间），不覆盖并发的权限等配置改动
	return &sm
}

// appendMessageThinking 同 appendMessage，但带上该步的思考过程原文。
func (a *App) appendMessageThinking(sess *SessionMeta, m Message, thinking string) *StoredMessage {
	sm := &StoredMessage{Message: m, Kind: kindChat, Thinking: thinking, CreatedAt: now()}
	_ = a.Store.AppendRow(sess.ID, sm)
	// 只有用户输入与模型最终输出刷新「最近更新」时间（口径见 StoredMessage.touchesActivity）：
	// 工具结果、中间步、注入行都不算，否则几个并行的会话会在侧栏来回跳
	if sm.touchesActivity() {
		sess.UpdatedAt = now()
	}
	_ = a.Store.TouchRunMeta(sess) // 只回写运行列（用量/时间），不覆盖并发的权限等配置改动
	return sm
}

// modelTries 与 modelRetryDelays 是模型调用失败后的自动重试参数：总共尝试
// 5 次（首次 + 4 次重试），失败间隔 1s/2s/3s/4s 递增——网络抖动/网关瞬断
// 通常在数秒内恢复；连不通 5 次就停手，等人工处理好环境再点「重试」。
const modelTries = 5

var modelRetryDelays = [modelTries - 1]time.Duration{
	1 * time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second,
}

// interruptNoteNotRun / interruptNoteAborted 是会话被中断时给未执行 / 被中止的
// 工具调用补的合成结尾（对齐 DSH 的 "tool call aborted before dispatch" /
// "tool call aborted"——每个 tool_call 都必须有结果，历史与回放才不留悬空）。
// 模型在下一轮能看到这些结尾，自行决定要不要重做那个调用。
const (
	interruptNoteNotRun  = "（会话被中断，此工具调用未执行）"
	interruptNoteAborted = "（会话被中断，工具执行被中止）"
)

// errMaxSteps 是「达到步数上限自动停止」的专用错误：上层据此把这一轮记成
// 步数上限中断（左侧栏「⚠ 已中断」的悬停文案与真正的执行失败不同），
// 而不是普通报错。Error 文案与原先的 fmt.Errorf 逐字一致。
type errMaxSteps struct{ n int }

func (e errMaxSteps) Error() string {
	return fmt.Sprintf("连续 %d 步仍未完成，已自动停止（可在设置中调大最大步数），可点重试", e.n)
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
	// 落库身份取待生效项自带的 kind/origin（用户插话=chat，派发注入=report）。
	interjectsBefore := func(emit func(Event)) bool {
		got := false
		for _, pm := range a.drainSteer(sess.ID) {
			a.landUserSide(sess, pm.Content, pm.Kind, pm.Origin, emit)
			got = true
		}
		return got
	}

	// 开轮前先补齐历史里的悬空工具结尾（异常中断——进程崩溃、服务重启等
	// 留下的「调用已落库、结果未落库」）：重试与新消息都经这里，保证发给
	// 模型的历史配对完整，模型也能看到上次工具被中断的真实结尾
	a.repairDanglingToolCalls(sess, emit)
	// 开轮留痕：本轮拼装快照（系统提示词/环境快照/工具定义）由上下文引擎
	// 组装并落 kind=prompt 行（OpenRound），主循环不再亲手拼这条行
	a.ctxEngine().OpenRound(sess, emit)
	// Claude Code 插件：SessionStart hook（只在会话首轮发，对齐 Claude 的
	// startup 语义；additionalContext 忽略——首条环境快照已定）
	if msgs, mErr := a.Store.Messages(sess.ID); mErr == nil && len(msgs) == 0 {
		if oc := a.ccEmitHookEvent(ctx, sess, "SessionStart", "", nil, map[string]any{"source": "startup"}); oc.SystemMsg != "" {
			emit(Event{Type: "report", Output: oc.SystemMsg})
		}
	}
	defer a.requeueSteer(sess.ID) // 正常/异常退出后残留插话转为排队，由执行体接着发

	// planReset 是「计划获批、步数从头计」的信号：exit_plan_mode 获批时经
	// toolContext.planApproved 置位，本循环在工具执行完检测并重置 step——出计划
	// 的探索步数不再挤占执行计划的预算（两段各有一份完整的 MaxSteps）。
	planReset := false

	for step := 0; step < a.Cfg.MaxSteps; step++ {
		a.syncRunPerms(sess) // 面板中途改权限：每步从库合并，下一步即按新权限执行
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
		// 模型调用（含网络自动重试）：同一请求最多尝试 modelTries 次，失败间隔
		// 1s/2s/3s/4s 递增；等待可用停止按钮打断。瞬态网络故障（连接失败、
		// 超时、流中途断开、网关 5xx/限流 429）自动重试；请求级错误（鉴权/
		// 余额/模型不存在/请求非法）与上下文超限重试无益，直接交给下方兜底。
		// 每次尝试都从零累积输出：上一次失败尝试流出的半截作废（前端收到
		// retry 事件也同步清掉），成功后从完整输出继续。
		var err error
		attempt := 0 // 本步模型调用已尝试次数（循环外的收尾判断重试是否发生过）
		for {
			attempt++
			text.Reset()
			reasoning.Reset()
			calls = nil
			usage = nil
			callStart := time.Now()
			err = StreamChat(ctx, target, req, StreamCallbacks{
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
					_ = a.Store.TouchRunMeta(sess) // 只回写用量列，不覆盖并发的权限等配置改动
				},
			})
			// 调用日志（引擎记录）：成败都落——发送规模、前缀指纹、返回、缓存命中、耗时（重试的每次尝试各一条）
			a.ctxEngine().RecordCall(sess, step+1, len(msgs), pack.EstTokens, pack.SysHash, pack.EnvHash, time.Since(callStart).Milliseconds(),
				usage, len([]rune(text.String())), toolNamesOf(calls), err)
			if err == nil {
				if attempt > 1 {
					emit(Event{Type: "auto_retry"}) // 重试成功：收掉重试提示条
				}
				break
			}
			if attempt >= modelTries || ctx.Err() != nil || !isRetryableModelErr(err) {
				break
			}
			delay := modelRetryDelays[attempt-1]
			emit(Event{Type: "retry"}) // 半截流式作废：前端清掉，下一次尝试从头输出
			emit(Event{Type: "auto_retry", Output: fmt.Sprintf("网络异常（%s）；%d 秒后自动重试（第 %d/%d 次）…",
				truncateRunes(strings.Join(strings.Fields(err.Error()), " "), 120), int(delay/time.Second), attempt+1, modelTries)})
			select {
			case <-ctx.Done(): // 等待期间被停止：不再重试，走下方的停止收尾
				err = ctx.Err()
			case <-time.After(delay):
				continue
			}
			break
		}
		if err != nil {
			if attempt > 1 {
				emit(Event{Type: "auto_retry"}) // 重试耗尽：收掉重试提示条，失败原因走报错区
			}
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
			// 半截正文落盘，历史不留空洞（这是助手自己说过的话，标一下被中断；
			// 用户停止与网络故障给的标注不同，便于回看时区分）
			// 说明：异常收尾不往对话里塞“系统交代”，用户可见的报错统一走前端的报错区，
			// 见 executeRun 的终态事件与前端 runErrorBar
			if strings.TrimSpace(text.String()) != "" {
				note := "\n\n（本轮输出中断）"
				if ctx.Err() != nil {
					note = "\n\n（会话被中断）"
				}
				a.appendMessageThinking(sess, TextMessage("assistant", text.String()+note), reasoning.String())
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
			// Claude Code 插件：Stop / SubagentStop hook（观察通知；阻断语义不支持）
			if a.CCPlugins != nil && a.CCPlugins.hasAnyHooks() {
				evName := "Stop"
				if sess.ParentID != "" {
					evName = "SubagentStop"
				}
				if oc := a.ccEmitHookEvent(ctx, sess, evName, "", nil, nil); oc.SystemMsg != "" {
					emit(Event{Type: "report", Output: oc.SystemMsg})
				}
			}
			// Claude Code 插件 mods：turn.complete（观察；answer.text 转报告行）
			a.ccModTurnComplete(ctx, sess, emit)
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
				// 会话已中断：不再启动新调用，给每个剩余调用补合成结尾
				// （对齐 DSH「aborted before dispatch」：每个 tool_call 都必须
				// 有结果，历史与回放才不留悬空），并实时下发给前端
				tm := Message{Role: "tool", Content: interruptNoteNotRun, ToolCallID: tc.ID}
				tsm := a.appendMessage(sess, tm)
				emit(Event{Type: "message", Message: &tsm.Message, Seq: tsm.Seq, Kind: kindChat})
				emit(Event{Type: "tool_end", Name: tc.Function.Name, Output: interruptNoteNotRun})
				interrupted = true
				continue
			}
			emit(Event{Type: "tool_start", Name: tc.Function.Name, Args: tc.Function.Arguments})
			var out string
			// Claude Code 插件：PreToolUse hook + mods 的 tool.call（拒绝/改参/
			// 自答；无注册时零开销直通；mods 在 settings hooks 之后）
			if hookDeny := a.ccPreToolUse(ctx, sess, &tc); hookDeny != "" {
				out = hookDeny
			} else if modDeny := a.ccModPreToolUse(ctx, sess, &tc); modDeny != "" {
				out = modDeny
			} else if sess.PlanMode && suspendedInPlan(sess.Mode, tc.Function.Name) {
				// 计划模式硬拦：工具目录保持不变（请求形状/缓存稳定），调用时拒绝；
				// exit_plan_mode 批准后 PlanMode 即刻置 false，同一回合内自动放行
				out = "计划模式下该工具已暂停：" + tc.Function.Name + "。当前仅可只读探索；请完成调研后调用 exit_plan_mode 提交计划供用户批准。"
			} else if run, ok := runs[tc.Function.Name]; ok {
				tctx := &toolContext{ctx: ctx, app: a, sess: sess, emit: emit,
					seq: asm.Seq, callID: tc.ID, toolName: tc.Function.Name, planApproved: &planReset}
				res, rerr := run(tctx, json.RawMessage(tc.Function.Arguments))
				if ctx.Err() != nil {
					// 执行期间会话被中断：工具已被取消信号尽力中止（能停则停），
					// 结尾统一标注中断语义——原始错误（多为 "context canceled"）
					// 对模型没有信息量；中断前已产出的输出附后不丢。模型下一轮
					// 自行决定要不要重做该调用
					out = interruptNoteAborted
					if strings.TrimSpace(res) != "" {
						out += "\n" + res
					}
				} else if rerr != nil {
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
			// Claude Code 插件：PostToolUse hook（改写工具结果；无 hook 零开销）
			if rewritten := a.ccPostToolUse(ctx, sess, tc, out); rewritten != "" {
				out = rewritten
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
		// 计划获批：步数预算从头计（计划阶段不挤占执行阶段）。置 -1 是因为
		// continue 会先执行 for 的 step++，下一轮 step 从 0 重新数起
		if planReset {
			planReset = false
			step = -1
			a.auditWrite(sess, map[string]any{"type": "plan_step_reset"})
			continue
		}
	}
	return errMaxSteps{n: a.Cfg.MaxSteps}
}
