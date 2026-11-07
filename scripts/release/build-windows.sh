#!/usr/bin/env bash
#
# Build the Windows x64 installer and portable package.
#
# Usage: build-windows.sh <amd64> <output-dir>
#
# TEAMVA_VERSION must be set (for example 0.4.0). The NSIS installer installs
# per user without UAC; the archive contains the executable, the bundled
# OpenCode runtime and the license files, so it can also run without setup.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ARCH="${1:-}"
OUTPUT_DIR="${2:-}"
VERSION="${TEAMVA_VERSION:-}"

if [[ -z "$ARCH" || -z "$OUTPUT_DIR" ]]; then
  printf 'usage: %s <amd64> <output-dir>\n' "$0" >&2
  exit 1
fi
if [[ "$ARCH" != "amd64" ]]; then
  printf 'Windows 目前只构建 amd64，当前值：%s\n' "$ARCH" >&2
  exit 1
fi
if [[ ! "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  printf 'TEAMVA_VERSION 必须是语义化版本号，当前值：%s\n' "$VERSION" >&2
  exit 1
fi
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) ;;
  *)
    printf 'Windows 制品只能在 Windows 构建机生成\n' >&2
    exit 1
    ;;
esac
for command_name in go pnpm npm node cygpath powershell.exe makensis; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '缺少 Windows 构建命令：%s\n' "$command_name" >&2
    exit 1
  fi
done

# Workflow paths arrive as Windows paths; keep the rest of the script POSIX.
OUTPUT_DIR="$(cygpath -u "$OUTPUT_DIR")"
mkdir -p "$OUTPUT_DIR"
VERSION="${VERSION#v}"
ASSET_NAME="Teamva-$VERSION-windows-x64.zip"

(
  cd "$ROOT_DIR"
  pnpm generate:providers
  pnpm --filter @teamva/web build
  bash ./scripts/desktop/stage-frontend.sh
)
# The installer embeds the bundled runtime from vendor/, so fetch it first.
bash "$ROOT_DIR/scripts/vendor/opencode.sh" --platform windows-x64

# Wails resolves INFO_PRODUCTVERSION from wails.json for the installer and the
# exe version resource. Patch it for this build, then restore the checkout.
WAILS_JSON="$ROOT_DIR/desktop/wails.json"
WAILS_JSON_BACKUP="$(mktemp)"
STAGING=""
cp "$WAILS_JSON" "$WAILS_JSON_BACKUP"
restore_wails_json() {
  cp "$WAILS_JSON_BACKUP" "$WAILS_JSON"
  rm -f "$WAILS_JSON_BACKUP"
}
trap 'restore_wails_json; [[ -z "$STAGING" ]] || rm -rf "$STAGING"' EXIT
node -e '
const fs = require("node:fs");
const [file, version] = process.argv.slice(1);
const data = JSON.parse(fs.readFileSync(file, "utf8"));
data.info = { ...(data.info ?? {}), productVersion: version.split(/[-+]/)[0] };
fs.writeFileSync(file, JSON.stringify(data, null, 2) + "\n");
' "$WAILS_JSON" "$VERSION"

(
  cd "$ROOT_DIR/desktop"
  go run github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 build \
    -clean \
    -trimpath \
    -s \
    -nsis \
    -installscope user \
    -platform windows/amd64 \
    -ldflags "-s -w -X main.version=$VERSION"
)

INSTALLER_PATH="$ROOT_DIR/desktop/build/bin/Teamva-amd64-installer.exe"
if [[ ! -f "$INSTALLER_PATH" ]]; then
  printf 'NSIS installer was not produced: %s\n' "$INSTALLER_PATH" >&2
  exit 1
fi
INSTALLER_NAME="Teamva-$VERSION-windows-x64-setup.exe"
cp "$INSTALLER_PATH" "$OUTPUT_DIR/$INSTALLER_NAME"

STAGING="$(mktemp -d "${TMPDIR:-/tmp}/teamva-windows.XXXXXX")"
PACKAGE="$STAGING/Teamva"
mkdir -p "$PACKAGE/Resources/opencode/windows-x64"

cp "$ROOT_DIR/desktop/build/bin/Teamva.exe" "$PACKAGE/Teamva.exe"
cp "$ROOT_DIR/vendor/opencode/windows-x64/opencode.exe" "$PACKAGE/Resources/opencode/windows-x64/opencode.exe"
cp "$ROOT_DIR/LICENSE" "$ROOT_DIR/NOTICE" "$ROOT_DIR/THIRD_PARTY_NOTICES.md" "$PACKAGE/"

PACKAGE_WIN="$(cygpath -w "$PACKAGE")"
OUTPUT_WIN="$(cygpath -w "$OUTPUT_DIR/$ASSET_NAME")"
rm -f "$OUTPUT_DIR/$ASSET_NAME"
powershell.exe -NoProfile -NonInteractive -Command \
  "Compress-Archive -Path '$PACKAGE_WIN' -DestinationPath '$OUTPUT_WIN' -Force"

node -e '
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const out = process.argv[1];
const files = process.argv.slice(2);
const lines = files.map((file) => {
  const hash = crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
  return hash + "  " + path.basename(file);
});
fs.writeFileSync(out, lines.join("\n") + "\n");
' "$OUTPUT_DIR/sha256.txt" "$OUTPUT_DIR/$ASSET_NAME" "$OUTPUT_DIR/$INSTALLER_NAME"

printf 'Windows 安装包已生成：%s\n' "$OUTPUT_DIR/$INSTALLER_NAME"
printf 'Windows 便携包已生成：%s\n' "$OUTPUT_DIR/$ASSET_NAME"
