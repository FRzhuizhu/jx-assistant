package main

// 内嵌的 Linux 沙箱 helper（build.bat 先产出，再编主程序）：
//   sandboxhelper/bin/jx-sandbox-linux-amd64
//   sandboxhelper/bin/jx-sandbox-linux-arm64
// 远程工作区首次执行命令时按远端架构上传到 ~/.jx-assistant/bin/。

import (
	"embed"
	"io"
)

//go:embed all:sandboxhelper/bin
var helperBinFS embed.FS

// helperBinary 返回指定架构（amd64/arm64）的 helper 二进制内容。
func helperBinary(goarch string) ([]byte, bool) {
	f, err := helperBinFS.Open("sandboxhelper/bin/jx-sandbox-linux-" + goarch)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}
	return data, true
}
