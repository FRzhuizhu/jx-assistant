package main

// IM 接入的单元测试：只测纯逻辑（分段/纯文本/协议头/归一化/错误码/配置门），
// 不碰网络——协议联调在隔离实例上用 curl 跑（见 AGENT.md「IM 接入」的验证步骤）。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestIMSplitWeixinText(t *testing.T) {
	// 短文本原样一段
	if got := splitWeixinText("你好", 1800); len(got) != 1 || got[0] != "你好" {
		t.Fatalf("短文本应原样返回，得到 %#v", got)
	}
	// 空白内容不产生段（避免给平台发空消息）
	if got := splitWeixinText("   \n  ", 1800); got != nil {
		t.Fatalf("空白内容应返回 nil，得到 %#v", got)
	}
	// 长文本按上限切开，且每段不超限、不丢字
	long := strings.Repeat("这是一句用来测试分段的中文。", 400) // 5600 字
	parts := splitWeixinText(long, 1800)
	if len(parts) < 3 {
		t.Fatalf("5600 字应切成至少 3 段，得到 %d 段", len(parts))
	}
	for i, p := range parts {
		if n := len([]rune(p)); n > 1800 {
			t.Fatalf("第 %d 段 %d 字超过上限", i, n)
		}
	}
	if strings.Join(parts, "") != long {
		t.Fatalf("分段后内容应与原文一致（不丢字）")
	}
	// 换行优先：切点靠后时应切在换行处（段尾不带半个句子）
	line := strings.Repeat("a", 1700) + "\n" + strings.Repeat("b", 500)
	got := splitWeixinText(line, 1800)
	if len(got) != 2 {
		t.Fatalf("应在换行处切成 2 段，得到 %d 段", len(got))
	}
	if strings.Contains(got[0], "b") {
		t.Fatalf("第一段不该跨过换行")
	}
}

func TestIMPlainText(t *testing.T) {
	in := "## 标题\n\n**加粗**和`代码`还有[链接](https://x.test/a)\n\n- 一\n- 二\n\n---\n\n```\ncode line\n```\n"
	out := imPlainText(in)
	for _, bad := range []string{"**", "`", "## ", "[链接]"} {
		if strings.Contains(out, bad) {
			t.Fatalf("纯文本里不该残留 %q：\n%s", bad, out)
		}
	}
	for _, want := range []string{"标题", "加粗", "代码", "链接（https://x.test/a）", "· 一", "code line"} {
		if !strings.Contains(out, want) {
			t.Fatalf("纯文本缺少 %q：\n%s", want, out)
		}
	}
}

func TestIMWeixinHeaders(t *testing.T) {
	h := weixinCommonHeaders("")
	if h["iLink-App-Id"] != weixinAppID {
		t.Fatalf("App-Id 不对：%q", h["iLink-App-Id"])
	}
	if h["iLink-App-ClientVersion"] != "132102" { // (2<<16)|(4<<8)|6
		t.Fatalf("ClientVersion 不对：%q", h["iLink-App-ClientVersion"])
	}
	if _, ok := h["X-WECHAT-UIN"]; !ok {
		t.Fatalf("缺 X-WECHAT-UIN 设备标识")
	}
	if _, ok := h["Authorization"]; ok {
		t.Fatalf("无 token 时不该带 Authorization")
	}
	auth := weixinCommonHeaders("tok-1")
	if auth["Authorization"] != "Bearer tok-1" || auth["AuthorizationType"] != "ilink_bot_token" {
		t.Fatalf("鉴权头不对：%#v", auth)
	}
	if bi := weixinBaseInfo(); bi["channel_version"] != weixinChannelVersion {
		t.Fatalf("base_info 缺 channel_version：%#v", bi)
	}
}

