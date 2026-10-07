# jx-server 部署（210 服务器）

jx-server 是一体化服务端：**远程访问中继**（deploy-210 网关 server.js 的 Go 移植，
协议逐字节兼容）+ **技能/组件/插件市场**。单静态二进制，无运行时依赖。

## 端口与并行策略

| 端口 | 服务 | 说明 |
|---|---|---|
| 2227 | chat-p2p（现有 Node 网关） | **保持不动**，现有手机端/聊天服务继续用它 |
| 2228 | jx-server（本服务） | 新并行验证；验证通过后客户端把网关端口从 2227 改 2228 即完成切换 |

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
scp 打包\输出服务端\jx-server-linux-*.zip jxmes-git:/opt/
ssh jxmes-git
cd /opt && mkdir -p jx-server && cd jx-server && unzip -o ../jx-server-linux-*.zip && sh setup.sh
```

## 日常运维

```sh
systemctl status jx-server          # 状态（healthz 里有 hub 连接数与市场条目数）
journalctl -u jx-server -f          # 日志（中继注册/断开都有记录）
curl http://127.0.0.1:PORT/healthz  # 健康检查
```

## 发布市场条目（技能 / 组件 / 插件）

市场匿名只读（公司内部够用）；发布在服务器上经 SSH 用子命令完成：

```sh
ssh jxmes-git
cd /opt/jx-server

# 发布：来源可以是目录或 zip（scp 上来的），id 缺省从包清单推导
./jx-server publish skill     /tmp/演示技能目录                       -data data
./jx-server publish component /tmp/输入字数统计组件  -tags 前端,模板   -data data
./jx-server publish plugin    /tmp/某插件.zip                          -data data

# 下架
./jx-server unpublish component input-counter -data data
```

发布门槛：技能根（或唯一子目录）须有 `SKILL.md`；组件须有 `manifest.json` 且
其声明的 `client.js`/`client.css` 在包里（**声明 `host:true` 的带宿主半边组件拒收**，
那种组件随主程序发版，市场装不了宿主半边）；插件须有 `.claude-plugin/plugin.json`
（或根 `plugin.json`）且 name 非空。中文展示名随便用，但 id 会清洗成 ASCII——
纯中文名请用 `-id` 指定（如 `-id demo-skill`）。

客户端在 设置 · 网络访问 · 市场服务器 填
`http://nas.example.org:PORT/market/manifest.json`（局域网内
`http://192.0.2.210:PORT/market/manifest.json`）。

## 与 Node 网关的差异（有意为之）

- **不提供 /peerjs 信令**：Go 客户端（remoteaccess.go）本就不走 PeerJS 直连；
  配对页（gate.html，内嵌副本）在服务时被注入 `_forceRelay` 直接走中继，省 3 秒等待。
- 其余协议逐条对齐 server.js：`/hub` 注册（15s 窗口/新顶旧/30s ping×2 踢死链）、
  `POST /relay`（NDJSON 透传/150s 超时/70MB/每 IP 8 并发/abort 三触发）、
  `/a/<设备号>` 配对页、`/healthz`。
- 单测与本机 E2E（真实客户端 + 真实浏览器全链路）见 `jxserver/relay_test.go`
  与本次提交说明；切换 2227 前先在 2228 跑稳。

## 升级

```sh
# 开发机：重新出包并上传
powershell -File 打包\出服务端包.ps1
scp 打包\输出服务端\jx-server-linux-*.zip jxmes-git:/opt/
ssh jxmes-git "cd /opt/jx-server && unzip -o ../jx-server-linux-*.zip && systemctl restart jx-server"
```

数据目录 `data/`（market.json 与 items/）不受升级影响，直接留存。

## 回滚 / 下线

```sh
ssh jxmes-git "systemctl disable --now jx-server"   # 停用；客户端改回 2227 即回到旧网关
```

## 三期预留：模型中转

jx-server 的子命令入口（main.go）为模型中转（OpenAI 兼容反代）留了位置；
优先方案是直接复用服务器上已部署的开源中转（providers 的 baseURL 指过去即可，
零开发），需要收进来时再实现。
