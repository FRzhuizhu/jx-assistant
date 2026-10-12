#!/bin/sh
# 助理连接（jx-link）一键构建：图标 → 签名 → gradle 打包 → APK 路径。
# 用法：sh build.sh   （在任意目录执行均可）
set -e
cd "$(dirname "$0")"

echo "[1/3] 由主仓库 logo 生成图标"
python make_icons.py || py -3 make_icons.py

echo "[2/3] 开发签名（首次生成，仅本机自用）"
KS=app/debug.keystore
if [ ! -f "$KS" ]; then
  keytool -genkeypair -keystore "$KS" -alias jianxiang -storepass jianxiang \
    -keypass jianxiang -keyalg RSA -keysize 2048 -validity 10950 \
    -dname "CN=JxLink, O=Jianxiang, C=CN"
fi

echo "[3/3] Gradle 打包（首次会下载依赖，需几分钟）"
if [ -x "./gradlew" ]; then GRADLE=./gradlew; else GRADLE=gradle; fi
$GRADLE assembleDebug --no-daemon -q || $GRADLE assembleDebug --no-daemon

echo "完成："
ls -lh app/build/outputs/apk/debug/*.apk
echo "安装: adb install -r app/build/outputs/apk/debug/app-debug.apk"
