package main

// 技能运行时装配的单元测试：就绪标记的判定语义（已就绪不重装、标记不能单独
// 算数、版本升级后重装）与「已就绪时装配函数不联网」。
// 真实下载链路（11MB + pip）不在这里跑——首次装配在隔离实例上真机验过。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ④ 就绪标记的三个前提：标记在、版本对、python.exe 真在。
func TestRuntimeReadyMarker(t *testing.T) {
	dir := t.TempDir()
	if runtimeReady(dir) {
		t.Fatalf("空目录不该算就绪")
	}

	// 只有标记、没有 python.exe：手工残局不能当就绪（否则会一路静默失败）
	if err := os.WriteFile(filepath.Join(dir, runtimeReadyMarker), []byte(pythonVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtimeReady(dir) {
		t.Fatalf("没有 python.exe 时，光有标记不该算就绪")
	}

	// exe 在，但标记版本不符（运行时升级后）→ 需要重新装配
	exe := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runtimeReadyMarker), []byte("3.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtimeReady(dir) {
		t.Fatalf("标记版本与当前 pythonVersion 不符，不该算就绪")
	}

	// exe + 当前版本标记（带结尾换行也认）→ 就绪
	if err := os.WriteFile(filepath.Join(dir, runtimeReadyMarker), []byte(pythonVersion+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !runtimeReady(dir) {
		t.Fatalf("exe + 版本一致的标记应算就绪")
	}
}

// ④ 已就绪的运行时不许再走装配（不下载、不装 pip）：装配函数应立即返回，
// 且不做任何网络动作——测试里没有网也能过。
func TestProvisionReadyRuntimeSkipsInstall(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := markRuntimeReady(dir); err != nil {
		t.Fatal(err)
	}
	var notes []string
	py, err := provisionPythonRuntime(dir, nil, nil, func(s string) { notes = append(notes, s) })
	if err != nil {
		t.Fatalf("已就绪的运行时不该装配失败: %v", err)
	}
	if py == nil || py.Exe != exe {
		t.Fatalf("应直接复用已有运行时，得到 %+v", py)
	}
	for _, n := range notes {
		if strings.Contains(n, "正在下载") || strings.Contains(n, "安装 pip") || strings.Contains(n, "正在安装技能依赖") {
			t.Fatalf("已就绪时不该再装配，却出现了：%q（全部：%v）", n, notes)
		}
	}
}

// ④ 装配失败不能留下「就绪」标记：中途退出下次必须重来。
// 这条不写成用例——失败路径会真的去下载 get-pip.py（测试不联网），改为在
// TestRuntimeReadyMarker 里覆盖「半成品目录不算就绪」这一半语义，标记只在
// provisionPythonRuntime 全部步骤成功后才写（代码顺序保证）。
