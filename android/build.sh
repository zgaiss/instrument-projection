#!/bin/bash
# build.sh —— 从零构建 iclock.apk（不依赖 gradle / Android Studio）
#
# 依赖：JDK21、aapt2、r8.jar、apksig.jar、android-all-12.jar（后三者取自 tools/sdk）
# 产物：ICLock.apk
set -e
cd "$(dirname "$0")"

SDK=${SDK:-$HOME/Library/Android/sdk-tools-iclock}
if [ ! -f "$SDK/bin/aapt2" ] || [ ! -f "$SDK/r8.jar" ] || [ ! -f "$SDK/apksig.jar" ] || [ ! -f "$SDK/android-all-12.jar" ]; then
  cat >&2 <<'EOS'
需要准备构建工具目录（用 SDK=/path/to/sdk 指定），至少包含：
  bin/aapt2              Android SDK build-tools
  r8.jar                 https://maven.google.com/com/android/tools/r8/
  apksig.jar             https://maven.google.com/com/android/tools/build/apksig/
  android-all-12.jar     https://repo1.maven.org/maven2/org/robolectric/android-all/12-robolectric-7732740/
EOS
  exit 1
fi
export PATH="/opt/homebrew/bin:/opt/homebrew/opt/openjdk@21/bin:$PATH"
AAPT2=$SDK/bin/aapt2
ANDROID_JAR=$SDK/android-all-12.jar

PKG=com.iclock
OUT=out
rm -rf "$OUT"
mkdir -p "$OUT/classes" "$OUT/dex"

echo "== 1/6 编译 armv7 二进制 =="
( cd build && GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w" -o iclock iclock.go )
mkdir -p assets
cp build/iclock assets/iclock
ls -l assets/iclock | awk '{print "   iclock " $5 " B"}'

echo "== 2/6 编译 Java =="
javac -nowarn --release 8 -classpath "$ANDROID_JAR" \
      -d "$OUT/classes" src/com/iclock/MainActivity.java

echo "== 3/6 生成 dex =="
java -cp "$SDK/r8.jar" com.android.tools.r8.D8 \
     --min-api 21 --release --no-desugaring \
     --output "$OUT/dex" $(find "$OUT/classes" -name '*.class')
ls -l "$OUT/dex/classes.dex" | awk '{print "   classes.dex " $5 " B"}'

echo "== 4/6 打包资源与清单 =="
"$AAPT2" link -o "$OUT/base.apk" \
    -I "$ANDROID_JAR" \
    --manifest AndroidManifest.xml \
    -A assets \
    --min-sdk-version 21 --target-sdk-version 29 \
    --version-code 1 --version-name 1.0 \
    --no-compress assets/iclock \
    2>&1 | head -20

echo "== 5/6 塞入 dex =="
( cd "$OUT/dex" && zip -q -X ../base.apk classes.dex )
ls -l "$OUT/base.apk" | awk '{print "   base.apk " $5 " B"}'

echo "== 6/6 签名 =="
# 密钥库放在 out/ 之外，避免每次构建重新生成（否则无法覆盖安装已装的旧版本）。
# .keys/ 已在 .gitignore 中，不会随仓库分发。
KS=".keys/iclock.p12"
KSPASS=${KSPASS:-iclock123}
mkdir -p .keys
if [ ! -f "$KS" ]; then
  keytool -genkeypair -keystore "$KS" -storetype PKCS12 -alias iclock \
      -keyalg RSA -keysize 2048 -validity 10000 \
      -storepass "$KSPASS" -keypass "$KSPASS" \
      -dname "CN=ICLock, OU=Test, O=Test, L=NA, S=NA, C=CN" >/dev/null 2>&1
  echo "   已生成本地测试密钥 $KS（不要提交到仓库）"
fi
javac -nowarn -cp "$SDK/apksig.jar" -d "$OUT" Sign.java
java -cp "$SDK/apksig.jar:$OUT" Sign "$OUT/base.apk" ICLock.apk "$KS" iclock "$KSPASS"

echo
echo "== 自检 =="
javac -nowarn -cp "$SDK/apksig.jar" -d tools tools/Verify.java 2>/dev/null || true
java -cp "$SDK/apksig.jar:tools" Verify ICLock.apk

echo
echo "== 完成 =="
ls -lh ICLock.apk
unzip -l ICLock.apk | tail -12
