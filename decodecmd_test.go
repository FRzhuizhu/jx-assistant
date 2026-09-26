package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 回归背景：默认的沙箱执行路径曾把 GBK 控制台输出按 UTF-8 有损转换后直接
// 入库（bytes→string 隐式替换），中文全部变成 U+FFFD 黑框乱码且不可恢复。
// 解码必须发生在字节层：合法 UTF-8 原样通过，否则按 GB18030 还原。
func TestDecodeCmdOutput(t *testing.T) {
	const want = `D:\智能体工作区\软著版数字助理> dir / 输出`
	gbk, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if utf8.Valid(gbk) {
		t.Skip("编码巧合：GBK 字节恰为合法 UTF-8，无法构造用例")
	}
	if got := decodeCmdOutput(gbk); got != want {
		t.Fatalf("GBK 输出未被正确还原：%q", got)
	}

	const utf8Out = "UTF-8 输出 ✓ 中文注释"
	if got := decodeCmdOutput([]byte(utf8Out)); got != utf8Out {
		t.Fatalf("合法 UTF-8 输出被改动：%q", got)
	}

	// 0xFF 不是合法 GB18030 字节：两条路都走不通时替换符兜底，且结果恒为合法 UTF-8
	out := decodeCmdOutput([]byte{0xff, 'o', 'k'})
	if !utf8.ValidString(out) || !strings.Contains(out, "ok") {
		t.Fatalf("兜底路径结果异常：%q", out)
	}
}
