package main

// IM 文本处理的公共小工具（各平台共用，平台差异只有分段上限一个参数）。
//
// 两件事：
//   1. splitWeixinText：按平台单条上限切段。微信 1800 字是硬上限，超了会被拒
//      或截断——所以长答复必须切，且尽量切在换行处（切在句中读起来很怪）。
//   2. imPlainText：把 Markdown 降级成聊天软件里好读的纯文本。IM 客户端不渲染
//      Markdown，原样发出去就是一堆 ** 和 ###，用户看着像乱码。

import (
	"regexp"
	"strings"
)

// 行首的 Markdown 结构符号：标题井号、引用、列表、分隔线。
var (
	imHeadingRe  = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	imQuoteRe    = regexp.MustCompile(`(?m)^[ \t]{0,3}>[ \t]?`)
	imListRe     = regexp.MustCompile(`(?m)^([ \t]*)[-*+][ \t]+`)
	imFenceRe    = regexp.MustCompile("(?m)^[ \t]*```.*$")
	imBoldRe     = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	imItalicRe   = regexp.MustCompile(`(^|[\s（(])\*([^*\n]+)\*($|[\s，。；：！？）)])`)
	imStrikeRe   = regexp.MustCompile(`~~([^~\n]+)~~`)
	imInlineRe   = regexp.MustCompile("`([^`\n]+)`")
	imLinkRe     = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	imTableSepRe = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t:-]*-{2,}[ \t:|-]*$`)
	// 分隔线：RE2 不支持反向引用，用「重复的 [-*_] + 空白」三段以上来认。
	imHrRe     = regexp.MustCompile(`(?m)^[ \t]*(?:[-*_][ \t]*){3,}$`)
	imSpacesRe = regexp.MustCompile(`\n{3,}`)
)

// imPlainText 把 Markdown 降级成纯文本（保留层次感，去掉符号噪音）。
func imPlainText(md string) string {
	s := strings.ReplaceAll(md, "\r\n", "\n")
	s = imFenceRe.ReplaceAllString(s, "")          // 代码围栏行删掉，代码本身留着
	s = imTableSepRe.ReplaceAllString(s, "")       // 表格对齐行（|---|---|）删掉
	s = imHrRe.ReplaceAllString(s, "———")          // 分隔线换成一个短横线组
	s = imHeadingRe.ReplaceAllString(s, "")        // ### 标题 → 标题
	s = imQuoteRe.ReplaceAllString(s, "")          // > 引用 → 引用
	s = imListRe.ReplaceAllString(s, "$1· ")       // - 列表 → · 列表
	s = imLinkRe.ReplaceAllString(s, "$1（$2）")     // [标题](url) → 标题（url）
	s = imBoldRe.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Trim(m, "*_")
	})
	s = imItalicRe.ReplaceAllString(s, "$1$2$3")
	s = imStrikeRe.ReplaceAllString(s, "$1")
	s = imInlineRe.ReplaceAllString(s, "$1") // `code` → code
	s = imSpacesRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// splitWeixinText 按 max 个字符切段：优先在段内最后一个换行处切开（且切点不能
// 太靠前，否则会切出很多极短的段），没有合适换行就硬切。
func splitWeixinText(text string, max int) []string {
	if max <= 0 {
		max = weixinMaxChars
	}
	r := []rune(text)
	if len(r) <= max {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}
	var out []string
	for len(r) > max {
		cut := max
		if idx := lastNewlineRun(r[:max]); idx >= 0 && idx >= max*6/10 {
			cut = idx
		}
		chunk := strings.TrimRight(string(r[:cut]), "\n")
		if strings.TrimSpace(chunk) != "" {
			out = append(out, chunk)
		}
		r = []rune(strings.TrimLeft(string(r[cut:]), "\n"))
	}
	if strings.TrimSpace(string(r)) != "" {
		out = append(out, string(r))
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

// lastNewlineRun 返回 r 里最后一个换行符的下标（没有则 -1）。
func lastNewlineRun(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == '\n' {
			return i
		}
	}
	return -1
}
