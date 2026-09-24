package main

// 用户技能管理：自带技能（pdf/ocr/office）只读展示、常开；
// 用户技能位于 技能/user/<目录名>/（必须含 SKILL.md），可添加/开关/删除。
// 开关状态存 data/skills.json。开启的用户技能，其 SKILL.md 正文（去
// frontmatter）会追加在系统提示词的最末尾——基础提示词与环境块保持
// 稳定前缀，开关技能只影响尾部，不影响缓存命中率。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 内置技能 id：只读展示、不可删除、不可关闭。
var builtinSkillIDs = map[string]bool{"pdf": true, "ocr": true, "office": true}

const (
	userSkillBodyMax = 6000 // read_skill 返回的单个技能说明上限（字符）
)

type userSkillEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	BuiltIn     bool   `json:"builtin"`
}

type skillsState struct {
	Disabled map[string]bool `json:"disabled"` // 用户技能默认开启，只记关闭项
}

var skillsStateMu sync.Mutex

func (a *App) skillsStatePath() string { return filepath.Join(a.Cfg.DataDir, "skills.json") }

func (a *App) loadSkillsState() skillsState {
	skillsStateMu.Lock()
	defer skillsStateMu.Unlock()
	st := skillsState{Disabled: map[string]bool{}}
	data, err := os.ReadFile(a.skillsStatePath())
	if err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Disabled == nil {
		st.Disabled = map[string]bool{}
	}
	return st
}

func (a *App) saveSkillsState(st skillsState) error {
	skillsStateMu.Lock()
	defer skillsStateMu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.skillsStatePath(), data, 0644)
}

// userSkillsDir 用户技能根目录：技能/user/。
func (a *App) userSkillsDir() (string, error) {
	dir, err := a.SkillsDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "user")
	if err := os.MkdirAll(p, 0755); err != nil {
		return "", err
	}
	return p, nil
}

// stripFrontmatter 去掉 SKILL.md 开头的 YAML frontmatter（--- ... ---）。
func stripFrontmatter(body string) string {
	s := strings.TrimSpace(body)
	if !strings.HasPrefix(s, "---") {
		return s
	}
	if i := strings.Index(s[3:], "\n---"); i >= 0 {
		return strings.TrimSpace(s[3+i+4:])
	}
	return s
}

// skillMeta 从 SKILL.md 的 frontmatter 提取 name / description；
// 没有 frontmatter 时回退为目录名与正文首行。
func skillMeta(dir string) (name, description, body string) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return filepath.Base(dir), "", ""
	}
	text := string(data)
	s := strings.TrimSpace(text)
	name = filepath.Base(dir)
	description = ""
	if strings.HasPrefix(s, "---") {
		if i := strings.Index(s[3:], "\n---"); i >= 0 {
			front := s[3 : 3+i]
			body = strings.TrimSpace(s[3+i+4:])
			for _, line := range strings.Split(front, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				} else if strings.HasPrefix(line, "description:") {
					description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				}
			}
			return name, description, body
		}
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t != "" {
			description = strings.TrimPrefix(t, "#")
			break
		}
	}
	return name, strings.TrimSpace(description), text
}

// SkillLists 是 GET /api/skills 的载荷：自带技能 + 用户技能。
type SkillLists struct {
	BuiltIn []userSkillEntry `json:"builtin"`
	User    []userSkillEntry `json:"user"`
}

// listSkills 汇总自带与用户技能清单。
func (a *App) listSkills() (*SkillLists, error) {
	out := &SkillLists{BuiltIn: []userSkillEntry{}, User: []userSkillEntry{}}
	dir, err := a.SkillsDir()
	if err != nil {
		return out, nil // 技能目录不可用不算错，返回空清单
	}
	st := a.loadSkillsState()
	// 自带技能：读各自 manifest
	for _, id := range []string{"pdf", "ocr", "office"} {
		m, _, err := a.readSkillManifest(id)
		if err != nil {
			continue
		}
		out.BuiltIn = append(out.BuiltIn, userSkillEntry{
			ID: m.ID, Name: m.Name, Description: m.Description, Enabled: true, BuiltIn: true,
		})
	}
	// 用户技能：扫描 技能/user/
	userDir := filepath.Join(dir, "user")
	entries, err := os.ReadDir(userDir)
	if err != nil {
		return out, nil
	}
	for _, e := range entries {
		if !e.IsDir() || builtinSkillIDs[e.Name()] {
			continue
		}
		skillDir := filepath.Join(userDir, e.Name())
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			continue // 没有 SKILL.md 的目录不算技能
		}
		name, desc, _ := skillMeta(skillDir)
		out.User = append(out.User, userSkillEntry{
			ID: e.Name(), Name: name, Description: desc,
			Enabled: !st.Disabled[e.Name()], BuiltIn: false,
		})
	}
	sort.Slice(out.User, func(i, j int) bool { return out.User[i].ID < out.User[j].ID })
	return out, nil
}

