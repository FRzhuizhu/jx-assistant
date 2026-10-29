package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// envUpdateMessage 的差分分流：纯新增 → 短补充；条目有减或同 key 换值 →
// 【移除】/【新增】变更清单（不再全量重发；全量只留给组装层的差分自愈）。
// 快照正文用 envPrompt 的真实形状（标题行 + "- key：value" 条目行 + 尾部注记行）。
func TestEnvUpdateMessageTiers(t *testing.T) {
	prev := "# 运行环境快照（最新，取代更早的快照）\n" +
		"- 操作系统：Windows 11\n" +
		"- 工作区目录：D:\\demo\n" +
		"- 权限模式：修改工作区——文件写入仅限工作区\n"

	t.Run("纯新增走补充消息", func(t *testing.T) {
		cur := prev + "- 能力：已开启「操作浏览器」——可用 browser_* 工具\n"
		got := envUpdateMessage(prev, cur)
		if !strings.HasPrefix(got, "# 环境更新") {
			t.Fatalf("应是补充消息，got 首行: %q", firstLine(got))
		}
		if !strings.Contains(got, "操作浏览器") {
			t.Fatalf("补充应含新增行:\n%s", got)
		}
		if strings.Contains(got, "操作系统") {
			t.Fatalf("未变化的行不该重复出现在补充里:\n%s", got)
		}
		if strings.Contains(got, "【移除】") || strings.Contains(got, "【新增】") {
			t.Fatalf("纯新增不该带增删标签:\n%s", got)
		}
	})

	t.Run("条目减少走变更清单", func(t *testing.T) {
		// 技能清单少了一条（"- 自带技能" 之外的用户技能行被移除）
		cur := "# 运行环境快照（最新，取代更早的快照）\n" +
			"- 操作系统：Windows 11\n" +
			"- 工作区目录：D:\\demo\n"
		got := envUpdateMessage(prev, cur)
		if !strings.HasPrefix(got, "# 环境更新（条目有增删/变更") {
			t.Fatalf("有减应走变更清单，got 首行: %q", firstLine(got))
		}
		if !strings.Contains(got, "【移除】- 权限模式：修改工作区——文件写入仅限工作区") {
			t.Fatalf("变更清单应整行点名被移除条目:\n%s", got)
		}
		if strings.Contains(got, "【新增】") {
			t.Fatalf("纯删除不该有新增段:\n%s", got)
		}
		if strings.Contains(got, "- 操作系统") {
			t.Fatalf("未变化条目不该重复出现:\n%s", got)
		}
	})

	t.Run("同key换值成对移除新增", func(t *testing.T) {
		cur := "# 运行环境快照（最新，取代更早的快照）\n" +
			"- 操作系统：Windows 11\n" +
			"- 工作区目录：D:\\other\n" + // 同 key 新值
			"- 权限模式：修改工作区——文件写入仅限工作区\n"
		got := envUpdateMessage(prev, cur)
		if !strings.HasPrefix(got, "# 环境更新（条目有增删/变更") {
			t.Fatalf("换值应走变更清单，got 首行: %q", firstLine(got))
		}
		if !strings.Contains(got, "【移除】- 工作区目录：D:\\demo") {
			t.Fatalf("变更清单应【移除】整行旧值:\n%s", got)
		}
		if !strings.Contains(got, "【新增】- 工作区目录：D:\\other") {
			t.Fatalf("变更清单应【新增】整行新值:\n%s", got)
		}
		if strings.Count(got, "【移除】") != 1 || strings.Count(got, "【新增】") != 1 {
			t.Fatalf("换值应恰好一移一增:\n%s", got)
		}
		if strings.Contains(got, "- 操作系统") {
			t.Fatalf("未变化条目不该重复出现:\n%s", got)
		}
	})

	t.Run("纯注记行新增走补充且过期不触发清单", func(t *testing.T) {
		withNote := prev + "（技能变更：新增 报表助手）\n"
		got := envUpdateMessage(prev, withNote)
		if !strings.HasPrefix(got, "# 环境更新") {
			t.Fatalf("注记新增属纯新增，应走补充，got 首行: %q", firstLine(got))
		}
		// 下一份快照注记消失（注记只在落库那一条上）：不构成条目减少，不发清单
		got2 := envUpdateMessage(withNote, prev)
		if got2 != "" {
			t.Fatalf("注记过期应静默消失（无消息），got:\n%s", got2)
		}
	})

	t.Run("同文返回空", func(t *testing.T) {
		if got := envUpdateMessage(prev, prev); got != "" {
			t.Fatalf("同文应返回空，got:\n%s", got)
		}
	})
}

