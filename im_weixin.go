package main

// 微信 iLink（社区叫 ClawBot）渠道：扫码绑定一个微信机器人身份，长轮询收发。
//
// 协议来源：@botharness/im-provider 的 src/channels/weixin/（MIT，见
// THIRD-PARTY-NOTICES.md），并与 hermesclaw 的独立实现互证。腾讯没有公开文档，
// 所以协议常量、字段名、错误码全部集中在本文件顶部——平台改字段时只改这里。
//
// 三条已知硬约束（设置页照抄给用户）：
//  1. 同一微信账号只能一个 poller（第二个会 403/丢消息）——本机若还跑着
//     OpenClaw/Tobs 的微信通道必须关掉；
//  2. 主动消息可能被拒（带 context_token 成功率高）；
//  3. token 失效（ret=-14）只能重新扫码，没有 refresh。
//
// 收发模型：getupdates 是 35 秒长轮询（客户端主动出网，**不需要公网回调**），
// 返回的 get_updates_buf 是下一次的游标，必须落盘——重启后从这里续读，否则
// 会重复处理旧消息。去重另用 seen 集合（平台也会重投）。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	weixinQRBaseURL      = "https://ilinkai.weixin.qq.com/"
	weixinAppID          = "bot"
	weixinChannelVersion = "2.4.6"
	weixinBotType        = 3
	weixinMaxChars       = 1800 // 单条文本上限（超过平台会拒或截断）
	weixinPollTimeout    = 35 * time.Second
	weixinAPITimeout     = 15 * time.Second
	weixinLoginTimeout   = 5 * time.Minute
	weixinSeenCap        = 512 // 去重集合上限（滚动淘汰最旧）
	weixinMaxFailures    = 3   // 连续失败达到即进入 failed（等用户手动重连或重扫码）
)

// iLink-App-ClientVersion = (2 << 16) | (4 << 8) | 6，与参考实现的 2.4.6 对齐。
const weixinClientVersion = (2 << 16) | (4 << 8) | 6

// ---------- 协议载荷 ----------

// weixinFlexID 兼容平台把 id 给成数字或字符串两种写法（message_id 是 64 位，
// 走 float64 会掉精度，所以统一收成字符串）。
type weixinFlexID string

func (f *weixinFlexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = weixinFlexID(v)
		return nil
	}
	*f = weixinFlexID(s)
	return nil
}

type weixinMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AESKey            string `json:"aes_key"`
	EncryptType       int    `json:"encrypt_type"`
	FullURL           string `json:"full_url,omitempty"`
}

type weixinTextItem struct {
	Text string `json:"text"`
}

type weixinVoiceItem struct {
	Text  string       `json:"text"`
	Media *weixinMedia `json:"media,omitempty"`
}

type weixinFileItem struct {
	FileName string       `json:"file_name"`
	Len      string       `json:"len"`
	Media    *weixinMedia `json:"media,omitempty"`
}

type weixinImageItem struct {
	Media *weixinMedia `json:"media,omitempty"`
}

type weixinItem struct {
	Type      int              `json:"type"`
	TextItem  *weixinTextItem  `json:"text_item,omitempty"`
	VoiceItem *weixinVoiceItem `json:"voice_item,omitempty"`
	ImageItem *weixinImageItem `json:"image_item,omitempty"`
	FileItem  *weixinFileItem  `json:"file_item,omitempty"`
	VideoItem *weixinImageItem `json:"video_item,omitempty"`
}

// 入站消息的 item 类型（参考实现：1=文本 2=图片 3=语音 4=文件 5=视频）。
const (
	weixinItemText  = 1
	weixinItemImage = 2
	weixinItemVoice = 3
	weixinItemFile  = 4
	weixinItemVideo = 5
)

type weixinMessage struct {
	MessageID    weixinFlexID `json:"message_id"`
	ClientID     string       `json:"client_id"`
	FromUserID   string       `json:"from_user_id"`
	ToUserID     string       `json:"to_user_id"`
	MessageType  int          `json:"message_type"`  // 1=用户发来的消息；2=机器人自己发的（回显，跳过）
	MessageState int          `json:"message_state"`
	GroupID      string       `json:"group_id"`
	ContextToken string       `json:"context_token"`
	RunID        string       `json:"run_id"`
	ItemList     []weixinItem `json:"item_list"`
}

type weixinUpdatesResp struct {
	Ret           *json.Number    `json:"ret"`
	Errcode       *json.Number    `json:"errcode"`
	Msgs          []weixinMessage `json:"msgs"`
	GetUpdatesBuf string          `json:"get_updates_buf"`
}

type weixinLoginResp struct {
	Status        string `json:"status"`
	BotToken      string `json:"bot_token"`
	IlinkBotID    string `json:"ilink_bot_id"`
	IlinkUserID   string `json:"ilink_user_id"`
	BaseURL       string `json:"baseurl"`
	RedirectHost  string `json:"redirect_host"`
	QRCode        string `json:"qrcode"`
	QRCodeContent string `json:"qrcode_img_content"`
}

