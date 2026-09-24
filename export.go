package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 导出：把一个会话连同其全部派生子会话、辅助对话组装成一棵树，
// 由此产出两种成品——
//   Markdown：人读的完整日志（含思考过程、逐条时间戳、工具全量结果）；
//   JSON   ：机器可存的原始记录（存储层字段原样，含 thinking/origin 等）。
// 两种格式同源同料，都取自 messages 表的 append-only 原始历史，
// 与「发给模型的压缩视图」无关。

type exportNode struct {
	Meta     *SessionMeta     `json:"meta"`
	Messages []*StoredMessage `json:"messages"`
	Children []*exportNode    `json:"children,omitempty"`
}

// buildExportTree 递归组装导出树（子任务/辅助对话按 parent_id 归队）。
// visited 只在当前路径上生效，防环形引用；正常数据不会触发。
func (a *App) buildExportTree(id string, path map[string]bool) (*exportNode, error) {
	if path[id] {
		return nil, fmt.Errorf("会话层级出现环：%s", id)
	}
	path[id] = true
	defer delete(path, id)

	m, err := a.Store.Get(id)
	if err != nil {
		return nil, err
	}
	msgs, err := a.Store.RawMessagesAll(id)
	if err != nil {
		return nil, err
	}
	node := &exportNode{Meta: m, Messages: msgs}
	kids, err := a.Store.Children(id)
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		child, err := a.buildExportTree(k.ID, path)
		if err != nil {
			continue // 坏分支不拖垮整份导出
		}
		node.Children = append(node.Children, child)
	}
	return node, nil
}

func modeLabelOf(mode string) string {
	switch mode {
	case "task":
		return "任务"
	case "assistant":
		return "数字助理"
	case "aux":
		return "辅助对话"
	}
	return mode
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// htmlEscape 只用于 <pre> 块内的原文，防止内容里的标签破坏结构。
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// expText 保证导出文档是合法 UTF-8：历史上控制台 GBK 输出可能以非法
// 字节入库，这里统一替换为 U+FFFD，文档不因坏字节损坏。
func expText(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}

func isSysNote(text string) bool {
	for _, p := range []string{"【派发回报】", "【系统中断】", "【执行中断】", "【文件上传】"} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// renderExportMessages 渲染一个会话的消息序列（原始顺序，无删减）。
func renderExportMessages(b *strings.Builder, msgs []*StoredMessage) {
	for _, m := range msgs {
		ts := ""
		if m.CreatedAt != "" {
			ts = " · " + m.CreatedAt
		}
		if m.Kind == "request" {
			// 模型输入快照体积大，导出只留一行占位（完整原文在应用内对话流展开查看）
			var r struct {
				Step     int    `json:"step"`
				Model    string `json:"model"`
				Messages []any  `json:"messages"`
			}
			_ = json.Unmarshal([]byte(m.Text()), &r)
			b.WriteString("> **[模型输入 #" + strconv.Itoa(r.Step) + " · " + r.Model +
				" · " + strconv.Itoa(len(r.Messages)) + " 条消息 · 含工具定义]**" + ts + "\n\n")
			continue
		}
		if m.Kind == "summary" {
			label := "摘要"
			if m.Origin == "manual" {
				label += " · 手动"
			} else if m.Origin == "auto" {
				label += " · 自动"
			}
			if m.CoversUpTo > 0 {
				label += fmt.Sprintf(" · 覆盖至第 %d 条", m.CoversUpTo)
			}
			if m.Superseded {
				label += " · 已被更新的自动摘要合并"
			}
			b.WriteString("> **[" + label + "]**" + ts + "\n>\n> " +
				strings.ReplaceAll(strings.TrimSpace(expText(m.Text())), "\n", "\n> ") + "\n\n")
			continue
		}
		switch m.Role {
		case "user":
			if isSysNote(m.Text()) {
				b.WriteString("## 📎 系统记录" + ts + "\n\n" + expText(m.Text()) + "\n\n")
			} else {
				b.WriteString("## 🧑 用户" + ts + "\n\n" + expText(m.Text()) + "\n\n")
			}
		case "assistant":
			head := "## 🤖 助手"
			if len(m.ToolCalls) > 0 {
				names := []string{}
				for _, tc := range m.ToolCalls {
					names = append(names, tc.Function.Name)
				}
				head += "（调用：" + strings.Join(names, ", ") + "）"
			}
			b.WriteString(head + ts + "\n\n")
			if m.Thinking != "" {
				b.WriteString("<details><summary>思考过程（完整原文）</summary>\n\n<pre>" +
					htmlEscape(expText(m.Thinking)) + "</pre>\n\n</details>\n\n")
			}
			if t := m.Text(); strings.TrimSpace(t) != "" {
				b.WriteString(expText(t) + "\n\n")
			}
			for _, tc := range m.ToolCalls {
				b.WriteString("<details><summary>工具调用 " + tc.Function.Name +
					"（参数完整原文）</summary>\n\n<pre>" + htmlEscape(expText(tc.Function.Name + " " + tc.Function.Arguments)) +
					"</pre>\n\n</details>\n\n")
			}
		case "tool":
			out := expText(m.Text())
			b.WriteString(fmt.Sprintf("<details><summary>工具结果%s · 全文 %d 字（未截断）</summary>\n\n<pre>",
				ts, len([]rune(out))) + htmlEscape(out) + "</pre>\n\n</details>\n\n")
		}
	}
}

// renderExportNode 渲染一个会话节点（头 + 消息 + 子会话附录，递归）。
func (a *App) renderExportNode(b *strings.Builder, n *exportNode, topLevel bool) {
	m := n.Meta
	if topLevel {
		b.WriteString("# " + m.Title + "\n\n")
	} else {
		b.WriteString("---\n\n## 📎 " + modeLabelOf(m.Mode) + "：" + m.Title + "\n\n")
	}
	info := []string{
		"- 会话：" + m.ID + "（" + modeLabelOf(m.Mode) + "）",
		"- 模型：" + m.Provider + "/" + m.Model,
		"- 工作区：" + wsOr(a, m) + remoteSuffix(m),
		"- 权限：" + permOf(m) + "｜计划模式：" + onOff(m.PlanMode) + "｜目标模式：" + onOff(m.GoalMode),
		"- 创建：" + m.CreatedAt + "｜导出：" + now() + "｜消息数：" + fmt.Sprint(len(n.Messages)),
	}
	if m.ParentID != "" {
		info = append(info, "- 来源：派生自会话 "+m.ParentID)
	}
	b.WriteString(strings.Join(info, "\n") + "\n\n---\n\n")
	renderExportMessages(b, n.Messages)
	if len(n.Children) > 0 {
		b.WriteString("---\n\n### 子会话（" + fmt.Sprint(len(n.Children)) + " 个，含派发子任务与辅助对话）\n\n")
		for _, c := range n.Children {
			a.renderExportNode(b, c, false)
		}
	}
}

func remoteSuffix(m *SessionMeta) string {
	if m.Remote == nil {
		return ""
	}
	u := m.Remote.User
	if u == "" {
		u = "root"
	}
	return fmt.Sprintf("（SSH 远程 %s@%s:%s）", u, m.Remote.Host, m.Remote.Dir)
}

// ExportMarkdown 生成整份 Markdown 日志。
func (a *App) ExportMarkdown(root *exportNode) string {
	var b strings.Builder
	a.renderExportNode(&b, root, true)
	return b.String()
}
