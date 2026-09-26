package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// decodeCmdOutput 把控制台输出字节还原成合法 UTF-8 文本：优先按 UTF-8，
// 失败按 GB18030（Windows 中文控制台的默认代码页），仍不行用替换符兜底。
// 不做这一步，GBK 输出会以非法字节入库，界面与导出都会出现乱码。
func decodeCmdOutput(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if s, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b); err == nil {
		return string(s)
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// 工具集：每个工具 = JSON Schema 定义 + Go 实现。
// 所有会改动外界（写文件 / 执行命令）的工具都先过权限引擎 authorize()
// （见 perm.go）：评估 → 签发授权凭证 → 执行器核销凭证 → 执行；
// 只读与检索类工具做受保护路径检查后直接放行。
// 会话挂载 SSH 远程工作区时，文件操作走 SFTP、命令走远程 shell，
// 工具的参数与行为保持一致（执行环境抽象 env* 方法分发本地/远程）。

// Tool 是模型可调用的一个工具：JSON Schema 定义 + Go 实现。
type Tool struct {
	Spec ToolDef
	Run  func(t *toolContext, args json.RawMessage) (string, error)
}

// toolContext 是一次工具执行的上下文：请求级取消信号、应用引用、
// 当前会话（决定工作区范围与权限模式）与事件上报函数。seq/callID/toolName
// 标出本次调用所在的助手步骤，文件快照据此锚定回退位置。
type toolContext struct {
	ctx      context.Context
	app      *App
	sess     *SessionMeta
	emit     func(Event)
	seq      int64
	callID   string
	toolName string
}

// isRemote 判断会话是否挂载了 SSH 远程工作区。
func (t *toolContext) isRemote() bool {
	return t.sess != nil && t.sess.Remote != nil
}

// workspace 返回会话的工作区根目录（远程会话 = 远程目录）。
func (t *toolContext) workspace() string {
	if t.isRemote() {
		return t.sess.Remote.Dir
	}
	if t.sess.Workspace != "" {
		return t.sess.Workspace
	}
	return t.app.Cfg.Workspace
}

// resolve 把（可能相对的）路径解析为绝对路径；相对路径基于会话工作区。
// 远程会话按 POSIX 规则，本地按 OS 规则。
func (t *toolContext) resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	if t.isRemote() {
		if p == "" {
			return path.Clean(t.workspace()), nil
		}
		if !strings.HasPrefix(p, "/") {
			p = path.Join(t.workspace(), p)
		}
		return path.Clean(p), nil
	}
	if p == "" {
		return filepath.Abs(t.workspace())
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(t.workspace(), p)
	}
	return filepath.Abs(p)
}

// toolsFor 按会话模式返回可用工具。数字助理总控拥有编排类工具（不亲自读写
// 文件、不执行命令）；任务会话拥有全部实操工具 + 子对话派发；辅助对话只有
// 只读工具（定位是答疑，不修改任何东西）。
// 注意：计划/目标模式【不】在这里裁剪工具——目录跨模式保持不变以稳定
// 请求形状（缓存友好），约束靠环境快照公告 + RunAgent 调用时的守卫硬拦
// （见 agent.go 的 suspendedInPlan）；enter/exit_plan_mode / update_goal 因此常驻。
func (a *App) toolsFor(mode string) []Tool {
	history := []Tool{searchSessionsTool(), readSessionMessagesTool()}
	// 待办与定时任务工具（组件 todo-scheduler）：除辅助对话（定位是只答疑）外
	// 各模式都有；组件关掉时为空。
	todos := a.todoTools()
	if mode == "assistant" {
		return append([]Tool{listSessionsTool(), readSummaryTool(), dispatchTaskTool(), a.dispatchSubTool(), a.listExpertsTool(), sendToSessionTool(),
			enterPlanModeTool(), exitPlanModeTool(), updateGoalTool()}, append(todos, history...)...)
	}
	if mode == "aux" {
		// enter_plan_mode 不配：aux 只读答疑，没有可暂停的执行动作，计划无意义
		// （exit 保留，用户手动给 aux 会话开计划模式时仍可退出）。
		return append([]Tool{readFileTool(), listDirTool(), globTool(), grepTool(), webFetchTool(), readImageTool(),
			exitPlanModeTool(), updateGoalTool()}, history...)
	}
	return append([]Tool{
		readFileTool(), writeFileTool(), editFileTool(), listDirTool(),
		globTool(), grepTool(), runCommandTool(), webFetchTool(), readImageTool(),
		pdfTool(), ocrTool(), officeSkillTool(), readUserSkillTool(),
		snapshotFileTool(), deliverResultTool(), a.dispatchSubTool(), a.listExpertsTool(),
		askUserQuestionTool(), enterPlanModeTool(), exitPlanModeTool(), updateGoalTool(), progressWriteTool(),
		jobOutputTool(), jobListTool(), jobKillTool(),
	}, append(append(append(todos, browserTools()...), computerTools()...), history...)...)
}

// ---------- Schema 辅助 ----------