// 扫码状态机（参考实现 LOGIN_STATUSES 全集）。
const (
	weixinQRWait          = "wait"
	weixinQRScaned        = "scaned"
	weixinQRConfirmed     = "confirmed"
	weixinQRedirect       = "scaned_but_redirect"
	weixinQRNeedVerify    = "need_verifycode"
	weixinQRVerifyBlocked = "verify_code_blocked"
	weixinQRExpired       = "expired"
	weixinQRBinded        = "binded_redirect"
)

// ---------- 错误 ----------

// weixinErr 是协议层错误的统一形态：code 给状态页与文案映射用（人话化在
// im_weixinErrorText），providerCode 是平台原始错误码（ret/errcode）。
type weixinErr struct {
	Code         string
	ProviderCode string
	Msg          string
	Timeout      bool
}

func (e *weixinErr) Error() string {
	if e.ProviderCode != "" {
		return e.Msg + "（平台错误码 " + e.ProviderCode + "）"
	}
	return e.Msg
}

func weixinErrOf(code, msg string) *weixinErr { return &weixinErr{Code: code, Msg: msg} }

// weixinErrorText 把协议错误码转成人话（设置页与 IM 回执共用，别再各自拼）。
func weixinErrorText(code, providerCode string) string {
	switch code {
	case "stale-token":
		return "微信登录凭据已失效，请重新扫码"
	case "updates-rejected":
		return "微信消息同步被拒绝，请检查该账号是否在别处登录了另一个机器人实例"
	case "timeout":
		return "连接微信超时，稍后自动重试"
	case "unauthorized":
		return "微信凭据无效，请重新扫码"
	case "network":
		return "连不上微信服务，稍后自动重试"
	case "rate-limited":
		return "微信服务限流，稍后自动重试"
	case "already-bound":
		return "该微信账号已绑定，请先在别处解绑"
	case "expired":
		return "二维码已过期，请重新生成"
	case "verify-blocked":
		return "配对码多次错误，请重新生成二维码"
	case "incomplete-login":
		return "微信授权成功，但返回的账号凭据不完整，请重试"
	default:
		if providerCode == "-14" {
			return "微信登录凭据已失效，请重新扫码"
		}
		return "微信服务返回错误" + strings.TrimSuffix(fmt.Sprintf("（%s）", providerCode), "（）")
	}
}

// ---------- HTTP 调用 ----------

func weixinCommonHeaders(token string) map[string]string {
	h := map[string]string{
		"content-type":            "application/json",
		"iLink-App-Id":            weixinAppID,
		"iLink-App-ClientVersion": strconv.Itoa(weixinClientVersion),
	}
	// X-WECHAT-UIN：参考实现用随机 uint32 的十进制串再 base64，平台当设备标识用。
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		dec := strconv.FormatUint(uint64(binary.BigEndian.Uint32(b[:])), 10)
		h["X-WECHAT-UIN"] = base64.StdEncoding.EncodeToString([]byte(dec))
	}
	if token != "" {
		h["AuthorizationType"] = "ilink_bot_token"
		h["Authorization"] = "Bearer " + token
	}
	return h
}

func weixinBaseInfo() map[string]any {
	return map[string]any{
		"channel_version": weixinChannelVersion,
		"bot_agent":       "jx-assistant/" + appVersion,
	}
}

// weixinCall 发一次协议请求，返回原始响应体。transport/HTTP 层错误归一成
// *weixinErr；平台业务错误（ret/errcode）由调用方按需检查（有的接口负值是
// 正常语义，比如长轮询超时）。
func weixinCall(ctx context.Context, baseURL, endpoint string, body any, token string, timeout time.Duration) ([]byte, error) {
	base := strings.TrimSuffix(baseURL, "/")
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, weixinErrOf("bad-body", "请求内容无法序列化")
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+endpoint, payload)
	if err != nil {
		return nil, weixinErrOf("bad-request", "请求地址无效")
	}
	for k, v := range weixinCommonHeaders(token) {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &weixinErr{Code: "timeout", Msg: "请求微信服务超时", Timeout: true}
		}
		return nil, &weixinErr{Code: "network", Msg: "连不上微信服务: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, weixinErrOf("network", "读取微信响应失败")
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return raw, weixinErrOf("unauthorized", "微信凭据被拒绝")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return raw, weixinErrOf("rate-limited", "微信服务限流")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, weixinErrOf("http-status", fmt.Sprintf("微信服务返回 HTTP %d", resp.StatusCode))
	}
	return raw, nil
}

// weixinCallGet 是 get_qrcode_status 用的 GET 版本（长轮询，超时按空轮处理）。
func weixinCallGet(ctx context.Context, baseURL, endpoint string, query url.Values, timeout time.Duration) ([]byte, error) {
	base := strings.TrimSuffix(baseURL, "/")
	u := base + "/" + endpoint
	if enc := query.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, weixinErrOf("bad-request", "请求地址无效")
	}
	for k, v := range weixinCommonHeaders("") {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &weixinErr{Code: "timeout", Msg: "请求微信服务超时", Timeout: true}
		}
		return nil, &weixinErr{Code: "network", Msg: "连不上微信服务: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, weixinErrOf("network", "读取微信响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, weixinErrOf("http-status", fmt.Sprintf("微信服务返回 HTTP %d", resp.StatusCode))
	}
	return raw, nil
}

