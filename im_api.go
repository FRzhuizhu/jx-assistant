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
	mux.HandleFunc("POST /api/im/weixin/qr", a.handleIMWeixinQr)              // 生成二维码（需人扫）
	mux.HandleFunc("GET /api/im/weixin/qr", a.handleIMWeixinQrPoll)           // 轮询扫码进度
	mux.HandleFunc("POST /api/im/weixin/logout", a.handleIMWeixinLogout)      // 解绑（删凭据）
	mux.HandleFunc("POST /api/im/weixin/reconnect", a.handleIMWeixinReconnect) // 手动重连
	mux.HandleFunc("POST /api/im/weixin/test", a.handleIMWeixinTest)          // 发一条测试消息
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
		a.Cfg.IM.Weixin.Allow = imNormalizeAllow(body.Allow)
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

func (a *App) handleIMWeixinQr(w http.ResponseWriter, r *http.Request) {
	snap, err := a.weixinChannelForAPI().BeginLogin()
	if err != nil {
		writeErr(w, http.StatusBadGateway, weixinErrorOf(err))
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (a *App) handleIMWeixinQrPoll(w http.ResponseWriter, r *http.Request) {
	snap, err := a.weixinChannelForAPI().LoginStatus(r.URL.Query().Get("id"), r.URL.Query().Get("verify"))
	if err != nil {
		writeErr(w, http.StatusNotFound, weixinErrorOf(err))
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (a *App) handleIMWeixinLogout(w http.ResponseWriter, r *http.Request) {
	a.weixinChannelForAPI().Logout()
	// 换账号后旧会话不该继续收到答复：绑定表一并清掉（下次发消息自动重建）。
	a.imUnbindChannel("weixin")
	a.Cfg.IM.Weixin.Enabled = false
	_ = a.Cfg.Save(a.CfgPath)
	a.syncIM()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": a.imStatuses()})
}

func (a *App) handleIMWeixinReconnect(w http.ResponseWriter, r *http.Request) {
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

func (a *App) handleIMWeixinTest(w http.ResponseWriter, r *http.Request) {
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

