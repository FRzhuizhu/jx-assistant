#!/bin/sh
# 数字助理安卓壳一键构建：交叉编译引擎 → jniLibs → gradle 打包 → APK 路径。
# 用法：sh android/build-android.sh   （在仓库根目录执行）
set -e
cd "$(dirname "$0")/.."
ROOT="$(pwd)"
NDK_CC='D:\Android\Sdk\ndk\29.0.14206865\toolchains\llvm\prebuilt\windows-x86_64\bin\x86_64-linux-android35-clang.cmd'

echo "[1/4] 交叉编译引擎 arm64 + x86_64"
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -ldflags="-s -w" -o android/app/src/main/jniLibs/arm64-v8a/libjx.so .
mkdir -p android/app/src/main/jniLibs/x86_64
CGO_ENABLED=1 CC="$NDK_CC" GOOS=android GOARCH=amd64 go build -ldflags="-s -w" -o android/app/src/main/jniLibs/x86_64/libjx.so .

echo "[2/4] 开发签名（首次生成，仅本机自用）"
KS=android/app/debug.keystore
if [ ! -f "$KS" ]; then
  keytool -genkeypair -keystore "$KS" -alias jianxiang -storepass jianxiang \
    -keypass jianxiang -keyalg RSA -keysize 2048 -validity 10950 \
    -dname "CN=Jianxiang Assistant, O=Jianxiang, C=CN"
fi

echo "[3/4] Gradle 打包（首次会下载 gradle 8.9 与依赖，需几分钟）"
cd android
if [ -x "./gradlew" ]; then GRADLE=./gradlew; else GRADLE="gradle"; fi
$GRADLE assembleDebug --no-daemon -q || $GRADLE assembleDebug --no-daemon

echo "[4/4] 完成"
ls -lh app/build/outputs/apk/debug/*.apk
echo "安装: adb install -r android/app/build/outputs/apk/debug/app-debug.apk"
