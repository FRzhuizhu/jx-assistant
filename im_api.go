package main

// IM 接入的 HTTP 接口（设置页与运维脚本用；也是 AI 侧 call_api 的入口之一）。
//
// 口径：
//   - 读状态开放给 AI（permReadOnly），**扫码/解绑/发测试消息不对 AI 开放**——
//     那几步等于「替用户绑定一个外部账号并对外发消息」，必须由人点。
//   - 开关与白名单在 config.json 的 im 段（会落盘），token 在 data/im_weixin.json
//     （DPAPI），两者都不经这些接口回传明文。

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// registerIMRoutes 注册 IM 相关路由（registerRoutes 里一行调用）。
func (a *App) registerIMRoutes(mux *http.ServeMux) {
	a.apiRoute(mux, "GET /api/im/status", a.handleIMStatus, apiMeta{
		Summary: "IM 接入状态（渠道/账号掩码/连接态/最近收发/最近错误）",
		Params:  "无",
		Perm:    permReadOnly,
	})
	a.apiRoute(mux, "POST /api/im/settings", a.handleIMSettings, apiMeta{
		Summary: "改 IM 接入开关与白名单（关掉即停轮询）",
		Params:  `{"enabled":bool,"weixinEnabled":bool,"allow":["ilink_user_id"],"testUser":"id"}`,
		Perm:    permFull,
		Danger:  "改 IM 接入配置会立即生效：关掉即断开微信长轮询，白名单决定谁能通过 IM 给助手下指令",
	})
	a.apiRoute(mux, "GET /api/im/chats", a.handleIMChats, apiMeta{
		Summary: "IM 聊天清单（最后一条摘要/收发条数/失败数/未读/已推序号）",
		Params:  "无",
		Perm:    permReadOnly,
	})
	a.apiRoute(mux, "GET /api/im/messages", a.handleIMMessages, apiMeta{
		Summary: "IM 聊天流水（方向/时间/推送状态/失败原因/重试次数）",
		Params:  "chat=聊天id（必填）、channel=weixin（默认）、before=翻页锚点（流水 id）、limit=条数（默认100，上限500）",
		Perm:    permReadOnly,
	})
	a.apiRoute(mux, "GET /api/im/session-marks", a.handleIMSessionMarks, apiMeta{
		Summary: "某个会话的 IM 标记（哪些消息来自微信、哪些答复推到了微信、送达状态）",
		Params:  "session=会话 id（必填）、limit=条数（默认500，上限2000）",
		Perm:    permReadOnly,
	})
	// 裸路由（不走 apiRoute）：这几步要么是界面动作、要么需要人扫码，
	// 一律不登记进 AI 的接口目录；写操作在 handler 内另有同源 Origin 闸。
	mux.HandleFunc("POST /api/im/remark", a.handleIMRemark)
	mux.HandleFunc("POST /api/im/read", a.handleIMRead)
	mux.HandleFunc("POST /api/im/weixin/qr", a.handleIMWeixinQr)
	mux.HandleFunc("GET /api/im/weixin/qr", a.handleIMWeixinQrPoll) // 轮询：只读，不加闸
	mux.HandleFunc("POST /api/im/weixin/logout", a.handleIMWeixinLogout)
	mux.HandleFunc("POST /api/im/weixin/reconnect", a.handleIMWeixinReconnect)
	mux.HandleFunc("POST /api/im/weixin/test", a.handleIMWeixinTest)
}

// imChatView 把「流水汇总 + 绑定 + 白名单」拼成面板左列的一行（接口与前端共用）。
// 几个派生字段：masked=打码 id（拿不到昵称时的兜底显示）、display=备注优先的显示名、
// allowed=在白名单里（否则消息一律被拒）、pushedUpTo=已推到的数字经理序号、
// readUpTo=已读到哪条流水、unread=该聊天未读条数、failed=出站失败+静默跳过（「仅失败」筛选）。
type imChatView struct {
	Channel    string `json:"channel"`
	ChatID     string `json:"chatId"`
	Masked     string `json:"masked"`
	Remark     string `json:"remark,omitempty"`
	Display    string `json:"display"`
	Allowed    bool   `json:"allowed"`
	SessionID  string `json:"sessionId,omitempty"`
	PushedUpTo int64  `json:"pushedUpTo"`
	ReadUpTo   int64  `json:"readUpTo"`
	Unread     int    `json:"unread"`
	LastAt     string `json:"lastAt,omitempty"`
	LastText   string `json:"lastText,omitempty"`
	LastDir    string `json:"lastDir,omitempty"`
	Inbound    int    `json:"inbound"`
	Outbound   int    `json:"outbound"`
	Failed     int    `json:"failed"`
}

