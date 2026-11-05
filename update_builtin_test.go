package main

// 内置组件更新块：解析与"本机没有安装位置就跳过"的防呆。
// 真正的下载/替换路径与 runtime/skills 同构（共用 downloadUpdateFile /
// unzip / copyDir），不在这里重复造轮子测。

import (
	"encoding/json"
	"testing"
)

func TestBuiltinBlockParse(t *testing.T) {
	raw := `{"builtin":{"version":"bi-20261103","file":"builtin-bi-20261103.zip","sha256":"ABcd"}}`
	var b updateBlocks
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if b.Builtin == nil {
		t.Fatal("builtin 块没解析出来")
	}
	if b.Builtin.Version != "bi-20261103" || b.Builtin.File != "builtin-bi-20261103.zip" {
		t.Fatalf("builtin 块字段不对: %+v", b.Builtin)
	}
}

// 没有可用安装位置时（例如跑在 go test 的临时目录里），applyBuiltinBlock
// 必须安静跳过，不能凭空造一个内置组件目录出来。
func TestApplyBuiltinBlockSkipsWhenNoInstallDir(t *testing.T) {
	if builtinComponentsPath() != "" {
		t.Skip("本机 exe 同级存在 内置组件，跳过该前提的用例")
	}
	a := &App{Cfg: &Config{DataDir: t.TempDir()}}
	// 不 panic、不落盘即算通过
	a.applyBuiltinBlock(&updateBlock{Version: "x", File: "y.zip"}, releaseInfo{}, "")
}