func objSchema(props map[string]any, required ...string) map[string]any {
	// required 为空时必须整体省略：序列化成 null 会被服务端按 schema 校验拒绝。
	if len(required) == 0 {
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func toolSpec(name, desc string, params map[string]any) ToolDef {
	return ToolDef{Type: "function", Function: ToolSpec{Name: name, Description: desc, Parameters: params}}
}

// ---------- 执行环境抽象（本地 / SSH 远程统一文件与目录操作） ----------

// envEntry 是与本地/远程无关的目录项信息。
type envEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

func (t *toolContext) envReadFile(p string) ([]byte, error) {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return nil, err
		}
		return rc.readFile(p)
	}
	return os.ReadFile(p)
}

// envWriteFile 写文件（不核销凭证；带凭证的写入口是 envWriteFileWithGrant）。
func (t *toolContext) envWriteFile(p string, data []byte) error {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return err
		}
		if err := rc.mkdirAll(path.Dir(p)); err != nil {
			return err
		}
		return rc.writeFile(p, data)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return writeFileAtomic(p, data)
}

// envWriteFileWithGrant 先核销授权凭证再写文件（模型写文件的唯一入口）。
func (t *toolContext) envWriteFileWithGrant(p string, data []byte, g *Grant) error {
	if err := t.app.consumeGrant(g); err != nil {
		return err
	}
	return t.envWriteFile(p, data)
}

// envDeleteFile 删除文件（文件快照回退里"撤回新建文件"用；本地与远程一致）。
func (t *toolContext) envDeleteFile(p string) error {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return err
		}
		return rc.remove(p)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// envMkdirAll 递归建目录（本地与远程一致），.jxai 目录体系用。
func (t *toolContext) envMkdirAll(p string) error {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return err
		}
		return rc.mkdirAll(p)
	}
	return os.MkdirAll(p, 0755)
}

func (t *toolContext) envListDir(p string) ([]envEntry, error) {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return nil, err
		}
		return rc.listDir(p)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := []envEntry{}
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			out = append(out, envEntry{Name: e.Name(), IsDir: e.IsDir()})
			continue
		}
		out = append(out, envEntry{Name: e.Name(), IsDir: e.IsDir(), Size: info.Size()})
	}
	return out, nil
}

func (t *toolContext) envStat(p string) (isDir bool, size int64, err error) {
	if t.isRemote() {
		rc, rerr := t.app.getRemoteClient(t.sess)
		if rerr != nil {
			return false, 0, rerr
		}
		return rc.stat(p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return false, 0, err
	}
	return info.IsDir(), info.Size(), nil
}

// envWalk 遍历目录树（跳过 .git、.jxai 与 node_modules），fn 收到相对 root 的
// 斜杠路径；fn 返回 filepath.SkipAll 时整体停止。远程经 SFTP 递归。
// .jxai 是本助手的系统目录（快照正文 + 模型临时文件），不进目录检索；
// 按路径直接 read 访问不受影响。
func (t *toolContext) envWalk(root string, fn func(rel string, e envEntry) error) error {
	if t.isRemote() {
		rc, err := t.app.getRemoteClient(t.sess)
		if err != nil {
			return err
		}
		visited := 0
		var rec func(dir, rel string) error
		rec = func(dir, rel string) error {
			if visited > 4000 {
				return filepath.SkipAll
			}
			entries, err := rc.listDir(dir)
			if err != nil {
				return nil // 单个目录读不了就跳过，不中断整个遍历
			}
			for _, e := range entries {
				visited++
				r := e.Name
				if rel != "" {
					r = rel + "/" + e.Name
				}
				if e.IsDir {
					if e.Name == ".git" || e.Name == "node_modules" || e.Name == jxAIName {
						continue
					}
					if err := rec(dir+"/"+e.Name, r); err != nil {
						return err
					}
					continue
				}
				if err := fn(r, e); err != nil {
					return err
				}
			}
			return nil
		}
		err = rec(root, "")
		if err == filepath.SkipAll {
			return nil
		}
		return err
	}
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == jxAIName || (name == "node_modules" && p != root) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		return fn(filepath.ToSlash(rel), envEntry{Name: info.Name(), Size: info.Size()})
	})
}

// ---------- 文件类工具 ----------

func readFileTool() Tool {
	return Tool{
		Spec: toolSpec("read", "读取文本文件内容并带行号返回。用本工具——而不是 cat 等命令行——查看文本文件；大文件用 offset/limit 分段继续读取。path 支持相对工作区的路径。",
			objSchema(map[string]any{
				"path":   strProp("文件路径（相对或绝对）"),
				"offset": intProp("起始行号，从 1 开始，可选"),
				"limit":  intProp("最多读取行数，可选，默认 2000"),
			}, "path")),
		Run: runReadFile,
	}
}

func runReadFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "read", Path: path}); err != nil {
		return "", err
	}
	data, err := t.envReadFile(path)
	if err != nil {
		return "", err
	}
	t.markObserved(path) // 观察策略：read 过的文件之后才能被 write 覆盖
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s 是二进制文件，无法按文本读取", path)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start := p.Offset
	if start < 1 {
		start = 1
	}
	if start > len(lines) {
		return "（起始行超出文件范围）", nil
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 2000
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	out := strings.Join(lines[start-1:end], "\n")
	if len(out) > 256*1024 {
		out = out[:256*1024] + "\n…（内容过长已截断）"
	}
	suffix := ""
	if end < len(lines) {
		suffix = fmt.Sprintf("\n…（共 %d 行，可调大 limit 或用 offset 继续读取）", len(lines))
	}
	return out + suffix, nil
}

