#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export TEAMVA_LOCAL_WEB_DIR="${TEAMVA_LOCAL_WEB_DIR:-$ROOT_DIR/web/dist}"
export TEAMVA_LOCAL_HTTP_ADDR="${TEAMVA_LOCAL_HTTP_ADDR:-127.0.0.1:41720}"

if [[ ! -s "$TEAMVA_LOCAL_WEB_DIR/index.html" ]]; then
  printf '请先运行 pnpm build，或设置 TEAMVA_LOCAL_WEB_DIR 指向完整 Web 构建。\n' >&2
  exit 1
fi

# The service prints its non-secret loopback origin. Open it in any local browser;
# authentication is established by the same-origin page, without a URL token.
go -C "$ROOT_DIR/node" generate ./provider/catalog
exec go -C "$ROOT_DIR/server" run ./cmd/local
