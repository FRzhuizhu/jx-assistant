package main

// 权限与授权引擎：移植 DSH（DeepSeek 官方 Agent）权限管理的核心理念，
// 按本栈重写。模型如下：
//
//  1. 三档权限模式（会话粒度，权限芯片可选）：只读 / 工作区内读写 / 完全访问。
//  2. 沙箱内执行：命令默认在会话工作区内执行——工作目录锁定、环境变量
//     最小化（剔除一切非白名单变量）、限时、输出截断；沙箱内允许联网。
//  3. 授权凭证（Grant）：每一次敏感操作（写文件 / 执行命令）都必须先由
//     权限引擎评估并签发一张短时效、一次性的凭证，执行器核销凭证后才
//     真正动手；凭证与具体动作绑定，过期或已核销即失效（fail closed）。
//  4. 能力开关 + 逐次审批：工作区外执行命令需要先在权限面板开启
//     「允许工作区外执行命令」能力；开启后每次越界执行仍要经用户批准
//     （前端弹审批卡，可选"本会话总是允许"），未开启能力则直接拒绝。
//     完全访问模式不弹审批。
//  5. 受保护路径：助手自身的配置与数据目录（内含 API Key 与全部会话
//     记录）在任何模式下都禁止模型读写——防自我提权与密钥泄漏。
//  6. 危险命令硬拦截：不可逆的破坏性命令（格式化盘 / 删根目录 / 写引导
//     等）在任何模式下都拒绝。
//
// 远程工作区（SSH）会话走同一套引擎：工作区边界换为远程目录，
// 路径按 POSIX 规则判断。

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	permReadOnly   = "read-only"       // 只读
	permWorkspace  = "workspace-write" // 工作区内读写（默认）
	permFull       = "full"            // 完全访问
	capUnsandboxed = "unsandboxed"     // 能力：允许工作区外执行命令（逐次审批）
	capBrowser     = "browser"         // 能力：允许操作浏览器（browser_* 工具）
	capComputer    = "computer"        // 能力：允许操作电脑（computer_* 工具）
	grantTTL       = 5 * time.Minute   // 授权凭证有效期
	approvalTTL    = 2 * time.Minute   // 审批等待上限，超时视为拒绝
)

// permModeOf 返回会话的权限模式（未设置时默认工作区内读写）。
func permModeOf(s *SessionMeta) string {
	switch s.Permission {
	case permReadOnly, permFull, permWorkspace:
		return s.Permission
	default:
		return permWorkspace
	}
}

func permModeLabel(mode string) string {
	switch mode {
	case permReadOnly:
		return "只读"
	case permFull:
		return "完全访问"
	default:
		return "工作区内读写"
	}
}

// hasCapability 判断会话是否开启了某个能力开关（权限面板右侧多选）。
func hasCapability(s *SessionMeta, cap string) bool {
	for _, c := range s.Tools {
		if c == cap {
			return true
		}
	}
	return false
}

// ---------- 授权凭证 ----------

// Grant 是一次敏感操作的执行凭证：短时效、一次性、与动作绑定。
// 策略评估通过（或用户审批通过）才签发；执行器核销后才执行。
type Grant struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Tool      string `json:"tool"`
	Op        string `json:"op"`                // write | exec
	Summary   string `json:"summary"`           // 人类可读的动作摘要
	Escaped   bool   `json:"escaped,omitempty"` // 沙箱外（工作区外执行）
	FullEnv   bool   `json:"fullEnv,omitempty"` // 执行命令时携带完整环境变量
	issuedAt  time.Time
	expiresAt time.Time
	used      bool
}

// issueGrant 签发一张授权凭证（不做事前评估——调用方必须已经过 authorize）。
func (a *App) issueGrant(sessionID, tool, op, summary string, escaped, fullEnv bool) *Grant {
	g := &Grant{
		ID: "g-" + newID(), SessionID: sessionID, Tool: tool, Op: op, Summary: summary,
		Escaped: escaped, FullEnv: fullEnv,
		issuedAt: time.Now(), expiresAt: time.Now().Add(grantTTL),
	}
	a.permMu.Lock()
	a.grants[g.ID] = g
	a.permMu.Unlock()
	return g
}

