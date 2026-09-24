package main

// 权限与授权引擎：移植 DSH（DeepSeek 官方 Agent）的沙箱与权限模型，
// 与官方同构的三层结构：
//
//  1. 三档沙箱策略（会话粒度，权限芯片可选）：read-only（沙箱内执行、
//     无任何写授权）/ workspace-write（沙箱内执行、授权工作区+私有临时
//     目录）/ full（不受限）。强制力来自操作系统：Windows 受限令牌+ACL
//     （sandbox_windows.go）、Linux Landlock（经 sandboxhelper 自限制）、
//     SSH 远程经上传到远端的同一 helper。文件工具以路径边界为沙箱。
//  2. 拒绝驱动的升级（对齐官方 bash 工具）：不做威胁命令黑名单、不做
//     执行前预检——命令先按会话策略进沙箱真实执行；写操作被操作系统
//     拒绝时结果带 [sandbox: write denied under <mode> mode] 标记，模型
//     同轮可带 sandbox_permissions（够用的最宽档）+ justification 重试
//     一次，该重试弹审批卡由用户决定，批准后以更宽策略执行这一次；
//     「始终允许」= 会话切到完全访问（官方预设捆绑：完全访问=不再审批）。
//     被拒绝的升级对该命令终局。
//  3. 一次性授权凭证（Grant）：每次写文件/执行命令签发短时效凭证，
//     执行器核销后才动手；过期或重复核销即拒绝（fail closed）。
//  4. 受保护路径：助手自身的配置与数据目录（内含 API Key 与全部会话
//     记录）任何模式下工具都禁止访问——确定性的路径边界检查。
//
// 远程工作区（SSH）会话走同一套引擎：工作区边界换为远程目录，命令经
// 远端 helper 在远端沙箱内执行（官方 sandbox-ssh 同思路）。

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
	permReadOnly    = "read-only"       // 只读（沙箱内执行但无任何写授权）
	permWorkspace   = "workspace-write" // 工作区内读写（默认）
	permFull        = "full"            // 完全访问（不受限）
	capUnsandboxed  = "unsandboxed"     // 能力：允许沙箱外（不受限）执行命令
	capSandboxWrite = "sandbox-write"   // 能力：允许命令在沙箱内写入（只读档位下）
	capFsEscape     = "fs-escape"       // 能力：允许读写工作区外的文件
	capBrowser      = "browser"         // 能力：允许操作浏览器（browser_* 工具）
	capComputer     = "computer"        // 能力：允许操作电脑（computer_* 工具）
	grantTTL        = 5 * time.Minute   // 授权凭证有效期
	approvalTTL     = 2 * time.Minute   // 审批等待上限，超时视为拒绝
)

// escalationCapability 返回某类升级请求对应的能力开关 id：
// 「始终允许」点亮它，之后该类动作不再逐次审批（常设授权）。
func escalationCapability(req string) string {
	switch req {
	case permFull:
		return capUnsandboxed
	case permWorkspace:
		return capSandboxWrite
	}
	return ""
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

// addCapability 给会话开启能力开关并持久化（「始终允许」用）。
func (a *App) addCapability(s *SessionMeta, cap string) {
	if hasCapability(s, cap) {
		return
	}
	s.Tools = append(s.Tools, cap)
	_ = a.Store.SaveMeta(s)
	a.broadcast(Event{Type: "session_updated", SessionID: s.ID})
}

// permModeOf 返回会话的权限模式（未设置时默认工作区内读写）。
func permModeOf(s *SessionMeta) string {
	switch s.Permission {
	case permReadOnly, permFull, permWorkspace:
		return s.Permission
	default:
		return permWorkspace
	}
}

// permModeLabel 返回权限模式的中文展示名。
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

// ---------- 授权凭证 ----------

// Grant 是一次敏感操作的执行凭证：短时效、一次性、与动作绑定。
// 策略评估通过（或用户审批通过）才签发；执行器核销后才执行。
// Policy 是本次执行使用的沙箱策略（read-only / workspace-write / full），
// 执行器据此选择受限启动或普通启动。
type Grant struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Tool      string `json:"tool"`
	Op        string `json:"op"`      // write | exec
	Summary   string `json:"summary"` // 人类可读的动作摘要
	Policy    string `json:"policy"`  // 本次执行的沙箱策略档位
	issuedAt  time.Time
	expiresAt time.Time
	used      bool
}

