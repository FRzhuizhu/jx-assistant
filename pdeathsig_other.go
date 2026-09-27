//go:build !android

package main

// 非安卓平台不需要父进程死亡信号（Windows 有 Job 对象兜底，桌面
// Linux 引擎与桌面程序同生命周期）。

func setupParentDeathSignal() {}
