// jx-sandbox：Linux 沙箱 helper 的独立可执行入口。
// 构建见根目录 build.bat（GOOS=linux，amd64/arm64 两个产物，
// 经 go:embed 随主程序分发、按需上传到 SSH 远程主机）。
package main

import (
	"os"

	"jxassistant/sandboxhelper"
)

func main() {
	os.Exit(sandboxhelper.Main(os.Args[1:]))
}
