package main

// 计划的读取与导出端点（右侧边栏「计划」视图的数据源）。
//
// 计划正文本身不需要新的存储：exit_plan_mode 的调用参数就是计划全文，而调用它的
// 助手消息在工具执行**之前**就已落 messages 表（agent.go），因此计划与对话历史
// 同寿命——刷新页面、切走再回来、重启进程之后都能重新打开（对齐数字助理2 的
// plan 资源：从会话日志里读回那一次 exit_plan_mode 调用）。
//
// 只有一件事这里不做：判断某份计划现在是不是"待审批"。那属于运行时状态
// （interact.go 的 questions 表），前端拿 /api/questions 与这里的清单合并即可。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// PlanDoc 是一份提交过的计划：Markdown 全文 + 出处（会话内序号与调用 id）。
type PlanDoc struct {
	Seq       int64  `json:"seq"`                 // 提交它的助手消息在会话内的序号
	CallID    string `json:"callId,omitempty"`    // 该次工具调用 id（同一条消息可含多个调用）
	Title     string `json:"title"`               // 首个标题行（Markdown 里的 # 行）
	Markdown  string `json:"markdown"`            // 计划全文（原样，前端按 Markdown 渲染）
	CreatedAt string `json:"createdAt,omitempty"` // 该条消息的落库时间
}

// plansOf 从会话日志里按提交顺序取出全部计划（新的在后）。
func (a *App) plansOf(sessionID string) ([]PlanDoc, error) {
	msgs, err := a.Store.MessagesWithSeq(sessionID)
	if err != nil {
		return nil, err
	}
	plans := []PlanDoc{}
	for _, msg := range msgs {
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name != "exit_plan_mode" {
				continue
			}
			var p struct {
				Plan string `json:"plan"`
			}
			// 参数坏行（模型输出的半截 JSON）跳过：它本就在对话里以普通工具行呈现
			if json.Unmarshal([]byte(tc.Function.Arguments), &p) != nil {
				continue
			}
			md := strings.TrimSpace(p.Plan)
			if md == "" {
				continue
			}
			plans = append(plans, PlanDoc{
				Seq: msg.Seq, CallID: tc.ID, Title: planTitleOf(md),
				Markdown: md, CreatedAt: msg.CreatedAt,
			})
		}
	}
	return plans, nil
}

// handleListPlans 返回某会话历史里提交过的全部计划。
func (a *App) handleListPlans(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	plans, err := a.plansOf(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"plans": plans})
}

// handleExportPlan 把某份计划写成会话工作区里的一个 .md 文件（右栏「计划」的「导出」）。
//
// 几条边界：
//   - 走会话自己的文件环境（本地直写 / 远程经 SFTP），与「文件上传」同一条路；
//   - **不覆盖**同名文件：`计划-20260929-0102-标题.md` 已存在就顺延 `-2`、`-3`；
//   - 不走交付（deliver）机制：那条路需要上传记录才能定位目标目录，而导出只是
//     "把这份计划放进工作区"，用户自己再决定拿去哪里；
//   - 请求体 `{seq}` 缺省/为 0 时导出最新一份。
func (a *App) handleExportPlan(w http.ResponseWriter, r *http.Request) {
	m := a.sessionFromPath(w, r)
	if m == nil {
		return
	}
	var body struct {
		Seq int64 `json:"seq"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // 空体也接受（导出最新一份）
	}
	plans, err := a.plansOf(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(plans) == 0 {
		writeErr(w, 404, "本会话还没有提交过计划（计划模式下模型调用 exit_plan_mode 后才有）")
		return
	}
	doc := plans[len(plans)-1] // 缺省：最新一份
	if body.Seq > 0 {
		found := false
		for _, p := range plans {
			if p.Seq == body.Seq {
				doc, found = p, true
				break
			}
		}
		if !found {
			writeErr(w, 404, fmt.Sprintf("找不到 seq=%d 的计划（可用 %d 份）", body.Seq, len(plans)))
			return
		}
	}

	tctx := &toolContext{app: a, sess: m}
	ws, err := tctx.resolve("")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	base := planFileNameOf(doc)
	join := func(name string) string {
		if m.Remote != nil {
			return path.Join(ws, name)
		}
		return filepath.Join(ws, name)
	}
	dst, name := join(base), base
	for i := 2; ; i++ {
		if _, _, serr := tctx.envStat(dst); serr != nil {
			break // 不存在 → 用这个名字
		}
		if i > 99 {
			writeErr(w, 500, "同名文件太多（已到 -99），换个标题再导出")
			return
		}
		name = planNameWithIndex(base, i)
		dst = join(name)
	}
	if err := tctx.envWriteFile(dst, []byte(strings.TrimRight(doc.Markdown, "\n")+"\n")); err != nil {
		writeErr(w, 500, "写入工作区失败: "+err.Error())
		return
	}
	dir := path.Dir(filepath.ToSlash(dst))
	writeJSON(w, 200, map[string]any{
		"ok": 1, "seq": doc.Seq, "name": name, "path": filepath.ToSlash(dst), "dir": dir,
	})
}

// planFileNameOf 生成导出文件名：`计划-YYYYMMDD-HHMM-标题.md`。
// 时间取该份计划的落库时间（拿不到就当前时间），标题去掉文件名非法字符。
func planFileNameOf(d PlanDoc) string {
	ts := time.Now()
	if t, err := time.Parse("2006-01-02 15:04:05", d.CreatedAt); err == nil {
		ts = t
	}
	return "计划-" + ts.Format("20060102-1504") + "-" + planTitleForFile(d.Title) + ".md"
}

// planNameWithIndex 给同名文件的第 n 份（n≥2）加序号：`xxx.md` → `xxx-2.md`。
func planNameWithIndex(name string, n int) string {
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext) + fmt.Sprintf("-%d", n) + ext
}

// planTitleForFile 清洗标题里的文件名非法字符（Windows 那套 + 前后空白与点），
// 空白折成短横、限 40 字；清完为空就用「计划」。
func planTitleForFile(title string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(title) {
		switch {
		case strings.ContainsRune(`/\:*?"<>|`, r), unicode.IsControl(r):
			continue
		case unicode.IsSpace(r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-.")
	out = truncateRunes(out, 40)
	if strings.TrimSpace(out) == "" {
		return "计划"
	}
	return out
}

// planTitleOf 取计划的标题：第一个 Markdown 标题行的文字，没有就用首行。
func planTitleOf(md string) string {
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		}
		return truncateRunes(t, 60)
	}
	return "计划"
}
