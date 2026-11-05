package main

import (
	"strings"
	"testing"
)

// filterOfficeGuide 的边界：office_skill 按分册返回指南，只带对应分册
// 才能省上下文。曾经写成 kindTitle[4:]，把 "## " 当成 4 个字符切出
// "ocx 分册要点"，HasPrefix 恒假、静默退回全文（三册一起进上下文）。
func TestFilterOfficeGuide(t *testing.T) {
	guide := `---
name: office
---

# Office 文档技能

## 通用纪律

- 纪律一

## docx 分册要点

- docx 要点 A

### 老式 .doc（OLE2）只能走 Word COM

- COM 要完全访问

## xlsx 分册要点

- xlsx 要点 A

## pptx 分册要点

- pptx 要点 A

## 结构校验

- 校验
`

	cases := []struct{ kind, want, notWant string }{
		{"docx", "docx 要点 A", "xlsx 要点 A"},
		{"xlsx", "xlsx 要点 A", "pptx 要点 A"},
		{"pptx", "pptx 要点 A", "docx 要点 A"},
	}
	for _, c := range cases {
		got := filterOfficeGuide(guide, c.kind)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s 分册缺少 %q\n---\n%s", c.kind, c.want, got)
		}
		if strings.Contains(got, c.notWant) {
			t.Errorf("%s 分册不该含 %q（别的分册混进来了）\n---\n%s", c.kind, c.notWant, got)
		}
		if !strings.Contains(got, "纪律一") {
			t.Errorf("%s 分册缺少通用纪律", c.kind)
		}
	}

	// ### 子节不是分册边界：docx 分册要把 .doc/COM 子节一起带出
	if got := filterOfficeGuide(guide, "docx"); !strings.Contains(got, "COM 要完全访问") {
		t.Errorf("docx 分册应包含 .doc/COM 子节，实际：\n%s", got)
	}

	// 抽不到时原样返回全文（老版 SKILL.md 或标题改名时的兜底）
	if got := filterOfficeGuide(guide, "unknown"); got != guide {
		t.Errorf("未知 kind 应原样返回全文，实际长度 %d，原文 %d", len(got), len(guide))
	}
}