func writeFileTool() Tool {
	return Tool{
		Spec: toolSpec("write", "创建文件或整体替换文件内容（UTF-8 文本）。既有文件本会话 read 过才能覆盖（观察策略）；局部修改优先用 edit。按会话权限模式的文件沙箱执行：工作区内读写档只允许写工作区与系统临时目录内，被拒绝时可带 sandbox_permissions=full 与 justification 申请把会话提升为完全访问（弹审批）。",
			objSchema(map[string]any{
				"path":                strProp("目标文件路径（相对或绝对）"),
				"content":             strProp("要写入的完整内容"),
				"sandbox_permissions": strProp("仅在被拒绝后的重试中使用：full，申请把会话提升为完全访问以写入工作区之外（弹审批）"),
				"justification":       strProp("与 sandbox_permissions 搭配：一句话向用户说明理由"),
			}, "path", "content")),
		Run: runWriteFile,
	}
}

func runWriteFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path               string `json:"path"`
		Content            string `json:"content"`
		SandboxPermissions string `json:"sandbox_permissions"`
		Justification      string `json:"justification"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	// 观察策略（对齐 DSH fs-observation-policy 的写意图）：既有文件本会话
	// read 过才能整体覆盖——防基于臆测/过期记忆的盲覆盖；本会话刚创建或
	// 刚 write/edit 过的文件视为已观察。新建文件不受限。
	if _, _, serr := t.envStat(path); serr == nil && !t.isObserved(path) {
		return "", fmt.Errorf("%s 已存在但本会话尚未读取过（观察策略）：先用 read 读取该文件再重试覆盖，或改用 edit 做局部修改", filepath.ToSlash(path))
	}
	grant, err := t.authorize(permAction{Op: "write", Tool: "write", Path: path,
		EscalateTo: p.SandboxPermissions, Justification: p.Justification})
	if err != nil {
		return "", err
	}
	// 带快照写入：改动前内容自动落 checkpoint，编辑历史消息时可回退
	if err := t.snapshotWrite(path, grant, []byte(p.Content)); err != nil {
		return "", err
	}
	t.markObserved(path)
	return fmt.Sprintf("已写入 %s（%d 字节）", filepath.ToSlash(path), len(p.Content)), nil
}

func editFileTool() Tool {
	return Tool{
		Spec: toolSpec("edit", "对既有文本文件做局部修改：把 old_string 字面量替换为 new_string。默认要求 old_string 在文件中恰好出现一次（多次出现时给出更长的片段或设 replace_all）。被拒绝时可带 sandbox_permissions=full 与 justification 申请把会话提升为完全访问（弹审批）。",
			objSchema(map[string]any{
				"path":                strProp("文件路径"),
				"old_string":          strProp("要被替换的原文（需保证唯一）"),
				"new_string":          strProp("替换后的新文本"),
				"replace_all":         boolProp("为 true 时替换全部出现，可选"),
				"sandbox_permissions": strProp("仅在被拒绝后的重试中使用：full，申请把会话提升为完全访问以写入工作区之外（弹审批）"),
				"justification":       strProp("与 sandbox_permissions 搭配：一句话向用户说明理由"),
			}, "path", "old_string", "new_string")),
		Run: runEditFile,
	}
}

func runEditFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path               string `json:"path"`
		OldString          string `json:"old_string"`
		NewString          string `json:"new_string"`
		ReplaceAll         bool   `json:"replace_all"`
		SandboxPermissions string `json:"sandbox_permissions"`
		Justification      string `json:"justification"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.OldString == "" {
		return "", fmt.Errorf("old_string 不能为空")
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "edit", Path: path}); err != nil {
		return "", err
	}
	data, err := t.envReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, p.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string 在文件中不存在")
	}
	if count > 1 && !p.ReplaceAll {
		return "", fmt.Errorf("old_string 出现了 %d 次，请提供更长的片段保证唯一，或设置 replace_all", count)
	}
	grant, err := t.authorize(permAction{Op: "write", Tool: "edit", Path: path,
		EscalateTo: p.SandboxPermissions, Justification: p.Justification})
	if err != nil {
		return "", err
	}
	var updated string
	replaced := 1
	if p.ReplaceAll {
		updated = strings.ReplaceAll(content, p.OldString, p.NewString)
		replaced = count
	} else {
		updated = strings.Replace(content, p.OldString, p.NewString, 1)
	}
	// 带快照写入：改动前内容自动落 checkpoint（上面 envReadFile 读到的即旧内容）
	if err := t.snapshotWrite(path, grant, []byte(updated)); err != nil {
		return "", err
	}
	// edit 的唯一匹配本身就是更强的 CAS（old_string 必须与当前内容精确
	// 匹配，盲改不可能通过），故不设 read 前置；落盘后记为已观察，后续
	// write 覆盖随之放行（对齐 DSH「刚编辑过视为已观察」）。
	t.markObserved(path)
	return fmt.Sprintf("已编辑 %s（替换 %d 处）", filepath.ToSlash(path), replaced), nil
}

