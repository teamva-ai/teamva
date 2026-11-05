#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${TEAMVA_VERSION:-}"
ASSET_NAME="teamva-desktop-darwin-universal.zip"
DMG_ASSET_NAME="teamva-desktop-darwin-universal.dmg"
OUTPUT_DIR="${TEAMVA_OUTPUT_DIR:-$ROOT_DIR/dist/desktop-$VERSION}"
APP_PATH="$ROOT_DIR/desktop/build/bin/Teamva.app"

if [[ ! "$VERSION" =~ ^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  printf 'TEAMVA_VERSION 必须是语义化版本号，当前值：%s\n' "$VERSION" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Darwin" ]]; then
  printf '桌面制品只能在 macOS 构建机生成\n' >&2
  exit 1
fi
for command_name in go pnpm plutil lipo ditto shasum codesign; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '缺少桌面构建命令：%s\n' "$command_name" >&2
    exit 1
  fi
done

mkdir -p "$OUTPUT_DIR"

(
  cd "$ROOT_DIR"
  pnpm generate:providers
  pnpm --filter @teamva/web build
  ./scripts/desktop/stage-frontend.sh
)
(
  cd "$ROOT_DIR/desktop"
  go run github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 build \
    -clean \
    -trimpath \
    -platform darwin/universal \
    -ldflags "-s -w -X main.version=$VERSION"
)

INFO_PLIST="$APP_PATH/Contents/Info.plist"
EXECUTABLE="$APP_PATH/Contents/MacOS/Teamva"
# Keep the Apple-rendered ICNS instead of Wails' automatic PNG re-encoding.
cp "$ROOT_DIR/desktop/build/appicon.icns" "$APP_PATH/Contents/Resources/iconfile.icns"
cp "$ROOT_DIR/LICENSE" "$ROOT_DIR/NOTICE" "$ROOT_DIR/THIRD_PARTY_NOTICES.md" "$APP_PATH/Contents/Resources/"
BUILD_VERSION="${VERSION#v}"
BUILD_VERSION="${BUILD_VERSION%%[-+]*}"
plutil -replace CFBundleShortVersionString -string "${VERSION#v}" "$INFO_PLIST"
plutil -replace CFBundleVersion -string "$BUILD_VERSION" "$INFO_PLIST"

# Wails creates an ad-hoc signature for local convenience. Published MVP
# artifacts are deliberately unsigned until formal signing/notarisation exists.
codesign --remove-signature "$APP_PATH"
rm -rf "$APP_PATH/Contents/_CodeSignature"
ARCHITECTURES="$(lipo -archs "$EXECUTABLE")"
if [[ "$ARCHITECTURES" != *arm64* || "$ARCHITECTURES" != *x86_64* ]]; then
  printf '桌面可执行文件不是 universal2：%s\n' "$ARCHITECTURES" >&2
  exit 1
fi

# 内置 OpenCode 运行时：拉取两个架构的平台载荷并放进 App 资源目录，
# 让没有安装任何 Coding CLI 的用户也能开箱即用（不写入系统 PATH）。
# 用户也可在设置中指定外部 opencode 可执行文件覆盖内置运行时。
(
  cd "$ROOT_DIR"
  ./scripts/vendor/opencode.sh --platform darwin-arm64
  ./scripts/vendor/opencode.sh --platform darwin-x64
)
OPENCODE_RESOURCES="$APP_PATH/Contents/Resources/opencode"
rm -rf "$OPENCODE_RESOURCES"
mkdir -p "$OPENCODE_RESOURCES"
ditto "$ROOT_DIR/vendor/opencode/darwin-arm64" "$OPENCODE_RESOURCES/darwin-arm64"
ditto "$ROOT_DIR/vendor/opencode/darwin-x64" "$OPENCODE_RESOURCES/darwin-x64"

rm -f "$OUTPUT_DIR/$ASSET_NAME" "$OUTPUT_DIR/$DMG_ASSET_NAME" "$OUTPUT_DIR/VERSION" "$OUTPUT_DIR/SHA256SUMS"
ditto -c -k --sequesterRsrc --keepParent "$APP_PATH" "$OUTPUT_DIR/$ASSET_NAME"

# Drag-to-Applications installer that also ships the Gatekeeper unblock notes.
"$ROOT_DIR/scripts/release/build-dmg.sh" \
  "$APP_PATH" "$OUTPUT_DIR" "Teamva" "$DMG_ASSET_NAME"

printf '%s\n' "$VERSION" >"$OUTPUT_DIR/VERSION"
(
  cd "$OUTPUT_DIR"
  shasum -a 256 "$ASSET_NAME" "$DMG_ASSET_NAME" >SHA256SUMS
)

printf '未签名 macOS universal2 制品已生成：%s\n' "$OUTPUT_DIR"