// weixinRejectCode 抽平台业务错误码（ret 优先，其次 errcode）；0/空 = 正常。
func weixinRejectCode(raw []byte) string {
	var probe struct {
		Ret     *json.Number `json:"ret"`
		Errcode *json.Number `json:"errcode"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return ""
	}
	for _, n := range []*json.Number{probe.Ret, probe.Errcode} {
		if n == nil {
			continue
		}
		s := strings.TrimSpace(n.String())
		if s == "" || s == "0" {
			continue
		}
		return s
	}
	return ""
}

// ---------- 持久化 ----------

// weixinAccount 是扫码绑定后的凭据（token 落盘前 DPAPI 加密）。
type weixinAccount struct {
	AccountID string `json:"accountId"`
	OwnerID   string `json:"ownerId"`
	Token     string `json:"token"`
	BaseURL   string `json:"baseUrl,omitempty"`
	BoundAt   string `json:"boundAt"`
}

// weixinCredFile 是 data/im_weixin.json 的结构。
type weixinCredFile struct {
	Account *weixinAccount `json:"account,omitempty"`
}

// weixinStateFile 是 data/im_weixin_state.json：长轮询游标、去重集合、每个
// 会话的 context_token（回信要原样带回）。
type weixinStateFile struct {
	GetUpdatesBuf string            `json:"getUpdatesBuf,omitempty"`
	Seen          []string          `json:"seen,omitempty"`
	ContextTokens map[string]string `json:"contextTokens,omitempty"`
}

func (c *weixinChannel) credPath() string {
	return filepathAbsOf(filepath.Join(c.app.Cfg.DataDir, "im_weixin.json"))
}

func (c *weixinChannel) statePath() string {
	return filepathAbsOf(filepath.Join(c.app.Cfg.DataDir, "im_weixin_state.json"))
}

// loadAccount 读凭据（token 透明解密）。文件不存在或损坏都当「未绑定」。
func (c *weixinChannel) loadAccount() *weixinAccount {
	raw, err := os.ReadFile(c.credPath())
	if err != nil {
		return nil
	}
	var f weixinCredFile
	if json.Unmarshal(raw, &f) != nil || f.Account == nil || f.Account.Token == "" || f.Account.AccountID == "" {
		return nil
	}
	f.Account.Token = unprotectSecret(f.Account.Token)
	return f.Account
}

func (c *weixinChannel) saveAccount(acc *weixinAccount) error {
	stored := *acc
	var encErr error
	stored.Token, encErr = protectSecret(stored.Token)
	if encErr != nil {
		log.Printf("[IM] 微信 token 加密失败（按明文落盘，不影响使用）: %v", encErr)
	}
	raw, _ := json.MarshalIndent(weixinCredFile{Account: &stored}, "", " ")
	if err := os.MkdirAll(filepath.Dir(c.credPath()), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(c.credPath(), raw)
}

func (c *weixinChannel) clearAccount() {
	_ = os.Remove(c.credPath())
	_ = os.Remove(c.statePath())
}

func (c *weixinChannel) loadRuntimeState() {
	raw, err := os.ReadFile(c.statePath())
	if err != nil {
		return
	}
	var st weixinStateFile
	if json.Unmarshal(raw, &st) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor = st.GetUpdatesBuf
	for _, id := range st.Seen {
		if id == "" {
			continue
		}
		c.seen[id] = true
		c.seenOrder = append(c.seenOrder, id)
	}
	for k, v := range st.ContextTokens {
		if k != "" && v != "" {
			c.ctxTokens[k] = v
		}
	}
}

// saveRuntimeState 落盘游标/去重集/会话凭据（调用方不必持锁）。
func (c *weixinChannel) saveRuntimeState() {
	c.mu.Lock()
	st := weixinStateFile{
		GetUpdatesBuf: c.cursor,
		Seen:          append([]string(nil), c.seenOrder...),
		ContextTokens: make(map[string]string, len(c.ctxTokens)),
	}
	for k, v := range c.ctxTokens {
		st.ContextTokens[k] = v
	}
	c.mu.Unlock()
	raw, _ := json.MarshalIndent(st, "", " ")
	if err := os.MkdirAll(filepath.Dir(c.statePath()), 0o755); err != nil {
		return
	}
	if err := writeFileAtomic(c.statePath(), raw); err != nil {
		log.Printf("[IM] 微信状态落盘失败: %v", err)
	}
}

// ---------- 渠道 ----------

type weixinChannel struct {
	app     *App
	baseURL string

	mu         sync.Mutex
	cancel     context.CancelFunc
	state      string
	errText    string
	errCode    string
	account    *weixinAccount
	since      time.Time
	lastIn     time.Time
	lastInText string
	lastSend   time.Time
	lastPush   IMPushStatus
	cursor     string
	seen       map[string]bool
	seenOrder  []string
	ctxTokens  map[string]string

	loginMu sync.Mutex
	login   *weixinLogin
}

func newWeixinChannel(a *App) *weixinChannel {
	c := &weixinChannel{
		app:       a,
		baseURL:   weixinQRBaseURL,
		state:     "waiting_scan", // 没凭据就是等扫码；有凭据才谈连接（下面按实际账号覆盖）
		seen:      map[string]bool{},
		ctxTokens: map[string]string{},
	}
	if acc := c.loadAccount(); acc != nil {
		c.account = acc
		c.state = "connecting"
		if acc.BaseURL != "" {
			c.baseURL = acc.BaseURL
		}
	}
	c.loadRuntimeState()
	return c
}

func (c *weixinChannel) Name() string { return "weixin" }

func (c *weixinChannel) Status() IMChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := IMChannelStatus{
		Channel:       "weixin",
		Enabled:       c.app.Cfg.imWeixinOn(),
		State:         c.state,
		LastError:     c.errText,
		LastErrorCode: c.errCode,
		Allow:         c.app.Cfg.imAllowCount(),
	}
	if c.account != nil {
		st.AccountID = maskIMID(c.account.AccountID)
		st.OwnerID = maskIMID(c.account.OwnerID)
	}
	if !c.since.IsZero() {
		st.Since = c.since.Format("2006-01-02 15:04:05")
	}
	if !c.lastIn.IsZero() {
		st.LastInboundAt = c.lastIn.Format("2006-01-02 15:04:05")
		st.LastInbound = c.lastInText
	}
	if !c.lastSend.IsZero() {
		st.LastSendAt = c.lastSend.Format("2006-01-02 15:04:05")
	}
	if c.account != nil {
		st.ContextToken = c.ctxTokens[c.account.OwnerID] != ""
	}
	if c.lastPush.At != "" {
		p := c.lastPush
		st.LastPush = &p
	}
	if c.state == "" {
		st.State = "disabled"
	}
	return st
}

// hasContextToken 该会话是否已有上下文凭据（最近一次入站带来的 context_token）。
// 没有它，平台对「主动消息」照样 200 接受但不投递——/test 与出站日志都靠这个
// 判断来把话说清楚，而不是让人对着一个 ok:true 干等。
func (c *weixinChannel) hasContextToken(userID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctxTokens[userID] != ""
}

// notePush 记一条推送回执（imPushWorker 在成功/失败时调；seq 0 = 命令回执这类
// 不占序号的话，照样记——排查「回执到底发出去没有」同样需要它）。
func (c *weixinChannel) notePush(seq int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastPush = IMPushStatus{Seq: seq, At: imNow(), OK: ok}
}

// Start 起长轮询。没有凭据时不报错，只把状态摆成 waiting_scan——用户需要先
// 扫码绑定，这是正常状态而不是故障。
func (c *weixinChannel) Start(ctx context.Context) error {
	// 启停很快时（开→关→再开）Start 的 goroutine 可能轮到跑时渠道已被换掉：
	// 不再是当前登记实例就什么都不做，别留下一个没人管的长轮询。
	if cur := c.app.imChannelOf("weixin"); cur != IMChannel(c) {
		return nil
	}
	c.stopRun()
	acc := c.loadAccount()
	c.mu.Lock()
	c.account = acc
	if acc != nil {
		if acc.BaseURL != "" {
			c.baseURL = acc.BaseURL
		}
		c.state = "connecting"
		c.errText, c.errCode = "", ""
		c.since = time.Now()
	} else {
		c.state = "waiting_scan"
	}
	c.mu.Unlock()
	if acc == nil {
		log.Printf("[IM] 微信渠道已启用但尚未扫码绑定：请到设置页生成二维码")
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	go c.run(runCtx)
	log.Printf("[IM] 微信渠道开始长轮询（账号 %s）", maskIMID(acc.AccountID))
	// 老用户升级补齐：白名单为空但凭据里有 owner → 自动写入，不用重新扫码。
	c.app.imAllowOwnerOnStart("weixin", acc.OwnerID)
	return nil
}

// Stop 停轮询并发 notifyStop（告诉平台「这个 poller 下线了」，避免消息积压）。
func (c *weixinChannel) Stop() {
	c.stopRun()
	c.loginMu.Lock()
	if lg := c.login; lg != nil && lg.cancel != nil {
		lg.cancel()
		c.login = nil
	}
	c.loginMu.Unlock()
	c.mu.Lock()
	if c.state != "waiting_scan" {
		c.state = "disabled"
	}
	acc := c.account
	c.mu.Unlock()
	if acc != nil && acc.Token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := weixinCall(ctx, c.weixinBaseURL(), "ilink/bot/msg/notifystop",
			map[string]any{"base_info": weixinBaseInfo()}, acc.Token, 5*time.Second); err != nil {
			log.Printf("[IM] 微信 notifystop 失败（忽略）: %v", err)
		}
	}
}

func (c *weixinChannel) stopRun() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *weixinChannel) weixinBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.baseURL == "" {
		return weixinQRBaseURL
	}
	return c.baseURL
}

func (c *weixinChannel) setErr(code, text string) {
	c.mu.Lock()
	c.errCode, c.errText = code, text
	c.mu.Unlock()
}

func (c *weixinChannel) setState(st string) {
	c.mu.Lock()
	c.state = st
	if st == "connected" {
		c.errText, c.errCode = "", ""
	}
	c.mu.Unlock()
}

// run 是长轮询主循环：notifystart → 拉取 → 处理 → 落游标；连续失败退避重连，
// 达到上限或 token 失效就停（前者等用户点重连，后者必须重新扫码）。
func (c *weixinChannel) run(ctx context.Context) {
	defer c.saveRuntimeState()
	c.notifyStart(ctx)
	failures := 0
	for ctx.Err() == nil {
		acc := c.currentAccount()
		if acc == nil {
			return
		}
		c.mu.Lock()
		cursor := c.cursor
		c.mu.Unlock()
		raw, err := weixinCall(ctx, c.weixinBaseURL(), "ilink/bot/getupdates",
			map[string]any{"get_updates_buf": cursor, "base_info": weixinBaseInfo()},
			acc.Token, weixinPollTimeout+10*time.Second)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			var we *weixinErr
			if errors.As(err, &we) && we.Timeout {
				// 长轮询超时 = 没有新消息，正常空轮。
				c.setState("connected")
				failures = 0
				continue
			}
			failures++
			code, text := "network", weixinErrorText("network", "")
			if errors.As(err, &we) {
				code, text = we.Code, weixinErrorText(we.Code, we.ProviderCode)
			}
			c.setErr(code, text)
			log.Printf("[IM] 微信拉取失败（第 %d 次）: %v", failures, err)
			if code == "unauthorized" {
				c.setState("failed")
				c.setErr("stale-token", weixinErrorText("stale-token", ""))
				return
			}
			if failures >= weixinMaxFailures {
				c.setState("failed")
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(1<<uint(failures)) * time.Second):
			}
			continue
		}
		if code := weixinRejectCode(raw); code != "" {
			if code == "-14" {
				c.setState("failed")
				c.setErr("stale-token", weixinErrorText("stale-token", code))
				log.Printf("[IM] 微信凭据失效（ret=%s），需要重新扫码", code)
				return
			}
			failures++
			c.setErr("updates-rejected", weixinErrorText("updates-rejected", code))
			log.Printf("[IM] 微信消息同步被拒绝（ret=%s，第 %d 次）", code, failures)
			if failures >= weixinMaxFailures {
				c.setState("failed")
				return
			}
			time.Sleep(time.Duration(1<<uint(failures)) * time.Second)
			continue
		}
		failures = 0
		c.setState("connected")
		var resp weixinUpdatesResp
		if json.Unmarshal(raw, &resp) != nil {
			log.Printf("[IM] 微信响应无法解析，跳过本轮")
			continue
		}
		for i := range resp.Msgs {
			c.accept(&resp.Msgs[i])
		}
		if resp.GetUpdatesBuf != "" {
			c.mu.Lock()
			changed := c.cursor != resp.GetUpdatesBuf
			c.cursor = resp.GetUpdatesBuf
			c.mu.Unlock()
			if changed {
				c.saveRuntimeState()
			}
		}
	}
}

// notifyStart 告诉平台 poller 上线（失败不影响收消息，只记日志）。
func (c *weixinChannel) notifyStart(ctx context.Context) {
	acc := c.currentAccount()
	if acc == nil {
		return
	}
	if _, err := weixinCall(ctx, c.weixinBaseURL(), "ilink/bot/msg/notifystart",
		map[string]any{"base_info": weixinBaseInfo()}, acc.Token, weixinAPITimeout); err != nil {
		log.Printf("[IM] 微信 notifystart 失败（忽略）: %v", err)
	}
}

func (c *weixinChannel) currentAccount() *weixinAccount {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.account
}

// accept 处理一条平台消息：去重 → 归一化 → 交给 imOnInbound（阶段 2 接数字经理）。
func (c *weixinChannel) accept(m *weixinMessage) {
	if m.MessageType != 1 {
		return // 机器人自己发的回显
	}
	id := string(m.MessageID)
	if id == "" {
		id = m.ClientID
	}
	if id == "" {
		id = fmt.Sprintf("%s-%d", m.FromUserID, time.Now().UnixNano())
	}
	if !c.markSeen(id) {
		return
	}
	in, ok := weixinInboundOf(m, id)
	if !ok {
		return
	}
	c.mu.Lock()
	c.lastIn = time.Now()
	c.lastInText = imTruncate(in.Text, 80)
	if in.ContextToken != "" {
		c.ctxTokens[in.Conv.UserID] = in.ContextToken
	}
	c.mu.Unlock()
	// 入站也写一行引擎日志：此前入站完全不留痕，真机联调时只能靠 state 文件
	// 与数据库反推「到底有没有收到」，排障成本太高。ctx 一并记，出站缺凭据
	// 的锅一眼能对上。
	ctxFlag := "no"
	if in.ContextToken != "" {
		ctxFlag = "yes"
	}
	log.Printf("[IM] 入站 %s %s ctx=%s", maskIMID(in.Conv.UserID), imTruncate(in.Text, 40), ctxFlag)
	c.saveRuntimeState()
	c.app.imOnInbound(in)
}

// markSeen 记一条消息 id，返回「是否首次见到」（false = 重投，直接丢）。
func (c *weixinChannel) markSeen(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[id] {
		return false
	}
	c.seen[id] = true
	c.seenOrder = append(c.seenOrder, id)
	if len(c.seenOrder) > weixinSeenCap {
		drop := len(c.seenOrder) - weixinSeenCap
		for _, old := range c.seenOrder[:drop] {
			delete(c.seen, old)
		}
		c.seenOrder = append([]string(nil), c.seenOrder[drop:]...)
	}
	return true
}

// weixinInboundOf 把平台消息归一化成 IMInbound（纯函数，单测直接覆盖）。
func weixinInboundOf(m *weixinMessage, id string) (IMInbound, bool) {
	if m.FromUserID == "" {
		return IMInbound{}, false
	}
	in := IMInbound{
		Conv: IMConversation{
			Channel:  "weixin",
			ChatID:   m.FromUserID,
			ChatType: "dm",
			UserID:   m.FromUserID,
		},
		MessageID:    id,
		Kind:         "text",
		ContextToken: m.ContextToken,
		RunID:        m.RunID,
		At:           imNow(),
	}
	if m.GroupID != "" {
		// 群消息首版不处理（参考实现对外部消费者同样忽略群），只登记不进会话。
		in.Conv.ChatType = "group"
		in.Conv.ChatID = m.GroupID
		in.Kind = "group"
	}
	for _, it := range m.ItemList {
		switch it.Type {
		case weixinItemText:
			if it.TextItem != nil && strings.TrimSpace(it.TextItem.Text) != "" {
				in.Text = strings.TrimSpace(it.TextItem.Text)
			}
		case weixinItemVoice:
			if it.VoiceItem != nil {
				if in.Text == "" && strings.TrimSpace(it.VoiceItem.Text) != "" {
					in.Text = strings.TrimSpace(it.VoiceItem.Text) // 平台转写文本，等于免费 ASR
					in.Kind = "voice"
				}
				if it.VoiceItem.Media != nil {
					in.Files = append(in.Files, IMFileRef{Name: "voice", Media: weixinMediaMap(it.VoiceItem.Media)})
				}
			}
		case weixinItemImage:
			in.Kind = "image"
			if it.ImageItem != nil && it.ImageItem.Media != nil {
				in.Files = append(in.Files, IMFileRef{Name: "image", Media: weixinMediaMap(it.ImageItem.Media)})
			}
		case weixinItemFile:
			in.Kind = "file"
			if it.FileItem != nil {
				size, _ := strconv.ParseInt(strings.TrimSpace(it.FileItem.Len), 10, 64)
				ref := IMFileRef{Name: it.FileItem.FileName, Size: size}
				if it.FileItem.Media != nil {
					ref.Media = weixinMediaMap(it.FileItem.Media)
				}
				in.Files = append(in.Files, ref)
			}
		case weixinItemVideo:
			in.Kind = "video"
			if it.VideoItem != nil && it.VideoItem.Media != nil {
				in.Files = append(in.Files, IMFileRef{Name: "video", Media: weixinMediaMap(it.VideoItem.Media)})
			}
		}
	}
	if in.Conv.ChatType == "group" {
		return in, true // 交上层决定怎么回（当前只登记）
	}
	if in.Text == "" && len(in.Files) == 0 {
		return IMInbound{}, false // 空消息（表情/系统提示）不打扰会话
	}
	return in, true
}

func weixinMediaMap(m *weixinMedia) map[string]string {
	if m == nil {
		return nil
	}
	out := map[string]string{}
	if m.EncryptQueryParam != "" {
		out["encrypt_query_param"] = m.EncryptQueryParam
	}
	if m.AESKey != "" {
		out["aes_key"] = m.AESKey
	}
	if m.EncryptType != 0 {
		out["encrypt_type"] = strconv.Itoa(m.EncryptType)
	}
	if m.FullURL != "" {
		out["full_url"] = m.FullURL
	}
	return out
}

// SendText 发一段文本（内部按 1800 字切分，返回平台侧消息 id 列表）。
func (c *weixinChannel) SendText(ctx context.Context, conv IMConversation, text string) ([]string, error) {
	acc := c.currentAccount()
	if acc == nil {
		return nil, weixinErrOf("unauthorized", "微信尚未绑定")
	}
	body := imPlainText(text)
	if strings.TrimSpace(body) == "" {
		return nil, weixinErrOf("bad-body", "内容为空，不发送")
	}
	c.mu.Lock()
	ctxToken := c.ctxTokens[conv.UserID]
	c.mu.Unlock()
	var ids []string
	for _, chunk := range splitWeixinText(body, weixinMaxChars) {
		clientID := "jx-weixin-" + newID()
		msg := map[string]any{
			"from_user_id":  "",
			"to_user_id":    conv.UserID,
			"client_id":     clientID,
			"message_type":  2,
			"message_state": 2,
			"item_list":     []any{map[string]any{"type": weixinItemText, "text_item": map[string]any{"text": chunk}}},
		}
		if ctxToken != "" {
			msg["context_token"] = ctxToken
		}
		raw, err := weixinCall(ctx, c.weixinBaseURL(), "ilink/bot/sendmessage",
			map[string]any{"msg": msg, "base_info": weixinBaseInfo()}, acc.Token, weixinAPITimeout)
		if err != nil {
			var we *weixinErr
			if errors.As(err, &we) {
				c.setErr(we.Code, weixinErrorText(we.Code, we.ProviderCode))
			}
			return ids, err
		}
		if code := weixinRejectCode(raw); code != "" {
			c.setErr("send-rejected", "微信拒绝发送消息")
			return ids, &weixinErr{Code: "send-rejected", ProviderCode: code, Msg: "微信拒绝发送消息"}
		}
		var ok struct {
			ProviderMessageIDs []string `json:"provider_message_ids"`
		}
		_ = json.Unmarshal(raw, &ok)
		// 平台通常不回消息 id（参考实现也是拿自己生成的 client_id 当回执），
		// 所以先记本地 client_id，再补平台回的（若有）。
		ids = append(ids, clientID)
		ids = append(ids, ok.ProviderMessageIDs...)
	}
	c.mu.Lock()
	c.lastSend = time.Now()
	c.mu.Unlock()
	return ids, nil
}

// ---------- 扫码登录 ----------

// weixinLogin 是一次扫码绑定的过程态（前端按 id 轮询）。
type weixinLogin struct {
	ID           string `json:"id"`
	Status       string `json:"status"` // wait|scaned|need_verifycode|confirmed|expired|failed|binded
	QRCode       string `json:"qrcode,omitempty"`
	QRImage      string `json:"qrImage,omitempty"` // 二维码图片地址/内容（前端直接显示）
	Error        string `json:"error,omitempty"`
	VerifyNeeded bool   `json:"verifyNeeded,omitempty"`
	ExpiresIn    int    `json:"expiresIn"` // 剩余秒数

	verifyCode string
	cancel     context.CancelFunc
	startedAt  time.Time
	done       bool
}

func (lg *weixinLogin) snapshot() map[string]any {
	left := int(time.Until(lg.startedAt.Add(weixinLoginTimeout)).Seconds())
	if left < 0 {
		left = 0
	}
	out := map[string]any{
		"id":           lg.ID,
		"status":       lg.Status,
		"qrImage":      lg.QRImage,
		"qrcode":       lg.QRCode,
		"error":        lg.Error,
		"verifyNeeded": lg.VerifyNeeded,
		"expiresIn":    left,
		"done":         lg.done,
	}
	return out
}

// BeginLogin 生成二维码并起后台轮询（返回首次快照）。扫码是唯一拿 token 的
// 途径，平台没有「换 token」接口。
func (c *weixinChannel) BeginLogin() (map[string]any, error) {
	c.loginMu.Lock()
	if c.login != nil && c.login.cancel != nil && !c.login.done {
		c.login.cancel()
	}
	c.loginMu.Unlock()

	raw, err := weixinCall(context.Background(), weixinQRBaseURL,
		fmt.Sprintf("ilink/bot/get_bot_qrcode?bot_type=%d", weixinBotType),
		map[string]any{"local_token_list": []string{}}, "", weixinAPITimeout)
	if err != nil {
		return nil, err
	}
	if code := weixinRejectCode(raw); code != "" {
		return nil, &weixinErr{Code: "qr-rejected", ProviderCode: code, Msg: "微信拒绝生成二维码"}
	}
	var lr weixinLoginResp
	if err := json.Unmarshal(raw, &lr); err != nil || lr.QRCode == "" {
		return nil, weixinErrOf("qr-rejected", "微信没有返回二维码，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), weixinLoginTimeout)
	lg := &weixinLogin{
		ID:        newID(),
		Status:    weixinQRWait,
		QRCode:    lr.QRCode,
		QRImage:   lr.QRCodeContent,
		startedAt: time.Now(),
		cancel:    cancel,
	}
	c.loginMu.Lock()
	c.login = lg
	c.loginMu.Unlock()
	go c.runLogin(ctx, lg)
	return lg.snapshot(), nil
}

// LoginStatus 按 id 取扫码进度；verify 非空时视作「用户输入配对码」。
func (c *weixinChannel) LoginStatus(id, verify string) (map[string]any, error) {
	c.loginMu.Lock()
	lg := c.login
	if lg == nil || (id != "" && lg.ID != id) {
		c.loginMu.Unlock()
		return nil, weixinErrOf("no-login", "没有进行中的扫码，请重新生成二维码")
	}
	if verify != "" {
		lg.verifyCode = verify
		lg.VerifyNeeded = false
	}
	snap := lg.snapshot()
	c.loginMu.Unlock()
	return snap, nil
}

// runLogin 轮询 qrcode 状态直到确认/失败/超时；确认后保存凭据并（重新）起轮询。
func (c *weixinChannel) runLogin(ctx context.Context, lg *weixinLogin) {
	defer func() {
		c.loginMu.Lock()
		lg.done = true
		c.loginMu.Unlock()
		if lg.cancel != nil {
			lg.cancel()
		}
	}()
	base := weixinQRBaseURL
	for ctx.Err() == nil {
		q := url.Values{}
		q.Set("qrcode", lg.QRCode)
		c.loginMu.Lock()
		verify := lg.verifyCode
		c.loginMu.Unlock()
		if verify != "" {
			q.Set("verify_code", verify)
		}
		raw, err := weixinCallGet(ctx, base, "ilink/bot/get_qrcode_status", q, weixinPollTimeout+5*time.Second)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			var we *weixinErr
			if errors.As(err, &we) && we.Timeout {
				continue // 长轮询空轮
			}
			c.loginMu.Lock()
			lg.Status = "failed"
			lg.Error = weixinErrorText("network", "")
			c.loginMu.Unlock()
			return
		}
		var lr weixinLoginResp
		if json.Unmarshal(raw, &lr) != nil || lr.Status == "" {
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		switch lr.Status {
		case weixinQRWait:
			c.loginMu.Lock()
			lg.Status = weixinQRWait
			c.loginMu.Unlock()
			time.Sleep(1500 * time.Millisecond)
		case weixinQRScaned:
			c.loginMu.Lock()
			lg.Status = "scaned"
			c.loginMu.Unlock()
			time.Sleep(1500 * time.Millisecond)
		case weixinQRNeedVerify:
			c.loginMu.Lock()
			lg.Status = weixinQRNeedVerify
			lg.VerifyNeeded = true
			c.loginMu.Unlock()
			time.Sleep(1500 * time.Millisecond)
		case weixinQRedirect:
			if host := strings.TrimSpace(lr.RedirectHost); host != "" {
				base = "https://" + strings.TrimSuffix(host, "/") + "/"
			}
			time.Sleep(1500 * time.Millisecond)
		case weixinQRVerifyBlocked:
			c.finishLogin(lg, "failed", weixinErrorText("verify-blocked", ""))
			return
		case weixinQRExpired:
			c.finishLogin(lg, "expired", weixinErrorText("expired", ""))
			return
		case weixinQRBinded:
			c.finishLogin(lg, "failed", weixinErrorText("already-bound", ""))
			return
		case weixinQRConfirmed:
			if lr.BotToken == "" || lr.IlinkBotID == "" || lr.IlinkUserID == "" {
				c.finishLogin(lg, "failed", weixinErrorText("incomplete-login", ""))
				return
			}
			acc := &weixinAccount{
				AccountID: lr.IlinkBotID,
				OwnerID:   lr.IlinkUserID,
				Token:     lr.BotToken,
				BoundAt:   imNow(),
			}
			if host := strings.TrimSpace(lr.BaseURL); host != "" {
				acc.BaseURL = strings.TrimSuffix(host, "/") + "/"
			}
			if err := c.saveAccount(acc); err != nil {
				c.finishLogin(lg, "failed", "微信凭据保存失败："+err.Error())
				return
			}
			c.mu.Lock()
			c.account = acc
			if acc.BaseURL != "" {
				c.baseURL = acc.BaseURL
			}
			c.state = "connecting"
			c.mu.Unlock()
			log.Printf("[IM] 微信绑定成功（账号 %s）", maskIMID(acc.AccountID))
			// 绑定者默认放行：owner 是登录确认时平台给的，确定就是扫码的人。
			// 不再要求用户去某个界面手填 id（见 im_allow.go 顶部说明）。
			c.app.imAllowOwnerOnBind(acc.OwnerID)
			c.finishLogin(lg, "confirmed", "")
			// 绑好后立刻起轮询（同步 IM 配置开没开由调用方保证：扫码成功说明用户要它）
			c.app.imEnableWeixin()
			c.app.restartIM("weixin")
			return
		default:
			time.Sleep(1500 * time.Millisecond)
		}
	}
}

func (c *weixinChannel) finishLogin(lg *weixinLogin, status, errText string) {
	c.loginMu.Lock()
	lg.Status = status
	lg.Error = errText
	c.loginMu.Unlock()
}

// Logout 解绑：停轮询、删凭据与状态（下次要重新扫码）。
func (c *weixinChannel) Logout() {
	c.Stop()
	c.clearAccount()
	c.mu.Lock()
	c.account = nil
	c.cursor = ""
	c.seen = map[string]bool{}
	c.seenOrder = nil
	c.ctxTokens = map[string]string{}
	c.state = "waiting_scan"
	c.mu.Unlock()
}
