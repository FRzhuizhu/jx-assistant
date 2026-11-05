package main

import "os"

// fileExists 判断路径存在且是文件（不是目录）。voicetype 语音历史留档等模块使用；
// 上游主线的同名函数位于内置连接器管理文件，本仓库暂未包含该文件，先独立提供。
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
