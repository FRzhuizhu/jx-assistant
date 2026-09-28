package main

// 会话内后台作业（对齐官方标准栈的 run_in_background + job_* 工具族）：
// run_command 带 run_in_background=true 时不等待命令结束，立即返回作业 id，
// 命令在后台继续跑（不限时）；模型用 job_output 读输出（可阻塞等待）、
// job_list 列出本会话的作业、job_kill 终止。输出进内存环形缓冲，上限
// 512KB，超出截头。作业仅本地执行（SSH 远程会话不支持，返回明确错误）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	jobBufMax = 512 * 1024 // 单作业输出缓冲上限
	jobKeep   = time.Hour  // 已结束作业的保留时长（之后在新作业启动时清理）
)

// lockedBuffer 是并发安全的输出缓冲，超过上限时丢弃最早的内容。
type lockedBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	drop int // 已丢弃的字节数
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if b.buf.Len() > jobBufMax {
		drop := b.buf.Len() - jobBufMax
		b.buf.Next(drop)
		b.drop += drop
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 控制台字节按 UTF-8/GB18030 还原（与前台 runCommand 同规则）：
	// 缓冲里是原始字节，这里是对外的唯一字符串出口，解码只此一处
	s := decodeCmdOutput(b.buf.Bytes())
	if b.drop > 0 {
		return fmt.Sprintf("…（开头 %d 字节已被截断）\n%s", b.drop, s)
	}
	return s
}

// jobRun 是一个后台作业的运行时记录。
type jobRun struct {
	ID        string
	SessionID string
	Command   string
	Cwd       string
	Policy    string // 本次作业的沙箱档位（job_output 附加拒绝/故障 marker 用）
	startedAt time.Time
	buf       *lockedBuffer
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	exitInfo  string // 空=仍在运行；否则 "exit 0" / "exit 3" / "超时" 等
}

func (j *jobRun) Status() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.exitInfo == "" {
		return "running"
	}
	return "done: " + j.exitInfo
}

func (j *jobRun) setExit(info string) {
	j.mu.Lock()
	if j.exitInfo == "" {
		j.exitInfo = info
	}
	j.mu.Unlock()
	close(j.done)
}

// startJob 启动一个后台命令（权限已由调用方 authorize 并核销凭证）。
// policy 为 full 时不进沙箱；其余档位走 OS 级沙箱后端（与前台同路径）。
func (a *App) startJob(t *toolContext, command, shell, cwd, policy string) (*jobRun, error) {
	if info, serr := os.Stat(cwd); serr != nil || !info.IsDir() {
		return nil, fmt.Errorf("工作目录不存在: %s", cwd)
	}
	a.jobsMu.Lock()
	// 顺手清理超过保留期的已结束作业
	now := time.Now()
	for id, j := range a.jobs {
		select {
		case <-j.done:
			if now.Sub(j.startedAt) > jobKeep {
				delete(a.jobs, id)
			}
		default:
		}
	}
	j := &jobRun{
		ID: "job-" + newID(), SessionID: t.sess.ID, Command: command, Cwd: cwd, Policy: policy,
		startedAt: time.Now(), buf: &lockedBuffer{}, done: make(chan struct{}),
	}
	a.jobs[j.ID] = j
	a.jobsMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	if policy != permFull {
		// 沙箱后台作业：与前台同一后端（Windows 受限令牌 / Linux Landlock），
		// 不限时；终止只经 ctx（job_kill）。
		run := sandboxRun{Dir: cwd, Command: command, Shell: shell, Policy: policy, Workspace: t.workspace()}
		wait, err := startConfinedAsync(ctx, run, j.buf)
		if err != nil {
			a.jobsMu.Lock()
			delete(a.jobs, j.ID)
			a.jobsMu.Unlock()
			cancel()
			return nil, err
		}
		go func() {
			wait()
			if ctx.Err() != nil {
				j.setExit("被 job_kill 终止")
				return
			}
			j.setExit("exit ?（沙箱作业）")
		}()
		return j, nil
	}
	var cmd *exec.Cmd
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "cmd":
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	default:
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	}
	cmd.Dir = cwd
	hideConsoleWindow(cmd) // 后台作业同样悄悄跑（桌面形态没有控制台可复用）
	cmd.Env = sandboxEnv(true)
	cmd.Stdout = j.buf
	cmd.Stderr = j.buf
	go func() {
		defer cancel()
		err := cmd.Run()
		if ctx.Err() != nil {
			j.setExit("被 job_kill 终止")
			return
		}
		if err != nil {
			j.setExit(fmt.Sprintf("%v", err))
			return
		}
		j.setExit("exit 0")
	}()
	return j, nil
}

