#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SOURCE_DIR="$ROOT_DIR/web/dist"
TARGET_DIR="$ROOT_DIR/desktop/frontend/dist"

[[ -s "$SOURCE_DIR/index.html" ]] || {
  printf 'Web build not found. Run pnpm --filter @teamva/web build first.\n' >&2
  exit 1
}

go -C "$ROOT_DIR/node" generate ./provider/catalog
rm -rf "$TARGET_DIR"
mkdir -p "$TARGET_DIR"
cp -R "$SOURCE_DIR"/. "$TARGET_DIR"/
: > "$TARGET_DIR/.gitkeep"

printf 'Staged desktop frontend from %s\n' "$SOURCE_DIR"
