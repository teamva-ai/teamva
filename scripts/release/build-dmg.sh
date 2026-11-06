#!/usr/bin/env bash
#
# Build the drag-to-Applications installer disk image.
#
# The volume ships an ad-hoc signed app, so Gatekeeper still blocks the first launch.
# Two plain-text helpers sit under the app icons and explain the one-time
# `xattr` command; the background art points at them with arrows.
#
# Icon positions live in desktop/build/dmg/DS_Store, checked in as a binary
# template. Writing .DS_Store through Finder needs the build machine to grant
# "Automation → Finder" to whatever starts the build, which CI and headless
# shells cannot do; a prebuilt template makes the layout deterministic and
# removes that permission from the release path entirely.
#
# Regenerate the template only when the item names or coordinates change:
# see "Rebuilding the layout template" in docs/DESKTOP_ARCHITECTURE.md.
#
# Usage: build-dmg.sh <path-to-app> <output-dir> <volume-name> <asset-name>
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP_PATH="${1:-}"
OUTPUT_DIR="${2:-}"
VOLUME_NAME="${3:-}"
ASSET_NAME="${4:-}"

if [[ -z "$APP_PATH" || -z "$OUTPUT_DIR" || -z "$VOLUME_NAME" || -z "$ASSET_NAME" ]]; then
  printf 'usage: %s <path-to-app> <output-dir> <volume-name> <asset-name>\n' "$0" >&2
  exit 1
fi
if [[ ! -d "$APP_PATH" ]]; then
  printf 'app bundle not found: %s\n' "$APP_PATH" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Darwin" ]]; then
  printf 'DMG 只能在 macOS 构建机生成\n' >&2
  exit 1
fi
for command_name in hdiutil SetFile sips tiffutil; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '缺少 DMG 构建命令：%s\n' "$command_name" >&2
    exit 1
  fi
done

BUILD_ASSETS="$ROOT_DIR/desktop/build"
LAYOUT="$BUILD_ASSETS/dmg/DS_Store"
if [[ ! -f "$LAYOUT" ]]; then
  printf '缺少图标布局模板：%s\n' "$LAYOUT" >&2
  exit 1
fi
# The template embeds a Finder alias to the background file that resolves by
# volume name, so the volume name is part of the layout contract.
if [[ "$VOLUME_NAME" != "Teamva" ]]; then
  printf '警告：布局模板按卷名 Teamva 生成，当前卷名 %s 可能导致背景图不显示\n' "$VOLUME_NAME" >&2
fi

STAGING="$(mktemp -d "${TMPDIR:-/tmp}/teamva-dmg.XXXXXX")"
RW_DMG="$STAGING/rw.dmg"
MOUNT_POINT="$STAGING/mnt"
APP_NAME="$(basename "$APP_PATH")"

cleanup() {
  if [[ -d "$MOUNT_POINT" ]]; then
    hdiutil detach "$MOUNT_POINT" -force >/dev/null 2>&1 || true
  fi
  rm -rf "$STAGING"
}
trap cleanup EXIT

# ---- staging ------------------------------------------------------------
mkdir -p "$STAGING/source"
ditto "$APP_PATH" "$STAGING/source/$APP_NAME"
ln -s /Applications "$STAGING/source/Applications"
cp "$BUILD_ASSETS/dmg/解除拦截.txt" "$STAGING/source/解除拦截.txt"
cp "$BUILD_ASSETS/dmg/Unblock.txt" "$STAGING/source/Unblock.txt"
cp "$LAYOUT" "$STAGING/source/.DS_Store"
cp "$BUILD_ASSETS/icon_installer.icns" "$STAGING/source/.VolumeIcon.icns"

# Finder reads one multi-representation TIFF; a single PNG only looks right on
# 1x displays. The @2x layer must stay tagged 144 dpi: tiffutil -cathidpicheck
# rewrites both directories to 72 dpi, which makes Finder treat the 2x layer as
# twice the physical size and fall back to the blurry 1x image on Retina.
sips -s format tiff "$BUILD_ASSETS/dmg_background.png" \
  --out "$STAGING/background-1x.tiff" >/dev/null
if [[ -f "$BUILD_ASSETS/dmg_background@2x.png" ]]; then
  sips -s format tiff -s dpiHeight 144 -s dpiWidth 144 "$BUILD_ASSETS/dmg_background@2x.png" \
    --out "$STAGING/background-2x.tiff" >/dev/null
  tiffutil -cat "$STAGING/background-1x.tiff" "$STAGING/background-2x.tiff" \
    -out "$STAGING/source/.background.tiff" >/dev/null
else
  cp "$STAGING/background-1x.tiff" "$STAGING/source/.background.tiff"
fi

# ---- build --------------------------------------------------------------
mkdir -p "$MOUNT_POINT"
hdiutil create -srcfolder "$STAGING/source" -volname "$VOLUME_NAME" \
  -fs HFS+ -format UDRW -ov -quiet "$RW_DMG"
hdiutil attach "$RW_DMG" -readwrite -noverify -noautoopen -mountpoint "$MOUNT_POINT" -quiet >/dev/null

# The custom-icon bit lives on the volume root, which -srcfolder does not copy.
SetFile -a C "$MOUNT_POINT"
sync
hdiutil detach "$MOUNT_POINT" -quiet

mkdir -p "$OUTPUT_DIR"
rm -f "$OUTPUT_DIR/$ASSET_NAME"
hdiutil convert "$RW_DMG" -format UDZO -imagekey zlib-level=9 -o "$OUTPUT_DIR/$ASSET_NAME.tmp" -quiet
mv "$OUTPUT_DIR/$ASSET_NAME.tmp.dmg" "$OUTPUT_DIR/$ASSET_NAME"

printf 'DMG 已生成：%s\n' "$OUTPUT_DIR/$ASSET_NAME"
