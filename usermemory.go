package main

// 用户记忆组件（组件/user-memory，「AI 记忆与拟人化」）的宿主半边：用户称呼、
// 用户身份与喜好、AI 助理名字与身份、AI 对话气泡颜色的长期记忆。设计意图
// （用户拍板）：
//   - 全部内容存一个 md 文档（data/user-memory.md，组件自管，可手工编辑、
//     完整保存不截断，保存后下一轮对话经环境快照差分自然生效）；
//   - 记忆内容注入各会话的**环境提示词**（agent.go envPrompt →
//     userMemoryEnvBlock），不进系统提示词、不进工具定义——组件关闭即整块
//     消失，前缀缓存不受影响；注入侧对超长字段截断（手工编辑的超长内容
//     不撑爆快照，文件仍存全文）；
//   - 权限分级：普通任务对话拿到专用工具 user_memory（查询全部字段，但只
//     能更新「用户身份与喜好」记忆段——称呼与 AI 名字/身份是用户的自我
//     设定，模型只读）；数字经理（assistant 模式）在环境提示词里被告知
//     整个记忆文件可读可改，走 call_api 的 GET/POST /api/user-memory——
//     文件在助手数据目录内，文件工具被受保护路径拦下，接口是唯一通道；
//   - 长度约束（1000 字/字段，rune 计）在写入入口把关而非存储层：设置页
//     前端超长时红字告知并按截断版提交（浏览器保留完整草稿）；模型侧
//     （工具与 call_api POST）超长整单报错、不写入；
//   - AI 对话气泡颜色是纯前端设置（web/style.css 的 --ai-bubble 变量，
//     组件 client.js 注入，本地生效即可），不进任何提示词。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const userMemoryComponentID = "user-memory"

// 各文本字段上限（rune 计，中文友好）：保存时统一硬截断（前端 input 也限，
// 但后端是权威——自查项「1000 字截断」以这里为准）。
const userMemoryFieldLimit = 1000

// UserMemoryData 是记忆文档的全部字段。
type UserMemoryData struct {
	UserName    string `json:"userName"`    // 用户称呼
	Memory      string `json:"userMemory"`  // 用户身份与喜好（记忆段——AI 可经 user_memory 工具更新）
	AIName      string `json:"aiName"`      // AI 助理名字
	AIIdentity  string `json:"aiIdentity"`  // AI 助理身份
	BubbleColor string `json:"bubbleColor"` // AI 对话气泡颜色（#rrggbb 或空=默认灰，前端展示用）
}

// md 文档的段标题（解析与渲染同一份，顺序即文档顺序）。
var userMemorySectionTitles = []struct {
	field string // 字段名（仅用于注释可读性，解析按下标对应）
	title string
}{
	{"userName", "## 用户称呼"},
	{"userMemory", "## 用户身份与喜好"},
	{"aiName", "## AI 助理名字"},
	{"aiIdentity", "## AI 助理身份"},
	{"bubbleColor", "## AI 对话框颜色"},
}

const userMemoryDocHeader = `# 用户记忆

> 本文件由「AI 记忆与拟人化」组件管理（设置 → AI 记忆与拟人化）；也可手工编辑，保存后下一轮对话自动生效。
> 写入入口有 1000 字/字段的上限（设置页超长保存按截断版入库，AI 调用超长直接报错）；手工编辑不受此限，文件内容完整保存。
> 段标题与顺序请保持不变，便于程序解析。
`

// userMemoryEnabled 组件门控（todoEnabled 同款）：组件缺失或关闭视为未启用。
func (a *App) userMemoryEnabled() bool {
	if a.Components == nil {
		return false
	}
	if _, ok := a.Components.Manifest(userMemoryComponentID); !ok {
		return false
	}
	return a.Components.IsEnabled(userMemoryComponentID)
}

// ---------- 文档读写 ----------

var userMemoryMu sync.Mutex // 读改写全走这里：并发保存（设置页/工具/经理）不互相踩

func (a *App) userMemoryPath() string {
	return filepath.Join(a.Cfg.DataDir, "user-memory.md")
}

