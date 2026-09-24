package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillsTestApp：技能目录与数据目录都在临时目录的 App；
// 首次访问 SkillsDir 会自动释放内嵌技能母本（与真实行为一致）。
func skillsTestApp(t *testing.T) *App {
	t.Helper()
	dataDir := t.TempDir()
	store, err := NewStore(filepath.Join(dataDir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a := &App{Store: store, Cfg: &Config{DataDir: dataDir, SkillsDir: filepath.Join(dataDir, "技能"), Workspace: t.TempDir()}}
	if _, err := a.SkillsDir(); err != nil {
		t.Fatalf("释放技能母本失败: %v", err)
	}
	return a
}

func writeUserSkill(t *testing.T, a *App, id, name, description, body string) {
	t.Helper()
	dir := filepath.Join(a.Cfg.SkillsDir, "user", id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestUserSkillLifecycle(t *testing.T) {
	a := skillsTestApp(t)
	writeUserSkill(t, a, "周报", "汇总周报", "把目录下的文件整理成中文周报。", "调用时给出目录路径与周期，我会汇总生成周报。")

	list, err := a.listSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.BuiltIn) != 3 {
		t.Fatalf("自带技能应为 3 个（pdf/ocr/office），得到 %d", len(list.BuiltIn))
	}
	if len(list.User) != 1 || list.User[0].ID != "周报" || !list.User[0].Enabled {
		t.Fatalf("用户技能清单不符: %+v", list.User)
	}
	if list.User[0].Description != "把目录下的文件整理成中文周报。" {
		t.Fatalf("frontmatter 描述解析失败: %q", list.User[0].Description)
	}

	// 注入块：task 会话只收到清单（名字+id+描述），正文不注入、frontmatter 不出现
	block := a.userSkillsPromptBlock()
	if !strings.Contains(block, "汇总周报（id=周报）") || !strings.Contains(block, "把目录下的文件整理成中文周报。") {
		t.Fatalf("清单应含名字与描述: %s", block)
	}
	if !strings.Contains(block, "read_skill") {
		t.Fatalf("清单应指引用 read_skill 读完整说明: %s", block)
	}
	if strings.Contains(block, "调用时给出目录路径与周期") || strings.Contains(block, "description:") || strings.Contains(block, "---") {
		t.Fatalf("清单不应包含技能正文/frontmatter: %s", block)
	}

	// read_skill：按需读正文，frontmatter 剥离；停用的技能被拒
	tctx := &toolContext{app: a, sess: &SessionMeta{ID: "s1", Mode: "task"}}
	out, err := runReadUserSkill(tctx, json.RawMessage(`{"id":"周报"}`))
	if err != nil {
		t.Fatalf("read_skill 失败: %v", err)
	}
	if !strings.Contains(out, "调用时给出目录路径与周期") {
		t.Fatalf("read_skill 应返回正文: %s", out)
	}
	if strings.Contains(out, "description:") {
		t.Fatalf("read_skill 不应返回 frontmatter: %s", out)
	}
	if err := a.toggleUserSkill("周报", false); err != nil {
		t.Fatal(err)
	}
	if _, err := runReadUserSkill(tctx, json.RawMessage(`{"id":"周报"}`)); err == nil {
		t.Fatal("停用的技能应被 read_skill 拒绝")
	}
	if _, err := runReadUserSkill(tctx, json.RawMessage(`{"id":"../pdf"}`)); err == nil {
		t.Fatal("不在清单里的 id 应被拒绝（路径穿越防护）")
	}
	if err := a.toggleUserSkill("周报", true); err != nil {
		t.Fatal(err)
	}

	// 关闭后清单不再列它
	if err := a.toggleUserSkill("周报", false); err != nil {
		t.Fatal(err)
	}
	if block := a.userSkillsPromptBlock(); block != "" {
		t.Fatalf("全部停用后清单应为空: %s", block)
	}

	// 重开
	if err := a.toggleUserSkill("周报", true); err != nil {
		t.Fatal(err)
	}
	if block := a.userSkillsPromptBlock(); !strings.Contains(block, "汇总周报") {
		t.Fatal("重开后应恢复注入")
	}

	// 自带技能不可删不可关
	if err := a.deleteUserSkill("pdf"); err == nil {
		t.Fatal("自带技能不应可删除")
	}
	if err := a.toggleUserSkill("pdf", false); err == nil {
		t.Fatal("自带技能不应可关闭")
	}

	// 删除用户技能
	if err := a.deleteUserSkill("周报"); err != nil {
		t.Fatal(err)
	}
	list, _ = a.listSkills()
	if len(list.User) != 0 {
		t.Fatalf("删除后应无用户技能: %+v", list.User)
	}
	if block := a.userSkillsPromptBlock(); block != "" {
		t.Fatal("删除后注入块应为空")
	}
}

// 技能清单的增减经环境快照补在对话尾部：新快照带「技能变更」行说明增删了哪个
// 技能，上文历史快照不动；清单未变时不重复落快照（剔除变更行后的规范比较）。
func TestEnvSnapshotSkillChangeNote(t *testing.T) {
	a := skillsTestApp(t)
	writeUserSkill(t, a, "周报", "汇总周报", "整理周报。", "正文说明")
	sess := &SessionMeta{ID: "s1", Mode: "task", CreatedAt: now(), UpdatedAt: now()}
	if err := a.Store.Create(sess); err != nil {
		t.Fatal(err)
	}
	countEnv := func() int {
		raw, _ := a.Store.RawMessagesAll(sess.ID)
		n := 0
		for _, r := range raw {
			if r.Kind == "env" {
				n++
			}
		}
		return n
	}

	env1 := "快照A\n" + a.userSkillsPromptBlock()
	if got := a.maybeAppendEnvSnapshot(sess, env1, nil); strings.Contains(got, "技能变更") {
		t.Fatalf("首条快照不应有变更行: %s", got)
	}
	if got := a.maybeAppendEnvSnapshot(sess, env1, nil); got != env1 || countEnv() != 1 {
		t.Fatalf("清单未变不应落新快照（%d 条）", countEnv())
	}

	writeUserSkill(t, a, "日报", "汇总日报", "整理日报。", "正文说明")
	env2 := "快照A\n" + a.userSkillsPromptBlock()
	got2 := a.maybeAppendEnvSnapshot(sess, env2, nil)
	if !strings.Contains(got2, "新增技能：汇总日报") {
		t.Fatalf("应说明新增了哪个技能: %s", got2)
	}
	if a.maybeAppendEnvSnapshot(sess, env2, nil) != env2 || countEnv() != 2 {
		t.Fatalf("变更行不应自我累积、不应重复落快照（%d 条）", countEnv())
	}

	if err := a.deleteUserSkill("周报"); err != nil {
		t.Fatal(err)
	}
	env3 := "快照A\n" + a.userSkillsPromptBlock()
	if got3 := a.maybeAppendEnvSnapshot(sess, env3, nil); !strings.Contains(got3, "移除技能：汇总周报") {
		t.Fatalf("应说明移除了哪个技能: %s", got3)
	}
}