// ---------- 文件快照与交付登记（不可追踪修改的配套工具） ----------

func arrayProp(desc string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items}
}

func snapshotFileTool() Tool {
	return Tool{
		Spec: toolSpec("snapshot_file",
			"为工作区文件打一条历史快照（记录当前内容），供编辑历史消息时回退文件。write/edit 的修改会自动记录快照，无需本工具；凡是准备通过 run_command、外部程序、Office 文档运行库等无法自动追踪的方式修改一个已存在的重要文件，必须先调用本工具，否则该文件将无法回退。",
			objSchema(map[string]any{
				"path": strProp("目标文件路径（相对或绝对）"),
			}, "path")),
		Run: runSnapshotFile,
	}
}

func runSnapshotFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if !t.app.snapshotsEnabled() {
		return "文件备份已在设置中关闭：本次未记录快照。若任务需要可回退，请提示用户在设置-常规里打开「文件备份」。", nil
	}
	abs, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "snapshot_file", Path: abs}); err != nil {
		return "", err
	}
	var before []byte
	existed := true
	isDir, size, serr := t.envStat(abs)
	switch {
	case serr != nil && os.IsNotExist(serr):
		existed = false
	case serr != nil:
		return "", fmt.Errorf("无法读取文件状态: %w", serr)
	case isDir:
		return "", fmt.Errorf("目标是一个目录，快照只支持文件")
	case size > snapshotManualMaxFileBytes:
		return "", fmt.Errorf("文件超过手动快照单文件上限（%d MB）", snapshotManualMaxFileBytes>>20)
	default:
		if before, err = t.envReadFile(abs); err != nil {
			return "", err
		}
	}
	t.markObserved(abs) // 手动快照读过内容，观察策略随之放行后续覆盖
	cp := &Checkpoint{
		SessionID: t.sess.ID, Seq: t.seq, ToolCallID: t.callID, ToolName: t.toolName,
		Path: t.displayPath(abs), ExistedBefore: existed, BeforeSize: int64(len(before)),
		AfterHash: hashBytes(before), AfterSize: size, CreatedAt: now(),
	}
	id, err := t.app.Store.AppendCheckpoint(cp)
	if err != nil {
		return "", fmt.Errorf("快照落库失败: %w", err)
	}
	if existed {
		if err := t.writeSnapshotBody(id, before); err != nil {
			_ = t.app.Store.DeleteCheckpoints(t.sess.ID, []int64{id})
			return "", fmt.Errorf("快照正文写入 .jxai 失败: %w", err)
		}
	}
	if existed {
		return fmt.Sprintf("已记录 %s 的快照（%d 字节，存于 .jxai/snapshots）。之后可以用不可追踪的方式修改它；用户编辑历史消息时可回退到此刻的内容。", cp.Path, len(before)), nil
	}
	return fmt.Sprintf("%s 当前不存在，已记录\"新增文件\"状态：之后创建它，用户编辑历史消息时会把它作为本轮新增撤回。", cp.Path), nil
}

func deliverResultTool() Tool {
	return Tool{
		Spec: toolSpec("deliver_result",
			"登记本轮的最终交付文件：用户界面会展示交付卡片并提供下载，这是用户拿到成果的正式入口。产出成果文件（尤其是 docx/xlsx/pptx 等经程序生成的文档）后必须调用；只在回复里贴路径不算交付。",
			objSchema(map[string]any{
				"files": arrayProp("交付文件清单", map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": strProp("文件路径（相对或绝对）"),
						"note": strProp("一句话说明这个文件是什么（可选）"),
					},
					"required": []string{"path"},
				}),
				"summary": strProp("本次交付的整体说明（可选）"),
			}, "files")),
		Run: runDeliverResult,
	}
}