// loadUserMemory 读记忆文档；文件不存在或段缺失按空字段处理（损坏不抛错——
// 空字段顶上，原文档还在磁盘上没被动过）。
func (a *App) loadUserMemory() UserMemoryData {
	data, err := os.ReadFile(a.userMemoryPath())
	if err != nil {
		return UserMemoryData{}
	}
	return parseUserMemoryDoc(string(data))
}

// saveUserMemory 整份重写记忆文档。**不做截断**（用户拍板：md 文件存完整
// 内容——手工编辑可以写任意长），长度约束在两个写入入口把关：设置页前端
// 截断后提交、模型侧超限直接报错（handleUserMemorySet / userMemoryTool）。
func (a *App) saveUserMemory(d UserMemoryData) (UserMemoryData, error) {
	err := writeFileAtomic(a.userMemoryPath(), []byte(renderUserMemoryDoc(d)))
	return d, err
}

// overLimitFields 返回超长字段的中文名（rune 计）。设置页前端提交前已截断，
// 走到这里的是模型侧（call_api POST / user_memory 工具）——超限一律整单拒绝，
// 不静默截断（模型必须知道写入失败，改短再试）。
func (d UserMemoryData) overLimitFields() []string {
	var over []string
	for name, s := range map[string]string{
		"userName": d.UserName, "userMemory": d.Memory,
		"aiName": d.AIName, "aiIdentity": d.AIIdentity,
	} {
		if utf8.RuneCountInString(s) > userMemoryFieldLimit {
			over = append(over, name)
		}
	}
	sort.Strings(over)
	return over
}

// parseUserMemoryDoc 解析 md 文档：按「## 标题」分段，段内容为其后连续正文
// （跳过空行与引用行）。未知段落忽略——手工添加的额外内容不参与解析，
// 但会在下次程序化保存时被重建的文档覆盖。
func parseUserMemoryDoc(text string) UserMemoryData {
	var d UserMemoryData
	fields := []*string{&d.UserName, &d.Memory, &d.AIName, &d.AIIdentity, &d.BubbleColor}
	// 标题 → 字段指针的索引
	idx := map[string]int{}
	for i, s := range userMemorySectionTitles {
		idx[s.title] = i
	}
	cur := -1
	var buf []string
	flush := func() {
		if cur >= 0 {
			*fields[cur] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
		buf = nil
	}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimRight(line, "\r")
		if strings.HasPrefix(t, "## ") {
			flush()
			if i, ok := idx[strings.TrimSpace(t)]; ok {
				cur = i
			} else {
				cur = -1
			}
			continue
		}
		if cur < 0 {
			continue // 文件头与引用说明行
		}
		if strings.HasPrefix(t, ">") { // 段内的引用/注释行不算正文
			continue
		}
		buf = append(buf, t)
	}
	flush()
	return d
}

// renderUserMemoryDoc 渲染 md 文档（固定段序；空段保留标题，用户填了就有）。
func renderUserMemoryDoc(d UserMemoryData) string {
	var b strings.Builder
	b.WriteString(userMemoryDocHeader)
	vals := []string{d.UserName, d.Memory, d.AIName, d.AIIdentity, d.BubbleColor}
	for i, s := range userMemorySectionTitles {
		b.WriteString("\n" + s.title + "\n\n")
		if v := strings.TrimSpace(vals[i]); v != "" {
			b.WriteString(v + "\n")
		}
	}
	return b.String()
}

// ---------- 环境提示词块（agent.go envPrompt 调用；不进系统提示词/工具定义） ----------

