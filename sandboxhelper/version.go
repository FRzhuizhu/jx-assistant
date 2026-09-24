package sandboxhelper

// Version 是沙箱 helper 的语义版本：主程序的 ensureRemoteHelper 用它与
// 远端已安装 helper 的 `version` 输出比对，不一致即重新上传覆盖。
// helper 行为/语义有变化时必须递增此版本号。
const Version = "2"
