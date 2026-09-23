//go:build !windows

package main

import "errors"

// pickFolderSerialized 非 Windows 平台暂不支持原生选择框（本产品当前面向 Windows）。
func pickFolderSerialized(startDir, title string) (string, error) {
	return "", errors.New("文件夹选择框仅支持 Windows")
}
