# jxai-server 部署（210 服务器）

jxai-server 是一体化服务端：**远程访问中继**（deploy-210 网关 server.js 的 Go 移植，
协议逐字节兼容）+ **技能/组件/插件市场**。单静态二进制，无运行时依赖。

## 端口与并行策略

| 端口 | 服务 | 说明 |
|---|---|---|
| 2227 | chat-p2p（现有 Node 网关） | **保持不动**，现有手机端/聊天服务继续用它 |
| 2228 | jxai-server（本服务） | 新并行验证；验证通过后客户端把网关端口从 2227 改 2228 即完成切换 |

`-peer-key` 与 chat-p2p 一致（`chat_p2p_v1`），客户端的 RegKey 无需改。
公网访问需在路由器上映射 `WAN 2228/TCP → 192.0.2.210:PORT`（与 2227 同款规则），
并放行 iptables：`iptables -I TCP -p tcp --dport 2228 -j ACCEPT`（写入
`/etc/iptables/iptables.rules` 后 `systemctl reload iptables`，照 2227 的既有规则抄）。

## 首次部署

在开发机（能 `ssh jxmes-git` 的机器）上：

```powershell
# 1. 出包（交叉编译 Linux 二进制 + 部署脚本，产 zip）
cd D:\智能体工作区\软著版数字助理
powershell -File 打包\出服务端包.ps1

# 2. 上传并安装
scp 打包\输出服务端\jxai-server-linux-*.zip jxmes-git:/opt/
ssh jxmes-git
cd /opt && mkdir -p jxai-server && cd jxai-server && unzip -o ../jxai-server-linux-*.zip && sh setup.sh
```

## 日常运维

```sh
systemctl status jxai-server          # 状态（healthz 里有 hub 连接数与市场条目数）
journalctl -u jxai-server -f          # 日志（中继注册/断开都有记录）
curl http://127.0.0.1:PORT/healthz  # 健康检查
```

## 发布市场条目（技能 / 组件 / 插件）

市场匿名只读（公司内部够用）；发布在服务器上经 SSH 用子命令完成：

```sh
ssh jxmes-git
cd /opt/jxai-server

# 发布：来源可以是目录或 zip（scp 上来的），id 缺省从包清单推导
./jxai-server publish skill     /tmp/演示技能目录                       -data data
./jxai-server publish component /tmp/输入字数统计组件  -tags 前端,模板   -data data
./jxai-server publish plugin    /tmp/某插件.zip                          -data data

# 下架
./jxai-server unpublish component input-counter -data data
```

发布门槛：技能根（或唯一子目录）须有 `SKILL.md`；组件须有 `manifest.json` 且
其声明的 `client.js`/`client.css` 在包里（**声明 `host:true` 的带宿主半边组件拒收**，
那种组件随主程序发版，市场装不了宿主半边）；插件须有 `.claude-plugin/plugin.json`
（或根 `plugin.json`）且 name 非空。中文展示名随便用，但 id 会清洗成 ASCII——
纯中文名请用 `-id` 指定（如 `-id demo-skill`）。

客户端在 设置 · 网络访问 · 市场服务器 填
`http://nas.example.org:PORT/market/manifest.json`（局域网内
`http://192.0.2.210:PORT/market/manifest.json`）。

## 市场管理页（/admin）

浏览器打开 `http://<服务地址:PORT>/admin`：技能/组件/插件三个页签 + 搜索，
每条目可「下架」；顶部表单可「上传发布」zip 包（元数据可选填）——与 CLI
publish/unpublish 走同一条校验/落盘链路，二选一皆可。

**访问控制是「知道网址才能进」的过渡形态**：页面没有任何公开入口（配对页、
客户端市场页都不出现它），服务也只在内网/公司内部可达。用户/角色/管理员体系
（用户名登录、用户组、管理员授权）是后续计划，接上后在  路由前加一道
鉴权即可，页面本身不用改。

## 软件更新分发（两级拉取式）

```
总服务端（公司总部）   jxai-server publish-update 安装包.exe -version 20261008V1 -notes "说明"
                       └─ GET /updates/latest ──┐
客户公司服务端         -update-upstream http://总服务端:PORT
                       （启动 10s 首拉、每小时对账，上游有新版即下载镜像）
                       └─ GET /updates/latest ──┐
客户端（数字助理）     config.updateURL = http://本服务端:PORT/updates/latest
                       （现有轮询/横幅/确认更新全部复用，无需改客户端）
```

- **版本号体系：日期+V序号**（`20261008V1`，同日发多版用 `V2` `V3`）。恒新于
  旧点分号（`0.1.9`），客户端（update.go 的 versionCompare）与服务端
  （updates.go）同规则；主程序 appVersion 已切到该体系。
- `/updates/latest` 下发 **Gitea releases/latest 兼容 JSON**（tag_name +
  assets 直链），附件直链按请求方到达的 Host 现场生成——127.0.0.1、内网 IP、
  公网域名随请求自动正确；未发布过版本时 404（客户端静默视为无更新）。
- 总服务端发布（SSH 上服务器执行）：

```sh
scp 坚祥数字助理GO-安装包-20261008V1.exe jxai-server主机:/tmp/
ssh 主机 "cd /opt/jxai-server && ./jxai-server publish-update /tmp/坚祥数字助理GO-安装包-20261008V1.exe \
  -version 20261008V1 -notes '本次更新内容一句话' -data data"
```

- 客户公司服务端只需启动参数加 `-update-upstream`（systemd 单元里改
  ExecStart 后 `systemctl restart jxai-server`）；拉取失败保留现有版本。
- 旧版本包在发新版/同步新版时自动清理，存储不随版本累积。

## 与 Node 网关的差异（有意为之）

- **不提供 /peerjs 信令**：Go 客户端（remoteaccess.go）本就不走 PeerJS 直连；
  配对页（gate.html，内嵌副本）在服务时被注入 `_forceRelay` 直接走中继，省 3 秒等待。
- 其余协议逐条对齐 server.js：`/hub` 注册（15s 窗口/新顶旧/30s ping×2 踢死链）、
  `POST /relay`（NDJSON 透传/150s 超时/70MB/每 IP 8 并发/abort 三触发）、
  `/a/<设备号>` 配对页、`/healthz`。
- 单测与本机 E2E（真实客户端 + 真实浏览器全链路）见 `jxaiserver/relay_test.go`
  与本次提交说明；切换 2227 前先在 2228 跑稳。

## 升级

```sh
# 开发机：重新出包并上传
powershell -File 打包\出服务端包.ps1
scp 打包\输出服务端\jxai-server-linux-*.zip jxmes-git:/opt/
ssh jxmes-git "cd /opt/jxai-server && unzip -o ../jxai-server-linux-*.zip && systemctl restart jxai-server"
```

数据目录 `data/`（market.json 与 items/）不受升级影响，直接留存。

## 回滚 / 下线

```sh
ssh jxmes-git "systemctl disable --now jxai-server"   # 停用；客户端改回 2227 即回到旧网关
```

## 三期预留：模型中转

jxai-server 的子命令入口（main.go）为模型中转（OpenAI 兼容反代）留了位置；
优先方案是直接复用服务器上已部署的开源中转（providers 的 baseURL 指过去即可，
零开发），需要收进来时再实现。
