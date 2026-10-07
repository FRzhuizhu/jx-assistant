package main

// 子对话专家（专家团预设）：给 dispatch_sub 的命名预设。每个专家 =
// 追加系统提示词 + 默认模型（空=继承主对话）+ 是否继承上下文 + 启用开关。
// 设置页「子对话专家」管理；调用只有一条路径：dispatch_sub 的 expert=名称。
// 内置专家与自定义专家同名同路径——内置两条（标准子对话 / 继承上下文子对话）
// 就是原有的两种上下文模式收编为系统专家，inherit_context 参数已从工具中删除，
// 「名称即参数」对内置与自定义 100% 成立。设置页展示的名称直接来自后端
// （/api/experts 的 builtin/user 两个清单），前端没有第二份抄写。
// 会话上只记专家名（SessionMeta.Expert），提示词构建时按名解析拼接：
// 专家被删改后新会话自然生效，存量子对话的前缀随之变化（与引用文件
// 覆盖提示词同一取舍，版本行不追踪）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ExpertCfg 是一个子对话专家预设。
type ExpertCfg struct {
	ID             string `json:"id"`
	Name           string `json:"name"`        // 调用参数：dispatch_sub 的 expert 填它
	Description    string `json:"description"` // 给主对话看的选用依据（进工具说明）
	Prompt         string `json:"prompt"`      // 追加系统提示词（拼在模式基础提示词之后）
	Model          string `json:"model,omitempty"`
	InheritContext bool   `json:"inheritContext"`
	Enabled        bool   `json:"enabled"`
}

// systemExperts 系统内置专家：常开、不可修改、不落 config.json（代码即事实
// 来源）。Prompt 为空 = 无追加段。名称是调用参数，永不允许自定义专家占用。
func systemExperts() []ExpertCfg {
	return []ExpertCfg{
		{
			ID:          "builtin-standard",
			Name:        "标准子对话",
			Description: "系统内置：不继承上下文，子对话只收到一段自足的任务描述，与主对话记录完全隔离（不传 expert 时的默认行为）",
			Enabled:     true,
		},
		{
			ID:             "builtin-inherit",
			Name:           "继承上下文子对话",
			Description:    "系统内置：完整继承主对话的聊天记录（含系统提示词，命中缓存成本低），任务描述只需写「接下来要做什么」",
			InheritContext: true,
			Enabled:        true,
		},
	}
}

// isSystemExpertName 名称是否与内置专家重名（内置优先，自定义不得占用）。
func isSystemExpertName(name string) bool {
	for _, e := range systemExperts() {
		if e.Name == name {
			return true
		}
	}
	return false
}

// defaultExperts 首个演示专家：对抗性审查编程助手（老配置没有 experts 键时
// 补种一次；用户删掉后不会再回来）。
func defaultExperts() []ExpertCfg {
	return []ExpertCfg{{
		ID:          newID(),
		Name:        "对抗性审查编程助手",
		Description: "最挑剔的评审视角：专找逻辑漏洞、边界缺陷与自相矛盾，只指出问题给证据，不亲自修改",
		Prompt: `你此刻的角色是「对抗性审查」：用最挑剔、最不客气的资深评审视角检查交给你的代码、方案或文档。

工作方式：
1. 立场先行：假设被审查对象一定有问题，你的任务是把它们全部找出来，而不是给出客套评价。不许说「整体不错」这类圆场话。
2. 逐条列问题：每条包含问题描述、出处（文件路径/位置或具体段落）、严重程度（致命/严重/一般/建议）、可复现或可验证的依据（触发输入、场景或推理链）。
3. 重点盯：逻辑漏洞与边界条件（空值、越界、并发、异常路径）、安全与权限隐患、与既有约定或需求的自相矛盾、错误处理缺失、性能陷阱。
4. 不确定就验证：能用只读工具（读文件、检索、跑只读检查）核实的先核实再下结论；核实不了的明确标注「未验证」，绝不编造。
5. 只审不改：指出问题并给出修复方向，但不直接修改被审查对象（除非任务明确要求给出修改后的版本）。
6. 结论分级：结尾在「放行 / 有条件放行 / 打回」三选一给出明确结论，并说明理由。`,
		Model:          "",
		InheritContext: true,
		Enabled:        true,
	}}
}

// expertByName 在自定义专家里按名精确匹配（内置专家无追加提示词，提示词
// 构建无需解析它们）；不论启停——存量子对话不因停用而丢追加段，只有删除
// 才会让这段消失。名称即调用参数，匹配必须逐字相等（不做大小写折叠——
// 100% 一致比宽松匹配更可预测）。Claude Code 插件的动态专家（ccplugin_agents.go）
// 同样参与匹配（插件卸载时自然消失，与自定义专家被删同语义）。
func (a *App) expertByName(name string) *ExpertCfg {
	name = strings.TrimSpace(name)
	for i := range a.Cfg.Experts {
		if strings.TrimSpace(a.Cfg.Experts[i].Name) == name {
			return &a.Cfg.Experts[i]
		}
	}
	if a.CCPlugins != nil {
		for _, e := range a.CCPlugins.Experts() {
			if e.Name == name {
				ee := e
				return &ee
			}
		}
	}
	return nil
}

// enabledExpertByName 在可派发的专家里按名精确找（dispatch_sub 的 expert
// 参数用）：内置在前常开，自定义需 Enabled，插件专家随插件启用。
func (a *App) enabledExpertByName(name string) *ExpertCfg {
	name = strings.TrimSpace(name)
	sys := systemExperts()
	for i := range sys {
		if sys[i].Name == name {
			return &sys[i]
		}
	}
	if e := a.expertByName(name); e != nil {
		if e.Enabled {
			return e
		}
		return nil
	}
	return nil
}