// userMemoryEnvBlock 渲染注入各会话环境快照的记忆块：组件开启且任一文本字段
// 有内容时出现；内容变化（设置页/工具/经理改动）经环境快照差分送达全部会话。
// 颜色是前端设置不进提示词。多行字段压成单行（快照条目行语义，差分按行精确）；
// md 文件存完整内容（写入入口把关长度），注入侧对超长字段截断到 1000 字加
// 尾注——手工编辑写了超长内容也不会撑爆环境快照。
func (a *App) userMemoryEnvBlock(sess *SessionMeta) string {
	if !a.userMemoryEnabled() {
		return ""
	}
	d := a.loadUserMemory()
	var b strings.Builder
	if v := envLine(d.UserName); v != "" {
		b.WriteString("- 用户称呼：" + v + "\n")
	}
	if v := envLine(d.Memory); v != "" {
		b.WriteString("- 用户身份与喜好：" + v + "\n")
	}
	if v := envLine(d.AIName); v != "" {
		b.WriteString("- AI 助理名字：" + v + "（回复中的自称用它）\n")
	}
	if v := envLine(d.AIIdentity); v != "" {
		b.WriteString("- AI 助理身份：" + v + "\n")
	}
	if b.Len() == 0 {
		return "" // 四个字段全空：整块不出现
	}
	switch sess.Mode {
	case "assistant":
		b.WriteString("- 记忆维护（数字经理专属）：整个记忆文件可读可改——call_api 调 GET /api/user-memory 读取、POST /api/user-memory 按字段保存（userName/userMemory/aiName/aiIdentity/bubbleColor，各上限 1000 字，超长会整单报错、不会静默截断；未给出的字段保持不变）。文件位于 " + filepath.ToSlash(a.userMemoryPath()) + "（助手数据目录内，文件工具被受保护路径拦截，读写一律走 call_api）。改动自下一轮起注入全部会话的环境提示词，请保持字段本意、谨慎修改\n")
	case "aux":
		b.WriteString("- 记忆维护：以上内容由用户在 设置 → AI 记忆与拟人化 维护（本会话为只读答疑，没有修改工具）\n")
	default:
		b.WriteString("- 记忆维护：user_memory 工具可查询并更新「用户身份与喜好」段（了解到用户新的身份/偏好/习惯/项目背景时主动更新；整体替换式写入，先查再改；单次写入上限 1000 字，超长会报错，需精简后重试）；用户称呼与 AI 助理名字/身份由用户在 设置 → AI 记忆与拟人化 维护，不要改动\n")
	}
	return "# 用户记忆（用户与助手的长期设定，全部会话生效）\n" + b.String()
}

// envLine 字段单行化 + 注入侧截断：oneLine 后超 1000 字取前 1000 字加尾注
// （文件仍存完整内容，这里只管快照不膨胀）。
func envLine(s string) string {
	v := oneLine(s)
	if utf8.RuneCountInString(v) <= userMemoryFieldLimit {
		return v
	}
	return string([]rune(v)[:userMemoryFieldLimit]) + "…（超长，已截断）"
}

// oneLine 把多行字段压成单行（换行→全角分号），保住快照条目行的差分语义。
func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", "；", "\n", "；", "\r", "").Replace(s))
}

// ---------- HTTP（前端设置页 + 数字经理 call_api 共用） ----------

func (a *App) handleUserMemoryGet(w http.ResponseWriter, r *http.Request) {
	if !a.userMemoryEnabled() {
		writeErr(w, 404, "「AI 记忆与拟人化」组件未启用")
		return
	}
	userMemoryMu.Lock()
	d := a.loadUserMemory()
	userMemoryMu.Unlock()
	writeJSON(w, 200, map[string]any{
		"userName": d.UserName, "userMemory": d.Memory,
		"aiName": d.AIName, "aiIdentity": d.AIIdentity, "bubbleColor": d.BubbleColor,
		"path": filepath.ToSlash(a.userMemoryPath()),
	})
}