// handleIMChats 返回面板左列：流水里出现过的聊天 ∪ 绑定表里的聊天。
// 流水补「最后一条/收发条数/失败数」，绑定补「备注/已推序号/已读位标」，配置补白名单。
func (a *App) handleIMChats(w http.ResponseWriter, r *http.Request) {
	if a.Cfg == nil {
		writeErr(w, http.StatusInternalServerError, "配置未加载")
		return
	}
	stats := a.imLogChats()
	binds := a.imBindingList()
	// 最近说过话的排前面；只有绑定、没有流水的聊天排后面（按绑定更新时间）。
	sort.SliceStable(stats, func(i, j int) bool { return stats[i].LastAt > stats[j].LastAt })
	seen := map[string]bool{}
	out := make([]imChatView, 0, len(stats)+len(binds))
	add := func(st imChatStat) {
		key := st.Channel + "|" + st.ChatID
		if st.ChatID == "" || seen[key] {
			return
		}
		seen[key] = true
		v := imChatView{
			Channel: st.Channel, ChatID: st.ChatID, Masked: maskIMID(st.ChatID),
			Allowed: a.Cfg.imAllowWeixin(st.ChatID), LastAt: st.LastAt, LastText: st.LastText,
			LastDir: st.LastDir, Inbound: st.Inbound, Outbound: st.Outbound, Failed: st.Failed,
		}
		for _, b := range binds {
			if b.Channel == st.Channel && b.ChatID == st.ChatID {
				v.Remark, v.SessionID, v.PushedUpTo, v.ReadUpTo = b.Remark, b.SessionID, b.LastSeq, b.ReadUpTo
				break
			}
		}
		v.Display = v.Remark
		if v.Display == "" {
			v.Display = v.Masked
		}
		v.Unread = a.imLogUnread(st.Channel, st.ChatID, v.ReadUpTo)
		out = append(out, v)
	}
	for _, st := range stats {
		add(st)
	}
	for _, b := range binds {
		add(imChatStat{Channel: b.Channel, ChatID: b.ChatID, LastAt: b.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   a.Cfg.imOn(),
		"channels":  a.imStatuses(),
		"whitelist": a.Cfg.imAllowCount(),
		"chats":     out,
	})
}

// handleIMMessages 返回某个聊天的流水页（按时间正序；before 用于向上翻历史）。
func (a *App) handleIMMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	chat := strings.TrimSpace(q.Get("chat"))
	if chat == "" {
		writeErr(w, http.StatusBadRequest, "需要 chat 参数（聊天 id）")
		return
	}
	channel := strings.TrimSpace(q.Get("channel"))
	if channel == "" {
		channel = "weixin"
	}
	limit := 0
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = v
	}
	before := int64(0)
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		before = v
	}
	rows, err := a.imLogMessages(channel, chat, before, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读流水失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel": channel, "chat": chat, "messages": rows, "before": before,
	})
}

// requireUIAction 只放行「界面里点的动作」：要求请求带同源 Origin。
// 为什么这两个接口要这一道（而不像 /api/im/weixin/qr 那样裸着）：
//   - 不登记 apiMeta 只是让 call_api 的目录里看不到（apiMetaFor 查不到就拒），
//     但 withCORS 明确放行「无 Origin 的非浏览器客户端」，AI 用 run_command
//     发一条 curl 就能绕过去改界面状态；
//   - 备注与已读位标没有外部副作用，可也不该给 AI 留一条改界面状态的口子；
//   - 浏览器里的 fetch（含 Electron/WebView 里的本地 GUI）同源 POST 必带
//     Origin，所以这一道对真人操作零影响。
func requireUIAction(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || !sameOrigin(origin, r.Host) {
		writeErr(w, http.StatusForbidden, "该操作只能在界面里点击（缺少同源 Origin）")
		return false
	}
	return true
}