// addUserSkill 把本机的一个技能目录复制进 技能/user/<名>/。
func (a *App) addUserSkill(srcPath string) (*userSkillEntry, error) {
	src := strings.TrimSpace(srcPath)
	if src == "" {
		return nil, fmt.Errorf("路径不能为空")
	}
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("路径不存在或不是目录: %s", src)
	}
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("目录里缺少 SKILL.md（技能必须自带说明文件）")
	}
	id := sanitizeSkillID(filepath.Base(src))
	if id == "" {
		return nil, fmt.Errorf("无法从路径推导技能名，请改用纯字母数字或中文命名目录")
	}
	if builtinSkillIDs[id] {
		return nil, fmt.Errorf("%s 是自带技能，不能重名", id)
	}
	userDir, err := a.userSkillsDir()
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(userDir, id)
	if dirExists(dest) {
		return nil, fmt.Errorf("技能 %s 已存在", id)
	}
	if err := copyDir(src, dest); err != nil {
		return nil, fmt.Errorf("复制技能失败: %w", err)
	}
	name, desc, _ := skillMeta(dest)
	return &userSkillEntry{ID: id, Name: name, Description: desc, Enabled: true}, nil
}

// toggleUserSkill 开关用户技能；自带技能不允许关闭。
func (a *App) toggleUserSkill(id string, enabled bool) error {
	if builtinSkillIDs[id] {
		return fmt.Errorf("自带技能常开，不能关闭")
	}
	userDir, err := a.userSkillsDir()
	if err != nil {
		return err
	}
	if !dirExists(filepath.Join(userDir, id)) {
		return fmt.Errorf("技能 %s 不存在", id)
	}
	st := a.loadSkillsState()
	if enabled {
		delete(st.Disabled, id)
	} else {
		st.Disabled[id] = true
	}
	return a.saveSkillsState(st)
}

// deleteUserSkill 删除用户技能（目录 + 开关状态）；自带技能拒绝。
func (a *App) deleteUserSkill(id string) error {
	if builtinSkillIDs[id] {
		return fmt.Errorf("自带技能不能删除")
	}
	userDir, err := a.userSkillsDir()
	if err != nil {
		return err
	}
	target := filepath.Join(userDir, id)
	if !dirExists(target) {
		return fmt.Errorf("技能 %s 不存在", id)
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	st := a.loadSkillsState()
	delete(st.Disabled, id)
	return a.saveSkillsState(st)
}

// userSkillsPromptBlock 生成用户技能清单段（归入环境快照，变化补在对话尾部）：
// 只列 名字（id）与一句话描述，正文不注入——模型要用哪个技能，先调用
// read_skill(id) 按需读完整说明（对齐 ZCode 的技能模式：清单先行、内容按需，
// 技能增减不动工具定义、不打断前缀缓存）。
func (a *App) userSkillsPromptBlock() string {
	list, err := a.listSkills()
	if err != nil || len(list.User) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range list.User {
		if !s.Enabled {
			continue
		}
		if s.Description == "" {
			b.WriteString("  - " + s.Name + "（id=" + s.ID + "）\n")
		} else {
			b.WriteString("  - " + s.Name + "（id=" + s.ID + "）：" + s.Description + "\n")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "# 用户技能清单（要用某个技能时，先调用 read_skill 读它的完整说明）\n" + b.String()
}

// readUserSkillTool 是用户技能的按需读取工具：清单只报名字与描述，
// 完整 SKILL.md 说明用它加载。工具常驻目录，技能增减不改变工具定义。
func readUserSkillTool() Tool {
	return Tool{
		Spec: toolSpec("read_skill", "读取一个用户技能的完整使用说明（SKILL.md 原文）。环境快照里的「用户技能清单」只列名字与一句话描述；动手用某个技能之前，先调用本工具看完整说明。",
			objSchema(map[string]any{
				"id": strProp("技能 id，来自环境快照里的用户技能清单"),
			}, "id")),
		Run: runReadUserSkill,
	}
}

func runReadUserSkill(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	id := strings.TrimSpace(p.ID)
	// 只允许读清单在列（存在且已启用）的技能：id 经名单校验，不拿用户输入拼路径
	inList := false
	if list, err := t.app.listSkills(); err == nil {
		for _, s := range list.User {
			if s.ID == id && s.Enabled {
				inList = true
				break
			}
		}
	}
	if !inList {
		return "", fmt.Errorf("技能 %s 不在当前清单里（不存在或已停用），请用清单里给出的 id", id)
	}
	userDir, err := t.app.userSkillsDir()
	if err != nil {
		return "", err
	}
	_, _, body := skillMeta(filepath.Join(userDir, id))
	body = stripFrontmatter(body)
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("技能 %s 没有可用的说明正文", id)
	}
	if r := []rune(body); len(r) > userSkillBodyMax {
		body = string(r[:userSkillBodyMax]) + "\n…（说明过长已截断，完整内容见技能目录）"
	}
	return body, nil
}

func sanitizeSkillID(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return -1
		}
		return r
	}, name)
	return strings.TrimSpace(name)
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// ---------- HTTP 处理器 ----------

func (a *App) handleListSkills(w http.ResponseWriter, r *http.Request) {
	list, err := a.listSkills()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (a *App) handleAddSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	entry, err := a.addUserSkill(body.Path)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, entry)
}

func (a *App) handleToggleSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, 400, "需要 id 字段")
		return
	}
	if err := a.toggleUserSkill(body.ID, body.Enabled); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, 400, "需要 id 字段")
		return
	}
	if err := a.deleteUserSkill(body.ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
