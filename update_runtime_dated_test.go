package main

// runtime 块版本的行为锁（2026-11-01 起）：
// 客户端对 runtime 块只用「标记相等才跳过」，不做大小比较。
// 应用包时**不再校验**块版本与包内 Python 版本的匹配（那道校验在块版本格式演进时
// 把合法包拒之门外，纯防自己人，已删；完整性由 sha256 兜底）——所以纯日期式版本串
// 现在也能正常应用，为几周后「复合格式 → 纯日期式」的切换铺平了路。
//   1) 复合串「py3.14.8-20261101-1」：标记相等 → 跳过；旧标记 vs 新复合串 → 触发下载
//   2) 纯日期串「20261101-2」：应用不被拦截（走到了冒烟/回滚，而不是被版本校验拒绝）

import (
	"path/filepath"
	"sync/atomic"
	"testing"
)

// 纯日期式版本串：应用路径不被版本校验拦截（走到了冒烟；假 python.exe 冒烟失败回滚，
// 恰好证明流程走通——之前那道校验会在这里直接拒绝并报「版本不符」）。
func TestRuntimeBlockDatedVersionAppliesWithoutTagCheck(t *testing.T) {
	a, skillsDir := newBlocksTestApp(t)
	rt := filepath.Join(skillsDir, "_runtime")
	writeFileTB(t, filepath.Join(rt, "python", "python314._pth"), "python314.zip\n")
	writeFileTB(t, filepath.Join(rt, runtimeMarkerName), "py3.14.8-20261101-1\n")

	rel, hits := serveZip(t, "runtime-20261101-2.zip",
		makeZip(t, map[string]string{"python/python314._pth": "python314.zip\n", "python/python.exe": "not-a-real-exe"}))
	a.applyRuntimeBlock(&updateBlock{Version: "20261101-2", File: "runtime-20261101-2.zip"}, rel, "")

	if n := atomic.LoadInt32(hits); n == 0 {
		t.Fatal("纯日期式版本必须能进入下载（标记不等）")
	}
	if !exists(filepath.Join(rt, "python", "python314._pth")) {
		t.Fatal("冒烟失败必须回滚保留旧运行时")
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
