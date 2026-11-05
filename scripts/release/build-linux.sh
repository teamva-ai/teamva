#!/usr/bin/env bash
#
# Build the Linux AppImage for one architecture.
#
# Usage: build-linux.sh <amd64|arm64> <output-dir>
#
# TEAMVA_VERSION must be set (for example 0.4.0) and is embedded in the app and
# the asset name. The build is native: the requested architecture must match the
# machine. Runtime dependencies (libwebkit2gtk-4.1, libfuse2) are documented in
# the release notes, not bundled.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ARCH="${1:-}"
OUTPUT_DIR="${2:-}"
VERSION="${TEAMVA_VERSION:-}"

if [[ -z "$ARCH" || -z "$OUTPUT_DIR" ]]; then
  printf 'usage: %s <amd64|arm64> <output-dir>\n' "$0" >&2
  exit 1
fi
if [[ ! "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  printf 'TEAMVA_VERSION 必须是语义化版本号，当前值：%s\n' "$VERSION" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  printf 'Linux 制品只能在 Linux 构建机生成\n' >&2
  exit 1
fi

case "$ARCH" in
  amd64)
    MACHINE="x86_64"
    ASSET_ARCH="x86_64"
    APPIMAGE_ARCH="x86_64"
    OPENCODE_PLATFORM="linux-x64"
    ;;
  arm64)
    MACHINE="aarch64"
    ASSET_ARCH="arm64"
    # appimagetool treats any "arm" prefix as 32-bit; aarch64 selects the arm64 runtime.
    APPIMAGE_ARCH="aarch64"
    OPENCODE_PLATFORM="linux-arm64"
    ;;
  *)
    printf '不支持的架构：%s（可用 amd64 或 arm64）\n' "$ARCH" >&2
    exit 1
    ;;
esac
if [[ "$(uname -m)" != "$MACHINE" ]]; then
  printf '当前构建机是 %s，不能原生构建 %s\n' "$(uname -m)" "$ARCH" >&2
  exit 1
fi
for command_name in go pnpm npm curl install pkg-config; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '缺少 Linux 构建命令：%s\n' "$command_name" >&2
    exit 1
  fi
done
if ! pkg-config --exists webkit2gtk-4.1; then
  printf '缺少 WebKitGTK 4.1 开发包（libwebkit2gtk-4.1-dev 与 libgtk-3-dev）\n' >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"
VERSION="${VERSION#v}"
ASSET_NAME="Teamva-$VERSION-linux-$ASSET_ARCH.AppImage"

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
    -s \
    -platform "linux/$ARCH" \
    -tags webkit2_41 \
    -ldflags "-s -w -X main.version=$VERSION"
)

# The AppImage carries the same bundled runtime layout the app resolves at
# startup: usr/Resources/opencode/<platform>/opencode next to usr/bin.
"$ROOT_DIR/scripts/vendor/opencode.sh" --platform "$OPENCODE_PLATFORM"

STAGING="$(mktemp -d "${TMPDIR:-/tmp}/teamva-appimage.XXXXXX")"
trap 'rm -rf "$STAGING"' EXIT
APPDIR="$STAGING/Teamva.AppDir"
mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/icons/hicolor/256x256/apps" \
  "$APPDIR/usr/share/doc/teamva" "$APPDIR/usr/Resources/opencode/$OPENCODE_PLATFORM"

install -m 0755 "$ROOT_DIR/desktop/build/bin/Teamva" "$APPDIR/usr/bin/Teamva"
install -m 0644 "$ROOT_DIR/assets/brand/icon.png" "$APPDIR/teamva.png"
install -m 0644 "$ROOT_DIR/assets/brand/icon.png" "$APPDIR/usr/share/icons/hicolor/256x256/apps/teamva.png"
install -m 0644 "$ROOT_DIR/LICENSE" "$ROOT_DIR/NOTICE" "$ROOT_DIR/THIRD_PARTY_NOTICES.md" "$APPDIR/usr/share/doc/teamva/"
install -m 0755 "$ROOT_DIR/vendor/opencode/$OPENCODE_PLATFORM/opencode" "$APPDIR/usr/Resources/opencode/$OPENCODE_PLATFORM/opencode"

cat >"$APPDIR/teamva.desktop" <<'DESKTOP'
[Desktop Entry]
Type=Application
Name=Teamva
Comment=Local-first multi-agent workspace
Exec=Teamva
Icon=teamva
Terminal=false
Categories=Development;
StartupWMClass=Teamva
DESKTOP
cat >"$APPDIR/AppRun" <<'APPRUN'
#!/bin/sh
exec "$APPDIR/usr/bin/Teamva" "$@"
APPRUN
chmod +x "$APPDIR/AppRun" "$APPDIR/teamva.desktop"

TOOL="$STAGING/appimagetool"
curl -fsSL --retry 3 --retry-delay 5 \
  -o "$TOOL" \
  "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-$APPIMAGE_ARCH.AppImage"
chmod +x "$TOOL"

rm -f "$OUTPUT_DIR/$ASSET_NAME"
ARCH="$APPIMAGE_ARCH" APPIMAGE_EXTRACT_AND_RUN=1 "$TOOL" --no-appstream \
  "$APPDIR" "$OUTPUT_DIR/$ASSET_NAME"

(
  cd "$OUTPUT_DIR"
  sha256sum "$ASSET_NAME" >sha256.txt
)

printf 'Linux AppImage 已生成：%s\n' "$OUTPUT_DIR/$ASSET_NAME"
