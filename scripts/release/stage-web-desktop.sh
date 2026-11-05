#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PUBLISH_DIR="$ROOT_DIR/web/public/downloads/desktop"

TEAMVA_OUTPUT_DIR="$PUBLISH_DIR" "$ROOT_DIR/scripts/release/build-desktop.sh"

printf 'Web 桌面下载制品已准备：%s\n' "$PUBLISH_DIR"
