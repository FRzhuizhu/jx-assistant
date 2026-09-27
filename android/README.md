# 安卓壳（android/）

把 Go 引擎变成真正的安卓应用：图标、前台服务保活、通知栏开关、WebView 界面。

## 结构

```
android/
├── app/build.gradle.kts          minSdk 26 / targetSdk 35 / arm64+x86_64
├── app/src/main/
│   ├── AndroidManifest.xml       INTERNET / 前台服务(dataSync) / 通知权限
│   ├── java/com/jianxiang/assistant/
│   │   ├── MainActivity.kt       通知权限引导 → 拉服务 → 等就绪广播 → WebView
│   │   └── EngineService.kt      前台服务：拉起 libjx.so、守护重启、通知栏「停止」
│   └── res/                      自适应图标（蓝底白气泡对勾）
└── app/jniLibs/arm64-v8a/libjx.so   ← 构建脚本从 ../ 复制（不入库）
```

## 引擎承载方式

引擎二进制**不入 assets**：预编译为 `libjx.so` 放 `jniLibs/<abi>/`，安装时
系统自动释放到 `nativeLibraryDir`——targetSdk 29+ 禁止执行应用数据目录里的
文件（W^X），nativeLibraryDir 是唯一豁免路径。引擎数据根 = 应用私有目录
`files/engine/`（config/data/技能），卸载即清、天然隔离其他应用。

## 构建

一条脚本（Windows Git Bash）：

```sh
sh android/build-android.sh
```

流程：交叉编译 arm64+x86_64 引擎（x86_64 需 NDK 链接器）→ 拷入 jniLibs →
生成开发签名（首次）→ gradlew assembleDebug → 输出 APK 路径。安装：
`adb install -r android/app/build/outputs/apk/debug/app-debug.apk`。

## 权限模型（壳侧现状与后续）

- 现在要的：INTERNET、前台服务、通知（拒绝不影响主功能）。
- 无障碍/屏幕识别/特权通道（Shizuku 等）**尚未接入**——对应后续「手机操控
  组件」阶段，本壳先把引擎应用化。
- API Key：首启引擎以 mock 模式跑，用户在设置页录入后落盘（0600，应用私有目录）。