func (a *App) getJob(id, sessionID string) (*jobRun, error) {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	j, ok := a.jobs[id]
	if !ok || j.SessionID != sessionID {
		return nil, fmt.Errorf("作业 %s 不存在（或属于其他会话）", id)
	}
	return j, nil
}

// ---------- 工具 ----------

func jobOutputTool() Tool {
	return Tool{
		Spec: toolSpec("job_output", "读取后台作业的输出。默认立即返回当前已有输出与运行状态；wait=true 时最多阻塞等待 timeout_sec 秒（默认 30，上限 120）直到作业结束。",
			objSchema(map[string]any{
				"id":          strProp("作业 id，来自 run_command(run_in_background) 的返回"),
				"wait":        boolProp("是否阻塞等待作业结束，默认 false"),
				"timeout_sec": intProp("wait=true 时的等待上限秒数，默认 30"),
			}, "id")),
		Run: runJobOutput,
	}
}

func runJobOutput(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		ID        string `json:"id"`
		Wait      bool   `json:"wait"`
		TimeoutSc int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	j, err := t.app.getJob(strings.TrimSpace(p.ID), t.sess.ID)
	if err != nil {
		return "", err
	}
	if p.Wait {
		timeout := time.Duration(30) * time.Second
		if p.TimeoutSc > 0 {
			timeout = time.Duration(min(p.TimeoutSc, 120)) * time.Second
		}
		select {
		case <-j.done:
		case <-time.After(timeout):
		case <-t.ctx.Done():
		}
	}
	// 沙箱作业的输出与前台同规则附加拒绝/执行器故障 marker（对齐官方
	// renderProcessRead 在每次读取时附加 notice），模型读到才有正确方向。
	out := j.buf.String()
	if j.Policy != "" && j.Policy != permFull {
		out += runDeniedMarker(j.Policy, out, t.isRemote())
	}
	return fmt.Sprintf("作业 %s｜状态：%s\n输出：\n%s", j.ID, j.Status(), out), nil
}

func jobListTool() Tool {
	return Tool{
		Spec: toolSpec("job_list", "列出本会话的全部后台作业（id、状态、命令、启动时间）。", objSchema(map[string]any{})),
		Run:  runJobList,
	}
}

func runJobList(t *toolContext, _ json.RawMessage) (string, error) {
	t.app.jobsMu.Lock()
	mine := []*jobRun{}
	for _, j := range t.app.jobs {
		if j.SessionID == t.sess.ID {
			mine = append(mine, j)
		}
	}
	t.app.jobsMu.Unlock()
	if len(mine) == 0 {
		return "（本会话没有后台作业）", nil
	}
	lines := []string{}
	for _, j := range mine {
		lines = append(lines, fmt.Sprintf("- %s｜%s｜启动于 %s｜%s", j.ID, j.Status(), j.startedAt.Format("15:04:05"), truncateRunes(j.Command, 80)))
	}
	return strings.Join(lines, "\n"), nil
}

func jobKillTool() Tool {
	return Tool{
		Spec: toolSpec("job_kill", "终止一个还在运行的后台作业。已结束的作业无法终止。",
			objSchema(map[string]any{
				"id": strProp("作业 id"),
			}, "id")),
		Run: runJobKill,
	}
}

func runJobKill(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	j, err := t.app.getJob(strings.TrimSpace(p.ID), t.sess.ID)
	if err != nil {
		return "", err
	}
	select {
	case <-j.done:
		return "作业 " + j.ID + " 已结束（" + j.Status() + "），无需终止。", nil
	default:
	}
	j.cancel()
	select {
	case <-j.done:
		return "作业 " + j.ID + " 已终止。", nil
	case <-time.After(5 * time.Second):
		return "终止信号已发出，作业 " + j.ID + " 尚未退出（稍后可用 job_output 确认）。", nil
	}
}
