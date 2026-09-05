#!/usr/bin/env bash
#
# 打包设备控制 App（被控端）发行版：产出 Android APK，重命名为发布契约名，
# 待拖入市场管理页「device-control-versions」上传弹框。与移动端 pack-release.sh
# 同风格——脚本只负责出产物，version.json 由服务端在上传后自动投影。
#
# 命名契约（与 validator.identify_device_control_asset 一致）：
#   device-control-<version>-android.apk    <version> = 数字/日期串或 semver（如 0.1.0）
#
# 上传后服务端把二进制发布到独立仓库 ai-lubricant-device-control 的 GitHub Release，
# asset 记录 Release 资产直链（browser_download_url），不入 marketplace 仓库。
#
# 用法（从 device-control/ 目录）：
#   bash scripts/pack-release.sh              # 默认版本取 app/build.gradle.kts 的 versionName
#   VERSION=0.2.0 bash scripts/pack-release.sh  # 显式指定版本
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_ROOT="$(cd "$HERE/.." && pwd)"
ANDROID_DIR="$APP_ROOT/android"

VERSION="${VERSION:-}"

# 默认版本号取 app/build.gradle.kts 的 versionName，与装机 App 自报版本一致。
if [ -z "$VERSION" ]; then
  VERSION="$(grep -oE 'versionName = "[^"]+"' "$ANDROID_DIR/app/build.gradle.kts" | head -1 | sed -E 's/.*"([^"]+)".*/\1/')"
fi
if [ -z "$VERSION" ]; then
  echo "无法从 app/build.gradle.kts 读出 versionName，请显式传 VERSION=…" >&2
  exit 1
fi
echo "==> 设备控制 App 打包版本: $VERSION"

OUT_NAME="device-control-${VERSION}-android.apk"
OUT_DIR="$APP_ROOT/dist"
mkdir -p "$OUT_DIR"

echo "==> Gradle 构建 release APK…"
( cd "$ANDROID_DIR" && ./gradlew assembleRelease 2>&1 ) \
  | tee "$OUT_DIR/gradle-build-$VERSION.log" || {
    echo "Gradle 构建失败，日志见 $OUT_DIR/gradle-build-$VERSION.log" >&2
    echo "（腾讯云 Gradle 镜像在国内更稳：在 gradle/wrapper/gradle-wrapper.properties 用 mirrors.cloud.tencent.com/gradle）" >&2
    exit 1
  }

# assembleRelease 产物在 app/build/outputs/apk/release/app-release.apk。release
# buildType 已接 deploy/keystore.properties 的发布签名（缺文件时回退 debug 签名，
# 见 android/app/build.gradle.kts），出包即已签名可直接装机。
APK_SRC="$ANDROID_DIR/app/build/outputs/apk/release/app-release.apk"
if [ ! -f "$APK_SRC" ]; then
  APK_SRC="$(find "$ANDROID_DIR/app/build/outputs" -name '*.apk' -path '*release*' 2>/dev/null | head -1 || true)"
fi
if [ -z "$APK_SRC" ] || [ ! -f "$APK_SRC" ]; then
  echo "找不到构建产物 APK（在 app/build/outputs/apk/release 下）。请手动定位并重命名为 $OUT_NAME 后拖入上传弹框。" >&2
  exit 1
fi

cp "$APK_SRC" "$OUT_DIR/$OUT_NAME"
echo
echo "==> 完成：$OUT_DIR/$OUT_NAME"
echo "下一步：把 $OUT_DIR/$OUT_NAME 拖进市场管理页 device-control-versions「上传新版本」弹框，"
echo "  版本号填 $VERSION。上传后服务端自动发布到 ai-lubricant-device-control 的 GitHub Release，"
echo "  并写 device-control-releases/version.json（「添加设备」弹框据此拿下载直链）。"