// handleUserMemorySet 按字段保存：body 里出现的字段替换、未出现的保持不变
// （设置页全量提交——前端已截断；经理可只改一个字段）。**超长整单拒绝**
// （各字段上限 1000 字，rune 计）——存储不截断，模型侧必须拿到明确错误。
func (a *App) handleUserMemorySet(w http.ResponseWriter, r *http.Request) {
	if !a.userMemoryEnabled() {
		writeErr(w, 404, "「AI 记忆与拟人化」组件未启用")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体应为 JSON 对象")
		return
	}
	str := func(k string) (string, bool) {
		v, ok := body[k]
		if !ok {
			return "", false
		}
		s, _ := v.(string)
		return s, true
	}
	userMemoryMu.Lock()
	defer userMemoryMu.Unlock()
	d := a.loadUserMemory()
	for k, p := range map[string]*string{
		"userName": &d.UserName, "userMemory": &d.Memory,
		"aiName": &d.AIName, "aiIdentity": &d.AIIdentity, "bubbleColor": &d.BubbleColor,
	} {
		if v, ok := str(k); ok {
			*p = v
		}
	}
	if over := d.overLimitFields(); len(over) > 0 {
		writeErr(w, 400, fmt.Sprintf("字段 %s 超过 %d 字上限（rune 计），未写入任何内容；请精简后重试", strings.Join(over, "、"), userMemoryFieldLimit))
		return
	}
	saved, err := a.saveUserMemory(d)
	if err != nil {
		writeErr(w, 500, "保存失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": 1,
		"userName": saved.UserName, "userMemory": saved.Memory,
		"aiName": saved.AIName, "aiIdentity": saved.AIIdentity, "bubbleColor": saved.BubbleColor,
		"path": filepath.ToSlash(a.userMemoryPath()),
	})
}

// ---------- 专用工具（普通任务对话：查询全部字段 + 只更新记忆段） ----------

// userMemoryTools 组件 user-memory 启用时返回 user_memory 工具（todoTools 同款
// 组件门控）。挂各执行模式：aux 只读答疑不挂；assistant 不挂（经理的通道是
// call_api 整文件读写，环境提示词里有说明——权限分级，工具是「弱通道」）。
func (a *App) userMemoryTools() []Tool {
	if !a.userMemoryEnabled() {
		return nil
	}
	return []Tool{a.userMemoryTool()}
}

func (a *App) userMemoryTool() Tool {
	return Tool{
		Spec: toolSpec("user_memory", "查询/更新用户记忆（用户的身份、相关信息与喜好的长期记忆，全部会话共享、每轮注入环境提示词）。不带参数 = 查询当前全部字段；带 memory 参数 = 把「用户身份与喜好」记忆段整体替换为给定内容（先查询、在结果基础上改写，避免丢掉已有记忆；上限 1000 字，超长会报错、本次不写入，需精简后重试）。其余字段（用户称呼、AI 助理名字/身份）是用户的自我设定，本工具只读不写。当你在对话中了解到值得长期记住的用户信息（身份、偏好、习惯、项目背景等）时，主动调用本工具更新记忆。",
			objSchema(map[string]any{
				"memory": strProp("新的「用户身份与喜好」记忆段全文（整体替换式写入，上限 1000 字）；不传 = 仅查询"),
			})),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			// 组件可能被会话中途关掉（工具目录在本轮开轮时定型）——调用时再核一次
			if !t.app.userMemoryEnabled() {
				return "", fmt.Errorf("「AI 记忆与拟人化」组件未启用，user_memory 不可用；请到 设置 → 内置组件 打开")
			}
			var p struct {
				Memory *string `json:"memory"` // 指针区分「未传=查询」与「传空串=清空」
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &p); err != nil {
					return "", err
				}
			}
			userMemoryMu.Lock()
			defer userMemoryMu.Unlock()
			d := t.app.loadUserMemory()
			if p.Memory == nil {
				out, _ := json.Marshal(map[string]string{
					"userName": d.UserName, "userMemory": d.Memory,
					"aiName": d.AIName, "aiIdentity": d.AIIdentity,
				})
				return string(out), nil
			}
			// 超长整单拒绝（用户拍板：写入入口报错，不静默截断、不写入）
			if n := utf8.RuneCountInString(*p.Memory); n > userMemoryFieldLimit {
				return "", fmt.Errorf("记忆段内容 %d 字超过 %d 字上限，本次未写入；请精简后重试", n, userMemoryFieldLimit)
			}
			d.Memory = *p.Memory
			saved, err := t.app.saveUserMemory(d)
			if err != nil {
				return "", err
			}
			return "已更新「用户身份与喜好」记忆段。当前内容：\n" + saved.Memory, nil
		},
	}
}
