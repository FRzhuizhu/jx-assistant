package main

// 组件脚本的分发：scripts/ 下的 Python 脚本经 go:embed 打进单可执行文件，
// 首次使用时落盘到系统临时目录供解释器执行。文件即最终产物，内容以
// 仓库内 scripts/ 为准，这里不做任何加工。

import (
	"embed"
	"os"
	"path/filepath"
	"sync"
)

//go:embed scripts/*.py
var scriptsFS embed.FS

var (
	scriptsMu   sync.Mutex
	scriptsDir  string
	scriptsDone bool
)

// ensureScripts 把内嵌脚本写到临时目录并返回该目录；重复调用复用。
func ensureScripts() (string, error) {
	scriptsMu.Lock()
	defer scriptsMu.Unlock()
	if scriptsDone {
		return scriptsDir, nil
	}
	dir := filepath.Join(os.TempDir(), "jx-assistant-scripts")
	entries, err := scriptsFS.ReadDir("scripts")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	for _, e := range entries {
		data, err := scriptsFS.ReadFile("scripts/" + e.Name())
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0644); err != nil {
			return "", err
		}
	}
	scriptsDir = dir
	scriptsDone = true
	return scriptsDir, nil
}