// enabledExperts 全部可派发的专家（进 dispatch_sub 工具说明的清单）：
// 内置在前 + 启用中的自定义 + 启用插件的动态专家。
func (a *App) enabledExperts() []ExpertCfg {
	out := systemExperts()
	for _, e := range a.Cfg.Experts {
		if e.Enabled {
			out = append(out, e)
		}
	}
	if a.CCPlugins != nil {
		out = append(out, a.CCPlugins.Experts()...)
	}
	return out
}

// ---------- 专家清单工具（工具目录恒定的关键） ----------
//
// 专家名单是易变数据（设置页随时增删改），嵌进 dispatch_sub 的工具说明会让
// tools 数组随设置变化、前缀缓存从最开头全断（工具定义渲染在 token 流最前）。
// 名单因此走「按需查询」：list_experts 常驻目录且描述静态（对齐 read_skill
// 的技能模式——清单先行、内容按需），专家增删只改变工具的返回内容（落位在
// 上下文尾部的工具结果），工具目录逐字节不变。

// listExpertsTool 列出可派发的子对话专家：内置两条（常开）+ 启用中的自定义。
func (a *App) listExpertsTool() Tool {
	return Tool{
		Spec: toolSpec("list_experts", "列出当前可用的子对话专家（内置与用户自定义）：名字、是否继承上下文、默认模型与用途。要用 dispatch_sub 派发非默认模式的子对话之前先调用它拿准确名单——expert 参数必须与名单里的名字逐字一致；名单是易变的，不要凭记忆猜名字。",
			objSchema(map[string]any{})),
		Run: runListExperts,
	}
}

func runListExperts(t *toolContext, args json.RawMessage) (string, error) {
	return t.app.expertListDoc(), nil
}

// expertListDoc 渲染专家清单文本（list_experts 工具的返回；内置在前，停用
// 的自定义专家不进清单）。
func (a *App) expertListDoc() string {
	var b strings.Builder
	b.WriteString("可用的子对话专家（dispatch_sub 的 expert 填名字，逐字一致；不传 expert 等同「标准子对话」）：")
	for _, e := range a.enabledExperts() {
		builtin := "自定义"
		if strings.HasPrefix(e.ID, "builtin-") {
			builtin = "内置"
		}
		ctx := "不继承上下文"
		if e.InheritContext {
			ctx = "继承上下文"
		}
		notes := ctx
		if m := strings.TrimSpace(e.Model); m != "" {
			notes += "，默认模型 " + m
		}
		fmt.Fprintf(&b, "\n- %s（%s，%s）：%s", e.Name, builtin, notes, strings.TrimSpace(e.Description))
	}
	return b.String()
}

// upsertExpert 按 id 更新或新建自定义专家；名称必填、不得与其他专家重名、
// 不得占用内置专家名（名称即参数，内置与自定义共用一个命名空间）。
func (a *App) upsertExpert(in ExpertCfg) (ExpertCfg, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Model = strings.TrimSpace(in.Model)
	if in.Name == "" {
		return in, fmt.Errorf("名称不能为空")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return in, fmt.Errorf("追加提示词不能为空")
	}
	if isSystemExpertName(in.Name) {
		return in, fmt.Errorf("「%s」是系统内置专家名，自定义专家不能占用（名称即调用参数）", in.Name)
	}
	for i := range a.Cfg.Experts {
		e := &a.Cfg.Experts[i]
		if strings.TrimSpace(e.Name) == in.Name && e.ID != in.ID {
			return in, fmt.Errorf("已存在同名专家「%s」，名称是调用参数，不能重复", in.Name)
		}
	}
	for i := range a.Cfg.Experts {
		if a.Cfg.Experts[i].ID == in.ID {
			a.Cfg.Experts[i] = in
			return in, a.saveExperts()
		}
	}
	if in.ID == "" {
		in.ID = newID()
	}
	a.Cfg.Experts = append(a.Cfg.Experts, in)
	return in, a.saveExperts()
}

// deleteExpert 按 id 删除自定义专家（内置不在 config 里，天然删不到）。
func (a *App) deleteExpert(id string) error {
	for i := range a.Cfg.Experts {
		if a.Cfg.Experts[i].ID == id {
			a.Cfg.Experts = append(a.Cfg.Experts[:i], a.Cfg.Experts[i+1:]...)
			return a.saveExperts()
		}
	}
	return fmt.Errorf("专家 %s 不存在", id)
}

// saveExperts 落盘配置（Experts 存在 config.json 里，与提供方同宿主）。
func (a *App) saveExperts() error {
	a.Cfg.syncLegacyFields()
	return a.Cfg.Save(a.CfgPath)
}

// ---------- HTTP 处理器 ----------

// handleListExperts 返回 builtin（系统内置，只读展示）与 user（自定义，可增删
// 改开关）两个清单；设置页照此渲染，名称与调用参数天然一致。
func (a *App) handleListExperts(w http.ResponseWriter, r *http.Request) {
	user := append([]ExpertCfg{}, a.Cfg.Experts...)
	if user == nil {
		user = []ExpertCfg{}
	}
	writeJSON(w, 200, map[string]any{"builtin": systemExperts(), "user": user})
}

func (a *App) handleUpsertExpert(w http.ResponseWriter, r *http.Request) {
	var in ExpertCfg
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	saved, err := a.upsertExpert(in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, saved)
}

func (a *App) handleDeleteExpert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, 400, "需要 id 字段")
		return
	}
	if err := a.deleteExpert(body.ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
