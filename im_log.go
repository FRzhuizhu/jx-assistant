package main

// IM 通道流水（data/jx.db 的 im_messages 表）：微信等聊天软件的收发账本，
// 给桌面端「微信」面板（web/app-im.js）看历史、看推送成败用。
//
// 为什么不复用 messages 表（设计意图，见方案评审）：
//   - messages 记的是「会话里发生了什么」，而 IM 有三类事实根本不进会话：
//     内置命令的即时回执（/status /stop）、推送失败、桌面在线时的静默跳过；
//   - 换数字经理版本 / 删会话 / 压缩都会让 messages 侧的 IM 痕迹失真，而用户
//     想看的正是「我和微信之间到底来回了什么」这条独立线索；
//   - 会话里已有 messages.origin = 'im' 作来源标记（阶段 3 打的），入站消息
//     仍以会话为准；本表只额外留一份正文快照（截断后），不做第二套真源。
//
// 纪律：流水是观测，绝不能反过来影响收发——写入失败只记日志，不向上返回错误；
// 写入点在长轮询与发件 worker 的热路径上，一律单行 INSERT。

import (
	"errors"
	"log"
	"strings"
)

// 方向
const (
	imDirIn  = "in"
	imDirOut = "out"
)

// 入站 status（这条消息最后怎么了）
const (
	imStatusAccepted = "accepted" // 已投递进数字经理（空闲落库起轮，忙则插话）
	imStatusRejected = "rejected" // 白名单不通过 / 群聊 / 只有图片文件：没进会话
	imStatusCommand  = "command"  // 内置命令或审批答复（已消费，不落会话）
	imStatusGroup    = "group"    // 群消息（首版只登记不处理）
)

// 出站 status（推送结局）
const (
	imStatusPending = "pending" // 已进发件队列
	imStatusSent    = "sent"    // 平台已接收
	imStatusFailed  = "failed"  // 发送失败（error 里是人话原因）
	imStatusSkipped = "skipped" // 策略性不推（如桌面在线时的「静默」开关）
)

// 出站 kind（这段话是从哪来的）
const (
	imKindAnswer = "answer" // 数字经理的最终答复（带会话序号）
	imKindNotice = "notice" // 命令回执 / 审批提示 / 拒绝说明（不带序号）
	imKindManual = "manual" // 预留：面板侧手发的（当前口径面板只读，不开放）
)

// imLogMaxChars 是流水正文的保存上限。答复推送本身已截到 imMaxPushChars(6000)，
// 这里留余量；再长也不是「聊天里能读」的东西，没必要让库跟着长。
const imLogMaxChars = 8000

// imLogRow 是一条流水（接口与前端共用同一形态）。
type imLogRow struct {
	ID        int64  `json:"id"`
	Channel   string `json:"channel"`
	ChatID    string `json:"chatId"`
	Direction string `json:"direction"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Retries   int    `json:"retries,omitempty"`
	At        string `json:"at"`
	Seq       int64  `json:"seq,omitempty"`
}

// imLogEntry 是写入一条流水的入参（字段多，用结构体而不是十个位置参数）。
type imLogEntry struct {
	Channel    string
	ChatID     string
	Direction  string
	Kind       string
	Text       string
	Status     string
	Error      string
	PlatformID string
	SessionID  string
	Seq        int64
	At         string
}

// imChatStat 是「一个聊天」在流水里的汇总（面板左列的数据源）。
type imChatStat struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chatId"`
	LastAt   string `json:"lastAt"`
	LastText string `json:"lastText"`
	LastDir  string `json:"lastDir"`
	Inbound  int    `json:"inbound"`
	Outbound int    `json:"outbound"`
	Failed   int    `json:"failed"` // 出站失败 + 静默跳过
	LastInID int64  `json:"lastInId"`
}

// imCutText 按字符截断正文，保留换行（imTruncate 会把换行压成空格，是给日志
// 摘要用的，不能拿来存正文）。
func imCutText(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…（已截断）"
}

// imInboundKind 归一化入站类别（适配层给 text/voice/image/file/video/group，
// 空值按 text——纯文本消息在微信里是最常见的那种）。
func imInboundKind(in IMInbound) string {
	if k := strings.TrimSpace(in.Kind); k != "" {
		return k
	}
	return "text"
}

// AddIMMessage 落一条流水，返回行 id（0 = 落库失败，调用方忽略即可）。
func (s *Store) AddIMMessage(e imLogEntry) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`INSERT INTO im_messages
		(channel, chat_id, direction, kind, text, status, error, platform_id, session_id, seq, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		e.Channel, e.ChatID, e.Direction, e.Kind, e.Text, e.Status, e.Error, e.PlatformID, e.SessionID, e.Seq, e.At)
	if err != nil {
		log.Printf("[IM] 流水落库失败: %v", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// UpdateIMMessageStatus 回填一条出站流水的结局（成功 / 失败原因 / 静默跳过）。
func (s *Store) UpdateIMMessageStatus(id int64, status, errText string) error {
	if id <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE im_messages SET status=?, error=? WHERE id=?`, status, errText, id)
	if err != nil {
		log.Printf("[IM] 流水状态回填失败: %v", err)
	}
	return err
}

