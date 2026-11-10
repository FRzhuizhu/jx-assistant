package main

// IM 白名单的「绑定即放行」（2026-11-10 用户定调）。
//
// 起因：微信里发消息收到「请在助手『设置 · IM 接入』里把这个账号加入白名单」，
// 但设置里根本没有那块界面（阶段 5 没做），用户无从操作，只有 API 能改。而微信
// ClawBot 的绑定关系本来就只认一个 owner——扫码的那个人就是账号主人，再让他去
// 手填一遍 id 纯属多余。
//
// 于是白名单从「必须先手填」改成「绑定即放行 owner」，三条入口都是幂等的：
//  1. imAllowOwnerOnBind —— 扫码确认那一刻把 owner 写进白名单（**主路径**）。
//     owner 是平台在登录确认时给的 `ilink_user_id`，确定就是扫码的人，比「第一个
//     发消息的人」可靠得多（后者有被别人抢先发一条就拿到信任的风险）。
//  2. imAllowOwnerOnStart —— 进程启动、渠道起来后补齐：老用户升级上来白名单是空的，
//     凭据里却有 owner，自动修好，不用重新扫码。
//  3. imAllowFirstSender —— 兜底 TOFU：拿不到 owner（凭据缺字段/异常）且白名单为空
//     时，信任第一个发消息的人，并回一句让他知情（「如果不是你本人操作，请立即
//     在桌面端解绑微信」）。
//
// 边界（别顺手放宽）：
//   - **只在白名单为空时兜底 / 补齐**：非空列表是用户明确表达过的信任边界（他手写了
//     谁能下指令），自动逻辑不许改它。唯一的例外是「刚扫码绑定」——那时 owner 就是
//     新主人，追加他本人是绑定语义的一部分。
//   - **陌生人照旧 fail closed**：owner 已知而发送者不是他，一律拒绝（TOFU 不触发）。
//   - 白名单接口 `/api/im/settings` 原样保留：以后要多放几个人进来还是走它。

import (
	"log"
	"strings"
	"sync"
)

// imAllowWriteMu 串行化「读-改-写白名单」这三步：自动信任（绑定/启动/首条消息）
// 与 /api/im/settings 的手工写入可能同时发生，没有它会出现「两边各写一份、
// 后写的把先写的覆盖掉」。
var imAllowWriteMu sync.Mutex

// imAddAllowQuiet 把 id 追加进白名单并落盘；已在其中（或空 id）返回 false。
// 清洗口径与 /api/im/settings 完全一致（imNormalizeAllow）——两处各写一遍规则，
// 迟早出现「存进去 2 条、读出来数到 3 条」这种偏差。
func (a *App) imAddAllowQuiet(id string) bool {
	id = strings.TrimSpace(id)
	if a == nil || a.Cfg == nil || id == "" {
		return false
	}
	imAllowWriteMu.Lock()
	defer imAllowWriteMu.Unlock()
	cur := imNormalizeAllow(a.Cfg.IM.Weixin.Allow)
	for _, v := range cur {
		if v == id {
			return false
		}
	}
	a.Cfg.IM.Weixin.Allow = imNormalizeAllow(append(cur, id))
	if a.CfgPath != "" {
		if err := a.Cfg.Save(a.CfgPath); err != nil {
			// 落盘失败也保留内存里的放行（本次运行照常可用），但必须留痕：
			// 重启后这条信任就没了，用户会遇到「怎么又不理我了」。
			log.Printf("[IM] 白名单写入失败（%s）：%v", maskIMID(id), err)
		}
	}
	return true
}

// imAllowOwnerOnBind 扫码绑定成功（或启动补齐）时调用：owner 直接放行。
func (a *App) imAllowOwnerOnBind(ownerID string) bool {
	if !a.imAddAllowQuiet(ownerID) {
		return false
	}
	log.Printf("[IM] 已自动把绑定账号加入白名单（owner）：%s", maskIMID(ownerID))
	return true
}

// imAllowOwnerOnStart 老用户升级补齐：白名单为空、凭据里有 owner 时写入。
// 只在**空**白名单时动手（见文件头「边界」）。
func (a *App) imAllowOwnerOnStart(channel, ownerID string) bool {
	if a == nil || a.Cfg == nil || a.Cfg.imAllowCount() > 0 {
		return false
	}
	return a.imAllowOwnerOnBind(ownerID)
}

// imAllowFirstSender 兜底 TOFU：白名单为空时信任第一个发消息的人。
// 仅当「拿不到 owner」或「发送者就是 owner」时成立——owner 已知却不是他，
// 那是别人的号，照旧 fail closed（不因为「谁先发消息」就把机器人交给陌生人）。
func (a *App) imAllowFirstSender(channel, senderID string) bool {
	if a == nil || a.Cfg == nil || a.Cfg.imAllowCount() > 0 {
		return false
	}
	if owner := a.imChannelOwner(channel); owner != "" && owner != strings.TrimSpace(senderID) {
		return false
	}
	if !a.imAddAllowQuiet(senderID) {
		return false
	}
	log.Printf("[IM] 白名单为空，已自动信任首个发送者：%s", maskIMID(senderID))
	return true
}

// imChannelOwner 取渠道的绑定者 id（拿不到返回空）。目前只有微信有「owner」概念：
// 它就是扫码登录时平台返回的 ilink_user_id。
func (a *App) imChannelOwner(channel string) string {
	if a == nil {
		return ""
	}
	if ch, ok := a.imChannelOf(channel).(*weixinChannel); ok && ch != nil {
		if acc := ch.currentAccount(); acc != nil {
			return strings.TrimSpace(acc.OwnerID)
		}
	}
	return ""
}
