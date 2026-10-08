#!/bin/sh
# jxai-server 一次性安装（210 服务器）：注册 systemd 并启动。
# 重复执行 = 更新单元并重启（升级二进制后：上传覆盖 jxai-server 再跑一遍本脚本）。
set -e
cd "$(dirname "$0")"

[ -x ./jxai-server ] || { echo "缺少可执行的 jxai-server（先把二进制与本文件放同一目录）"; exit 1; }
mkdir -p data

install -m 644 jxai-server.service /etc/systemd/system/jxai-server.service
systemctl daemon-reload
systemctl enable --now jxai-server
sleep 1
systemctl --no-pager --lines=3 status jxai-server || true
echo
echo "本机健康检查：curl http://127.0.0.1:PORT/healthz"
echo "防火墙放行（如已放过请跳过）：iptables -I TCP -p tcp --dport 2228 -j ACCEPT 并写回 /etc/iptables/iptables.rules"
echo "公网访问还需在路由器上映射 WAN 2228/TCP → 本机 2228（与 2227 同款规则）。"
