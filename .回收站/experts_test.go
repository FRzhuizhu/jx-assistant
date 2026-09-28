package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 专家预设解析：名字即参数、一条路径；不存在 / 已停用时报错并带回可用
// 名单（含内置专家）；内置专家与自定义专家同路径按名派发。
func TestDispatchSubExpertResolution(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.BaseURL = "mock"
	a.CfgPath = filepath.Join(t.TempDir(), "config.json")
	a.Cfg.Experts = []ExpertCfg{
		{ID: "e1", Name: "审查员", Description: "找问题", Prompt: "你是审查员。", InheritContext: true, Enabled: true},
		{ID: "e2", Name: "停用的", Description: "不可见", Prompt: "x", Enabled: false},
	}
	parent := &SessionMeta{ID: "p1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(parent); err != nil {
		t.Fatalf("建父会话失败: %v", err)
	}
	_ = a.Store.Append("p1", TextMessage("user", "先聊两句背景"))
	tctx := &toolContext{app: a, sess: parent, emit: func(Event) {}}

	// 名字不存在：报错且带可用名单（内置两条 + 启用中的自定义；停用的不在名单里）
	_, err := runDispatchSub(tctx, []byte(`{"task":"审查一下","expert":"不存在的"}`))
	if err == nil || !strings.Contains(err.Error(), "不存在") ||
		!strings.Contains(err.Error(), "「标准子对话」") || !strings.Contains(err.Error(), "「继承上下文子对话」") ||
		!strings.Contains(err.Error(), "「审查员」") {
		t.Fatalf("专家不存在应报错并带回含内置的名单：%v", err)
	}
	if strings.Contains(err.Error(), "停用的") {
		t.Fatalf("停用的专家不该出现在可用名单：%v", err)
	}
	// 停用的专家同样不可派
	if _, err := runDispatchSub(tctx, []byte(`{"task":"x","expert":"停用的"}`)); err == nil {
		t.Fatal("停用的专家应报错")
	}

	// 内置专家按名派发（与自定义同一条路径）：继承上下文、无追加提示词段
	out, err := runDispatchSub(tctx, []byte(`{"task":"接着背景继续","expert":"继承上下文子对话"}`))
	if err != nil {
		t.Fatalf("按内置专家派发失败: %v", err)
	}
	if !strings.Contains(out, "已继承本会话上下文") {
		t.Fatalf("内置继承专家应继承上下文：%s", out)
	}
	kids, _ := a.Store.Children("p1")
	var builtinSub *SessionMeta
	for _, k := range kids {
		if k.Expert == "继承上下文子对话" {
			builtinSub = k
		}
	}
	if builtinSub == nil {
		t.Fatalf("子会话应记录内置专家名，kids=%+v", kids)
	}
	if sys := a.systemPrompt(builtinSub); strings.Contains(sys, "子对话专家设定") {
		t.Fatalf("内置专家无追加提示词，不应出现专家设定段：%q", sys)
	}

	// 自定义专家按名派发：预设生效（继承上下文、子会话记专家名）
	out, err = runDispatchSub(tctx, []byte(`{"task":"审查 utils.go","expert":"审查员"}`))
	if err != nil {
		t.Fatalf("按专家派发失败: %v", err)
	}
	if !strings.Contains(out, "专家预设「审查员」") || !strings.Contains(out, "已继承本会话上下文") {
		t.Fatalf("返回文案应说明专家与继承：%s", out)
	}
	kids, _ = a.Store.Children("p1")
	var sub *SessionMeta
	for _, k := range kids {
		if k.Expert == "审查员" {
			sub = k
		}
	}
	if sub == nil {
		t.Fatalf("子会话应记录专家名，kids=%+v", kids)
	}
	// 系统提示词追加专家段（拼在基础提示词之后）
	sys := a.systemPrompt(sub)
	if !strings.Contains(sys, "## 子对话专家设定：审查员") || !strings.Contains(sys, "你是审查员。") {
		t.Fatalf("系统提示词应含专家追加段：%q", sys)
	}
	if !strings.Contains(sys, taskPrompt[:20]) {
		t.Fatal("专家段应是追加而非替换基础提示词")
	}

	// 继承完全由预设决定：不继承的自定义专家即使传旧参数 inherit_context 也不继承
	// （该参数已从工具删除，未知字段被忽略）
	a.Cfg.Experts = append(a.Cfg.Experts, ExpertCfg{
		ID: "e3", Name: "快审", Description: "快", Prompt: "快。", InheritContext: false, Enabled: true})
	out, err = runDispatchSub(tctx, []byte(`{"task":"再审查一个","expert":"快审","inherit_context":true}`))
	if err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if strings.Contains(out, "已继承") {
		t.Fatalf("不继承的预设不应继承（旧参数应被忽略）：%s", out)
	}
}