// IMMessages 取某个聊天的流水（按时间正序返回）。before>0 时取该 id 之前的一页
// （前端向上翻历史用，id 自增单调，比时间戳可靠）。
func (s *Store) IMMessages(channel, chatID string, before int64, limit int) ([]imLogRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT id, channel, chat_id, direction, kind, text, status, error, retries, created_at, seq
		FROM im_messages WHERE channel=? AND chat_id=?`
	args := []any{channel, chatID}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	s.mu.Lock()
	rows, err := s.db.Query(q, args...)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []imLogRow{}
	for rows.Next() {
		var r imLogRow
		if err := rows.Scan(&r.ID, &r.Channel, &r.ChatID, &r.Direction, &r.Kind, &r.Text,
			&r.Status, &r.Error, &r.Retries, &r.At, &r.Seq); err != nil {
			continue
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 查询按 id 倒序取最近 N 条，返回前翻正（前端从旧到新渲染）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// IMUnreadCount 数一个聊天里「readUpTo 之后的入站条数」（面板未读红点）。
func (s *Store) IMUnreadCount(channel, chatID string, after int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM im_messages
		WHERE channel=? AND chat_id=? AND direction='in' AND id > ?`, channel, chatID, after).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// IMMessageChats 按聊天汇总流水（面板左列：最后一条、时间、收发条数、失败数）。