// handleIMRemark 设/清联系人本地备注（微信协议不给昵称，只能靠用户自己起名）。
// 刻意不走 apiRoute：这是给人点的界面动作，不登记给 AI。
func (a *App) handleIMRemark(w http.ResponseWriter, r *http.Request) {
	if !requireUIAction(w, r) {
		return
	}
	var body struct {
		Channel string `json:"channel"`
		Chat    string `json:"chat"`
		Remark  string `json:"remark"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	channel := strings.TrimSpace(body.Channel)
	if channel == "" {
		channel = "weixin"
	}
	chat := strings.TrimSpace(body.Chat)
	if chat == "" {
		writeErr(w, http.StatusBadRequest, "需要 chat（聊天 id）")
		return
	}
	if len([]rune(strings.TrimSpace(body.Remark))) > 40 {
		writeErr(w, http.StatusBadRequest, "备注最多 40 个字")
		return
	}
	b, ok := a.imSetRemark(channel, chat, body.Remark)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个聊天：等对方先发一条消息，或从面板里打开它")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "remark": b.Remark})
}

// handleIMRead 推进某个聊天的已读位标（面板未读红点）。同样不登记给 AI，
// 且同样要求同源 Origin（见 requireUIAction）。
func (a *App) handleIMRead(w http.ResponseWriter, r *http.Request) {
	if !requireUIAction(w, r) {
		return
	}
	var body struct {
		Channel string `json:"channel"`
		Chat    string `json:"chat"`
		UpTo    int64  `json:"upTo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	channel := strings.TrimSpace(body.Channel)
	if channel == "" {
		channel = "weixin"
	}
	chat := strings.TrimSpace(body.Chat)
	if chat == "" || body.UpTo <= 0 {
		writeErr(w, http.StatusBadRequest, "需要 chat 与 upTo（读到哪条流水）")
		return
	}
	a.imMarkRead(channel, chat, body.UpTo)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleIMSessionMarks 返回「某个会话相关的 IM 流水」，供会话视图打标：
//   - direction=in  → 这条用户消息是从微信发来的（打「微信 · 昵称 · 时间」徽标）
//   - direction=out → 这条答复推到了微信（带 sent/failed/skipped 与失败原因）
//
// 只回 seq/方向/状态/时间，不回正文——正文以会话消息为准，避免第二份真源。
func (a *App) handleIMSessionMarks(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if sessionID == "" {
		writeErr(w, http.StatusBadRequest, "需要 session 参数（会话 id）")
		return
	}
	limit := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = v
	}
	rows, err := a.imLogBySession(sessionID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读 IM 标记失败："+err.Error())
		return
	}
	// 备注跟着标记一起回：徽标上要显示「微信 · 产品·王工 · 21:12」
	remarks := map[string]string{}
	for _, b := range a.imBindingList() {
		if b.Remark != "" {
			remarks[b.Channel+"|"+b.ChatID] = b.Remark
		}
	}
	type mark struct {
		Seq       int64  `json:"seq"`
		Direction string `json:"direction"`
		Status    string `json:"status"`
		Error     string `json:"error,omitempty"`
		Retries   int    `json:"retries,omitempty"`
		At        string `json:"at"`
		ChatID    string `json:"chatId"`
		Name      string `json:"name"` // 备注 > 打码 id
	}
	out := make([]mark, 0, len(rows))
	for _, r := range rows {
		if r.Seq <= 0 { // 命令回执/即时话没有会话序号，标不到具体消息上
			continue
		}
		name := remarks[r.Channel+"|"+r.ChatID]
		if name == "" {
			name = maskIMID(r.ChatID)
		}
		out = append(out, mark{
			Seq: r.Seq, Direction: r.Direction, Status: r.Status, Error: r.Error,
			Retries: r.Retries, At: r.At, ChatID: r.ChatID, Name: name,
		})
	}
	// 入站消息：流水里没有会话序号（落库时还不知道投给哪条消息），改从会话本身取
	// origin='im' 的序号——阶段 3 投递时打的标记，这里正好复用。
	seen := map[int64]bool{}
	for _, m := range out {
		seen[m.Seq] = true
	}
	for _, x := range a.imLogInboundSeqs(sessionID) {
		if seen[x.Seq] {
			continue
		}
		seen[x.Seq] = true
		out = append(out, mark{Seq: x.Seq, Direction: imDirIn, Status: imStatusAccepted, At: x.At})
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sessionID, "marks": out})
}

