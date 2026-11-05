#!/usr/bin/env bash
#
# 启动支持远程访问的本地服务，本机与局域网共用端口 41720。
# 尚无密码时仍只监听本机；在「设置 → 远程访问」设置密码并显式开启。

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export TEAMVA_LOCAL_WEB_DIR="${TEAMVA_LOCAL_WEB_DIR:-$ROOT_DIR/web/dist}"
export TEAMVA_LOCAL_HTTP_ADDR="${TEAMVA_LOCAL_HTTP_ADDR:-127.0.0.1:41720}"
export TEAMVA_LOCAL_ALLOW_EXTERNAL=true

[[ -s "$TEAMVA_LOCAL_WEB_DIR/index.html" ]] || {
  printf '请先运行 pnpm build，或设置 TEAMVA_LOCAL_WEB_DIR 指向完整 Web 构建。\n' >&2
  exit 1
}

[[ -n "${TEAMVA_LOCAL_ACCESS_TOKEN:-}" ]] && \
  printf '已提供环境变量中的初始密码（仅尚未设置密码时生效，只存储哈希）。\n' >&2
printf '请打开日志中的本机 origin，在「设置 → 远程访问」设置密码并开启。局域网与本机共用同一端口；没有密码不会开放外部监听。\n' >&2
go -C "$ROOT_DIR/node" generate ./provider/catalog
exec go -C "$ROOT_DIR/server" run ./cmd/local