func runDeliverResult(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Files []struct {
			Path string `json:"path"`
			Note string `json:"note"`
		} `json:"files"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if len(p.Files) == 0 {
		return "", fmt.Errorf("files 不能为空")
	}
	registered := []string{}
	missing := []string{}
	for _, f := range p.Files {
		abs, err := t.resolve(f.Path)
		if err != nil {
			return "", err
		}
		if _, err := t.authorize(permAction{Op: "read", Tool: "deliver_result", Path: abs}); err != nil {
			return "", err
		}
		if isDir, _, serr := t.envStat(abs); serr != nil || isDir {
			missing = append(missing, f.Path)
			continue
		}
		d := &Delivery{
			SessionID: t.sess.ID, Seq: t.seq, Path: t.displayPath(abs),
			Note: strings.TrimSpace(f.Note), CreatedAt: now(),
		}
		if p.Summary != "" {
			d.Note = strings.TrimSpace(d.Note + "；" + p.Summary)
		}
		if err := t.app.Store.AddDelivery(d); err != nil {
			return "", fmt.Errorf("交付登记落库失败: %w", err)
		}
		registered = append(registered, d.Path)
	}
	if len(registered) == 0 {
		return "", fmt.Errorf("以下文件不存在，未登记任何交付：%s", strings.Join(missing, "、"))
	}
	out := fmt.Sprintf("已登记 %d 个交付文件，用户界面已展示并提供下载：%s", len(registered), strings.Join(registered, "、"))
	if len(missing) > 0 {
		out += "。以下文件不存在已跳过：" + strings.Join(missing, "、")
	}
	return out, nil
}

func listDirTool() Tool {
	return Tool{
		Spec: toolSpec("list_dir", "列出目录内容。", objSchema(map[string]any{
			"path": strProp("目录路径，默认为工作区根目录"),
		})),
		Run: runListDir,
	}
}

func runListDir(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", err
		}
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "info", Tool: "list_dir", Path: path}); err != nil {
		return "", err
	}
	entries, err := t.envListDir(path)
	if err != nil {
		return "", err
	}
	lines := []string{}
	for _, e := range entries {
		if e.IsDir {
			lines = append(lines, e.Name+"/")
			continue
		}
		lines = append(lines, fmt.Sprintf("%s\t%d B", e.Name, e.Size))
	}
	if len(lines) == 0 {
		return "（空目录）", nil
	}
	return strings.Join(lines, "\n"), nil
}

func globTool() Tool {
	return Tool{
		Spec: toolSpec("glob", "按通配符模式查找文件，如 *.go 或 docs/*.md。返回相对 root 的路径。",
			objSchema(map[string]any{
				"pattern": strProp("通配符模式，如 *.go"),
				"path":    strProp("起始目录，默认工作区根目录"),
			}, "pattern")),
		Run: runGlob,
	}
}

func runGlob(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return "", fmt.Errorf("pattern 不能为空")
	}
	root, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if isDir, _, serr := t.envStat(root); serr != nil || !isDir {
		return "", fmt.Errorf("目录不存在: %s", root)
	}
	if _, err := t.authorize(permAction{Op: "info", Tool: "glob", Path: root}); err != nil {
		return "", err
	}
	matches := []string{}
	_ = t.envWalk(root, func(rel string, e envEntry) error {
		if len(matches) >= 500 {
			return filepath.SkipAll
		}
		ok := false
		if strings.Contains(p.Pattern, "/") || strings.Contains(p.Pattern, "\\") {
			ok, _ = path.Match(filepath.ToSlash(p.Pattern), rel)
		} else {
			ok, _ = path.Match(p.Pattern, e.Name)
		}
		if ok {
			matches = append(matches, rel)
		}
		return nil
	})
	if len(matches) == 0 {
		return "（无匹配文件）", nil
	}
	sort.Strings(matches)
	return strings.Join(matches, "\n"), nil
}

func grepTool() Tool {
	return Tool{
		Spec: toolSpec("grep", "在目录内的文本文件中搜索内容，pattern 为正则表达式（非法时按字面量匹配）。返回 文件:行号: 内容。",
			objSchema(map[string]any{
				"pattern": strProp("搜索模式（正则表达式）"),
				"path":    strProp("起始目录，默认工作区根目录"),
			}, "pattern")),
		Run: runGrep,
	}
}

func runGrep(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return "", fmt.Errorf("pattern 不能为空")
	}
	re, literalErr := regexp.Compile(p.Pattern)
	literal := literalErr != nil
	root, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "info", Tool: "grep", Path: root}); err != nil {
		return "", err
	}
	found := []string{}
	_ = t.envWalk(root, func(rel string, e envEntry) error {
		if len(found) >= 200 {
			return filepath.SkipAll
		}
		if e.Size > 2*1024*1024 {
			return nil
		}
		full := root
		if !t.isRemote() {
			full = filepath.Join(root, filepath.FromSlash(rel))
		} else {
			full = path.Join(root, rel)
		}
		data, rerr := t.envReadFile(full)
		if rerr != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, ln := range strings.Split(string(data), "\n") {
			hit := literal && strings.Contains(ln, p.Pattern)
			if !literal && re != nil {
				hit = re.MatchString(ln)
			}
			if hit {
				found = append(found, fmt.Sprintf("%s:%d: %s",
					filepath.ToSlash(full), i+1, truncateRunes(strings.TrimSpace(ln), 200)))
				if len(found) >= 200 {
					break
				}
			}
		}
		return nil
	})
	if len(found) == 0 {
		return "（无匹配）", nil
	}
	return strings.Join(found, "\n"), nil
}

// ---------- shell 工具（本地 Windows：powershell / cmd；远程：POSIX shell） ----------

func runCommandTool() Tool {
	return Tool{
		Spec: toolSpec("run_command", "执行一条 shell 命令并返回输出。命令按会话权限模式在 OS 级沙箱内执行：workspace-write 下只能写工作区与私有临时目录（Windows 受限令牌 / Linux Landlock / 远程沙箱 helper），read-only 下不能写任何位置，联网不受限。尝试沙箱可能拒绝的命令是安全且符合预期的：直接运行并读取标记。若命令因沙箱拒绝失败且更宽档位能让它成功，在同一轮内带 sandbox_permissions（够用的最宽档）与一句 justification 重试同一命令一次——该重试会弹审批由用户批准；未经真实拒绝不要请求升级；被拒后该命令终局，停止并说明，不要绕过。",
			objSchema(map[string]any{
				"command":             strProp("要执行的命令"),
				"shell":               strProp("本地执行时的 shell：powershell（默认）或 cmd；远程执行忽略"),
				"cwd":                 strProp("工作目录（可选，默认会话工作区；是否可写由沙箱决定，cwd 本身不受限）"),
				"timeout_sec":         intProp("超时秒数，默认 60，上限 600；后台作业忽略"),
				"run_in_background":   boolProp("true 时后台执行并立即返回作业 id（仅本地）"),
				"sandbox_permissions": strProp("仅用于被沙箱拒绝后的同轮重试：请求的更宽档位（workspace-write 或 danger-full-access），必须搭配 justification；会弹审批由用户决定"),
				"justification":       strProp("与 sandbox_permissions 搭配：一句话向用户解释为什么这条命令需要更宽权限"),
			}, "command")),
		Run: runCommand,
	}
}

func runCommand(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Command            string `json:"command"`
		Shell              string `json:"shell"`
		Cwd                string `json:"cwd"`
		TimeoutSec         int    `json:"timeout_sec"`
		RunInBackground    bool   `json:"run_in_background"`
		SandboxPermissions string `json:"sandbox_permissions"`
		Justification      string `json:"justification"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Command) == "" {
		return "", fmt.Errorf("command 不能为空")
	}
	cwd := p.Cwd
	if r, err := t.resolve(cwd); err == nil {
		cwd = r // 统一解析为绝对路径（空 = 会话工作区；远程为 POSIX 绝对路径）
	}
	// 拒绝驱动的升级在这里发生：EscalateTo 非空时弹审批，批准后按更宽档执行这一次
	grant, err := t.authorize(permAction{
		Op: "exec", Tool: "run_command", Command: p.Command, Cwd: cwd,
		EscalateTo: p.SandboxPermissions, Justification: p.Justification,
	})
	if err != nil {
		return "", err
	}
	if err := t.app.consumeGrant(grant); err != nil {
		return "", err
	}
	policy := grant.Policy

	// 后台作业：立即返回，不限时；与前台同一条策略与沙箱路径。
	if p.RunInBackground {
		if t.isRemote() {
			return "", fmt.Errorf("后台作业暂不支持远程工作区会话；请去掉 run_in_background 直接执行")
		}
		j, jerr := t.app.startJob(t, p.Command, p.Shell, cwd, policy)
		if jerr != nil {
			return "", jerr
		}
		return fmt.Sprintf("后台作业已启动：%s（沙箱 %s，不限时）。\n- job_output 查看输出（wait=true 可等它结束）\n- job_list 列出全部作业\n- job_kill 终止\n现在可以继续做别的事。", j.ID, policy), nil
	}

	timeout := time.Duration(60) * time.Second
	if p.TimeoutSec > 0 {
		timeout = time.Duration(min(p.TimeoutSec, 600)) * time.Second
	}

	run := sandboxRun{
		Dir: cwd, Command: p.Command, Shell: strings.ToLower(strings.TrimSpace(p.Shell)),
		Timeout: timeout, Policy: policy, Workspace: t.workspace(),
	}

	var out string
	switch policy {
	case permFull:
		// danger-full-access：不受限（官方语义：消费者绕过 confine）
		if t.isRemote() {
			rc, rerr := t.app.getRemoteClient(t.sess)
			if rerr != nil {
				return "", rerr
			}
			out, err = rc.run(t.ctx, cwd, p.Command, timeout)
			if err == nil {
				out += "\n[远程执行于 " + t.sess.Remote.Display() + "｜不受限]"
			}
		} else {
			out, err = runUnconfinedLocal(t.ctx, run)
		}
	default:
		out, err = t.runConfined(run)
		if err == nil {
			out += runDeniedMarker(policy, out)
		}
	}
	if err != nil {
		// 沙箱不可用也是一次「拒绝」：提示模型可走升级审批路径
		if strings.Contains(err.Error(), "sandbox unavailable") {
			return "", fmt.Errorf("%w。可带 sandbox_permissions=danger-full-access 与 justification 申请升级（弹审批由用户决定）", err)
		}
		return "", err
	}
	return out, nil
}