// 两条查询而不是窗口函数：SQLite 老版本对窗口函数的支持不稳定，这里用最通用的写法。
func (s *Store) IMMessageChats() ([]imChatStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT channel, chat_id, MAX(created_at),
			COALESCE(SUM(CASE WHEN direction='in' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN direction='out' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN direction='out' AND status IN ('failed','skipped') THEN 1 ELSE 0 END),0),
			COALESCE(MAX(CASE WHEN direction='in' THEN id END),0)
		FROM im_messages GROUP BY channel, chat_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []imChatStat{}
	index := map[string]int{}
	for rows.Next() {
		var c imChatStat
		if err := rows.Scan(&c.Channel, &c.ChatID, &c.LastAt, &c.Inbound, &c.Outbound, &c.Failed, &c.LastInID); err != nil {
			continue
		}
		index[c.Channel+"|"+c.ChatID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 每个聊天的最后一条正文：一次查完所有聊天的最大 id，再在内存里并进去
	lastRows, err := s.db.Query(`SELECT channel, chat_id, direction, text, created_at
		FROM im_messages WHERE id IN (SELECT MAX(id) FROM im_messages GROUP BY channel, chat_id)`)
	if err != nil {
		return out, nil // 最后一条取不到不影响汇总（面板只是少了摘要）
	}
	defer lastRows.Close()
	for lastRows.Next() {
		var channel, chatID, dir, text, at string
		if err := lastRows.Scan(&channel, &chatID, &dir, &text, &at); err != nil {
			continue
		}
		if i, ok := index[channel+"|"+chatID]; ok {
			out[i].LastDir = dir
			out[i].LastText = imCutText(strings.TrimSpace(text), 120)
			if out[i].LastAt == "" {
				out[i].LastAt = at
			}
		}
	}
	return out, lastRows.Err()
}

// SetIMMessageSession 把一条流水的 session_id 补上。入站消息在解析出「投递给了
// 哪一版数字经理」之前就先落库了（流水要在处理前记，才能记下被拒的那种），所以
// 拿到会话 id 后回填一次——面板要按会话查「这条答复推到微信了没有」。
func (s *Store) SetIMMessageSession(id int64, sessionID string) error {
	if id <= 0 || sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE im_messages SET session_id=? WHERE id=?`, sessionID, id)
	if err != nil {
		log.Printf("[IM] 流水会话回填失败: %v", err)
	}
	return err
}

// IMMessagesBySession 取「某个会话相关的全部流水」（入站投递 + 出站推送），
// 供会话视图打来源徽标与送达状态用（阶段 C）。
func (s *Store) IMMessagesBySession(sessionID string, limit int) ([]imLogRow, error) {
	if sessionID == "" {
		return nil, errors.New("需要会话 id")
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT id, channel, chat_id, direction, kind, text, status, error, retries, created_at, seq
		FROM im_messages WHERE session_id=? ORDER BY id ASC LIMIT ?`, sessionID, limit)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []imLogRow{}
	for rows.Next() {
		var r imLogRow
		if err := rows.Scan(&r.ID, &r.Channel, &r.ChatID, &r.Direction, &r.Kind, &r.Text,
			&r.Status, &r.Error, &r.Retries, &r.At, &r.Seq); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// imSeqAt 是「会话里某条消息」的最小标识（seq + 时间）。
type imSeqAt struct {
	Seq int64  `json:"seq"`
	At  string `json:"at"`
}

// MessageSeqsByOrigin 取某个会话里指定来源的消息序号（C：会话内的 IM 来源徽标）。
// 入站消息在 im_messages 里没有会话序号（落库时还不知道投给哪条消息），但会话本身
// 带 origin='im'（im_hub.go 的 imOriginName），所以从这里取最准。
func (s *Store) MessageSeqsByOrigin(sessionID, origin string) ([]imSeqAt, error) {
	if sessionID == "" || origin == "" {
		return nil, errors.New("需要会话 id 与来源")
	}
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT seq, created_at FROM messages
		WHERE session_id=? AND origin=? ORDER BY seq ASC`, sessionID, origin)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []imSeqAt{}
	for rows.Next() {
		var x imSeqAt
		if err := rows.Scan(&x.Seq, &x.At); err != nil {
			continue
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// imLogInboundSeqs 取会话里「来自 IM」的消息序号（App 侧包装，失败按空算）。
func (a *App) imLogInboundSeqs(sessionID string) []imSeqAt {
	if a == nil || a.Store == nil {
		return nil
	}
	out, err := a.Store.MessageSeqsByOrigin(sessionID, imOriginName)
	if err != nil {
		log.Printf("[IM] 读会话 IM 来源失败: %v", err)
		return nil
	}
	return out
}

// ---------- App 侧包装：写流水 + 通知界面 ----------

// imLogAppend 记一条流水并广播（面板开着就增量刷新）。任何失败都只进日志：
// 观测不能反过来影响收发。
func (a *App) imLogAppend(e imLogEntry) int64 {
	if a == nil || a.Store == nil || e.ChatID == "" {
		return 0
	}
	if e.At == "" {
		e.At = imNow()
	}
	if e.Channel == "" {
		e.Channel = "weixin"
	}
	e.Text = imCutText(e.Text, imLogMaxChars)
	id := a.Store.AddIMMessage(e)
	if id > 0 {
		a.broadcast(Event{Type: "im", Data: map[string]any{
			"chat": e.ChatID, "direction": e.Direction, "status": e.Status,
		}})
	}
	return id
}

// imLogUpdate 回填出站结局并广播（状态徽标要跟着变）。
func (a *App) imLogUpdate(id int64, status, errText string) {
	if a == nil || a.Store == nil || id <= 0 {
		return
	}
	if err := a.Store.UpdateIMMessageStatus(id, status, errText); err != nil {
		return
	}
	a.broadcast(Event{Type: "im", Data: map[string]any{"status": status}})
}

// imLogSetSession 给入站流水补上「投递给了哪一版数字经理」（见 SetIMMessageSession）。
func (a *App) imLogSetSession(id int64, sessionID string) {
	if a == nil || a.Store == nil || id <= 0 || sessionID == "" {
		return
	}
	_ = a.Store.SetIMMessageSession(id, sessionID)
}

// imLogBySession 取某个会话相关的全部流水（会话视图的 IM 标记用）。
func (a *App) imLogBySession(sessionID string, limit int) ([]imLogRow, error) {
	if a == nil || a.Store == nil {
		return nil, errors.New("存储未就绪")
	}
	return a.Store.IMMessagesBySession(sessionID, limit)
}

// imLogChats 取聊天汇总（面板左列）。
func (a *App) imLogChats() []imChatStat {
	if a == nil || a.Store == nil {
		return nil
	}
	out, err := a.Store.IMMessageChats()
	if err != nil {
		log.Printf("[IM] 读流水汇总失败: %v", err)
		return nil
	}
	return out
}

// imLogMessages 取某个聊天的流水页。
func (a *App) imLogMessages(channel, chatID string, before int64, limit int) ([]imLogRow, error) {
	if a == nil || a.Store == nil {
		return nil, errors.New("存储未就绪")
	}
	if channel == "" {
		channel = "weixin"
	}
	return a.Store.IMMessages(channel, chatID, before, limit)
}

// imLogUnread 取一个聊天的未读数（读失败按 0 算：面板少个红点，不影响任何收发）。
func (a *App) imLogUnread(channel, chatID string, after int64) int {
	if a == nil || a.Store == nil {
		return 0
	}
	n, err := a.Store.IMUnreadCount(channel, chatID, after)
	if err != nil {
		return 0
	}
	return n
}
