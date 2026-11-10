package main

// 出站推送的「来源收口」与定时任务的每日闸（2026-11-10 用户定规则）。
//
// 起因：微信平台开始拒发——`微信服务返回错误（-2）：微信拒绝发送消息`。原因是 jx
// 把桌面端发起的活儿也往微信推，主动消息太多被判骚扰。用户的规则原话：
//
//	「回复 jx 界面的消息不用转发微信了，微信发送/回复的消息仅是通过微信收到的消息」
//
// 于是最终答复的推送改成按**这一轮的来源**判定（sessionRun.origin，阶段 3 就有）：
//   - `im:weixin:<chatId>` → 推（微信入站触发的那一轮，本来就该回给它）；
//   - `todo:weixin` → 推（定时任务里**明确标记**过的那一类，见下）；但每天最多
//     `imTodoPushDailyMax` 条/聊天，超了不发、只记日志；
//   - 其它（空 = 桌面端 / 子任务 / 普通定时任务）→ **一律不推**。
//
// 定时任务的例外怎么标记（别用「内容里出现某字符串」这种脆弱判断）：任务描述
// **开头**写固定标记 `【推送到微信】`，判定基于任务定义本身（todo.go 的
// handToAssistant 投递时打 origin）。用户写了这个标记，等于明确授权「这条要推到
// 手机」，而不是放任所有定时任务刷屏。
//
// 审批/提问的推送不走这里（im_perm.go 按 origin 自己判断），指令回执、拒绝说明
// 也不走这里（那些本来就是微信里问的，imSay 直接入队）。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// imOriginTodoWeixin 是「明确要求推微信的定时任务」那一轮的来源标记。
	// 不是 im:<渠道>:<chatId> 形态——它不知道要推给谁，按绑定表全员推。
	imOriginTodoWeixin = "todo:weixin"
	// imTodoPushMark 是任务描述开头的固定标记（写死在文档里，便于用户照抄）。
	imTodoPushMark = "【推送到微信】"
	// imTodoPushDailyMax 是每日每聊天的上限：用户定的是 2 条。
	imTodoPushDailyMax = 2
)

// imPushableOrigin 判断这一轮该不该推到微信（只有两类来源该推）。
func imPushableOrigin(origin string) bool {
	if origin == imOriginTodoWeixin {
		return true
	}
	_, _, ok := imParseOrigin(origin)
	return ok
}

// imTodoPushWanted 判断一条定时任务是否「明确要求推微信」：只认描述**开头**的
// 标记（前后空白忽略）。executor/target 由调用方保证（只对投给数字经理的 AI 任务
// 打标），这里只管标记本身。
func imTodoPushWanted(desc string) bool {
	return strings.HasPrefix(strings.TrimSpace(desc), imTodoPushMark)
}

// ---------- 每日频次闸（按本地自然日 + 每聊天计数，落盘，重启不清零） ----------

// imTodoPushState 是计数文件的内容：今天是哪天、每个聊天今天推了几条。
type imTodoPushState struct {
	Day   string         `json:"day"`   // 本地自然日 YYYY-MM-DD
	Chats map[string]int `json:"chats"` // chatId → 今天已推条数
}

// imTodoPushMu 串行化「读文件 → 判上限 → 写回」，否则两条并发推送会一起看到
// count=1、一起放行，上限形同虚设。
var imTodoPushMu sync.Mutex

func (a *App) imTodoPushPath() string {
	return filepathAbsOf(filepath.Join(a.Cfg.DataDir, "im_todo_push.json"))
}

// imTodoPushLoad 读计数（调用方持 imTodoPushMu）。文件缺失/损坏都当「今天还没推」：
// 计数是限流用的，读不出来时按上限更保守的方向走不如直接重开一天——这里选择重开，
// 因为损坏几乎只发生在手工改坏/首启，重开会退化成「今天能推 2 条」而不是永久卡死。
func (a *App) imTodoPushLoad() *imTodoPushState {
	st := &imTodoPushState{Chats: map[string]int{}}
	data, err := os.ReadFile(a.imTodoPushPath())
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, st); err != nil || st.Chats == nil {
		return &imTodoPushState{Chats: map[string]int{}}
	}
	return st
}

func (a *App) imTodoPushSave(st *imTodoPushState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	p := a.imTodoPushPath()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		_ = os.Remove(tmp)
		log.Printf("[IM] 定时推送计数写入失败：%v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		log.Printf("[IM] 定时推送计数写入失败：%v", err)
	}
}

// imTodoPushAllow 是「今天这个聊天还能不能再推一条」。允许则计数 +1 并返回 true；
// 超限返回 false（调用方负责记日志、跳过）。
func (a *App) imTodoPushAllow(chatID string) bool {
	return a.imTodoPushAllowOn(chatID, time.Now())
}

// imTodoPushAllowOn 是带时间注入的版本（单测要验「跨天重置」）。
func (a *App) imTodoPushAllowOn(chatID string, now time.Time) bool {
	if a == nil || a.Cfg == nil || strings.TrimSpace(chatID) == "" {
		return false
	}
	day := now.Format("2006-01-02")
	imTodoPushMu.Lock()
	defer imTodoPushMu.Unlock()
	st := a.imTodoPushLoad()
	if st.Day != day { // 跨天：清零重来
		st = &imTodoPushState{Day: day, Chats: map[string]int{}}
	}
	if st.Chats[chatID] >= imTodoPushDailyMax {
		a.imTodoPushSave(st) // 日号可能刚变，顺手落一次
		return false
	}
	st.Chats[chatID]++
	a.imTodoPushSave(st)
	return true
}
