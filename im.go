package main

// IM 接入的统一契约与运行期登记（第一期只落地微信 iLink / ClawBot，协议实现
// 见 im_weixin.go）。
//
// 为什么这样分层（设计意图）：
//   - 平台差异（协议、鉴权、分段上限、媒体上传）全部收在 im_<channel>.go 一个
//     文件里；中枢与 jx 的会话体系只认两种东西——「一条入站消息」和「要发出去
//     的一段文本/一个文件」。加飞书、QQ 时各写一个实现，其余代码不动。
//   - 入站一律复用 deliverToSession（runtime.go:522），与前端发消息走同一条
//     收口：IM 只是「另一个用户入口」，绝不开第二条绕过 perm.go 的提权路径。
//   - 出站只挂 sessionRun.publish（runtime.go:416）一个点：所有执行体的事件都从
//     那里出去，IM 侧是纯消费者，业务代码不需要知道 IM 的存在。**不是**
//     broadcast——那里没有正文事件（见 im_hub.go 顶部说明）。
//
// 配置与凭据分开存：非敏感的开关/白名单在 config.json 的 im 段；账号 token 落
// data/im_weixin.json（DPAPI 加密，与 SSH 密码同一套 protectSecret），接口与
// 状态页只回掩码。

import (
	"context"
	"log"
	"strings"
	"time"
)

// IMConversation 是一条 IM 会话。首期只支持私聊（dm）：协议里有群字段，但群
// 行为没有可靠参考，验证过再开。
type IMConversation struct {
	Channel  string `json:"channel"`  // weixin（后续 feishu/qq）
	ChatID   string `json:"chatId"`   // 平台会话 id；微信 = 对方 ilink_user_id
	ChatType string `json:"chatType"` // dm | group
	UserID   string `json:"userId"`
	UserName string `json:"userName,omitempty"`
}

// IMInbound 是适配层归一化后的入站消息：各平台解析完只填这一份，中枢不认识
// 平台原始载荷。
type IMInbound struct {
	Conv      IMConversation `json:"conv"`
	MessageID string         `json:"messageId"`      // 平台消息 id（幂等去重用）
	Kind      string         `json:"kind"`           // text | voice | image | file | video | group
	Text      string         `json:"text,omitempty"` // 语音走平台转写文本
	// ContextToken / RunID 只用于回信（微信要求把入站会话凭据原样带回去），
	// 不进日志、不进接口回传。
	ContextToken string      `json:"-"`
	RunID        string      `json:"-"`
	Files        []IMFileRef `json:"files,omitempty"`
	At           string      `json:"at"`
}

// IMFileRef 是平台侧的媒体描述：微信 iLink 给的是加密查询参数 + AES key，
// 真正下载解密在阶段 4（im_weixin.go 的媒体处理）。
type IMFileRef struct {
	Name  string            `json:"name"`
	Size  int64             `json:"size,omitempty"`
	Media map[string]string `json:"media,omitempty"`
}

