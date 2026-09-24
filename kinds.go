package main

// 消息行类型（messages.kind）的单一源头。完整契约见《消息类型规范.md》：
// 每种行的含义、是否发给模型、SSE 形态与前端样式都在那里定义。
// 纪律：新增/改动类型先改规范再动代码；写入点一律用这里的常量，
// 前端按 kind 分发（不按正文猜类型，旧库兼容除外，见规范 §5）。

// 对话内容（发给模型）
const kindChat = "chat" // 正常对话：role=user/assistant/tool；assistant 行可带 thinking 与 tool_calls（复合行，前端拆条展示）

// 上下文注入（发给模型的拼装材料；历史行不进上下文，组装时现场归位）
const (
	kindSummary = "summary" // 压缩摘要（origin=auto/manual，covers_up_to 标覆盖边界）
	kindEnv     = "env"     // 运行环境快照：固定作为尾部 user 消息；技能说明也归这里
	kindPrompt  = "prompt"  // 每轮拼装快照 {sys,env,tools}；sys=请求最前部，tools=请求体平级数组
)

// 系统注入与审计工件
const (
	kindReport  = "report"  // 系统注入的用户侧通知（发给模型）：回报/中断/上传/回退/目标续跑/派发指令/待办派发；origin 记子类（规范 §1.4）
	kindInherit = "inherit" // 子对话继承标记 {parent,count}：不进上下文，前端折成「来自主对话」段
	kindRequest = "request" // 最近一次发给模型的完整请求体（审计工件，每会话只留最新；「导出原始数据」数据源）
	kindAudit   = "audit"   // 运行事件审计行：调用量与缓存命中/报错/停止/压缩/超限（「导出日志」数据源）
)

// inheritKind 是 kindInherit 的旧名（既有代码沿用，语义同一）
const inheritKind = kindInherit
