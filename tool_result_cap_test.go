package main

// 工具结果统一封顶的测试：agent 层 clipToolResult 是最后一层兜底，任何工具的
// 单次输出超过 toolResultMaxBytes（128KB）都截断并提示续读，且在 rune 边界
// 收口（不劈开多字节字符）。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipToolResult(t *testing.T) {
	// 未超限：原样返回
	small := "普通工具结果"
	if got := clipToolResult(small); got != small {
		t.Fatalf("未超限应原样返回, got %q", got)
	}
	// 超限：截断 + 续读提示，且保留部分不劈开 rune
	big := strings.Repeat("字", toolResultMaxBytes) // 每个汉字 3 字节，必然超 128KB
	got := clipToolResult(big)
	if !strings.Contains(got, "工具结果过长已截断") || !strings.Contains(got, "offset") {
		t.Fatal("超限应截断并提示续读")
	}
	if !utf8.ValidString(got) {
		t.Fatal("截断后必须是合法 UTF-8（不能在多字节字符中间切）")
	}
	// 截断后主体应明显小于原串
	if len(got) >= len(big)/2 {
		t.Fatalf("截断后应显著变短: got %d bytes, orig %d bytes", len(got), len(big))
	}
}