func TestIMWeixinInboundKinds(t *testing.T) {
	// 文本 + context_token + 数字型 message_id
	raw := `{"message_id":7312345678901234567,"from_user_id":"u1","message_type":1,
	         "context_token":"ct-9","item_list":[{"type":1,"text_item":{"text":" 帮我看看磁盘 "}}]}`
	var m weixinMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("解析入站消息失败: %v", err)
	}
	if string(m.MessageID) != "7312345678901234567" {
		t.Fatalf("64 位 message_id 掉精度：%q", string(m.MessageID))
	}
	in, ok := weixinInboundOf(&m, string(m.MessageID))
	if !ok || in.Text != "帮我看看磁盘" || in.Kind != "text" || in.ContextToken != "ct-9" {
		t.Fatalf("文本消息归一化不对：%+v ok=%v", in, ok)
	}
	if in.Conv.ChatType != "dm" || in.Conv.UserID != "u1" || in.Conv.Channel != "weixin" {
		t.Fatalf("会话归一化不对：%+v", in.Conv)
	}

	// 语音：用平台转写文本，并保留媒体引用
	voice := &weixinMessage{FromUserID: "u1", MessageType: 1, ItemList: []weixinItem{{
		Type:      weixinItemVoice,
		VoiceItem: &weixinVoiceItem{Text: "今天有什么安排", Media: &weixinMedia{EncryptQueryParam: "q", AESKey: "k"}},
	}}}
	vin, ok := weixinInboundOf(voice, "id2")
	if !ok || vin.Kind != "voice" || vin.Text != "今天有什么安排" || len(vin.Files) != 1 {
		t.Fatalf("语音消息归一化不对：%+v ok=%v", vin, ok)
	}
	if vin.Files[0].Media["encrypt_query_param"] != "q" {
		t.Fatalf("语音媒体描述丢了：%+v", vin.Files[0])
	}

	// 文件：带文件名与大小
	file := &weixinMessage{FromUserID: "u1", MessageType: 1, ItemList: []weixinItem{{
		Type:     weixinItemFile,
		FileItem: &weixinFileItem{FileName: "报表.xlsx", Len: "2048", Media: &weixinMedia{EncryptQueryParam: "q2"}},
	}}}
	fin, ok := weixinInboundOf(file, "id3")
	if !ok || fin.Kind != "file" || len(fin.Files) != 1 || fin.Files[0].Name != "报表.xlsx" || fin.Files[0].Size != 2048 {
		t.Fatalf("文件消息归一化不对：%+v ok=%v", fin, ok)
	}

	// 群消息：标记 group，交给上层决定（首版只登记）
	group := &weixinMessage{FromUserID: "u1", GroupID: "g1", MessageType: 1,
		ItemList: []weixinItem{{Type: weixinItemText, TextItem: &weixinTextItem{Text: "群里说话"}}}}
	gin, ok := weixinInboundOf(group, "id5")
	if !ok || gin.Conv.ChatType != "group" || gin.Conv.ChatID != "g1" || gin.Kind != "group" {
		t.Fatalf("群消息归一化不对：%+v ok=%v", gin, ok)
	}

	// 空消息（表情/系统提示）：丢弃
	empty := &weixinMessage{FromUserID: "u1", MessageType: 1}
	if _, ok := weixinInboundOf(empty, "id6"); ok {
		t.Fatalf("空消息应被丢弃")
	}
	// 没有发送者的消息：丢弃
	if _, ok := weixinInboundOf(&weixinMessage{MessageType: 1}, "id7"); ok {
		t.Fatalf("无发送者的消息应被丢弃")
	}
}

func TestIMWeixinRejectCode(t *testing.T) {
	cases := map[string]string{
		`{"ret":0}`:       "",
		`{"ret":"0"}`:     "",
		`{"errcode":0}`:   "",
		`{}`:              "",
		`not json`:        "",
		`{"ret":-14}`:     "-14",
		`{"errcode":-14}`: "-14",
		`{"ret":-1}`:      "-1",
		`{"ret":"-14"}`:   "-14",
	}
	for raw, want := range cases {
		if got := weixinRejectCode([]byte(raw)); got != want {
			t.Fatalf("weixinRejectCode(%s) = %q，期望 %q", raw, got, want)
		}
	}
}

func TestIMWeixinErrorText(t *testing.T) {
	if s := weixinErrorText("stale-token", "-14"); !strings.Contains(s, "重新扫码") {
		t.Fatalf("凭据失效应提示重新扫码：%q", s)
	}
	if s := weixinErrorText("unknown", "-14"); !strings.Contains(s, "重新扫码") {
		t.Fatalf("未知码但平台码是 -14 时也应提示重新扫码：%q", s)
	}
	if s := weixinErrorText("expired", ""); !strings.Contains(s, "过期") {
		t.Fatalf("过期文案不对：%q", s)
	}
	if s := weixinErrorText("", ""); s == "" {
		t.Fatalf("未知错误也要有一句人话")
	}
}

