#!/usr/bin/env bash

# 把固定版本的 OpenCode 平台二进制拉取到仓库忽略目录，供开发自测与
# 桌面打包使用。不安装到系统：只写 vendor/opencode/<platform>/，
# 不修改 PATH。
#
# 用法：
#   scripts/vendor/opencode.sh [--platform darwin-arm64]
#
# 环境变量：
#   OPENCODE_VERSION  要拉取的版本（默认 2.0.18）
#   OPENCODE_DEST     输出目录（默认 <repo>/vendor/opencode）

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${OPENCODE_VERSION:-2.0.18}"
DEST_ROOT="${OPENCODE_DEST:-$ROOT_DIR/vendor/opencode}"

platform=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --platform)
      platform="${2:-}"
      shift 2
      ;;
    *)
      printf '未知参数：%s\n' "$1" >&2
      exit 1
      ;;
  esac
done

if [[ -z "$platform" ]]; then
  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64) platform="darwin-arm64" ;;
    Darwin-x86_64) platform="darwin-x64" ;;
    Linux-x86_64) platform="linux-x64" ;;
    Linux-aarch64) platform="linux-arm64" ;;
    *)
      printf '不支持的平台：%s-%s（可用 --platform 指定）\n' "$(uname -s)" "$(uname -m)" >&2
      exit 1
      ;;
  esac
fi

for command_name in npm curl openssl tar; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '缺少命令：%s\n' "$command_name" >&2
    exit 1
  fi
done

package="@opencode/cli-$platform"
target_dir="$DEST_ROOT/$platform"
manifest="$target_dir/manifest.json"

metadata="$(npm view "$package@$VERSION" dist.integrity --json)"
integrity="$(printf '%s' "$metadata" | sed -n 's/.*"\(sha512-[^"]*\)".*/\1/p')"
if [[ -z "$integrity" ]]; then
  printf '无法读取 %s@%s 的完整性校验值\n' "$package" "$VERSION" >&2
  exit 1
fi

if [[ -f "$target_dir/opencode" && -f "$manifest" ]]; then
  current_version="$(sed -n 's/.*"version": "\([^"]*\)".*/\1/p' "$manifest")"
  current_integrity="$(sed -n 's/.*"integrity": "\([^"]*\)".*/\1/p' "$manifest")"
  if [[ "$current_version" == "$VERSION" && "$current_integrity" == "$integrity" ]]; then
    printf '已是最新：%s（%s，%s）\n' "$target_dir/opencode" "$VERSION" "$platform"
    exit 0
  fi
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

(
  cd "$tmp_dir"
  npm pack "$package@$VERSION" >/dev/null
)

tarball="$(find "$tmp_dir" -maxdepth 1 -name '*.tgz' | head -1)"
if [[ -z "$tarball" ]]; then
  printf 'npm pack 未生成 tarball\n' >&2
  exit 1
fi

actual="sha512-$(openssl dgst -sha512 -binary "$tarball" | openssl base64 -A)"
if [[ "$actual" != "$integrity" ]]; then
  printf '完整性校验失败：\n  期望 %s\n  实际 %s\n' "$integrity" "$actual" >&2
  exit 1
fi

tar -xzf "$tarball" -C "$tmp_dir"
source_binary="$tmp_dir/package/bin/opencode"
if [[ ! -f "$source_binary" ]]; then
  printf '包内未找到 bin/opencode\n' >&2
  exit 1
fi

mkdir -p "$target_dir"
install -m 0755 "$source_binary" "$target_dir/opencode"
cat >"$manifest" <<EOF
{
  "version": "$VERSION",
  "platform": "$platform",
  "package": "$package",
  "integrity": "$integrity",
  "source": "npm pack $package@$VERSION"
}
EOF

printf '已写入：%s（%s，%s）\n' "$target_dir/opencode" "$VERSION" "$platform"
