package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 目标模式：update_goal 工具（常驻注册，目录跨模式不变——与 exit_plan_mode
// 同策略，保持请求形状/缓存稳定；未开目标模式时调用直接报错）。
// 目标模式的自动续跑由 runtime.go 的 runGoalRounds 驱动：目标仍 active 时
// 每轮结束自动注入一条 goal_round 消息继续推进，直到 complete/blocked/上限。

func updateGoalTool() Tool {
	return Tool{
		Spec: toolSpec("update_goal", "目标模式下汇报目标的最终状态：确实达成时 result=complete；确认无法推进（缺权限/缺信息/外部受阻、继续没有意义）时 result=blocked。summary 用一两句话说明结果或阻碍。这是结束目标推进的唯一正式方式；未开目标模式时调用会报错。不要在目标未真正完成时谎报 complete。",
			objSchema(map[string]any{
				"result":  strProp("complete 或 blocked"),
				"summary": strProp("一两句话：达成了什么，或卡在哪里"),
			}, "result", "summary")),
		Run: runUpdateGoal,
	}
}

func runUpdateGoal(t *toolContext, args json.RawMessage) (string, error) {
	var in struct {
		Result  string `json:"result"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数不是合法 JSON")
	}
	in.Result = strings.TrimSpace(in.Result)
	in.Summary = strings.TrimSpace(in.Summary)
	if in.Result != "complete" && in.Result != "blocked" {
		return "", fmt.Errorf("result 只能是 complete 或 blocked")
	}
	if !t.sess.GoalMode {
		return "", fmt.Errorf("当前未开启目标模式，无需调用 update_goal；直接正常回复即可")
	}
	if in.Summary == "" {
		return "", fmt.Errorf("summary 不能为空：请说明达成了什么或卡在哪里")
	}
	t.sess.GoalStatus = in.Result
	if err := t.app.Store.SaveMeta(t.sess); err != nil {
		return "", err
	}
	t.app.broadcast(Event{Type: "session_updated", SessionID: t.sess.ID})
	label := "已达成"
	if in.Result == "blocked" {
		label = "无法推进（受阻）"
	}
	return fmt.Sprintf("目标已记录为「%s」：%s。请基于以上全部进展，用面向用户的中文写最终总结（结果、产物位置、未尽事项），不要再调用工具。", label, in.Summary), nil
}
