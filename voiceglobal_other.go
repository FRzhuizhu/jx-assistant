//go:build !windows

package main

// 全局语音输入法的非 Windows 桩：钩子、浮窗、注入全是 Windows 桌面壳专属，
// 其他平台（安卓/Linux/浏览器形态的服务端）一律空实现——路由可注册，调用
// 即报「仅桌面版支持」，不阻断编译。

import "errors"

var errVoiceGlobalUnsupported = errors.New("全局语音输入法仅支持 Windows 桌面版")

func voiceGlobalSupported() bool { return false }

func voiceGlobalApply(app *App, baseURL string) {}
func voiceGlobalShutdown()                      {}
func voiceGlobalToggle()                        {}
func voiceGlobalStopSession()                   {}
func voiceGlobalReapply(app *App)               {}
func voiceGlobalFinish(text string, epoch int64) {
}
