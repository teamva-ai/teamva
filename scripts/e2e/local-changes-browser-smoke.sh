#!/usr/bin/env bash

set -euo pipefail

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

STATE_FILE="${TEAMVA_LOCAL_STATE_FILE:-}"
unset TEAMVA_LOCAL_BOOTSTRAP_NONCE
CHROME_BIN="${TEAMVA_CHROME_BIN:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"

[[ -n "$STATE_FILE" && -s "$STATE_FILE" ]] || fail 'runtime state is unavailable'

for required_command in curl env git jq mktemp node pgrep; do
  command -v "$required_command" >/dev/null 2>&1 || fail "missing command: $required_command"
done
[[ -x "$CHROME_BIN" ]] || fail 'Chrome executable not found'
ENV_BIN="$(command -v env)"
GIT_BIN="$(command -v git)"
NODE_BIN="$(command -v node)"
PGREP_BIN="$(command -v pgrep)"
[[ "$ENV_BIN" == /* && "$GIT_BIN" == /* && "$NODE_BIN" == /* && "$PGREP_BIN" == /* ]] || \
  fail 'required executable paths could not be resolved safely'

validate_runtime_origin() {
  NO_PROXY='*' no_proxy='*' TEAMVA_ORIGIN_CANDIDATE="$1" "$NODE_BIN" -e '
    const raw = process.env.TEAMVA_ORIGIN_CANDIDATE || "";
    try {
      const parsed = new URL(raw);
      const loopbacks = new Set(["127.0.0.1", "[::1]", "::1"]);
      const port = Number(parsed.port);
      if (parsed.protocol !== "http:" || !loopbacks.has(parsed.hostname) ||
          !parsed.port || !Number.isInteger(port) || port < 1 || port > 65535 ||
          parsed.username || parsed.password || parsed.search || parsed.hash ||
          parsed.pathname !== "/" || raw !== parsed.origin) {
        process.exit(1);
      }
      process.stdout.write(parsed.origin);
    } catch {
      process.exit(1);
    }
  '
}

RAW_ORIGIN="$(jq -er '.origin | select(type == "string")' "$STATE_FILE" 2>/dev/null)" || \
  fail 'runtime state does not contain an origin'
if ! BASE_URL="$(validate_runtime_origin "$RAW_ORIGIN")"; then
  fail 'runtime origin must be an exact loopback HTTP origin with an explicit port'
fi
unset RAW_ORIGIN

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)"
FIXTURE_ROOT=""
PROFILE_DIR=""
CHROME_LOG=""
SCREENSHOT_PATH="${TEAMVA_CHANGES_SCREENSHOT_PATH:-}"
REMOVE_SCREENSHOT=true
[[ -z "${TEAMVA_CHANGES_SCREENSHOT_PATH:-}" ]] || REMOVE_SCREENSHOT=false
FIXTURE_REAL_ROOT=""
FIXTURE_BASENAME=""
SAFE_GIT_HOME=""
SAFE_GIT_HOOKS=""
SAFE_GIT_PATH="${GIT_BIN%/*}:/usr/bin:/bin:/usr/sbin:/sbin"
CHROME_PID=""
NODE_PID=""
NODE_TOTAL_TIMEOUT_SECONDS=180
SUCCESS_SUMMARY=""

stop_node() {
  local attempt
  [[ -n "$NODE_PID" ]] || return 0
  if kill -0 "$NODE_PID" 2>/dev/null; then
    kill -TERM "$NODE_PID" 2>/dev/null || true
    for ((attempt = 0; attempt < 50; attempt++)); do
      kill -0 "$NODE_PID" 2>/dev/null || break
      sleep 0.1
    done
  fi
  if kill -0 "$NODE_PID" 2>/dev/null; then
    kill -KILL "$NODE_PID" 2>/dev/null || true
    for ((attempt = 0; attempt < 50; attempt++)); do
      kill -0 "$NODE_PID" 2>/dev/null || break
      sleep 0.1
    done
  fi
  if kill -0 "$NODE_PID" 2>/dev/null; then
    return 1
  fi
  wait "$NODE_PID" 2>/dev/null || true
  NODE_PID=""
}

profile_processes_alive() {
  local pid pids result
  [[ -n "$PROFILE_DIR" ]] || return 1
  if pids="$("$PGREP_BIN" -f -- "$PROFILE_DIR" 2>/dev/null)"; then
    :
  else
    result=$?
    [[ "$result" -eq 1 ]] && return 1
    return 0
  fi
  while IFS= read -r pid; do
    [[ "$pid" =~ ^[0-9]+$ && "$pid" != "$$" ]] || continue
    kill -0 "$pid" 2>/dev/null && return 0
  done <<<"$pids"
  return 1
}

signal_profile_processes() {
  local signal="$1" pid pids result
  [[ -n "$PROFILE_DIR" ]] || return 0
  if pids="$("$PGREP_BIN" -f -- "$PROFILE_DIR" 2>/dev/null)"; then
    :
  else
    result=$?
    [[ "$result" -eq 1 ]] && return 0
    return 1
  fi
  while IFS= read -r pid; do
    [[ "$pid" =~ ^[0-9]+$ && "$pid" != "$$" ]] || continue
    kill "-$signal" "$pid" 2>/dev/null || true
  done <<<"$pids"
}

stop_chrome() {
  local attempt
  if [[ -n "$CHROME_PID" ]] && kill -0 "$CHROME_PID" 2>/dev/null; then
    kill -TERM "$CHROME_PID" 2>/dev/null || true
  fi
  signal_profile_processes TERM || true
  for ((attempt = 0; attempt < 50; attempt++)); do
    if { [[ -z "$CHROME_PID" ]] || ! kill -0 "$CHROME_PID" 2>/dev/null; } && \
      ! profile_processes_alive; then
      break
    fi
    sleep 0.1
  done

  if { [[ -n "$CHROME_PID" ]] && kill -0 "$CHROME_PID" 2>/dev/null; } || \
    profile_processes_alive; then
    [[ -z "$CHROME_PID" ]] || kill -KILL "$CHROME_PID" 2>/dev/null || true
    signal_profile_processes KILL || true
    for ((attempt = 0; attempt < 50; attempt++)); do
      if { [[ -z "$CHROME_PID" ]] || ! kill -0 "$CHROME_PID" 2>/dev/null; } && \
        ! profile_processes_alive; then
        break
      fi
      sleep 0.1
    done
  fi

  if { [[ -n "$CHROME_PID" ]] && kill -0 "$CHROME_PID" 2>/dev/null; } || \
    profile_processes_alive; then
    return 1
  fi
  if [[ -n "$CHROME_PID" ]]; then
    wait "$CHROME_PID" 2>/dev/null || true
    CHROME_PID=""
  fi
}

cleanup() {
  local exit_status="$1" subprocess_cleanup_failed=0 temp_cleanup_failed=0
  trap - EXIT INT TERM
  set +e
  if ! stop_node; then
    subprocess_cleanup_failed=1
  fi
  if ! stop_chrome; then
    subprocess_cleanup_failed=1
  fi
  if [[ -n "$FIXTURE_ROOT" ]] && ! rm -rf -- "$FIXTURE_ROOT" 2>/dev/null; then
    temp_cleanup_failed=1
  fi
  if [[ -n "$PROFILE_DIR" ]] && ! rm -rf -- "$PROFILE_DIR" 2>/dev/null; then
    temp_cleanup_failed=1
  fi
  if [[ -n "$CHROME_LOG" ]] && ! rm -f -- "$CHROME_LOG" 2>/dev/null; then
    temp_cleanup_failed=1
  fi
  if [[ "$REMOVE_SCREENSHOT" == true && -n "$SCREENSHOT_PATH" ]] && \
    ! rm -f -- "$SCREENSHOT_PATH" 2>/dev/null; then
    temp_cleanup_failed=1
  fi
  if ((subprocess_cleanup_failed != 0)); then
    printf 'FAIL: browser subprocess cleanup did not complete\n' >&2
    ((exit_status != 0)) || exit_status=1
  fi
  if ((temp_cleanup_failed != 0)); then
    printf 'FAIL: browser smoke temporary cleanup did not complete\n' >&2
    ((exit_status != 0)) || exit_status=1
  fi
  if ((exit_status == 0)) && [[ -n "$SUCCESS_SUMMARY" ]]; then
    printf '%s\n' "$SUCCESS_SUMMARY"
  fi
  exit "$exit_status"
}
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

FIXTURE_ROOT="$(mktemp -d -t teamva-changes-browser-fixture.XXXXXX)" || \
  fail 'browser fixture root could not be created'
PROFILE_DIR="$(mktemp -d -t teamva-changes-browser-profile.XXXXXX)" || \
  fail 'browser profile root could not be created'
CHROME_LOG="$(mktemp -t teamva-changes-browser-chrome.XXXXXX)" || \
  fail 'browser Chrome log could not be created'
if [[ -z "$SCREENSHOT_PATH" ]]; then
  SCREENSHOT_PATH="$(mktemp -t teamva-changes-browser.XXXXXX)" || \
    fail 'browser screenshot target could not be created'
fi
if ! FIXTURE_REAL_ROOT="$(CDPATH='' cd -- "$FIXTURE_ROOT" 2>/dev/null && pwd -P)"; then
  fail 'browser fixture root could not be canonicalized'
fi
FIXTURE_BASENAME="${FIXTURE_REAL_ROOT##*/}"
SAFE_GIT_HOME="$FIXTURE_ROOT/git-home"
SAFE_GIT_HOOKS="$FIXTURE_ROOT/git-hooks"

curl -q --noproxy '*' --connect-timeout 2 --max-time 10 -fsS "$BASE_URL/readyz" >/dev/null || \
  fail 'local runtime is not ready'

ALPHA_ROOT="$FIXTURE_ROOT/alpha"
BETA_ROOT="$FIXTURE_ROOT/beta"
mkdir "$ALPHA_ROOT" "$BETA_ROOT" "$SAFE_GIT_HOME" "$SAFE_GIT_HOOKS" 2>/dev/null || \
  fail 'browser fixture directories could not be created'

safe_git() {
  "$ENV_BIN" -i \
    HOME="$SAFE_GIT_HOME" \
    PATH="$SAFE_GIT_PATH" \
    LANG=C \
    LC_ALL=C \
    GIT_ATTR_NOSYSTEM=1 \
    GIT_CONFIG_COUNT=0 \
    GIT_CONFIG_GLOBAL=/dev/null \
    GIT_CONFIG_NOSYSTEM=1 \
    GIT_CONFIG_SYSTEM=/dev/null \
    GIT_OPTIONAL_LOCKS=0 \
    GIT_TERMINAL_PROMPT=0 \
    "$GIT_BIN" --no-pager --no-optional-locks --no-replace-objects --literal-pathspecs \
      -c color.ui=false \
      -c core.fsmonitor=false \
      -c core.hooksPath="$SAFE_GIT_HOOKS" \
      -c core.attributesFile=/dev/null \
      -c core.excludesFile=/dev/null \
      -c diff.external= \
      -c diff.trustExitCode=false \
      -c protocol.allow=never \
      -c credential.helper= \
      -c commit.gpgsign=false \
      "$@"
}

init_repository() {
  local directory="$1" prefix="$2" count="$3" index file_number
  safe_git -C "$directory" init -q --template= 2>/dev/null || fail 'browser fixture Git initialization failed'
  safe_git -C "$directory" symbolic-ref HEAD refs/heads/main 2>/dev/null || \
    fail 'browser fixture branch setup failed'
  safe_git -C "$directory" config user.name 'Teamva Browser Smoke' 2>/dev/null || \
    fail 'browser fixture Git identity setup failed'
  safe_git -C "$directory" config user.email 'browser-smoke@teamva.invalid' 2>/dev/null || \
    fail 'browser fixture Git identity setup failed'
  safe_git -C "$directory" config commit.gpgsign false 2>/dev/null || \
    fail 'browser fixture Git signing setup failed'
  safe_git -C "$directory" config core.hooksPath "$SAFE_GIT_HOOKS" 2>/dev/null || \
    fail 'browser fixture Git hooks setup failed'
  for ((index = 1; index <= count; index++)); do
    file_number="$(printf '%02d' "$index")"
    printf 'base %02d\n' "$index" 2>/dev/null >"$directory/$prefix-$file_number.txt" || \
      fail 'browser fixture base file could not be written'
  done
  safe_git -C "$directory" add -- . 2>/dev/null || fail 'browser fixture staging failed'
  safe_git -C "$directory" commit -q -m 'browser fixture' 2>/dev/null || \
    fail 'browser fixture commit failed'
  for ((index = 1; index <= count; index++)); do
    file_number="$(printf '%02d' "$index")"
    printf 'changed %02d\n' "$index" 2>/dev/null >"$directory/$prefix-$file_number.txt" || \
      fail 'browser fixture changed file could not be written'
  done
}

init_repository "$ALPHA_ROOT" alpha 8
init_repository "$BETA_ROOT" beta 1

# Fetch is page-target scoped; also deny hostname resolution from worker/child targets.
NO_PROXY='*' no_proxy='*' "$CHROME_BIN" \
  --headless=new \
  --disable-gpu \
  --disable-background-networking \
  --disable-component-update \
  --disable-extensions \
  --host-resolver-rules='MAP * ~NOTFOUND, EXCLUDE 127.0.0.1' \
  --no-proxy-server \
  --no-first-run \
  --no-default-browser-check \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=0 \
  --user-data-dir="$PROFILE_DIR" \
  --window-size=1280,820 \
  about:blank >"$CHROME_LOG" 2>&1 &
CHROME_PID="$!"

for ((attempt = 0; attempt < 300; attempt++)); do
  [[ -s "$PROFILE_DIR/DevToolsActivePort" ]] && break
  kill -0 "$CHROME_PID" 2>/dev/null || fail 'Chrome exited before CDP became ready'
  sleep 0.1
done
[[ -s "$PROFILE_DIR/DevToolsActivePort" ]] || fail 'Chrome CDP did not become ready'

FORBIDDEN_TEXTS="$(jq -cn --arg root "$FIXTURE_ROOT" --arg real "$FIXTURE_REAL_ROOT" \
  --arg basename "$FIXTURE_BASENAME" '[$root,$real,$basename] | unique')"

run_cdp_helper() {
  local deadline node_status
  NO_PROXY='*' no_proxy='*' \
  TEAMVA_CHROME_PROFILE_DIR="$PROFILE_DIR" \
  TEAMVA_CHROME_ORIGIN="$BASE_URL" \
  TEAMVA_CHROME_ALPHA_ROOT="$ALPHA_ROOT" \
  TEAMVA_CHROME_BETA_ROOT="$BETA_ROOT" \
  TEAMVA_CHROME_SCREENSHOT_PATH="$SCREENSHOT_PATH" \
  TEAMVA_CHROME_FORBIDDEN_TEXTS="$FORBIDDEN_TEXTS" \
  TEAMVA_CHROME_EXPECTED_TEXT='["Alpha","Beta","alpha-01.txt","alpha-08.txt","beta-01.txt","未暂存"]' \
  TEAMVA_CHROME_SINGLE_LABEL='单文件' \
  TEAMVA_CHROME_STACKED_LABEL='堆叠' \
  TEAMVA_CHROME_STACKED_SUMMARY='显示 8 个文件中的 6 个' \
    "$NODE_BIN" "$SCRIPT_DIR/local-changes-browser-cdp.mjs" &
  NODE_PID="$!"

  deadline=$((SECONDS + NODE_TOTAL_TIMEOUT_SECONDS))
  while kill -0 "$NODE_PID" 2>/dev/null; do
    if ((SECONDS >= deadline)); then
      if ! stop_node; then
        fail 'Chrome CDP helper exceeded its total timeout and could not be terminated'
      fi
      fail 'Chrome CDP helper exceeded its total timeout'
    fi
    sleep 0.1
  done
  if wait "$NODE_PID"; then
    node_status=0
  else
    node_status=$?
  fi
  NODE_PID=""
  ((node_status == 0)) || fail 'Chrome CDP helper failed'
}

run_cdp_helper
stop_chrome || fail 'Chrome subprocess cleanup did not complete'
SUCCESS_SUMMARY='Chrome Changes smoke passed: repositories=Alpha,Beta stacked=6/8 real-diffs=6'