func (a *App) handleIMStatus(w http.ResponseWriter, r *http.Request) {
	if a.Cfg == nil {
		writeErr(w, http.StatusInternalServerError, "配置未加载")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  a.Cfg.imOn(),
		"channels": a.imStatuses(),
		"notes": []string{
			"同一微信账号只能有一个轮询进程：本机若还跑着 OpenClaw/Tobs 的微信通道，必须关掉",
			"主动消息（无会话凭据时）可能被回收，任务结果建议在交互后的一段时间内送达",
			"登录凭据失效只能重新扫码，没有自动续期",
		},
	})
}

func (a *App) handleIMSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled       *bool    `json:"enabled"`
		WeixinEnabled *bool    `json:"weixinEnabled"`
		Allow         []string `json:"allow"`
		TestUser      string   `json:"testUser"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if body.Enabled != nil {
		a.Cfg.IM.Enabled = *body.Enabled
	}
	if body.WeixinEnabled != nil {
		a.Cfg.IM.Weixin.Enabled = *body.WeixinEnabled
	}
	if body.Allow != nil {
		// 与自动信任（im_allow.go）抢同一字段：取同一把锁，免得「自动写入」
		// 与「界面保存」各写一份、后写的把先写的覆盖掉。
		imAllowWriteMu.Lock()
		a.Cfg.IM.Weixin.Allow = imNormalizeAllow(body.Allow)
		imAllowWriteMu.Unlock()
	}
	if body.TestUser != "" {
		a.Cfg.IM.Weixin.TestUser = strings.TrimSpace(body.TestUser)
	}
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}
	a.syncIM()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"enabled":  a.Cfg.imOn(),
		"channels": a.imStatuses(),
	})
}

// handleIMWeixinQr 生成绑定二维码（需人扫；写操作，过 requireUIAction 闸）。
func (a *App) handleIMWeixinQr(w http.ResponseWriter, r *http.Request) {
	if !requireUIAction(w, r) {
		return
	}
	snap, err := a.weixinChannelForAPI().BeginLogin()
	if err != nil {
		writeErr(w, http.StatusBadGateway, weixinErrorOf(err))
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleIMWeixinQrPoll 是扫码进度的**轮询**接口，刻意不加 requireUIAction：
//   - 它只读（不生成二维码、不改凭据），没有外部副作用；
//   - 浏览器对**同源 GET 不发 Origin**，加闸会把界面自己的轮询打成 403，
//     扫码进度当场停更——这是加闸唯一会误伤界面的地方，故单独说明。
func (a *App) handleIMWeixinQrPoll(w http.ResponseWriter, r *http.Request) {
	snap, err := a.weixinChannelForAPI().LoginStatus(r.URL.Query().Get("id"), r.URL.Query().Get("verify"))
	if err != nil {
		writeErr(w, http.StatusNotFound, weixinErrorOf(err))
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleIMWeixinLogout 解绑：删凭据、清绑定表、关掉渠道开关并落盘。
// 换账号后旧会话不该继续收到答复，所以绑定表一并清掉（下次发消息自动重建）。
func (a *App) handleIMWeixinLogout(w http.ResponseWriter, r *http.Request) {
	if !requireUIAction(w, r) {
		return
	}
	a.weixinChannelForAPI().Logout()
	// 换账号后旧会话不该继续收到答复：绑定表一并清掉（下次发消息自动重建）。
	a.imUnbindChannel("weixin")
	a.Cfg.IM.Weixin.Enabled = false
	_ = a.Cfg.Save(a.CfgPath)
	a.syncIM()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": a.imStatuses()})
}

// handleIMWeixinReconnect 手动重连：打开开关、落盘、重启渠道（长轮询重新拉起）。
func (a *App) handleIMWeixinReconnect(w http.ResponseWriter, r *http.Request) {
	if !requireUIAction(w, r) {
		return
	}
	a.Cfg.IM.Enabled = true
	a.Cfg.IM.Weixin.Enabled = true
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}
	a.restartIM("weixin")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": a.imStatuses()})
}

// imTestNoContextMsg 是没有 context_token 时的拒绝文案：平台对「凭空主动消息」
// 会 200 接受但不投递，与其假装成功，不如把原因直接说清楚（真机实测：没凭据
// 的两条一条都没到手机）。
const imTestNoContextMsg = "该会话还没有 context_token：微信不投递没有上下文的主动消息。" +
	"请先在微信里给机器人发一句话（入站消息会带来 context_token），再点测试"

// imTestReply 是 /test 的成功响应。ok 只代表「请求被平台接受」：messageIds 里
// 是本端生成的 client_id（平台通常不回真实消息 id），所以把 submitted /
// platformReceipt / contextToken 分开写清楚，别让 ok:true 被当成「已送达」。
func imTestReply(ids []string) map[string]any {
	return map[string]any{
		"ok":              true,
		"submitted":       true,
		"platformReceipt": false,
		"contextToken":    true,
		"messageIds":      ids,
		"note": "已提交给微信（请求被平台接受）；平台未回真实消息 id，" +
			"messageIds 里是本端生成的 client_id。是否送达以手机为准。",
	}
}

// handleIMWeixinTest 发一条测试消息（会真的给真人发微信，副作用最重，故过闸）。
// 没 context_token 一律 409 拒发：平台对凭空主动消息会 200 接受但不投递。
func (a *App) handleIMWeixinTest(w http.ResponseWriter, r *http.Request) {
	if !requireUIAction(w, r) {
		return
	}
	var body struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // 允许空请求体（默认给自己发一条）
	ch := a.weixinChannelForAPI()
	to := strings.TrimSpace(body.To)
	if to == "" {
		if acc := ch.currentAccount(); acc != nil {
			to = acc.OwnerID
		}
	}
	if to == "" {
		writeErr(w, http.StatusBadRequest, "没有收件人：请先扫码绑定，或显式传 to")
		return
	}
	// 没有 context_token 就不发：平台会 200 接受但不投递，发出去只会让人对着
	// ok:true 干等。
	if !ch.hasContextToken(to) {
		writeErr(w, http.StatusConflict, imTestNoContextMsg)
		return
	}
	text := body.Text
	if strings.TrimSpace(text) == "" {
		text = "来自坚祥数字助理的测试消息（" + imNow() + "）。收到这条说明微信通道双向可达。"
	}
	// /test 直接调渠道的 SendText（不经过 imPushWorker），所以出站日志要在这里补：
	// 排查「点测试到底发出去没有」时，日志里必须能看到这一行。
	log.Printf("[IM] 出站（test）→ %s ctx=yes", maskIMID(to))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ids, err := ch.SendText(ctx, IMConversation{Channel: "weixin", ChatID: to, ChatType: "dm", UserID: to}, text)
	if err != nil {
		writeErr(w, http.StatusBadGateway, weixinErrorOf(err))
		return
	}
	writeJSON(w, http.StatusOK, imTestReply(ids))
}

// weixinChannelForAPI 取微信渠道；不存在时按需建一个（保证扫码/测试在「还没
// 开开关」时也能用——扫码成功会自动把开关打开）。
func (a *App) weixinChannelForAPI() *weixinChannel {
	if ch := a.imChannelOf("weixin"); ch != nil {
		if wx, ok := ch.(*weixinChannel); ok {
			return wx
		}
	}
	wx := newWeixinChannel(a)
	a.imSetChannel("weixin", wx)
	return wx
}

// weixinErrorOf 把内部错误转成给人看的一句话（状态页与接口共用同一套文案）。
func weixinErrorOf(err error) string {
	if err == nil {
		return ""
	}
	var we *weixinErr
	if errors.As(err, &we) {
		if txt := weixinErrorText(we.Code, we.ProviderCode); txt != "" {
			return txt + "：" + we.Msg
		}
		return we.Msg
	}
	return err.Error()
}

// imEnableWeixin 打开 IM 与微信渠道的开关并落盘（扫码成功后调用：能扫码就说明
// 用户要它，不该再让人多点一次开关）。
func (a *App) imEnableWeixin() {
	if a.Cfg == nil {
		return
	}
	a.Cfg.IM.Enabled = true
	a.Cfg.IM.Weixin.Enabled = true
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		log.Printf("[IM] 保存 IM 开关失败: %v", err)
	}
}
