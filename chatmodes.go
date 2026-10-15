package main

// 工作模式清单（单一事实源）：这就是「人工新建会话」时模式胶囊下拉里能选的
// 那几个模式——值、中文名、一句话说明都只写在这里。
//
// 三处消费者共用这一份，不存在第二套清单：
//  1. 前端模式下拉（web/app-chat.js 的 CHAT_MODES）启动时经 GET /api/modes
//     拉取本清单重建（值/名/说明由后端下发，前端只按值挂自己的「模式前端
//     定制」ui 钩子，见 CHAT_MODES 上方注释）——与子对话专家清单同一取舍
//     （experts.go 文件头：前端没有第二份抄写）；
//  2. dispatch_task 的 mode 参数：取值校验与工具说明都用本清单（数字助理
//     按任务性质选模式，选出来的就是人工能选的那几个）；
//  3. 建会话校验（handleCreateSession 的 modeKnown → sessionModes）：本清单
//     的值必须都在 sessionModes 全集里，启动自检 assertChatModes 看护。
//
// 加新模式 = 这里加一项 + 后端加对应提示词分支（systemPrompt/toolsFor 按
// mode 分流）+ sessionModes 加值；前端零改动即出现在下拉里（要界面定制再在
// 前端 MODE_UI 里按值挂 ui）。注意：内部的 task（通用工作任务，dispatch_task
// 不传 mode 时的默认）与 assistant（数字助理主对话）/aux（辅助答疑）都不是
// 「人工新建会话」的可选工作模式，因此不在本清单里。
type chatModeDef struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

// chatModes 是可选工作模式的全量清单（顺序即前端下拉的展示顺序）。
var chatModes = []chatModeDef{
	{Value: "office", Label: "标准办公", Desc: "面向日常办公：文档、资料、表格、沟通文案，成果落文件"},
	{Value: "coding", Label: "编程", Desc: "面向软件开发：读改代码、编译构建、调试排障，成果直接落项目文件"},
	{Value: "beginner-coding", Label: "新手编程", Desc: "面向不懂编程的你：先问清需求再动手，全程大白话讲解，成果落项目文件"},
	{Value: "skill-create", Label: "创建技能", Desc: "把「怎么做」沉淀成给 AI 的教程：点按钮录演示让 AI 分析，或直接派活让 AI 探路总结；默认完全访问"},
}

// chatModeByValue 按值取模式定义；未收录返回 nil（值必须是本清单里的原文，
// 不做别名与大小写折叠——与子对话专家的「名称即参数」同一口径）。
func chatModeByValue(v string) *chatModeDef {
	for i := range chatModes {
		if chatModes[i].Value == v {
			return &chatModes[i]
		}
	}
	return nil
}

// chatModesDoc 渲染「值=中文名」的一行清单（工具说明与校验报错共用同一份文案，
// 两边说的模式永远一致）。
func chatModesDoc() string {
	out := ""
	for i, m := range chatModes {
		if i > 0 {
			out += " / "
		}
		out += m.Value + "=" + m.Label
	}
	return out
}

// assertChatModes 启动自检（NewApp 调用）：本清单是「人工新建会话」的可选模式，
// 每个值都必须是 sessionModes（会话模式全集，建会话校验的事实源）里的已知模式
// ——否则下拉里会出现一个建不出来的模式，dispatch_task 也会派出一个建不出的
// 会话。漏加即拒绝启动，而不是等用户点了才炸。
func assertChatModes() {
	for _, m := range chatModes {
		if !modeKnown(m.Value) {
			panic("工作模式清单自检失败：模式 " + m.Value + " 不在 sessionModes 全集里（加模式时两处要一起改，见 chatmodes.go 文件头）")
		}
		if m.Label == "" || m.Desc == "" {
			panic("工作模式清单自检失败：模式 " + m.Value + " 缺中文名或说明（前端下拉直接展示这两个字段）")
		}
	}
}