// issueGrant 签发一张授权凭证（不做事前评估——调用方必须已经过 authorize）。
func (a *App) issueGrant(sessionID, tool, op, summary, policy string) *Grant {
	g := &Grant{
		ID: "g-" + newID(), SessionID: sessionID, Tool: tool, Op: op, Summary: summary,
		Policy:   policy,
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
// always=true 表示用户点了「始终允许」：给会话开启「允许工作区外执行命令」
// 能力（权限面板勾选点亮，持久化并广播），本次放行，之后的越界执行不再审批。
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
	// 注意：always 的能力点亮在 authorize 里按升级类别进行（那里知道
	// 请求的是哪一类动作），这里只负责唤醒等待中的工具执行。
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

// ---------- 受保护路径 ----------

// isProtectedPath 判断路径是否落在助手自身的配置 / 数据目录内。
// 这些位置存着 API Key 与全部会话记录，任何模式下工具都禁止访问。
// 这是确定性的路径边界检查（同官方 fs-sandbox 的路径判断），
// 不是威胁命令猜测。
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

// ---------- 危险命令护栏（可开关组件） ----------

// destructiveCmds 危险命令拦截表：不可逆破坏在任何模式下都拒绝。
// 官方没有这层（官方靠 OS 沙箱兜底）；这是本产品自己的加固，做成
// 可开关组件（组件/dangerous-cmd-guard，缺省启用），用户可关。
var destructiveCmds = []struct {
	re  *regexp.Regexp
	why string
}{
	{regexp.MustCompile(`(?i)\bformat\s+[a-z]:`), "格式化磁盘"},
	{regexp.MustCompile(`(?i)\b(shutdown|logoff|restart-computer)\b`), "关机/重启"},
	{regexp.MustCompile(`(?i)\bmkfs(\.\w+)?\b`), "格式化文件系统"},
	{regexp.MustCompile(`(?i)\bdd\s+[^|]*of=/dev/`), "直写块设备"},
	{regexp.MustCompile(`(?i)\brm\s+(-[a-z]*r[a-z]*f*|-rf)\s+(/|~|\$HOME)(\s|/|$|\*)`), "递归删除根目录或用户主目录"},
	{regexp.MustCompile(`(?i)\b(rd|rmdir)\b(\s+/[a-z0-9]+)*[\s"']*([a-z]):\\+["'\s]*$`), "递归删除盘根目录"},
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

// guardEnabled 危险命令护栏是否开启：组件缺省启用，组件缺失视为开启。
func (a *App) guardEnabled() bool {
	if a.Components == nil {
		return true
	}
	if _, ok := a.Components.Manifest("dangerous-cmd-guard"); !ok {
		return true
	}
	return a.Components.IsEnabled("dangerous-cmd-guard")
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
	Cwd     string // exec：工作目录（空 = 会话工作区；仅决定进程 cwd，不再做边界判断）

	EscalateTo    string // exec：模型请求的更宽沙箱档位（官方 sandbox_permissions）
	Justification string // exec：模型给出的升级理由（官方 justification）
}

// sandboxPolicyForMode 把会话权限模式映射为本次命令执行的沙箱策略档位。
// 这就是「预设捆绑」：模式即策略，完全访问 = 不受限。
func sandboxPolicyForMode(mode string) string {
	switch mode {
	case permReadOnly:
		return permReadOnly
	case permFull:
		return permFull
	default:
		return permWorkspace
	}
}

// sandboxPolicyRank 返回策略档位的宽窄序（窄 < 宽），用于升级方向校验。
func sandboxPolicyRank(p string) int {
	switch p {
	case permReadOnly:
		return 0
	case permWorkspace:
		return 1
	case permFull:
		return 2
	}
	return -1
}

// authorize 是所有工具的权限入口。exec 不再预检命令内容：命令先以会话
// 策略档进沙箱真实执行，被拒绝后模型带 sandbox_permissions+justification
// 重试（act.EscalateTo），该重试在这里弹审批、由用户决定后以更宽策略
// 放行这一次。拒绝驱动，不做威胁命令猜测。
func (t *toolContext) authorize(act permAction) (*Grant, error) {
	mode := permModeOf(t.sess)
	remote := t.isRemote()

	if act.Path != "" && t.app.isProtectedPath(act.Path, remote) {
		return nil, fmt.Errorf("权限拒绝：%s 是助手自身的受保护路径（配置/数据），任何模式下都不允许访问", act.Path)
	}

	switch act.Op {
	case "read", "info", "net":
		// 只读与联网放行（受保护路径除外；web_fetch 的内网目标
		// 限制在工具内自行判断）。
		return nil, nil

	case "write":
		// 文件工具的沙箱：路径边界是确定性的路径判断（fs-sandbox 等价物）。
		blocked := ""
		switch mode {
		case permReadOnly:
			blocked = "read-only"
		case permWorkspace:
			if !t.inWorkspace(act.Path) {
				blocked = "workspace-write"
			}
		}
		if blocked == "" {
			return t.app.issueGrant(t.sess.ID, act.Tool, "write", "写文件 "+act.Path, sandboxPolicyForMode(mode)), nil
		}
		// 能力「读写工作区外的文件」已点亮 → 用户已常设批准，直接放行。
		if hasCapability(t.sess, capFsEscape) {
			return t.app.issueGrant(t.sess.ID, act.Tool, "write", "写文件 "+act.Path+"（fs-escape 能力）", permFull), nil
		}
		// 升级申请：模型带 sandbox_permissions=full + justification → 弹审批，
		// 「始终允许」点亮 fs-escape 能力，之后写工作区外不再逐次审批。
		req := strings.TrimSpace(act.EscalateTo)
		if req == permFull || req == "danger-full-access" {
			just := strings.TrimSpace(act.Justification)
			if just == "" {
				return nil, fmt.Errorf("sandbox_permissions 必须搭配一句 justification（给用户看的理由），本次缺少")
			}
			summary := fmt.Sprintf("写入沙箱边界之外：%s｜理由：%s", truncateRunes(act.Path, 160), truncateRunes(just, 120))
			if t.emit != nil {
				t.emit(Event{Type: "tool_wait", Name: act.Tool, Output: "等待用户审批（工作区外写文件）…"})
			}
			approved, always := t.app.requestApproval(t.ctx, t.sess, act.Tool, summary,
				fmt.Sprintf("当前会话为 %s，模型请求写入工作区之外（%s）", permModeLabel(mode), act.Path))
			if !approved {
				return nil, fmt.Errorf("用户未批准该写入，未执行。请停止并如实说明，不要绕过")
			}
			if always {
				t.app.addCapability(t.sess, capFsEscape)
			}
			return t.app.issueGrant(t.sess.ID, act.Tool, "write", "写文件 "+act.Path, permFull), nil
		}
		hint := "可在重试时带 sandbox_permissions=full 与一句 justification 申请写入（弹审批由用户决定）"
		if blocked == "read-only" {
			return nil, fmt.Errorf("[fs sandbox: write denied under read-only mode] 目标 %s（当前会话为只读）。%s；或让用户把权限芯片切到「工作区内读写」", act.Path, hint)
		}
		return nil, fmt.Errorf("[fs sandbox: write denied under workspace-write mode] 目标 %s 不在工作区 %s 内。%s", act.Path, t.workspace(), hint)

	case "exec":
		if !t.app.Cfg.AllowShell {
			return nil, fmt.Errorf("shell 执行已在设置中禁用（allowShell=false）")
		}
		// 危险命令护栏（组件开关，缺省开）：不可逆破坏直接拒绝，
		// 不进入"先执行后升级"的流程——这类命令不该有被放行的机会。
		if t.app.guardEnabled() {
			if why := destructiveCommandHit(act.Command); why != "" {
				return nil, fmt.Errorf("权限拒绝：该命令被「危险命令拦截」护栏拦截（%s）。如确实需要，请让用户在设置→组件中评估该开关后手动执行", why)
			}
		}
		policy := sandboxPolicyForMode(mode)

		// 升级重试：模型因真实拒绝请求更宽档位（官方 sandbox_permissions 语义）。
		// 已开启「允许工作区外执行命令」能力 → 视为用户已常设批准，直接放行；
		// 未开启 → 每次升级弹审批，用户可点「始终允许」把能力勾选点亮。
		if req := strings.TrimSpace(act.EscalateTo); req != "" {
			if sandboxPolicyRank(req) <= sandboxPolicyRank(policy) {
				return nil, fmt.Errorf("sandbox_permissions=%s 无效：当前策略已是 %s，无需升级或请求了更窄的档位。请直接重跑命令", req, policy)
			}
			if req != permWorkspace && req != permFull {
				return nil, fmt.Errorf("sandbox_permissions 仅支持 workspace-write 或 danger-full-access（当前支持的最宽档位），收到 %s", req)
			}
			capID := escalationCapability(req)
			if !hasCapability(t.sess, capID) {
				just := strings.TrimSpace(act.Justification)
				if just == "" {
					return nil, fmt.Errorf("sandbox_permissions 必须搭配一句 justification（给用户看的理由），本次缺少")
				}
				summary := fmt.Sprintf("沙箱升级到 %s：%s｜理由：%s", req, truncateRunes(act.Command, 160), truncateRunes(just, 120))
				if t.emit != nil {
					t.emit(Event{Type: "tool_wait", Name: act.Tool, Output: "等待用户审批沙箱升级…"})
				}
				approved, always := t.app.requestApproval(t.ctx, t.sess, act.Tool, summary,
					fmt.Sprintf("模型请求把沙箱档位从 %s 升级到 %s 来重跑被拒绝的命令", policy, req))
				if !approved {
					return nil, fmt.Errorf("用户未批准沙箱升级（%s），该命令本轮终局：停止并如实说明，不要换着法绕过；其他命令仍可正常执行", req)
				}
				if always {
					// 「始终允许」= 点亮这类升级对应的能力开关（权限面板
					// 可见），之后该类动作不再逐次审批（常设授权）。
					t.app.addCapability(t.sess, capID)
				}
			}
			policy = req
		}
		return t.app.issueGrant(t.sess.ID, act.Tool, "exec", truncateRunes(act.Command, 200), policy), nil
	}
	return nil, fmt.Errorf("权限引擎：未知操作类型 %s", act.Op)
}
