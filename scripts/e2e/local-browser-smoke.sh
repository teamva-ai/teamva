#!/usr/bin/env bash

set -euo pipefail

STATE_FILE="${TEAMVA_LOCAL_STATE_FILE:?set TEAMVA_LOCAL_STATE_FILE to runtime.json}"
CHROME_BIN="${TEAMVA_CHROME_BIN:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
BASE_URL="$(jq -er '.origin' "$STATE_FILE")"
PROFILE_DIR="$(mktemp -d -t teamva-browser-profile.XXXXXX)"
DESKTOP_DOM="$(mktemp -t teamva-browser-desktop.XXXXXX)"
MOBILE_DOM="$(mktemp -t teamva-browser-mobile.XXXXXX)"
CHROME_LOG="$(mktemp -t teamva-browser-chrome.XXXXXX)"
CHROME_PID=""

cleanup() {
  if [[ -n "$CHROME_PID" ]]; then
    kill -TERM "$CHROME_PID" 2>/dev/null || true
    wait "$CHROME_PID" 2>/dev/null || true
  fi
  rm -rf "$PROFILE_DIR"
  rm -f "$DESKTOP_DOM" "$MOBILE_DOM" "$CHROME_LOG"
}
trap cleanup EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

dump_page() {
  local target_url="$1" viewport="$2" output="$3"
  : >"$output"
  "$CHROME_BIN" \
    --headless=new \
    --disable-gpu \
    --disable-background-networking \
    --disable-component-update \
    --no-first-run \
    --no-default-browser-check \
    --user-data-dir="$PROFILE_DIR" \
    --virtual-time-budget=5000 \
    --window-size="$viewport" \
    --dump-dom "$target_url" >"$output" 2>>"$CHROME_LOG" &
  CHROME_PID="$!"
  for _ in $(seq 1 300); do
    if grep -q '</html>' "$output"; then
      kill -TERM "$CHROME_PID" 2>/dev/null || true
      wait "$CHROME_PID" 2>/dev/null || true
      CHROME_PID=""
      return
    fi
    if ! kill -0 "$CHROME_PID" 2>/dev/null; then
      wait "$CHROME_PID" 2>/dev/null || true
      CHROME_PID=""
      fail "Chrome exited before rendering $target_url"
    fi
    sleep 0.1
  done
  fail "Chrome did not render $target_url within 30 seconds"
}

assert_contains() {
  local file="$1" text="$2"
  grep -Fq "$text" "$file" || fail "rendered page is missing: $text"
}

assert_absent() {
  local file="$1" text="$2"
  if grep -Fq "$text" "$file"; then
    fail "local page exposed remote-only UI: $text"
  fi
}

[[ -x "$CHROME_BIN" ]] || fail "Chrome executable not found: $CHROME_BIN"
curl -fsS "$BASE_URL/readyz" >/dev/null

dump_page "$BASE_URL/workbench" "1280,820" "$DESKTOP_DOM"
assert_contains "$DESKTOP_DOM" "本机"
assert_contains "$DESKTOP_DOM" "项目内容"
for remote_text in "还没有连接任何计算机" "去注册计算机" "安装并连接一台电脑" "邮箱验证码登录"; do
  assert_absent "$DESKTOP_DOM" "$remote_text"
done

# Reuse the browser profile and exercise the responsive shell in a second process.
dump_page "$BASE_URL/workbench" "390,844" "$MOBILE_DOM"
assert_contains "$MOBILE_DOM" "Teamva"
assert_contains "$MOBILE_DOM" "更多"
for remote_text in "还没有连接任何计算机" "去注册计算机" "邮箱验证码登录"; do
  assert_absent "$MOBILE_DOM" "$remote_text"
done

printf 'Local browser smoke passed: desktop=1280x820 mobile=390x844\n'