// 专家预设的默认模型：显式 model 缺省时生效，传了则覆盖。
func TestDispatchSubExpertDefaultModel(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.BaseURL = "mock"
	a.Cfg.Providers = []ProviderCfg{{ID: "prov", Name: "测试方", BaseURL: "mock",
		Models: []ModelCfg{{ID: "默认模型"}, {ID: "专家模型"}}}}
	a.Cfg.Experts = []ExpertCfg{
		{ID: "e1", Name: "快枪手", Description: "快", Prompt: "快。", Model: "专家模型", Enabled: true},
	}
	parent := &SessionMeta{ID: "p1", Mode: "task", Workspace: "ws", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(parent); err != nil {
		t.Fatalf("建父会话失败: %v", err)
	}
	tctx := &toolContext{app: a, sess: parent, emit: func(Event) {}}

	if _, err := runDispatchSub(tctx, []byte(`{"task":"干个活","expert":"快枪手"}`)); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	kids, _ := a.Store.Children("p1")
	if len(kids) != 1 || kids[0].Model != "专家模型" {
		t.Fatalf("预设模型应生效： %+v", kids)
	}
	// 显式 model 覆盖预设
	if _, err := runDispatchSub(tctx, []byte(`{"task":"再干一个","expert":"快枪手","model":"默认模型"}`)); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	kids, _ = a.Store.Children("p1")
	if len(kids) != 2 || kids[1].Model != "默认模型" {
		t.Fatalf("显式模型应覆盖预设： %+v", kids)
	}
	// 没配模型的专家：沿用全局默认（子会话 Model 为空，解析时回落第一个 provider）
	a.Cfg.Experts[0].Model = ""
	if _, err := runDispatchSub(tctx, []byte(`{"task":"第三个","expert":"快枪手"}`)); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	kids, _ = a.Store.Children("p1")
	if len(kids) != 3 || kids[2].Model != "" {
		t.Fatalf("无预设模型应留空回落全局默认： %+v", kids)
	}
}

// 专家被删后：存量子对话的系统提示词不再含追加段（名字解析不到，静默降级）。
func TestSystemPromptAfterExpertDeleted(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.Experts = []ExpertCfg{
		{ID: "e1", Name: "临时工", Description: "x", Prompt: "你是临时工。", Enabled: true},
	}
	sess := &SessionMeta{ID: "s1", Mode: "task", Expert: "临时工", CreatedAt: now(), UpdatedAt: now()}
	if s := a.systemPrompt(sess); !strings.Contains(s, "你是临时工。") {
		t.Fatal("专家存在时应含追加段")
	}
	a.Cfg.Experts = nil
	if s := a.systemPrompt(sess); strings.Contains(s, "临时工") {
		t.Fatal("专家删除后不应再含追加段")
	}
	// 停用不等于删除：存量会话的追加段保留
	a.Cfg.Experts = []ExpertCfg{{ID: "e1", Name: "临时工", Description: "x", Prompt: "你是临时工。", Enabled: false}}
	if s := a.systemPrompt(sess); !strings.Contains(s, "你是临时工。") {
		t.Fatal("停用的专家对存量会话仍应生效（只有删除才消失）")
	}
}

// 专家清单（list_experts 的返回）：内置两条始终在，启用中的自定义跟进，
// 停用的不进。
func TestExpertListDoc(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.Experts = nil
	doc := a.expertListDoc()
	if !strings.Contains(doc, "标准子对话") || !strings.Contains(doc, "继承上下文子对话") {
		t.Fatalf("没有自定义专家时清单也应含内置两条：%q", doc)
	}
	a.Cfg.Experts = []ExpertCfg{
		{ID: "e1", Name: "审查员", Description: "找问题", Prompt: "x", InheritContext: true, Model: "m1", Enabled: true},
		{ID: "e2", Name: "停用的", Description: "x", Prompt: "x", Enabled: false},
	}
	doc = a.expertListDoc()
	if !strings.Contains(doc, "审查员") || !strings.Contains(doc, "继承上下文") || !strings.Contains(doc, "默认模型 m1") {
		t.Fatalf("清单应含启用专家的名字与元信息：%q", doc)
	}
	if strings.Contains(doc, "停用的") {
		t.Fatalf("停用的专家不该进清单：%q", doc)
	}
}

// 工具目录恒定契约：专家增删改【不得】改变 tools 数组（专家名单走
// list_experts 按需查询，不嵌工具描述——否则前缀缓存从最开头全断）。
func TestToolCatalogStableAcrossExpertChanges(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.Cfg.Experts = nil
	snapshot := func(mode string) string {
		defs := toolDefsOf(a.toolsFor(mode))
		b, err := json.Marshal(defs)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, mode := range []string{"assistant", "task", "office"} {
		before := snapshot(mode)
		a.Cfg.Experts = []ExpertCfg{
			{ID: "e1", Name: "审查员", Description: "找问题", Prompt: "x", InheritContext: true, Model: "m1", Enabled: true},
		}
		afterAdd := snapshot(mode)
		a.Cfg.Experts = nil
		afterDel := snapshot(mode)
		if before != afterAdd || before != afterDel {
			t.Fatalf("%s 模式的工具目录随专家增删变化了（前缀缓存会全断）", mode)
		}
		if !strings.Contains(before, "list_experts") || !strings.Contains(before, "dispatch_sub") {
			t.Fatalf("%s 模式应常驻 list_experts 与 dispatch_sub", mode)
		}
		if strings.Contains(before, "审查员") || strings.Contains(before, "对抗性审查") {
			t.Fatalf("%s 模式的工具描述不应内嵌专家名", mode)
		}
	}
}

// upsert 校验：重名拒绝、占用内置名拒绝、名称/提示词必填、按 id 更新、按 id 删除。
func TestUpsertDeleteExpert(t *testing.T) {
	a := newTestApp(t, "rule", 5, 5)
	a.CfgPath = filepath.Join(t.TempDir(), "config.json")
	saved, err := a.upsertExpert(ExpertCfg{Name: "审查员", Description: "d", Prompt: "p", Enabled: true})
	if err != nil || saved.ID == "" {
		t.Fatalf("新建专家应有 id：%v %+v", err, saved)
	}
	if _, err := a.upsertExpert(ExpertCfg{Name: " 审查员 ", Prompt: "p2"}); err == nil || !strings.Contains(err.Error(), "同名") {
		t.Fatalf("重名应拒绝：%v", err)
	}
	if _, err := a.upsertExpert(ExpertCfg{Name: "标准子对话", Prompt: "p"}); err == nil || !strings.Contains(err.Error(), "内置") {
		t.Fatalf("占用内置专家名应拒绝：%v", err)
	}
	if _, err := a.upsertExpert(ExpertCfg{Name: "继承上下文子对话", Prompt: "p"}); err == nil {
		t.Fatal("占用第二个内置名也应拒绝")
	}
	if _, err := a.upsertExpert(ExpertCfg{Name: "", Prompt: "p"}); err == nil {
		t.Fatal("空名应拒绝")
	}
	if _, err := a.upsertExpert(ExpertCfg{Name: "n", Prompt: " "}); err == nil {
		t.Fatal("空提示词应拒绝")
	}
	// 按 id 更新（改名避开重名检查）
	saved.Name = "改过名的"
	if _, err := a.upsertExpert(saved); err != nil {
		t.Fatalf("按 id 更新失败：%v", err)
	}
	if len(a.Cfg.Experts) != 1 || a.Cfg.Experts[0].Name != "改过名的" {
		t.Fatalf("应原地更新：%+v", a.Cfg.Experts)
	}
	if err := a.deleteExpert(saved.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if len(a.Cfg.Experts) != 0 {
		t.Fatalf("删除后应清空：%+v", a.Cfg.Experts)
	}
}

// 配置迁移：老配置没有 experts 键 → 补种演示专家；写空数组（用户删光）→ 保持为空。
func TestLoadConfigExpertsMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"port": 8788, "workspace": "ws"}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(cfg.Experts) != 1 || cfg.Experts[0].Name != "对抗性审查编程助手" {
		t.Fatalf("老配置应补种演示专家：%+v", cfg.Experts)
	}
	if !cfg.Experts[0].InheritContext || cfg.Experts[0].Model != "" || !cfg.Experts[0].Enabled {
		t.Fatalf("演示专家应为：继承上下文 / 模型留空 / 启用：%+v", cfg.Experts[0])
	}

	// 用户删光后（显式空数组）：再次加载不得复活演示专家
	if err := os.WriteFile(path, []byte(`{"port": 8788, "workspace": "ws", "experts": []}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(cfg2.Experts) != 0 {
		t.Fatalf("显式空数组不该复活演示专家：%+v", cfg2.Experts)
	}
}
