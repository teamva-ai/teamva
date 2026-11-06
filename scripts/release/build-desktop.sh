#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${TEAMVA_VERSION:-}"
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

BUILD_VERSION="${VERSION#v}"
BUILD_VERSION="${BUILD_VERSION%%[-+]*}"
CHECKSUM_FILE="$OUTPUT_DIR/SHA256SUMS"
rm -f "$OUTPUT_DIR"/*.zip "$OUTPUT_DIR"/*.dmg "$OUTPUT_DIR/VERSION" "$CHECKSUM_FILE"

# Build each architecture separately. A universal binary hides which runtime
# payload is actually used and doubles the bundled OpenCode download; the
# release ships one thin app per architecture instead.
for pair in "arm64:arm64:arm64" "x64:amd64:x86_64"; do
  IFS=':' read -r label wails_arch expected_lipo <<<"$pair"
  ASSET_NAME="teamva-desktop-darwin-$label.zip"
  DMG_ASSET_NAME="teamva-desktop-darwin-$label.dmg"

  printf '\n=== Building macOS %s ===\n' "$label"
  (
    cd "$ROOT_DIR/desktop"
    go run github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 build \
      -clean \
      -trimpath \
      -platform "darwin/$wails_arch" \
      -ldflags "-s -w -X main.version=$VERSION"
  )

  INFO_PLIST="$APP_PATH/Contents/Info.plist"
  EXECUTABLE="$APP_PATH/Contents/MacOS/Teamva"
  # Keep the Apple-rendered ICNS instead of Wails' automatic PNG re-encoding.
  cp "$ROOT_DIR/desktop/build/appicon.icns" "$APP_PATH/Contents/Resources/iconfile.icns"
  cp "$ROOT_DIR/LICENSE" "$ROOT_DIR/NOTICE" "$ROOT_DIR/THIRD_PARTY_NOTICES.md" "$APP_PATH/Contents/Resources/"
  plutil -replace CFBundleShortVersionString -string "${VERSION#v}" "$INFO_PLIST"
  plutil -replace CFBundleVersion -string "$BUILD_VERSION" "$INFO_PLIST"

  ARCHITECTURES="$(lipo -archs "$EXECUTABLE")"
  if [[ "$ARCHITECTURES" != "$expected_lipo" ]]; then
    printf '桌面可执行文件架构错误：期望 %s，实际 %s\n' "$expected_lipo" "$ARCHITECTURES" >&2
    exit 1
  fi

  # Bundle only the matching OpenCode runtime. The vendor binary keeps its
  # upstream signature and entitlements; do not re-sign it.
  (
    cd "$ROOT_DIR"
    ./scripts/vendor/opencode.sh --platform "darwin-$label"
  )
  OPENCODE_RESOURCES="$APP_PATH/Contents/Resources/opencode"
  rm -rf "$OPENCODE_RESOURCES"
  mkdir -p "$OPENCODE_RESOURCES"
  ditto "$ROOT_DIR/vendor/opencode/darwin-$label" "$OPENCODE_RESOURCES/darwin-$label"

  # Wails removes its ad-hoc signature when the app is modified, and macOS
  # refuses to launch a completely unsigned bundle (RBSRequestError Code=5,
  # POSIX 153). Reseal the finished bundle with an ad-hoc signature. This is
  # still not Developer ID signing or notarisation.
  codesign --force --sign - "$APP_PATH"
  codesign --verify --strict --verbose=2 "$APP_PATH"

  ditto -c -k --sequesterRsrc --keepParent "$APP_PATH" "$OUTPUT_DIR/$ASSET_NAME"

  # Drag-to-Applications installer that also ships the Gatekeeper unblock notes.
  "$ROOT_DIR/scripts/release/build-dmg.sh" \
    "$APP_PATH" "$OUTPUT_DIR" "Teamva" "$DMG_ASSET_NAME"

  (
    cd "$OUTPUT_DIR"
    shasum -a 256 "$ASSET_NAME" "$DMG_ASSET_NAME" >>"$CHECKSUM_FILE"
  )
done

printf '%s\n' "$VERSION" >"$OUTPUT_DIR/VERSION"
(
  cd "$OUTPUT_DIR"
  LC_ALL=C sort -k 2 "$CHECKSUM_FILE" -o "$CHECKSUM_FILE"
)

printf 'macOS arm64/x64 ad-hoc 签名制品已生成：%s\n' "$OUTPUT_DIR"
