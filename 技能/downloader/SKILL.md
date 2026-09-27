---
name: downloader
description: 用内置的 aria2c 官方二进制下载文件落盘——HTTP/HTTPS/FTP 直链、断点续传、多连接加速。当需要下载安装包、附件、数据集等到磁盘，或所在环境没有 curl / https 版 wget（Windows 与安卓常见）时使用。
---

# 下载器技能（aria2c）

自带 aria2 官方预编译二进制（1.37.0），无需联网安装，HTTPS/TLS 开箱可用。
适用场景：把文件直接下载到工作区/磁盘；环境缺 curl、wget 时（安卓与
Windows 精简环境的常态）。

## 二进制位置

位于本技能目录的 `bin/` 下，按平台取用；技能根目录的绝对路径不确定时，
先用 list_dir / glob 从环境快照的技能目录线索定位：

| 平台 | 文件 |
|---|---|
| Windows（x64） | `bin/aria2c-windows-x64.exe` |
| 安卓（arm64） | `bin/aria2c-android-arm64` |

## 用法

基本下载（落到目标目录）：

```
<aria2c 路径> -d <目标目录> -o <文件名> -c "<URL>"
```

- `-c` 断点续传，大文件务必带上；`-x16 -s16` 单服务器多连接（直链限速时明显提速）
- `--max-tries=3 --retry-wait=2`：网络不稳时
- `--all-proxy=http://127.0.0.1:端口`：需要代理时
- 下载大文件用 run_command 的 `run_in_background: true`，用 job_output 看进度
- aria2c 只认直链：页面 JS 动态解析出直链的场景，先用 browser 工具拿到直链再交给它

## 平台注意

- **安卓**：/sdcard 多为 noexec 挂载，直接执行技能目录里的二进制会
  Permission denied——先 `cp` 到 `/data/local/tmp/`（adb 场景）再执行并
  `chmod 755`；打包 App 场景二进制由壳放行（nativeLibraryDir）。
- **安卓 HTTPS 证书**：报 certificate 错误时加
  `--ca-certificate=/etc/security/cacerts`（系统 CA 捆绑文件）。
- **Windows**：路径含空格加引号；不要从 PowerShell 里给 URL 裸传 `&`。
- 下载完成用 `sha256sum`（toybox/系统自带）与来源公布值核对完整性。

## 许可

aria2 官方二进制，GPLv2（含 OpenSSL 链接例外）；许可全文见本目录
`bin/COPYING` 与 `bin/LICENSE.OpenSSL`，源码：https://github.com/aria2/aria2
