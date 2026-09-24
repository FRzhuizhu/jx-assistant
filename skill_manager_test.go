package main

import (
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

	// 注入块：task 会话包含开启技能的正文，且 frontmatter 被剥离
	block := a.userSkillsPromptBlock()
	if !strings.Contains(block, "汇总周报") || !strings.Contains(block, "调用时给出目录路径与周期") {
		t.Fatalf("注入块缺技能内容: %s", block)
	}
	if strings.Contains(block, "description:") || strings.Contains(block, "---") {
		t.Fatalf("注入块不应包含 frontmatter: %s", block)
	}

	// 关闭后注入块变空
	if err := a.toggleUserSkill("周报", false); err != nil {
		t.Fatal(err)
	}
	if block := a.userSkillsPromptBlock(); block != "" {
		t.Fatalf("关闭后不应注入: %s", block)
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
