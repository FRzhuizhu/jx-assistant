# 助理连接（jx-link）

坚祥数字助理的**独立轻量手机壳**：只做一件事——扫码连接电脑端的数字助理远程应用。
不带引擎、不带运行时（对比完整手机端约 103MB，本壳约 5MB），界面就是系统 WebView。

与完整手机端（`../软著版数字助理/android/`，包名 `com.jianxiang.assistant`）完全独立，
可同时安装；本壳包名 `com.jianxiang.link`，图标与现有应用同款 logo。

## 功能

- **扫码连接多台电脑**：扫电脑端 设置 · 网络访问 的入口二维码
  （`http://网关:端口/a/设备号#pk=配对密钥`），也支持局域网直连地址（`http://ip:端口`）。
  首页列表显示每台电脑的在线/离线（离线灰字），点击进入。
- **电脑页**：WebView 打开入口，gate.html 自动经加密中继拉起应用外壳，与浏览器里
  的远程体验一致。顶部浮一条**应用级「⌂ 设备名」胶囊**（原生控件浮在网页之上、
  屏幕水平绝对居中，像浏览器菜单栏；不参与网页布局，网页顶栏怎么排都不受影响——
  2026-10-12 改造，此前注入网页顶栏的方案在编程模式下会把右侧栏按钮挤到第二行），
  点击回首页；从屏幕**顶缘下滑 / 底缘上滑**
  也回首页（仅认从边缘起手的滑动，中间滑对话不受影响，参数对齐电脑端左右栏手势）。
- **HTTPS 优先（2026-10-12）**：语音输入需要安全上下文（getUserMedia 仅 HTTPS）。
  壳子把 http 入口自动升级为 https 同端口加载，配对网关的自签证书自动「继续访问」
  （白名单只放行已配对 host，陌生站点照旧拒绝）；网关不支持 https 时自动回落
  http，一切如旧——浏览器扫码访问仍是 http，只有壳子享受 https。麦克风经
  `onPermissionRequest` 对配对 host 放行（首次弹系统权限框）。
- **返回键（2026-10-12）**：第一次交给网页的返回键守卫——对话页→呼出左栏、
  左栏→收回对话页、弹窗/设置页逐层收敛（设置页走它自己的两级 popstate 返回）；
  **3 秒内第二次返回 = 回壳首页**（不拦用户退出）。
- **记忆位置**：退出应用时停在哪台电脑里，下次冷启动直接进那台；回到首页后再退出
  则下次从首页开始。
- **缓存管理**：
  - 长按设备行 → 清空缓存 / 删除（均二次确认；删除同时清缓存）
  - 设置 → 清空应用缓存（所有电脑 + 应用自身）
  - 每台电脑一个独立 WebView Profile（WebView 114+），缓存与 localStorage 互不串门。
    清空时优先 `ProfileStore.deleteProfile` 整域删除；Chromium 对**本进程内用过的**
    Profile 会拒绝删除（"Cannot delete in-use profile"，销毁异步且常驻），此时退化为
    `Profile.getWebStorage().deleteAllData()` 清站点数据（localStorage/外壳缓存——
    远程模式下有意义的数据都在这里），磁盘缓存待进程重启后删除
  - 旧 WebView（<114 无多 Profile）退化为共享域 + 临时 WebView 打开该源后
    `localStorage.clear()`
- **在线探测**：对网关 `POST /relay` 发一个几十字节的加密探测帧（GET /api/shell-hash），
  网关对不在线设备回 503——不上注册号（同号重复注册会互踢），只借中继通道探活。
- **设置页**：清空应用缓存 + 反馈意见（multipart 封帧经中继发电脑端 `/api/feedback`，
  依次尝试、上次所在电脑优先）。

## 构建

```sh
sh build.sh        # 图标 → 签名 → gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

依赖：JDK 17、Android SDK（compileSdk 35）、带 Pillow 的 Python（生成图标）。
工具链版本与主仓库安卓壳对齐（AGP 8.7.3 / Kotlin 2.1.0 / Gradle 8.9），
`android.overridePathCheck=true` 应对中文路径。

## 结构

```
app/src/main/java/com/jianxiang/link/
├── MainActivity.kt      首页：两行按钮（扫码连接/设置）+ 设备列表 + 长按菜单 + 二次确认
├── ScanActivity.kt      CameraX + ZXing 扫码（四向旋转解码，屏幕码横放也能认）
├── DeviceActivity.kt    WebView 电脑页：独立 Profile、应用级首页浮条、边缘手势、失败浮层
├── DeviceStore.kt       设备列表与「上次所在」记忆（SharedPreferences JSON）；Cache 清理
├── Relay.kt             中继协议客户端：入口解析、AES-GCM 封包、探活、反馈发送
└── SettingsActivity.kt  设置页：清空应用缓存 + 反馈意见
```

## 已在模拟器（AVD jx-test，Android 15 / WebView 124）点验

首页两行按钮与设备列表（在线/离线灰字）、点击进电脑页（经真实 210 网关中继拉起
完整远程外壳）、应用级「⌂ 设备名」浮条（水平绝对居中、网页顶栏一行不被挤）、
HTTPS 升级 + 自签白名单（本机自签 https 服务器实测：http 入口自动升 https 加载、
无证书告警；网关仅 http 时自动回落）、麦克风权限链（getUserMedia → 系统权限框 →
grant 回传；模拟器无麦克风硬件止于 NotReadableError，真机即通）、返回键（对话页
呼出左栏 / 左栏收回对话页 / 3 秒双击回首页）、顶缘下滑/底缘上滑回首页（中部滑动不误触）、长按菜单与两处二次确认、清空缓存
（storage 清空 + deleteProfile 尽力）、删除设备、设置页两区域、反馈意见（真实发送
经中继落库电脑端 feedback 表，diagnostics 带 src=jx-link）、扫码页相机权限流程、
冷启动记忆直达（退出时在哪台电脑里，下次直接进哪台）。

## 设计要点

- **注入按钮为何能活过换页**：gate.html 用 `document.open()/write()` 把配对页整页换成
  应用外壳，window 上的 JS 全局状态是存活的（gate.html 自己的顶层 const 残留冲突即证），
  守护 `setInterval` 装一次即可，页面内部重建由轮询幂等补挂。
- **明文 http**：网关入口都是 http（远程接入形态如此），manifest 开了
  `usesCleartextTraffic`；链路本身有端到端 AES-256-GCM（密钥=SHA-256(配对密钥)）。
- **不碰注册通道**：探活绝不向 `/hub` 注册同设备号（网关「同号新顶旧」会把电脑踢下线）。
