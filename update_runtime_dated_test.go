package main

// runtime 块版本改复合格式「py3.14.8-20261101-1」的行为锁（2026-11-01 起）：
// 客户端对 runtime 块只用「标记相等才跳过」，不做大小比较；
// 应用前的包内校验要求块版本前缀与包内 Python 大版本匹配（tagMatchesVersion）。
//   1) 复合串过校验：tagMatchesVersion("3.14", "py3.14.8-20261101-1") = true
//   2) 纯日期式过不了校验（留作反例，防止将来有人改回纯日期）
//   3) 标记相等（复合串）→ 跳过；旧标记 py3.14.8 vs 新复合串 → 触发下载

import (
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRuntimeBlockCompositeVersionPassesTagCheck(t *testing.T) {
	if !tagMatchesVersion("3.14", "py3.14.8-20261101-1") {
		t.Fatal("复合格式必须能过包内 Python 版本校验（前缀 3.14.）")
	}
	if tagMatchesVersion("3.14", "20261101-1") {
		t.Fatal("纯日期式不含 Python 版本，包内校验必然拒绝——这正是采用复合串的原因")
	}
	if tagMatchesVersion("3.12", "py3.14.8-20261101-1") {
		t.Fatal("Python 大版本不同时必须不匹配（触发重装）")
	}
}

func TestRuntimeBlockCompositeSkipsWhenEqual(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	rt := filepath.Join(skillsDir, "_runtime")
	writeFileTB(t, filepath.Join(rt, "python", "python314._pth"), "python314.zip\n")
	writeFileTB(t, filepath.Join(rt, runtimeMarkerName), "py3.14.8-20261101-1\n")

	rel, hits := serveZip(t, "runtime-py3.14.8-20261101-1.zip",
		makeZip(t, map[string]string{"python/python.exe": "x"}))
	a.applyRuntimeBlock(&updateBlock{Version: "py3.14.8-20261101-1", File: "runtime-py3.14.8-20261101-1.zip"}, rel, "")

	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("复合串标记相等时不该下载，实际请求 %d 次", n)
	}
	if !exists(filepath.Join(rt, "python", "python314._pth")) {
		t.Fatal("未更新时运行时目录必须原样保留")
	}
}

func TestRuntimeBlockOldMarkerVsCompositeDownloads(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	rt := filepath.Join(skillsDir, "_runtime")
	writeFileTB(t, filepath.Join(rt, "python", "python314._pth"), "python314.zip\n")
	writeFileTB(t, filepath.Join(rt, runtimeMarkerName), "py3.14.8\n") // 旧格式标记（10-31 及以前）

	rel, hits := serveZip(t, "runtime-py3.14.8-20261101-1.zip",
		makeZip(t, map[string]string{"python/python314._pth": "python314.zip\n", "python/python.exe": "not-a-real-exe"}))
	a.applyRuntimeBlock(&updateBlock{Version: "py3.14.8-20261101-1", File: "runtime-py3.14.8-20261101-1.zip"}, rel, "")

	if n := atomic.LoadInt32(hits); n == 0 {
		t.Fatal("旧格式标记遇到新复合串必须触发下载（标记不等，装了库就要重下）")
	}
	// 假 python.exe 冒烟必失败 → 回滚；关键是版本校验不再拦截（之前纯日期式在这里被拒）
	if !exists(filepath.Join(rt, "python", "python314._pth")) {
		t.Fatal("冒烟失败必须回滚保留旧运行时")
	}
}