// consumeGrant 核销凭证：必须未核销且未过期，否则拒绝执行（fail closed）。
func (a *App) consumeGrant(g *Grant) error {
	if g == nil {
		return fmt.Errorf("缺少授权凭证，拒绝执行")
	}
	a.permMu.Lock()
	defer a.permMu.Unlock()
	if g.used {
		return fmt.Errorf("授权凭证 %s 已核销（一次性），需重新申请", g.ID)
	}
	if time.Now().After(g.expiresAt) {
		return fmt.Errorf("授权凭证 %s 已过期，需重新申请", g.ID)
	}
	g.used = true
	return nil
}

// ---------- 审批（ask 路径） ----------

type approvalResult struct {
	approved bool
	always   bool
}

// Approval 是一次等待用户决定的越权请求。工具执行同步等待结果；
// 前端经全局事件流收到 approval_required 后弹审批卡，决定经
// POST /api/approvals/{id} 回传。
type Approval struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Tool      string `json:"tool"`
	Summary   string `json:"summary"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
	expiresAt time.Time
	decided   bool
	ch        chan approvalResult
}

// requestApproval 广播审批请求并阻塞等待用户决定（超时/取消视为拒绝）。
func (a *App) requestApproval(ctx context.Context, sess *SessionMeta, tool, summary, reason string) (approved, always bool) {
	ap := &Approval{
		ID: "ap-" + newID(), SessionID: sess.ID, Tool: tool,
		Summary: summary, Reason: reason, CreatedAt: now(),
		expiresAt: time.Now().Add(approvalTTL), ch: make(chan approvalResult, 1),
	}
	a.permMu.Lock()
	a.approvals[ap.ID] = ap
	a.permMu.Unlock()
	a.broadcast(Event{Type: "approval_required", ApprovalID: ap.ID, SessionID: sess.ID,
		Name: tool, Summary: summary, Reason: reason})
	defer func() {
		a.permMu.Lock()
		delete(a.approvals, ap.ID)
		a.permMu.Unlock()
		a.broadcast(Event{Type: "approval_resolved", ApprovalID: ap.ID, SessionID: sess.ID})
	}()
	timer := time.NewTimer(approvalTTL)
	defer timer.Stop()
	select {
	case r := <-ap.ch:
		return r.approved, r.always
	case <-timer.C:
		return false, false
	case <-ctx.Done():
		return false, false
	}
}

// resolveApproval 登记用户的决定并唤醒等待中的工具执行。
func (a *App) resolveApproval(id string, approved, always bool) error {
	a.permMu.Lock()
	ap, ok := a.approvals[id]
	if ok && !ap.decided {
		ap.decided = true
		ap.ch <- approvalResult{approved: approved, always: always}
	}
	a.permMu.Unlock()
	if !ok {
		return fmt.Errorf("审批请求不存在或已处理（可能已超时）")
	}
	return nil
}

// pendingApprovals 返回全部待决定审批（前端轮询兜底用）。
func (a *App) pendingApprovals() []*Approval {
	a.permMu.Lock()
	defer a.permMu.Unlock()
	out := []*Approval{}
	for _, ap := range a.approvals {
		out = append(out, ap)
	}
	return out
}

// ---------- 会话级"总是允许" ----------

// allowSignature 稳定签名：同一动作（同命令同目录 / 同路径）再次出现时匹配。
func allowSignature(op, summary string) string { return op + "|" + summary }

func isRemembered(sess *SessionMeta, op, summary string) bool {
	sig := allowSignature(op, summary)
	for _, s := range sess.Allowed {
		if s == sig {
			return true
		}
	}
	return false
}

// rememberAllowed 把"本会话总是允许"落库到会话元信息。
func (a *App) rememberAllowed(sess *SessionMeta, op, summary string) {
	sig := allowSignature(op, summary)
	for _, s := range sess.Allowed {
		if s == sig {
			return
		}
	}
	sess.Allowed = append(sess.Allowed, sig)
	_ = a.Store.SaveMeta(sess)
}

// ---------- 受保护路径与危险命令 ----------

// isProtectedPath 判断路径是否落在助手自身的配置 / 数据目录内。
// 这些位置存着 API Key 与全部会话记录，任何权限模式都禁读禁写。
func (a *App) isProtectedPath(p string, remote bool) bool {
	if remote || p == "" {
		return false
	}
	lower := strings.ToLower(filepath.Clean(p))
	for _, pp := range a.protectPaths {
		if lower == pp || strings.HasPrefix(lower, pp+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// destructiveCmds 危险命令硬拦截表：不可逆破坏在任何模式下都拒绝。
var destructiveCmds = []struct {
	re  *regexp.Regexp
	why string
}{
	{regexp.MustCompile(`(?i)\bformat\s+[a-z]:`), "格式化磁盘"},
	{regexp.MustCompile(`(?i)\b(shutdown|logoff|restart-computer)\b`), "关机/重启"},
	{regexp.MustCompile(`(?i)\bmkfs(\.\w+)?\b`), "格式化文件系统"},
	{regexp.MustCompile(`(?i)\bdd\s+[^|]*of=/dev/`), "直写块设备"},
	{regexp.MustCompile(`(?i)\brm\s+(-[a-z]*r[a-z]*f*|-rf)\s+(/|~|\$HOME)(\s|/|$|\*)`), "递归删除根目录或用户主目录"},
	{regexp.MustCompile(`(?i)\b(rd|rmdir)\s+.*[a-z]:\\+\s*$`), "递归删除盘根目录"},
	{regexp.MustCompile(`(?i)\bdel\s+(/f\s+|/s\s+|/q\s+)+.*[a-z]:\\?\s*$`), "递归强删盘根目录"},
	{regexp.MustCompile(`(?i)\breg\s+(delete|add)\s+.*hklm`), "修改系统注册表 HKLM"},
	{regexp.MustCompile(`(?i)\b(bcdedit|diskpart|vssadmin\s+delete|wbadmin\s+delete|cipher\s+/w)\b`), "系统级破坏工具"},
	{regexp.MustCompile(`:\(\)\s*\{`), "fork 炸弹"},
}

// destructiveCommandHit 命中危险命令时返回拦截原因，空串表示放行。
func destructiveCommandHit(cmd string) string {
	for _, d := range destructiveCmds {
		if d.re.MatchString(cmd) {
			return d.why
		}
	}
	return ""
}

// ---------- 工作区边界（本地 Windows / 远程 POSIX 双规则） ----------

// isRemote 定义在 tools.go（执行环境抽象）

// posixRel 是 POSIX 风格的相对路径计算（path 包没有 Rel，按其语义实现）。
func posixRel(base, target string) (string, error) {
	base = path.Clean("/" + base)
	target = path.Clean("/" + target)
	if base == target {
		return ".", nil
	}
	if !strings.HasPrefix(target, base+"/") {
		return "", fmt.Errorf("不能求相对路径：%s 相对 %s", target, base)
	}
	return target[len(base)+1:], nil
}

// inWorkspace 判断绝对路径 p 是否落在会话工作区内。
// 远程会话按 POSIX 路径；本地 Windows 按大小写不敏感比较
// （工作区可能是配置里的相对路径，两侧都先绝对化再比）。
func (t *toolContext) inWorkspace(p string) bool {
	ws := t.workspace()
	if t.isRemote() {
		rel, err := posixRel(ws, p)
		if err != nil {
			return false
		}
		return rel != ".." && !strings.HasPrefix(rel, "../")
	}
	d, err := filepath.Abs(ws)
	if err != nil {
		return false
	}
	q, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(strings.ToLower(d), strings.ToLower(q))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ---------- 策略评估入口 ----------

// permAction 是一次待授权的动作。
type permAction struct {
	Op      string // read | write | exec
	Tool    string
	Path    string // write：目标绝对路径
	Command string // exec：完整命令
	Cwd     string // exec：工作目录（空 = 会话工作区，即沙箱内）
}

// authorize 是所有工具的权限入口：评估 → 放行（敏感操作签发凭证）或
// 拒绝（带可操作的中文原因）。需要用户批准时同步等待审批结果。
func (t *toolContext) authorize(act permAction) (*Grant, error) {
	mode := permModeOf(t.sess)
	remote := t.isRemote()

	if act.Path != "" && t.app.isProtectedPath(act.Path, remote) {
		return nil, fmt.Errorf("权限拒绝：%s 是助手自身的受保护路径（配置/数据），任何模式下都不允许访问", act.Path)
	}

	switch act.Op {
	case "read", "info", "net":
		// 只读与联网在三种模式下都放行（受保护路径除外；
		// web_fetch 的内网目标限制在工具内自行判断）。
		return nil, nil

	case "write":
		switch mode {
		case permReadOnly:
			return nil, fmt.Errorf("权限拒绝：当前会话为「只读」模式，不能写文件。请让用户在权限芯片中切换为「工作区内读写」或「完全访问」")
		case permWorkspace:
			if !t.inWorkspace(act.Path) {
				return nil, fmt.Errorf("权限拒绝：%s 不在工作区 %s 内（「工作区内读写」模式只允许写工作区）。请让用户把会话切到「完全访问」模式，或把文件放进工作区", act.Path, t.workspace())
			}
		}
		return t.app.issueGrant(t.sess.ID, act.Tool, "write", "写文件 "+act.Path, false, false), nil

	case "exec":
		if !t.app.Cfg.AllowShell {
			return nil, fmt.Errorf("shell 执行已在设置中禁用（allowShell=false）")
		}
		if mode == permReadOnly {
			return nil, fmt.Errorf("权限拒绝：当前会话为「只读」模式，不能执行命令。请让用户在权限芯片中切换权限")
		}
		if why := destructiveCommandHit(act.Command); why != "" {
			return nil, fmt.Errorf("权限拒绝：该命令被安全策略硬拦截（%s）。如确实需要执行，请让用户手动在终端完成", why)
		}
		cwd := strings.TrimSpace(act.Cwd)
		if cwd == "" {
			cwd = t.workspace()
		}
		// 沙箱内执行：工作目录在工作区内，最小环境变量。
		if t.inWorkspace(cwd) {
			return t.app.issueGrant(t.sess.ID, act.Tool, "exec",
				truncateRunes(act.Command, 200), false, false), nil
		}
		// —— 沙箱外执行（工作区外命令）：完全访问直接放行；
		// 其余模式要求「工作区外执行命令」能力 + 逐次审批。 ——
		if mode != permFull && !hasCapability(t.sess, capUnsandboxed) {
			return nil, fmt.Errorf("权限拒绝：命令的工作目录 %s 在工作区之外（沙箱外执行）。请让用户在权限芯片中开启「允许工作区外执行命令」能力，开启后每次越界执行都会弹出审批由用户决定", cwd)
		}
		summary := fmt.Sprintf("在工作区外执行（目录 %s）：%s", cwd, truncateRunes(act.Command, 200))
		if mode == permFull || isRemembered(t.sess, "exec", summary) {
			return t.app.issueGrant(t.sess.ID, act.Tool, "exec", summary, true, true), nil
		}
		if t.emit != nil {
		t.emit(Event{Type: "tool_wait", Name: act.Tool, Output: "等待用户审批（沙箱外执行）…"})
	}
		approved, always := t.app.requestApproval(t.ctx, t.sess, act.Tool, summary,
			"该命令将在工作区之外的目录执行（沙箱外执行）")
		if !approved {
			return nil, fmt.Errorf("用户未批准该沙箱外执行，未执行。可询问用户是否改用工作区内目录，或换一种做法")
		}
		if always {
			t.app.rememberAllowed(t.sess, "exec", summary)
		}
		return t.app.issueGrant(t.sess.ID, act.Tool, "exec", summary, true, true), nil
	}
	return nil, fmt.Errorf("权限引擎：未知操作类型 %s", act.Op)
}