// envRowRender 的差分自愈：首条整份、差分计数到顶（envFullResendAfter）完整重发
// 并清零，未到顶走差分并累加。全量重发是组装层决策，envUpdateMessage 自身不感知。
func TestEnvRowRenderHeal(t *testing.T) {
	base := "# 运行环境快照（最新，取代更早的快照）\n- 权限模式：修改工作区\n"
	t.Run("首条整份并清零计数", func(t *testing.T) {
		s, n := envRowRender("", base, 3)
		if s != base || n != 0 {
			t.Fatalf("prevEnv 空应整份返回并清零，got n=%d", n)
		}
	})
	t.Run("计数到顶完整重发并清零", func(t *testing.T) {
		cur := base + "- 新条目：值\n"
		s, n := envRowRender(base, cur, envFullResendAfter)
		if !strings.HasPrefix(s, "# 运行环境快照（完整重发") {
			t.Fatalf("计数到顶应完整重发，got 首行: %q", firstLine(s))
		}
		if n != 0 {
			t.Fatalf("完整重发后计数应清零，got n=%d", n)
		}
	})
	t.Run("未到顶走差分并累加", func(t *testing.T) {
		cur := base + "- 新条目：值\n"
		s, n := envRowRender(base, cur, envFullResendAfter-1)
		if !strings.HasPrefix(s, "# 环境更新") {
			t.Fatalf("未到顶应走差分，got 首行: %q", firstLine(s))
		}
		if n != envFullResendAfter {
			t.Fatalf("差分后计数应 +1，got n=%d", n)
		}
	})
	t.Run("同文不发且计数不变", func(t *testing.T) {
		s, n := envRowRender(base, base, 2)
		if s != "" || n != 2 {
			t.Fatalf("同文应不发且计数不变，got (%q,%d)", s, n)
		}
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ask_user_question 的 timeout_sec 指针语义：缺省（默认 300）、显式 0（不限时）、
// 正常值（钳制）三档在工具处理器入参解析后的形态。
func TestAskTimeoutSecParse(t *testing.T) {
	parse := func(raw string) (timeoutSec int, noTimeout bool) {
		var p struct {
			TimeoutSec *int `json:"timeout_sec"`
		}
		if err := json.Unmarshal(json.RawMessage(raw), &p); err != nil {
			t.Fatal(err)
		}
		if p.TimeoutSec == nil {
			return 0, false
		}
		if *p.TimeoutSec == 0 {
			return 0, true
		}
		return clampAskTimeoutSec(*p.TimeoutSec), false
	}
	if sec, no := parse(`{}`); sec != 0 || no {
		t.Fatalf("缺省应为默认档(0,false)，got (%d,%v)", sec, no)
	}
	if sec, no := parse(`{"timeout_sec":0}`); sec != 0 || !no {
		t.Fatalf("显式 0 应为不限时(0,true)，got (%d,%v)", sec, no)
	}
	if sec, no := parse(`{"timeout_sec":30}`); sec != 30 || no {
		t.Fatalf("30 秒档解析错误，got (%d,%v)", sec, no)
	}
	if sec, no := parse(`{"timeout_sec":99999}`); sec != 600 || no {
		t.Fatalf("超上限应钳到 600（10 分钟封顶），got (%d,%v)", sec, no)
	}
	if sec, no := parse(`{"timeout_sec":1}`); sec != 30 || no {
		t.Fatalf("低于下限应钳到 30，got (%d,%v)", sec, no)
	}
}

// questionTTLLabel：环境快照公告里的默认档位时长（当前 5 分钟）。
func TestQuestionTTLLabel(t *testing.T) {
	if got := questionTTLLabel(); got != "5 分钟" {
		t.Fatalf("questionTTLLabel = %q，想 \"5 分钟\"", got)
	}
}

// 拼装层：系统提示词全模式都带「提问的分寸」段；环境快照的「提问等待」公告
// 只落在配了 ask_user_question 的执行模式，且档位数值取自 questionTTL 变量。
func TestAskTierPromptAssembled(t *testing.T) {
	a := &App{Cfg: &Config{Workspace: t.TempDir()}}
	for _, mode := range []string{"task", "office", "coding", "beginner-coding", "skill-create", "assistant", "aux"} {
		if sys := a.systemPrompt(&SessionMeta{Mode: mode}); !strings.Contains(sys, "提问的分寸") {
			t.Fatalf("模式 %s 的系统提示词缺少「提问的分寸」段", mode)
		}
	}
	env := a.envPrompt(&SessionMeta{Mode: "task"})
	if !strings.Contains(env, "- 提问等待：一般提问默认 "+questionTTLLabel()+"，") {
		t.Fatalf("task 的环境快照缺少提问等待公告:\n%s", env)
	}
	if !strings.Contains(env, "timeout_sec=0（不限时）") || !strings.Contains(env, "停止计时") {
		t.Fatalf("提问等待公告缺不限时/停表说明:\n%s", env)
	}
	for _, mode := range []string{"assistant", "aux"} { // 无 ask 工具的模式不公告
		if e := a.envPrompt(&SessionMeta{Mode: mode}); strings.Contains(e, "提问等待") {
			t.Fatalf("模式 %s 没有 ask_user_question，不该公告提问等待", mode)
		}
	}
}
