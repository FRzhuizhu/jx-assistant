package main

// 模型与用户的交互工具（对齐官方标准栈）：
//
//   - ask_user_question：模型向用户提一个结构化问题（可带选项），同步等
//     用户回答后把答案返回给模型。对应官方 dsh-tool-ask-user。
//   - exit_plan_mode：计划模式下，模型完成探索后把计划提交给用户批准；
//     批准即退出计划模式开始执行，拒绝则带着反馈留在计划模式。
//     对应官方 dsh-plan-mode 的 exit_plan_mode。
//
// 两者共用同一条「等待用户输入」的通道：与审批（perm.go）同构——前端经
// 全局事件流收到 question_required 弹卡，回答经 POST /api/questions/{id}
// 回传；超时/取消返回"用户未回应"（fail-closed：不替用户做决定）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const questionTTL = 5 * time.Minute // 提问等待上限

// UserQuestion 是一次等待用户输入的请求（提问或计划批准）。
type UserQuestion struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionId"`
	Kind      string   `json:"kind"` // ask=普通提问 plan=计划批准
	Question  string   `json:"question"`
	Plan      string   `json:"plan,omitempty"` // kind=plan 时的计划正文
	Options   []string `json:"options,omitempty"`
	AllowFree bool     `json:"allowFree"`
	CreatedAt string   `json:"createdAt"`
	expiresAt time.Time
	decided   bool
	ch        chan string
}

// requestUserAnswer 广播提问并阻塞等待用户回答；返回用户的回答文本，
// 超时/取消返回 ok=false。
func (a *App) requestUserAnswer(ctx context.Context, sess *SessionMeta, q *UserQuestion) (string, bool) {
	q.ID = "q-" + newID()
	q.SessionID = sess.ID
	q.CreatedAt = now()
	q.expiresAt = time.Now().Add(questionTTL)
	q.ch = make(chan string, 1)
	a.permMu.Lock()
	a.questions[q.ID] = q
	a.permMu.Unlock()
	ev := Event{Type: "question_required", QuestionID: q.ID, SessionID: sess.ID,
		Kind: q.Kind, Summary: q.Question, Plan: q.Plan, Options: q.Options, Reason: q.Question}
	a.broadcast(ev)
	defer func() {
		a.permMu.Lock()
		delete(a.questions, q.ID)
		a.permMu.Unlock()
		a.broadcast(Event{Type: "question_resolved", QuestionID: q.ID, SessionID: sess.ID})
	}()
	timer := time.NewTimer(questionTTL)
	defer timer.Stop()
	select {
	case ans := <-q.ch:
		return ans, true
	case <-timer.C:
		return "", false
	case <-ctx.Done():
		return "", false
	}
}

// resolveQuestion 登记用户的回答并唤醒等待中的工具。
func (a *App) resolveQuestion(id, answer string) error {
	a.permMu.Lock()
	q, ok := a.questions[id]
	if ok && !q.decided {
		q.decided = true
		q.ch <- answer
	}
	a.permMu.Unlock()
	if !ok {
		return fmt.Errorf("提问不存在或已处理（可能已超时）")
	}
	return nil
}

// pendingQuestions 返回全部待回答提问（前端轮询兜底用）。
func (a *App) pendingQuestions() []*UserQuestion {
	a.permMu.Lock()
	defer a.permMu.Unlock()
	out := []*UserQuestion{}
	for _, q := range a.questions {
		out = append(out, q)
	}
	return out
}

// ---------- 工具 ----------

func askUserQuestionTool() Tool {
	return Tool{
		Spec: toolSpec("ask_user_question", "向用户提出一个需要其决定的问题并等待回答。用于：方案二选一、确认破坏性操作、缺失的关键信息。可带 2-6 个选项（用户也可自行输入）。不要用提问代替能自己查到的事实。",
			objSchema(map[string]any{
				"question":   strProp("问题本身，一句话说清楚要用户决定什么"),
				"options":    strProp("可选：以换行分隔的 2-6 个选项，每个一行"),
				"allow_free": boolProp("是否允许用户自由输入（默认 true）"),
			}, "question")),
		Run: runAskUserQuestion,
	}
}

func runAskUserQuestion(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Question  string   `json:"question"`
		Options   []string `json:"options"`
		AllowFree *bool    `json:"allow_free"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	q := strings.TrimSpace(p.Question)
	if q == "" {
		return "", fmt.Errorf("question 不能为空")
	}
	options := []string{}
	for _, o := range p.Options {
		if s := strings.TrimSpace(o); s != "" {
			options = append(options, s)
		}
	}
	if len(options) > 6 {
		options = options[:6]
	}
	allowFree := true
	if p.AllowFree != nil {
		allowFree = *p.AllowFree
	}
	if len(options) == 0 && !allowFree {
		return "", fmt.Errorf("没有选项且不允许自由输入，用户无从回答")
	}
	if t.emit != nil {
		t.emit(Event{Type: "tool_wait", Name: "ask_user_question", Output: "等待用户回答…"})
	}
	uq := &UserQuestion{Kind: "ask", Question: q, Options: options, AllowFree: allowFree}
	ans, ok := t.app.requestUserAnswer(t.ctx, t.sess, uq)
	if !ok {
		return "（用户未在时限内回答，视为暂缓。继续做不依赖该决定的部分，或稍后再问。）", nil
	}
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return "（用户跳过了这个问题。）", nil
	}
	return "用户回答：" + ans, nil
}

func exitPlanModeTool() Tool {
	return Tool{
		Spec: toolSpec("exit_plan_mode", "计划模式下使用：把探索得到的完整执行计划提交给用户审批。批准后会话自动退出计划模式，你随即开始执行；拒绝则带着用户反馈留在计划模式修订。不在计划模式时调用会报错。计划要具体：目标、步骤、涉及文件、验收标准。",
			objSchema(map[string]any{
				"plan": strProp("完整计划（Markdown），交给用户审批的正是这段文字"),
			}, "plan")),
		Run: runExitPlanMode,
	}
}

func runExitPlanMode(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	plan := strings.TrimSpace(p.Plan)
	if plan == "" {
		return "", fmt.Errorf("plan 不能为空")
	}
	if !t.sess.PlanMode {
		return "", fmt.Errorf("当前会话不在计划模式，无需提交计划；直接执行即可")
	}
	if t.emit != nil {
		t.emit(Event{Type: "tool_wait", Name: "exit_plan_mode", Output: "等待用户审批计划…"})
	}
	uq := &UserQuestion{Kind: "plan", Question: "请审批以下执行计划", Plan: truncateRunes(plan, 4000), AllowFree: true}
	ans, ok := t.app.requestUserAnswer(t.ctx, t.sess, uq)
	if !ok {
		return "（用户未在时限内审批计划，继续留在计划模式。可先做不落盘的只读探索，或请用户稍后审批。）", nil
	}
	ans = strings.TrimSpace(ans)
	if ans == "__approve__" {
		t.sess.PlanMode = false
		_ = t.app.Store.SaveMeta(t.sess)
		t.app.broadcast(Event{Type: "session_updated", SessionID: t.sess.ID})
		return "用户批准了计划，会话已退出计划模式。现在按计划开始执行；执行中写文件与执行命令仍受当前权限模式约束。", nil
	}
	return "用户未批准计划，会话仍处于计划模式。用户反馈：" + ans, nil
}
