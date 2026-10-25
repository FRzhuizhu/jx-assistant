package main

// git_sync 模型工具：组件 git-panel 的宿主半边，与 Git 面板共用同一条
// 执行缝（gitpanel.go 的 gitSync）。存在的理由是沙箱内 git 的远程操作
// 全灭（2026-10 实测，证据与结论见《Git远程同步工具-改动说明.md》）：
//   - 任何 fetch/push/pull 的传输子进程（MSYS sh.exe）在受限令牌下创建
//     Cygwin 共享内存被拒（CreateFileMapping Win32 error 5），本地路径
//     远程也死，与凭据无关；
//   - HTTPS 再叠加 schannel AcquireCredentialsHandle SEC_E_NO_CREDENTIALS
//     （受限令牌拿不到 TLS 凭据句柄），连无凭据需求的公共仓库都不通。
// 本地操作（status/add/commit/log 等）沙箱内正常，模型照常用 run_command；
// 远程同步走本工具（宿主进程全令牌，凭据助手可用，报错原样回给模型）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// gitToolEnabled 工具与 Git 面板接口同一开关（组件 git-panel）；
// 组件缺失视为未启用（todoEnabled 同款判定）。
func (a *App) gitToolEnabled() bool {
	if a.Components == nil {
		return false
	}
	if _, ok := a.Components.Manifest(gitComponentID); !ok {
		return false
	}
	return a.Components.IsEnabled(gitComponentID)
}

// gitTools 组件 git-panel 启用时返回 git_sync 工具（todoTools 同款组件
// 门控：组件开关即工具目录的增删边界）。只挂实操模式（aux 只读答疑、
// assistant 编排不亲自实操，都在 toolsFor 的对应分支外）。
func (a *App) gitTools() []Tool {
	if !a.gitToolEnabled() {
		return nil
	}
	return []Tool{gitSyncTool()}
}

// gitSyncOpDesc 模型词汇 → gitSync 的 op 映射说明：整套同步在 gitSync
// 里是空串（面板「⟲ 同步」按钮的原始语义），模型侧显式叫 sync。
const (
	gitSyncOpPush  = "push"
	gitSyncOpPull  = "pull"
	gitSyncOpSync  = "sync"
	gitSyncOpFetch = "fetch"
)

func gitSyncTool() Tool {
	return Tool{
		Spec: toolSpec("git_sync", "与远程仓库同步（op：push=把当前分支新提交推送到上游，缺省；pull=拉取远程更新并合并（快进优先，分叉自动合并，有冲突立即中止恢复原状）；sync=拉取+合并+推送一键；fetch=只更新远程跟踪状态、不动任何东西）。经 Git 面板组件的宿主通道执行，**不经命令沙箱**——命令沙箱内 git 的远程操作（fetch/pull/push/clone）会因受限令牌失败（本地操作 status/add/commit/log 等不受影响，直接用 run_command）。凭据走系统 git 的正常配置（凭据助手可用）。需要决策的情形（当前分支无上游、分叉有冲突、推送被拒）本工具不动手，原样报告 git 信息。只读模式调用会被拒绝（可用 request_permission 申请切档）。若本会话已是完全访问权限、或已开启「允许工作区外执行命令」能力，则不必用本工具：直接用 run_command 执行 git 命令行（fetch/pull/push 均可，凭据可用）。",
			objSchema(map[string]any{
				"op": map[string]any{
					"type":        "string",
					"enum":        []string{gitSyncOpPush, gitSyncOpPull, gitSyncOpSync, gitSyncOpFetch},
					"description": "同步方式：push=推送当前分支新提交（缺省）；pull=拉取并合并远程更新；sync=拉取+合并+推送；fetch=只更新远程跟踪状态",
				},
				"repo": strProp("仓库路径（多仓库工作区时用；缺省=会话工作区的仓库根）"),
			})),
		Run: runGitSync,
	}
}

func runGitSync(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Op   string `json:"op"`
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	// 组件可能被会话中途关掉（工具目录在本轮开轮时定型）——调用时再核一次
	if !t.app.gitToolEnabled() {
		return "", errors.New("「Git 面板」组件未启用，git_sync 不可用；请到 设置 → 内置组件 打开")
	}
	// 宿主通道是本地 git，SSH 远程工作区没有这个执行环境
	if t.isRemote() {
		return "", errors.New("git_sync 暂不支持 SSH 远程工作区会话（本地工作区专用）；远程仓库操作请在远程目录内用 run_command")
	}
	// 只读档：fetch/pull/push 都会写（远程跟踪分支/合并/推送），属于写操作
	if permModeOf(t.sess) == permReadOnly {
		return "", errors.New("全电脑只读模式下 git_sync 不可用：本工具会更新远程仓库与本地远程跟踪分支，属于写操作。可用 request_permission(target=workspace-write, justification=…) 向用户申请切换权限档（弹审批卡，批准后立即生效）；或请用户把权限切到修改工作区后再试")
	}
	// 模型词汇归一到 gitSync 的 op（整套同步在 gitSync 里是空串）
	op := strings.TrimSpace(p.Op)
	switch op {
	case "":
		op = gitSyncOpPush
	case gitSyncOpPush, gitSyncOpPull, gitSyncOpFetch:
	case gitSyncOpSync:
		op = ""
	default:
		return "", fmt.Errorf("op 仅支持 push/pull/sync/fetch，收到 %q", p.Op)
	}
	root, repos, reason := t.app.gitResolve(t.ctx, t.sess.ID, strings.TrimSpace(p.Repo))
	if root == "" {
		return "", errors.New(reason)
	}
	// gitResolve 对不认识的 repo 会静默回退 repos[0]；模型场景必须显式报错
	// 列出候选，否则推错仓库都没人发现
	if strings.TrimSpace(p.Repo) != "" && p.Repo != root {
		names := make([]string, 0, len(repos))
		for _, c := range repos {
			names = append(names, c.Path)
		}
		return "", fmt.Errorf("仓库 %s 不在会话工作区扫出的候选里（候选：%s）", p.Repo, strings.Join(names, "、"))
	}
	summary, needHelp, err := t.app.gitSync(t.ctx, t.sess.ID, root, op)
	if err != nil {
		return "", err
	}
	if needHelp {
		// gitSync 刻意不动手的情形（无上游/冲突/被拒等），summary 已是人话；
		// 作为正常结果返回让模型读后决定下一步（问用户或改用其他途径）
		return "无法自动完成：" + summary, nil
	}
	return summary, nil
}