func TestIMConfigGate(t *testing.T) {
	var nilCfg *Config
	if nilCfg.imOn() || nilCfg.imWeixinOn() || nilCfg.imAllowCount() != 0 {
		t.Fatalf("nil 配置必须一律关（测试里的裸 App 不该误开渠道）")
	}
	cfg := &Config{IM: IMCfg{Enabled: true, Weixin: IMWeixinCfg{Enabled: true, Allow: []string{"u1", " ", "u1", "u2"}}}}
	if !cfg.imOn() || !cfg.imWeixinOn() {
		t.Fatalf("开关应为开")
	}
	if n := cfg.imAllowCount(); n != 2 {
		t.Fatalf("白名单去空白后应 2 条，得到 %d", n)
	}
	if !cfg.imAllowWeixin("u1") || cfg.imAllowWeixin("u3") || cfg.imAllowWeixin("") {
		t.Fatalf("白名单判定不对")
	}
	// 总开关关掉时渠道不跑
	cfg.IM.Enabled = false
	if cfg.imWeixinOn() {
		t.Fatalf("总开关关掉时渠道必须停")
	}
	// 空白名单 = 谁都不受理（fail closed）
	empty := &Config{IM: IMCfg{Enabled: true, Weixin: IMWeixinCfg{Enabled: true}}}
	if empty.imAllowWeixin("u1") {
		t.Fatalf("空白名单不该放行任何人")
	}
	if got := imNormalizeAllow([]string{" a ", "a", "", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("白名单清洗不对：%#v", got)
	}
}

func TestIMSyncIMStartsAndStops(t *testing.T) {
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	a.syncIM()
	if ch := a.imChannelOf("weixin"); ch != nil {
		t.Fatalf("默认配置下不该起渠道")
	}
	// 打开开关：起一个渠道（没有凭据 → 状态 waiting_scan，不联网）
	a.Cfg.IM.Enabled = true
	a.Cfg.IM.Weixin.Enabled = true
	a.syncIM()
	ch := a.imChannelOf("weixin")
	if ch == nil {
		t.Fatalf("开关打开后应起渠道")
	}
	if st := ch.Status(); st.State != "waiting_scan" {
		t.Fatalf("未绑定时状态应为 waiting_scan，得到 %q", st.State)
	}
	// 再同步一次不该重建（重启会丢长轮询游标）
	a.syncIM()
	if again := a.imChannelOf("weixin"); again != ch {
		t.Fatalf("幂等同步不该换实例")
	}
	// 关掉：停并移除
	a.Cfg.IM.Enabled = false
	a.syncIM()
	if a.imChannelOf("weixin") != nil {
		t.Fatalf("关掉后渠道应移除")
	}
}

func TestIMWeixinAccountRoundtrip(t *testing.T) {
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	c := newWeixinChannel(a)
	if c.loadAccount() != nil {
		t.Fatalf("没绑定过不该读到账号")
	}
	acc := &weixinAccount{AccountID: "acct-1", OwnerID: "owner-1", Token: "secret-token", BoundAt: imNow()}
	if err := c.saveAccount(acc); err != nil {
		t.Fatalf("保存凭据失败: %v", err)
	}
	got := c.loadAccount()
	if got == nil || got.Token != "secret-token" || got.AccountID != "acct-1" {
		t.Fatalf("凭据往返不一致：%+v", got)
	}
	// 落盘内容：DPAPI 生效时是 dpapi:v1: 密文，绝不能是明文 token
	if raw, err := os.ReadFile(c.credPath()); err == nil {
		if strings.Contains(string(raw), "dpapi:v1:") && strings.Contains(string(raw), "secret-token") {
			t.Fatalf("token 明文落盘了：%s", raw)
		}
	} else {
		t.Fatalf("凭据文件应存在：%v", err)
	}
	c.clearAccount()
	if c.loadAccount() != nil {
		t.Fatalf("解绑后不该还读到账号")
	}
}

func TestIMWeixinMarkSeen(t *testing.T) {
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	c := newWeixinChannel(a)
	if !c.markSeen("m1") {
		t.Fatalf("首次见到应返回 true")
	}
	if c.markSeen("m1") {
		t.Fatalf("重复消息应被丢弃")
	}
	for i := 0; i < weixinSeenCap+20; i++ {
		c.markSeen(fmt.Sprintf("x-%04d", i))
	}
	if len(c.seenOrder) > weixinSeenCap {
		t.Fatalf("去重集合超过上限：%d", len(c.seenOrder))
	}
	if len(c.seen) != len(c.seenOrder) {
		t.Fatalf("seen 与 seenOrder 不一致：%d vs %d", len(c.seen), len(c.seenOrder))
	}
	// 最旧的已被淘汰，最新的还在
	if c.seen["m1"] {
		t.Fatalf("最旧的 id 应被淘汰")
	}
	if !c.seen[fmt.Sprintf("x-%04d", weixinSeenCap+19)] {
		t.Fatalf("最新的 id 应保留")
	}
}

func TestIMMaskIMID(t *testing.T) {
	if got := maskIMID(""); got != "" {
		t.Fatalf("空 id 应返回空，得到 %q", got)
	}
	short := maskIMID("abc")
	if strings.Contains(short, "abc") {
		t.Fatalf("短 id 不该原样露出：%q", short)
	}
	long := maskIMID("wxid_abcdefghijklmn")
	if strings.Contains(long, "abcdefghijklmn") || !strings.Contains(long, "wxid_a") {
		t.Fatalf("长 id 应保留头尾、遮住中间：%q", long)
	}
	if s := imTruncate("一二三四五六七八九十", 4); s != "一二三四…" {
		t.Fatalf("截断不对：%q", s)
	}
	if s := imTruncate("短", 4); s != "短" {
		t.Fatalf("未超长应原样：%q", s)
	}
}
