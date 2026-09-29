//go:build !android

package main

// 非安卓平台：无手机操控工具（device_* / adb_* 仅安卓注册；桥端点仍编译
// 进引擎——桌面本地 curl 自检等场景无害，无壳轮询自然永远空闲）。
func deviceTools() []Tool { return nil }