// IMChannelStatus 是给设置页与 /api/im/status 看的渠道状态（凭据一律脱敏）。
type IMChannelStatus struct {
	Channel       string `json:"channel"`
	Enabled       bool   `json:"enabled"`
	State         string `json:"state"` // disabled|waiting_scan|connecting|connected|failed
	AccountID     string `json:"accountId,omitempty"`
	OwnerID       string `json:"ownerId,omitempty"`
	Since         string `json:"since,omitempty"`
	LastInboundAt string `json:"lastInboundAt,omitempty"`
	LastInbound   string `json:"lastInbound,omitempty"` // 最近一条入站摘要（排障用，已截断）
	LastSendAt    string `json:"lastSendAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	LastErrorCode string `json:"lastErrorCode,omitempty"`
	Allow         int    `json:"allow"` // 白名单条数：0 = 没人能下指令（fail closed）
	// ContextToken 表示「与该账号的会话已有上下文凭据」，也就是现在发主动消息
	// 平台才会投递（没有凭据时平台照样 200 但不送达，见 im_weixin.go 发送段）。
	ContextToken bool `json:"contextToken"`
	// LastPush 是最近一条「答复推送」的结果：只看 lastSeq 分不清是推成功还是
	// 推失败（失败的也不推进序号），排障时必须能直接看到 ok=false。
	LastPush *IMPushStatus `json:"lastPush,omitempty"`
}

// IMPushStatus 一条答复推送的回执（seq 0 = 命令回执这类不占序号的话）。
type IMPushStatus struct {
	Seq int64  `json:"seq"`
	At  string `json:"at"`
	OK  bool   `json:"ok"`
}

// IMChannel 是一个 IM 渠道的长连接实现：Start 负责「登录态检查 + 长轮询循环」，
// 内部自管重连与退避；Stop 必须立刻收口（含 notifyStop）。
type IMChannel interface {
	Name() string
	Start(ctx context.Context) error
	Stop()
	Status() IMChannelStatus
}

// IMSender 是出站能力：分段在适配层内部做（各平台上限不同），中枢只管给全文。
type IMSender interface {
	SendText(ctx context.Context, conv IMConversation, text string) ([]string, error)
}

// ---------- 运行期登记 ----------

// imChannelOf 取当前在跑的渠道（nil = 没启用）。
func (a *App) imChannelOf(name string) IMChannel {
	a.imMu.Lock()
	defer a.imMu.Unlock()
	return a.imChannels[name]
}

// imChannelsSnapshot 复制一份当前渠道表（避免持锁调用 Stop/Start）。
func (a *App) imChannelsSnapshot() map[string]IMChannel {
	a.imMu.Lock()
	defer a.imMu.Unlock()
	out := make(map[string]IMChannel, len(a.imChannels))
	for k, v := range a.imChannels {
		out[k] = v
	}
	return out
}

func (a *App) imSetChannel(name string, ch IMChannel) {
	a.imMu.Lock()
	defer a.imMu.Unlock()
	if a.imChannels == nil {
		a.imChannels = map[string]IMChannel{}
	}
	a.imChannels[name] = ch
}

func (a *App) imDropChannel(name string) {
	a.imMu.Lock()
	defer a.imMu.Unlock()
	delete(a.imChannels, name)
}

// imStatuses 汇总当前渠道状态（未启用时也回一条 disabled，设置页好显示）。
func (a *App) imStatuses() []IMChannelStatus {
	chs := a.imChannelsSnapshot()
	out := make([]IMChannelStatus, 0, 1)
	if ch, ok := chs["weixin"]; ok && ch != nil {
		st := ch.Status()
		st.Enabled = a.Cfg.imWeixinOn()
		out = append(out, st)
		return out
	}
	out = append(out, IMChannelStatus{
		Channel: "weixin", Enabled: a.Cfg.imWeixinOn(), State: "disabled",
		Allow: a.Cfg.imAllowCount(),
	})
	return out
}

// syncIM 按当前配置起停渠道（启动流程与设置保存都走它，配置热生效）。
// 只做「该跑的跑起来、不该跑的停掉」：已在跑的不重启（重启会丢长轮询游标，
// 换账号的场合由登录流程自己 Stop 再 Start）。
func (a *App) syncIM() {
	if a == nil || a.Cfg == nil {
		return
	}
	for name, ch := range a.imChannelsSnapshot() {
		if name == "weixin" && a.Cfg.imWeixinOn() {
			continue
		}
		if ch != nil {
			ch.Stop()
		}
		a.imDropChannel(name)
		log.Printf("[IM] 渠道 %s 已停用", name)
	}
	if !a.Cfg.imWeixinOn() {
		return
	}
	if a.imChannelOf("weixin") != nil {
		return
	}
	ch := newWeixinChannel(a)
	a.imSetChannel("weixin", ch)
	go func() {
		if err := ch.Start(context.Background()); err != nil {
			log.Printf("[IM] 微信渠道启动失败: %v", err)
		}
	}()
}

// restartIM 停掉再用新凭据起一份（扫码成功、重新绑定、手动重连都走它）。
func (a *App) restartIM(name string) {
	if ch := a.imChannelOf(name); ch != nil {
		ch.Stop()
		a.imDropChannel(name)
	}
	a.syncIM()
}

// imNow 统一的时间戳口径（与 now() 一致，但给状态字段留一个可测的缝）。
func imNow() string { return time.Now().Format("2006-01-02 15:04:05") }

// imOriginTag 拼「这一轮是谁发起的」标记：im:<渠道>:<chatId>。
// 它挂在轮上（sessionRun.origin），不挂在会话上——同一个数字经理会话既可能被
// 桌面端触发、也可能被微信触发，只有轮知道这一次是谁发起；审批/提问按它决定
// 推不推微信（见 im_perm.go）。
func imOriginTag(channel, chatID string) string {
	channel, chatID = strings.TrimSpace(channel), strings.TrimSpace(chatID)
	if channel == "" || chatID == "" {
		return ""
	}
	return "im:" + channel + ":" + chatID
}

// imParseOrigin 解析来源标记；不是 IM 来源（空串 / 别的形态）返回 ok=false。
// 只按前两个冒号切：chatId 理论上不含冒号，但别假设它不含。
func imParseOrigin(origin string) (channel, chatID string, ok bool) {
	s := strings.TrimSpace(origin)
	if !strings.HasPrefix(s, "im:") {
		return "", "", false
	}
	rest := s[len("im:"):]
	i := strings.Index(rest, ":")
	if i <= 0 || i >= len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// 入站落点（imOnInbound）与出站消费（imOnEvent）在 im_hub.go：阶段 2 起，
// IM 的消息进 deliverToSession、答复从 sessionRun.publish 出站，这里不再放桩。
func maskIMID(id string) string {
	s := strings.TrimSpace(id)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 8 {
		return string(r[:1]) + "•••"
	}
	if len(r) <= 12 {
		return string(r[:3]) + "••••" + string(r[len(r)-2:])
	}
	return string(r[:6]) + "••••" + string(r[len(r)-4:])
}

// imTruncate 按字符截断（日志与状态摘要用，不切坏 UTF-8）。
func imTruncate(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// ---------- 配置访问器（nil 安全：测试里的裸 App 可能没有 Cfg） ----------

// imOn 是 IM 接入总开关。
func (c *Config) imOn() bool { return c != nil && c.IM.Enabled }

// imWeixinOn 是微信渠道是否该跑：总开关与渠道开关都开才算数。
func (c *Config) imWeixinOn() bool { return c != nil && c.IM.Enabled && c.IM.Weixin.Enabled }

// imAllowCount 是白名单条数（状态页显示「有人能下指令吗」）。
func (c *Config) imAllowCount() int {
	if c == nil {
		return 0
	}
	return len(imNormalizeAllow(c.IM.Weixin.Allow))
}

// imNormalizeAllow 清洗白名单：去空白、去重、保序、上限 50 条。
// 写配置（/api/im/settings）与运行时判读共用这一份口径——两处各写一遍规则，
// 迟早会出现「存进去 2 条、读出来数到 3 条」这种偏差。
func imNormalizeAllow(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		s := strings.TrimSpace(v)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= 50 {
			break
		}
	}
	return out
}

// imAllowWeixin 判断某个微信用户能否下指令。空白名单一律拒绝（fail closed）：
// 宁可让用户手动填一次 id，也不让扫码后默认人人可下指令。
func (c *Config) imAllowWeixin(userID string) bool {
	if c == nil || userID == "" {
		return false
	}
	for _, v := range imNormalizeAllow(c.IM.Weixin.Allow) {
		if v == userID {
			return true
		}
	}
	return false
}