// runUnconfinedLocal 不受限本地执行（full 策略）。
func runUnconfinedLocal(ctx context.Context, r sandboxRun) (string, error) {
	if info, serr := os.Stat(r.Dir); serr != nil || !info.IsDir() {
		return "", fmt.Errorf("工作目录不存在: %s", r.Dir)
	}
	shell := strings.TrimSpace(r.Shell)
	if shell == "" {
		shell = "powershell"
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	var cmd *exec.Cmd
	switch strings.ToLower(shell) {
	case "cmd":
		cmd = exec.CommandContext(ctx, "cmd", "/C", r.Command)
	case "pwsh", "powershell":
		exe := "powershell"
		if strings.ToLower(shell) == "pwsh" {
			exe = "pwsh"
		}
		cmd = exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", r.Command)
	default:
		return "", fmt.Errorf("不支持的 shell: %s（可选 powershell / cmd）", r.Shell)
	}
	cmd.Dir = r.Dir
	cmd.Env = sandboxEnv(true)
	out, err := cmd.CombinedOutput()
	note := ""
	if ctx.Err() == context.DeadlineExceeded {
		note += "\n[命令超时被终止]"
	}
	if len(out) > 64*1024 {
		out = out[:64*1024]
		note += "\n[输出过长已截断]"
	}
	res := strings.TrimRight(decodeCmdOutput(out), "\r\n") + note
	if err != nil && ctx.Err() == nil {
		res += fmt.Sprintf("\n[进程退出异常: %v]", err)
	}
	if strings.TrimSpace(res) == "" {
		res = "（命令执行完成，无输出）"
	}
	return res, nil
}

// sandboxEnvWhitelist 是沙箱内执行保留的环境变量白名单：只留运行常规
// 工具链必需的变量，其余（含宿主环境里可能的密钥类变量）全部剔除。
var sandboxEnvWhitelist = []string{
	"PATH", "PATHEXT", "SystemRoot", "windir", "COMSPEC", "TEMP", "TMP",
	"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
	"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "PROGRAMDATA",
	"ALLUSERSPROFILE", "OS", "COMPUTERNAME", "NUMBER_OF_PROCESSORS",
	"PROCESSOR_ARCHITECTURE", "USERNAME", "LANG", "LC_ALL", "TERM", "HOME",
}

// sandboxEnv 返回执行环境变量：沙箱内取白名单子集，沙箱外/完全访问取全量。
func sandboxEnv(full bool) []string {
	if full {
		return os.Environ()
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		for _, w := range sandboxEnvWhitelist {
			if strings.EqualFold(kv[:i], w) {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

// ---------- 联网工具 ----------

func webFetchTool() Tool {
	return Tool{
		Spec: toolSpec("web_fetch", "抓取一个 http(s) 地址并返回正文文本；HTML 自动剥离标签。用于查阅文档、API 说明、教程等。内网/本机地址在非「完全访问」模式下会被拒绝。",
			objSchema(map[string]any{
				"url":       strProp("要抓取的 http(s) 地址"),
				"max_chars": intProp("最多返回的字符数，默认 20000，上限 100000"),
			}, "url")),
		Run: runWebFetch,
	}
}

func runWebFetch(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	u := strings.TrimSpace(p.URL)
	if u == "" {
		return "", fmt.Errorf("url 不能为空")
	}
	if _, err := t.authorize(permAction{Op: "net", Tool: "web_fetch"}); err != nil {
		return "", err
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("仅支持 http/https 地址")
	}
	// 防 SSRF：非「完全访问」模式下拒绝内网 / 本机 / 链路本地目标
	if permModeOf(t.sess) != permFull && isPrivateHost(parsed.Hostname()) {
		return "", fmt.Errorf("权限拒绝：%s 是内网/本机地址，非「完全访问」模式下不允许抓取", parsed.Hostname())
	}
	maxChars := p.MaxChars
	if maxChars <= 0 {
		maxChars = 20000
	}
	if maxChars > 100000 {
		maxChars = 100000
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; JXAssistant/0.1)")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncateRunes(string(body), 300))
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	text := string(body)
	if strings.Contains(ct, "html") || looksLikeHTML(text) {
		text = stripHTML(text)
	} else if !strings.Contains(ct, "text") && !strings.Contains(ct, "json") &&
		!strings.Contains(ct, "xml") && !strings.Contains(ct, "javascript") && ct != "" {
		return "", fmt.Errorf("不支持的响应类型：%s（仅支持文本类内容）", ct)
	}
	r := []rune(strings.TrimSpace(text))
	if len(r) > maxChars {
		r = r[:maxChars]
	}
	return fmt.Sprintf("HTTP %d｜%s\n\n%s", resp.StatusCode, u, string(r)), nil
}

// looksLikeHTML 粗判正文是不是 HTML（Content-Type 缺失时的兜底）。
func looksLikeHTML(s string) bool {
	head := s
	if len(head) > 2048 {
		head = head[:2048]
	}
	l := strings.ToLower(head)
	return strings.HasPrefix(strings.TrimSpace(l), "<!doctype html") ||
		strings.Contains(l, "<html") || (strings.Contains(l, "<body") && strings.Contains(l, "<"))
}

var (
	htmlBlockRe   = regexp.MustCompile(`(?is)<(script|style|noscript)\b[^>]*>.*?</(script|style|noscript)\s*>`)
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	htmlSpaceRe   = regexp.MustCompile(`\n{3,}`)
)

// stripHTML 剥离 HTML 标签并解码常见实体，尽量保留可读的正文文本。
func stripHTML(s string) string {
	s = htmlBlockRe.ReplaceAllString(s, "")
	s = htmlCommentRe.ReplaceAllString(s, "")
	s = htmlTagRe.ReplaceAllString(s, "\n")
	replacer := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&#x27;", "'", "&nbsp;", " ", "&mdash;", "—", "&hellip;", "…",
	)
	s = replacer.Replace(s)
	lines := []string{}
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimRight(ln, " \t\r")
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	out := strings.Join(lines, "\n")
	out = htmlSpaceRe.ReplaceAllString(out, "\n\n")
	return out
}

func isPrivateHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false // 域名交由 DNS；仅拦截字面内网地址
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------- read_image：图片查看 ----------

// read_image：读取工作区里的图片文件，入库为附件并返回文本信封。
// 前端在工具卡片里直接渲染原图；支持视觉的模型在请求组装时由
// inlineImagesForRequest 自动内联看到图片内容（见 attachment.go）。
// DSH 的 read_image 与 ZCode 的 Read 工具都是这个定位，本实现为本栈新写。

func readImageTool() Tool {
	return Tool{
		Spec: toolSpec("read_image",
			"查看一张图片（png / jpg / gif / webp）。图片会展示给用户；若当前模型支持视觉，你将直接看到图片内容。path 支持相对工作区的路径。",
			objSchema(map[string]any{
				"path": strProp("图片文件路径（相对或绝对）"),
			}, "path")),
		Run: runReadImage,
	}
}

func runReadImage(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	// 只读动作：经权限引擎统一做受保护路径检查后放行。
	if _, err := t.authorize(permAction{Op: "read", Tool: "read_image", Path: path}); err != nil {
		return "", err
	}
	raw, err := t.envReadFile(path)
	if err != nil {
		return "", err
	}
	mediaType := sniffImageMediaType(raw)
	if mediaType == "" {
		return "", fmt.Errorf("%s 不是受支持的图片格式（支持 png / jpg / gif / webp）", path)
	}
	att, err := t.app.storeAttachment(raw, mediaType)
	if err != nil {
		return "", err
	}
	note := "图片已就绪：用户可在界面上直接查看"
	if t.app.resolveTarget(t.sess).SupportsImages {
		note = "图片已就绪：你将在下一条消息中直接看到它的内容"
	} else {
		note = "图片已就绪：用户可在界面上直接查看（当前模型不支持视觉输入，你只能看到这份元信息；可结合 read / grep 读取同目录文本，或请用户描述图片内容）"
	}
	return fmt.Sprintf("%s\n%s\n来源文件：%s\n格式：%s，尺寸 %dx%d，%d 字节\n如需查看图片里的文字，可配合 OCR 能力（若已安装）处理。",
		imageEnvelope(att, path), note, path, att.MediaType, att.Width, att.Height, att.Bytes), nil
}

// ---------- progress_write：会话进度清单 ----------

// progress_write（对齐官方标准栈的 todo 类工具；曾名 todo_write，为与定时
// 任务系统 todo.go / todotools.go 的 todo_task_* 工具划清界限而更名）：模型
// 维护当前任务的会话进度清单，前端在会话顶部渲染进度。清单整体替换写入
// （每次都提交全量列表），持久化在 meta 表（会话附属数据），经
// /api/sessions/{id}/progress 读取。

// ProgressItem 是清单中的一项。
type ProgressItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

func progressWriteTool() Tool {
	return Tool{
		Spec: toolSpec("progress_write", "维护当前任务的会话进度清单（整体替换：每次提交完整列表）。用于多步骤任务：开始前把工作拆成进度项；开工一项置 in_progress（同时只能有一项）；做完置 completed 再开下一项。简单到一两步的事不必用。",
			objSchema(map[string]any{
				"progress": objSchema(map[string]any{
					"content": strProp("进度项内容，一句话"),
					"status":  strProp("pending | in_progress | completed"),
				}),
			}, "progress")),
		Run: runProgressWrite,
	}
}

func runProgressWrite(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Progress []ProgressItem `json:"progress"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if len(p.Progress) == 0 {
		// 空清单 = 清空（任务取消/不再需要时可用）
		_ = t.app.Store.SetSessionData(t.sess.ID, "progress", "")
		t.app.broadcast(Event{Type: "progress", SessionID: t.sess.ID})
		return "进度清单已清空。", nil
	}
	inProgress := 0
	for i := range p.Progress {
		td := &p.Progress[i]
		td.Content = strings.TrimSpace(td.Content)
		if td.Content == "" {
			return "", fmt.Errorf("第 %d 项 content 为空", i+1)
		}
		switch td.Status {
		case "pending", "":
			td.Status = "pending"
		case "in_progress":
			inProgress++
		case "completed":
		default:
			return "", fmt.Errorf("第 %d 项状态非法：%s（可选 pending / in_progress / completed）", i+1, td.Status)
		}
		if len(td.Content) > 500 {
			td.Content = truncateRunes(td.Content, 500)
		}
	}
	if inProgress > 1 {
		return "", fmt.Errorf("同时只能有一项 in_progress（当前 %d 项），请把其余改为 pending", inProgress)
	}
	data, err := json.Marshal(p.Progress)
	if err != nil {
		return "", err
	}
	if err := t.app.Store.SetSessionData(t.sess.ID, "progress", string(data)); err != nil {
		return "", err
	}
	t.app.broadcast(Event{Type: "progress", SessionID: t.sess.ID})
	done := 0
	for _, td := range p.Progress {
		if td.Status == "completed" {
			done++
		}
	}
	return fmt.Sprintf("进度清单已更新（%d 项，完成 %d）。", len(p.Progress), done), nil
}

// progressListOf 读取会话的进度清单（前端 API 用）；空返回 nil。
func progressListOf(a *App, sessionID string) []ProgressItem {
	raw := a.Store.GetSessionData(sessionID, "progress")
	if raw == "" {
		return nil
	}
	out := []ProgressItem{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}
