#!/usr/bin/env bash

set -euo pipefail

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

STATE_FILE="${TEAMVA_LOCAL_STATE_FILE:-}"
PROVIDER="${TEAMVA_LOCAL_PROVIDER:-opencode}"
PREFERRED_MODEL="${TEAMVA_LOCAL_MODEL:-}"
unset TEAMVA_LOCAL_BOOTSTRAP_NONCE TEAMVA_LOCAL_RESOURCE_PATH \
  TEAMVA_LOCAL_RESOURCE_ISOLATED

TEMP_ROOT=""
LOCAL_DATA_DIR=""
PROJECT_WORKSPACE_PATH=""
RESOURCE_PATH=""
PARTIAL_REPOSITORY_ROOT=""
PARTIAL_RESOURCE_A=""
PARTIAL_RESOURCE_B=""
RESOURCE_LABEL="runtime-fixture"
RESOURCE_DISPLAY="Runtime Fixture"
PARTIAL_REPOSITORY_LABEL="runtime-partial-parent"
PARTIAL_RESOURCE_A_DISPLAY="Runtime Partial A"
PARTIAL_RESOURCE_B_DISPLAY="Runtime Partial B"
WORKSPACE_WRITE_MARKER="teamva-workspace-write.txt"
WORKSPACE_WRITE_CONTENT="workspace-write:$PROVIDER"
MCP_COMMENT_MARKER="teamva-local-runtime-smoke-mcp-marker-v1"
SAFE_GIT_HOME=""
SAFE_GIT_HOOKS=""
COOKIE_JAR=""
RESPONSE_FILE=""
CURL_SECRET_CONFIG=""
GIT_FINGERPRINT_ROOT=""
GIT_FINGERPRINT_BASELINE=""
GIT_FINGERPRINT_ISSUE=""
RESPONSE=""
CSRF_TOKEN=""
COMPUTER_ID=""
SERVICE_ID=""
PROJECT_ID=""
AGENT_ID=""
PARTIAL_RESOURCE_A_ID=""
PARTIAL_RESOURCE_B_ID=""
GIT_REPOSITORY_ID=""
PARTIAL_GIT_REPOSITORY_ID=""
ISSUE_ID=""
ISSUE_AGENT_INSTANCE_ID=""
TASK_ID=""
SCHEDULE_NAME=""
RUN_ID=""
SCHEDULE_RUN_ID=""
SUCCESS_SUMMARY=""
ISSUE_ACTIVATION_REQUESTED=false
SCHEDULE_CREATION_REQUESTED=false

cleanup() {
  local exit_status="$1" run_cleanup_failed=0 temp_cleanup_failed=0
  trap - EXIT INT TERM
  set +e

  # Pause a known or recoverable task while its Agent is still addressable. The
  # Agent deletion below is the global durable barrier before any Run discovery.
  if [[ "$SCHEDULE_CREATION_REQUESTED" == true || -n "$TASK_ID" ]]; then
    if ! recover_exact_scheduled_task; then
      printf 'FAIL: exact scheduled task recovery was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi
  if [[ -n "$TASK_ID" ]]; then
    if ! pause_exact_scheduled_task; then
      printf 'FAIL: exact scheduled task pause was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi

  if [[ -n "$AGENT_ID" ]]; then
    if ! delete_exact_agent_barrier; then
      printf 'FAIL: exact Agent deletion barrier was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi

  if [[ "$ISSUE_ACTIVATION_REQUESTED" == true || -n "$RUN_ID" ]]; then
    if ! pause_exact_issue_agent_instance; then
      printf 'FAIL: exact Issue Agent instance pause was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi

  if [[ -n "$SCHEDULE_RUN_ID" ]]; then
    if ! best_effort_stop_run "$SCHEDULE_RUN_ID" '' ''; then
      printf 'FAIL: known scheduled Run cleanup was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi
  if [[ -n "$TASK_ID" ]]; then
    if ! discover_and_stop_scheduled_runs; then
      printf 'FAIL: scheduled occurrence Run cleanup was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi

  if [[ -n "$RUN_ID" ]]; then
    if ! best_effort_stop_run "$RUN_ID" "$ISSUE_ID" "$ISSUE_AGENT_INSTANCE_ID"; then
      printf 'FAIL: known Issue Run cleanup was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi
  if [[ "$ISSUE_ACTIVATION_REQUESTED" == true || -n "$RUN_ID" ]]; then
    if ! discover_and_stop_issue_run; then
      printf 'FAIL: Issue Run discovery cleanup was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi

  # A successful terminal deliberately leaves its Provider warm for normal UI
  # reuse. Acceptance must instead prove a synchronous, bounded cleanup of the
  # exact Provider after every created Run is terminal.
  if [[ -n "$COMPUTER_ID" && -s "$CURL_SECRET_CONFIG" && -f "$COOKIE_JAR" ]]; then
    if ! recycle_idle_provider_runtime; then
      printf 'FAIL: idle Provider runtime cleanup was not verified\n' >&2
      run_cleanup_failed=1
    fi
  fi

  if [[ -n "$TEMP_ROOT" ]] && ! rm -rf -- "$TEMP_ROOT" 2>/dev/null; then
    temp_cleanup_failed=1
  fi
  if ((run_cleanup_failed != 0)); then
    ((exit_status != 0)) || exit_status=1
  fi
  if ((temp_cleanup_failed != 0)); then
    printf 'FAIL: runtime smoke temporary cleanup did not complete\n' >&2
    ((exit_status != 0)) || exit_status=1
  fi
  if ((exit_status == 0)) && [[ -n "$SUCCESS_SUMMARY" ]]; then
    printf '%s\n' "$SUCCESS_SUMMARY"
  fi
  exit "$exit_status"
}

[[ -n "$STATE_FILE" && -s "$STATE_FILE" ]] || fail 'runtime state is unavailable'
[[ "$PROVIDER" == opencode || "$PROVIDER" == codex ]] || fail 'unsupported local Provider'

for required_command in cmp curl env git grep jq mktemp node; do
  command -v "$required_command" >/dev/null 2>&1 || fail "missing command: $required_command"
done
ENV_BIN="$(command -v env)"
GIT_BIN="$(command -v git)"
NODE_BIN="$(command -v node)"
SAFE_GIT_PATH="${GIT_BIN%/*}:/usr/bin:/bin:/usr/sbin:/sbin"
[[ "$ENV_BIN" == /* && "$GIT_BIN" == /* && "$NODE_BIN" == /* ]] || \
  fail 'required executable paths could not be resolved safely'

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
      -c user.name='Teamva Runtime Smoke' \
      -c user.email=runtime-smoke@teamva.invalid \
      "$@"
}

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
LOCAL_DATA_DIR="${STATE_FILE%/*}"
if [[ "$LOCAL_DATA_DIR" == "$STATE_FILE" ]] || \
  ! LOCAL_DATA_DIR="$(CDPATH='' cd -- "$LOCAL_DATA_DIR" 2>/dev/null && pwd -P)"; then
  fail 'runtime state data directory cannot be canonicalized'
fi

TEMP_ROOT="$(mktemp -d -t teamva-local-runtime.XXXXXX)" || \
  fail 'runtime smoke temporary root could not be created'
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

RESOURCE_PATH="$TEMP_ROOT/$RESOURCE_LABEL"
PARTIAL_REPOSITORY_ROOT="$TEMP_ROOT/runtime-partial-parent"
PARTIAL_RESOURCE_A="$PARTIAL_REPOSITORY_ROOT/scope-a"
PARTIAL_RESOURCE_B="$PARTIAL_REPOSITORY_ROOT/scope-b"
SAFE_GIT_HOME="$TEMP_ROOT/git-home"
SAFE_GIT_HOOKS="$TEMP_ROOT/git-hooks"
COOKIE_JAR="$TEMP_ROOT/cookie.jar"
RESPONSE_FILE="$TEMP_ROOT/response.json"
CURL_SECRET_CONFIG="$TEMP_ROOT/curl-secret.config"
GIT_FINGERPRINT_ROOT="$TEMP_ROOT/fingerprints"
GIT_FINGERPRINT_BASELINE="$GIT_FINGERPRINT_ROOT/baseline"
GIT_FINGERPRINT_ISSUE="$GIT_FINGERPRINT_ROOT/issue"
mkdir "$RESOURCE_PATH" "$PARTIAL_REPOSITORY_ROOT" "$SAFE_GIT_HOME" "$SAFE_GIT_HOOKS" \
  "$GIT_FINGERPRINT_ROOT" \
  2>/dev/null || fail 'runtime smoke fixture directories could not be created'
mkdir "$PARTIAL_RESOURCE_A" "$PARTIAL_RESOURCE_B" 2>/dev/null || \
  fail 'runtime partial-scope fixture directories could not be created'
if ! printf '# Runtime Smoke Fixture\n' >"$RESOURCE_PATH/README.md" || \
  ! printf '.teamva/\n' >"$RESOURCE_PATH/.gitignore"; then
  fail 'runtime smoke fixture files could not be written'
fi
safe_git -C "$RESOURCE_PATH" init -q --template= 2>/dev/null || \
  fail 'runtime smoke fixture Git initialization failed'
safe_git -C "$RESOURCE_PATH" symbolic-ref HEAD refs/heads/main 2>/dev/null || \
  fail 'runtime smoke fixture branch setup failed'
safe_git -C "$RESOURCE_PATH" add -- README.md .gitignore 2>/dev/null || \
  fail 'runtime smoke fixture staging failed'
safe_git -C "$RESOURCE_PATH" commit -q -m 'runtime smoke fixture' 2>/dev/null || \
  fail 'runtime smoke fixture commit failed'
if ! printf 'partial a\n' >"$PARTIAL_RESOURCE_A/tracked-a.txt" || \
  ! printf 'partial b\n' >"$PARTIAL_RESOURCE_B/tracked-b.txt" || \
  ! printf 'outside partial roots\n' >"$PARTIAL_REPOSITORY_ROOT/outside.txt"; then
  fail 'runtime partial-scope fixture files could not be written'
fi
safe_git -C "$PARTIAL_REPOSITORY_ROOT" init -q --template= 2>/dev/null || \
  fail 'runtime partial-scope Git initialization failed'
safe_git -C "$PARTIAL_REPOSITORY_ROOT" symbolic-ref HEAD refs/heads/main 2>/dev/null || \
  fail 'runtime partial-scope branch setup failed'
safe_git -C "$PARTIAL_REPOSITORY_ROOT" add -- scope-a/tracked-a.txt scope-b/tracked-b.txt outside.txt \
  2>/dev/null || fail 'runtime partial-scope staging failed'
safe_git -C "$PARTIAL_REPOSITORY_ROOT" commit -q -m 'runtime partial-scope fixture' 2>/dev/null || \
  fail 'runtime partial-scope commit failed'
if ! RESOURCE_PATH="$(CDPATH='' cd -- "$RESOURCE_PATH" 2>/dev/null && pwd -P)"; then
  fail 'runtime smoke fixture cannot be canonicalized'
fi
if ! PARTIAL_REPOSITORY_ROOT="$(CDPATH='' cd -- "$PARTIAL_REPOSITORY_ROOT" 2>/dev/null && pwd -P)" || \
  ! PARTIAL_RESOURCE_A="$(CDPATH='' cd -- "$PARTIAL_RESOURCE_A" 2>/dev/null && pwd -P)" || \
  ! PARTIAL_RESOURCE_B="$(CDPATH='' cd -- "$PARTIAL_RESOURCE_B" 2>/dev/null && pwd -P)"; then
  fail 'runtime partial-scope fixture cannot be canonicalized'
fi

run_is_terminal() {
  case "$1" in
    succeeded|failed|cancelled|lost) return 0 ;;
    *) return 1 ;;
  esac
}

cleanup_get() {
  local path="$1"
  curl -q --noproxy '*' --connect-timeout 1 --max-time 2 -fsS \
    -b "$COOKIE_JAR" -H 'Accept: application/json' -H "Origin: $BASE_URL" \
    "$BASE_URL$path" 2>/dev/null
}

cleanup_post() {
  local path="$1" body="$2" max_time="${3:-2}"
  [[ -s "$CURL_SECRET_CONFIG" ]] || return 1
  printf '%s\n' "$body" | curl -q --noproxy '*' --connect-timeout 1 --max-time "$max_time" -fsS \
    -X POST -b "$COOKIE_JAR" -H 'Accept: application/json' \
    -H 'Content-Type: application/json' -H "Origin: $BASE_URL" \
    --config "$CURL_SECRET_CONFIG" --data-binary @- "$BASE_URL$path" 2>/dev/null
}

cleanup_get_status() {
  local path="$1"
  [[ -n "$RESPONSE_FILE" ]] || return 1
  : >"$RESPONSE_FILE" || return 1
  curl -q --noproxy '*' --connect-timeout 1 --max-time 2 -sS \
    -o "$RESPONSE_FILE" -w '%{http_code}' -b "$COOKIE_JAR" \
    -H 'Accept: application/json' -H "Origin: $BASE_URL" \
    "$BASE_URL$path" 2>/dev/null
}

cleanup_delete_status() {
  local path="$1"
  [[ -s "$CURL_SECRET_CONFIG" ]] || return 1
  curl -q --noproxy '*' --connect-timeout 1 --max-time 2 -sS \
    -o /dev/null -w '%{http_code}' -X DELETE -b "$COOKIE_JAR" \
    -H 'Accept: application/json' -H "Origin: $BASE_URL" \
    --config "$CURL_SECRET_CONFIG" "$BASE_URL$path" 2>/dev/null
}

delete_exact_agent_barrier() {
  local payload="" version="" get_status="" delete_status="" attempt
  local delete_attempted=false
  [[ -n "$AGENT_ID" && -n "$COMPUTER_ID" && -n "$SERVICE_ID" && \
    -s "$CURL_SECRET_CONFIG" && -f "$COOKIE_JAR" ]] || return 1
  [[ "$AGENT_ID" =~ ^[A-Za-z0-9_-]{1,128}$ && \
    "$COMPUTER_ID" =~ ^[A-Za-z0-9_-]{1,128}$ && \
    "$SERVICE_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1

  for ((attempt = 0; attempt < 10; attempt++)); do
    if ! get_status="$(cleanup_get_status "/v1/agents/$AGENT_ID")"; then
      sleep 0.1
      continue
    fi
    if [[ "$get_status" == 404 ]]; then
      [[ "$delete_attempted" == true ]] && return 0
      return 1
    fi
    if [[ "$get_status" != 200 ]]; then
      sleep 0.1
      continue
    fi

    payload="$(<"$RESPONSE_FILE")"
    jq -e --arg agent "$AGENT_ID" --arg computer "$COMPUTER_ID" \
      --arg service "$SERVICE_ID" '
        .data.id == $agent and .data.computer_id == $computer and
        .data.agent_service_id == $service and
        (.data.version | type) == "number" and .data.version >= 1 and
        (.data.version | floor) == .data.version
      ' <<<"$payload" >/dev/null 2>&1 || return 1
    version="$(jq -er '.data.version' <<<"$payload" 2>/dev/null)" || return 1

    # A transport error can happen after the DELETE commits. The following GET
    # proves the durable 404 barrier before cleanup proceeds to Run discovery.
    delete_attempted=true
    if ! delete_status="$(cleanup_delete_status \
      "/v1/agents/$AGENT_ID?expected_version=$version")"; then
      sleep 0.1
      continue
    fi
    case "$delete_status" in
      204|404|409) ;;
      *) return 1 ;;
    esac
    sleep 0.1
  done
  return 1
}

recover_exact_scheduled_task() {
  local payload="" candidate_count="" discovered_task_id=""
  [[ -n "$PROJECT_ID" && -n "$AGENT_ID" && -n "$SCHEDULE_NAME" ]] || return 1
  [[ "$PROJECT_ID" =~ ^[A-Za-z0-9_-]{1,128}$ && \
    "$AGENT_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1

  payload="$(cleanup_get "/v1/projects/$PROJECT_ID/scheduled-tasks")" || return 1
  jq -e '(.data.items | type) == "array"' <<<"$payload" >/dev/null 2>&1 || return 1
  candidate_count="$(jq -er --arg project "$PROJECT_ID" --arg name "$SCHEDULE_NAME" \
    --arg agent "$AGENT_ID" '
      [.data.items[] | select(
        .project_id == $project and .name == $name and .agent_id == $agent)] | length
    ' <<<"$payload" 2>/dev/null)" || return 1
  [[ "$candidate_count" -le 1 ]] || return 1
  if [[ "$candidate_count" -eq 0 ]]; then
    [[ -z "$TASK_ID" ]] || return 1
    return 0
  fi

  discovered_task_id="$(jq -er --arg project "$PROJECT_ID" --arg name "$SCHEDULE_NAME" \
    --arg agent "$AGENT_ID" '
      .data.items[] | select(
        .project_id == $project and .name == $name and .agent_id == $agent) | .id
    ' <<<"$payload" 2>/dev/null)" || return 1
  [[ "$discovered_task_id" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1
  [[ -z "$TASK_ID" || "$TASK_ID" == "$discovered_task_id" ]] || return 1
  TASK_ID="$discovered_task_id"
}

pause_exact_issue_agent_instance() {
  local payload="" candidate_count="" version="" status="" body="" attempt
  local expected_instance_id="$ISSUE_AGENT_INSTANCE_ID"
  [[ -n "$ISSUE_ID" && -n "$AGENT_ID" && -s "$CURL_SECRET_CONFIG" ]] || return 1
  [[ "$ISSUE_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1
  [[ -z "$expected_instance_id" || "$expected_instance_id" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1

  for ((attempt = 0; attempt < 50; attempt++)); do
    payload="$(cleanup_get "/v1/issues/$ISSUE_ID/agent-instances")" || return 1
    jq -e '(.data.items | type) == "array"' <<<"$payload" >/dev/null 2>&1 || return 1
    candidate_count="$(jq -er --arg issue "$ISSUE_ID" --arg agent "$AGENT_ID" \
      --arg instance "$expected_instance_id" '
      [.data.items[] | select(
        .issue_id == $issue and .agent_id == $agent and
        ($instance == "" or .id == $instance))] | length
    ' <<<"$payload" 2>/dev/null)" || return 1
    [[ "$candidate_count" -le 1 ]] || return 1
    if [[ "$candidate_count" -eq 0 ]]; then
      # Agent deletion is already the durable dispatch barrier. With no known
      # instance, zero rows means this mention can no longer create one later.
      [[ -z "$expected_instance_id" ]] && return 0
      return 1
    fi

    ISSUE_AGENT_INSTANCE_ID="$(jq -er --arg issue "$ISSUE_ID" --arg agent "$AGENT_ID" \
      --arg instance "$expected_instance_id" '
      .data.items[] | select(
        .issue_id == $issue and .agent_id == $agent and
        ($instance == "" or .id == $instance)) | .id
    ' <<<"$payload" 2>/dev/null)" || return 1
    [[ "$ISSUE_AGENT_INSTANCE_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1
    version="$(jq -er --arg instance "$ISSUE_AGENT_INSTANCE_ID" '
      .data.items[] | select(.id == $instance) | .version |
      select(type == "number" and . >= 1 and floor == .)
    ' <<<"$payload" 2>/dev/null)" || return 1
    status="$(jq -er --arg instance "$ISSUE_AGENT_INSTANCE_ID" '
      .data.items[] | select(.id == $instance) | .status | select(type == "string")
    ' <<<"$payload" 2>/dev/null)" || return 1
    [[ "$status" != paused ]] || return 0

    body="$(jq -cn --argjson version "$version" '{expected_version:$version}')" || return 1
    if payload="$(cleanup_post "/v1/issues/$ISSUE_ID/agent-instances/$ISSUE_AGENT_INSTANCE_ID/pause" "$body")" &&
      jq -e --arg instance "$ISSUE_AGENT_INSTANCE_ID" --arg issue "$ISSUE_ID" \
        --arg agent "$AGENT_ID" '
          .data.id == $instance and .data.issue_id == $issue and
          .data.agent_id == $agent and .data.status == "paused" and
          (.data.version | type) == "number"
        ' <<<"$payload" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

pause_exact_scheduled_task() {
  local payload="" version="" enabled="" body="" attempt
  [[ -n "$TASK_ID" && -n "$PROJECT_ID" && -n "$AGENT_ID" && \
    -s "$CURL_SECRET_CONFIG" ]] || return 1
  [[ "$TASK_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1

  for ((attempt = 0; attempt < 50; attempt++)); do
    payload="$(cleanup_get "/v1/scheduled-tasks/$TASK_ID")" || return 1
    jq -e --arg task "$TASK_ID" --arg project "$PROJECT_ID" --arg agent "$AGENT_ID" '
        .data.id == $task and .data.project_id == $project and .data.agent_id == $agent and
        (.data | has("git_repository_id") | not) and
        (.data.version | type) == "number" and .data.version >= 1 and
        (.data.enabled | type) == "boolean"
      ' <<<"$payload" >/dev/null 2>&1 || return 1
    version="$(jq -er '.data.version' <<<"$payload" 2>/dev/null)" || return 1
    enabled="$(jq -r '.data.enabled' <<<"$payload" 2>/dev/null)" || return 1
    [[ "$enabled" == true ]] || return 0

    body="$(jq -cn --argjson version "$version" '{expected_version:$version}')" || return 1
    if payload="$(cleanup_post "/v1/scheduled-tasks/$TASK_ID/pause" "$body")" &&
      jq -e --arg task "$TASK_ID" --arg project "$PROJECT_ID" --arg agent "$AGENT_ID" '
          .data.id == $task and .data.project_id == $project and .data.agent_id == $agent and
          (.data | has("git_repository_id") | not) and .data.enabled == false and
          (.data.version | type) == "number"
        ' <<<"$payload" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

run_matches_fixture() {
  local payload="$1" run_id="$2" issue_id="$3" instance_id="$4"
  jq -e --arg run "$run_id" --arg issue "$issue_id" --arg project "$PROJECT_ID" \
    --arg instance "$instance_id" --arg agent "$AGENT_ID" '
      .data.id == $run and (.data.issue_id // "") == $issue and
      (.data.issue_agent_instance_id // "") == $instance and
      .data.project_id == $project and .data.agent_id == $agent and
      (.data | has("git_repository_id") | not)
    ' <<<"$payload" >/dev/null 2>&1
}

best_effort_stop_run() {
  local run_id="$1" issue_id="$2" instance_id="$3" payload status="" attempt=0 deadline
  [[ -n "$run_id" && -s "$CURL_SECRET_CONFIG" && -f "$COOKIE_JAR" && \
    -n "$PROJECT_ID" && -n "$AGENT_ID" ]] || return 1
  [[ "$run_id" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1

  if ! payload="$(cleanup_get "/v1/runs/$run_id")"; then
    return 1
  fi
  run_matches_fixture "$payload" "$run_id" "$issue_id" "$instance_id" || return 1
  status="$(jq -r '.data.status // empty' <<<"$payload" 2>/dev/null || true)"
  run_is_terminal "$status" && return 0

  cleanup_post "/v1/runs/$run_id/stop" '{"mode":"force"}' >/dev/null || true

  deadline=$((SECONDS + 10))
  while ((SECONDS < deadline)); do
    payload="$(cleanup_get "/v1/runs/$run_id" || true)"
    if [[ -n "$payload" ]] && ! run_matches_fixture "$payload" "$run_id" "$issue_id" "$instance_id"; then
      return 1
    fi
    status="$(jq -r '.data.status // empty' <<<"$payload" 2>/dev/null || true)"
    run_is_terminal "$status" && return 0
    attempt=$((attempt + 1))
    if ((attempt % 20 == 0)); then
      cleanup_post "/v1/runs/$run_id/stop" '{"mode":"force"}' >/dev/null || true
    fi
    sleep 0.1
  done
  return 1
}

discover_and_stop_issue_run() {
  local payload="" candidate_count="" discovered_run_id="" discovered_instance_id=""
  local expected_run_id="$RUN_ID" expected_instance_id="$ISSUE_AGENT_INSTANCE_ID"
  local index cleanup_failed=0 known_run_found=false
  [[ -n "$ISSUE_ID" && -n "$PROJECT_ID" && -n "$AGENT_ID" ]] || return 1
  [[ "$ISSUE_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1
  payload="$(cleanup_get "/v1/issues/$ISSUE_ID/runs")" || return 1
  jq -e '(.data.items | type) == "array"' <<<"$payload" >/dev/null 2>&1 || return 1
  candidate_count="$(jq -er --arg issue "$ISSUE_ID" --arg project "$PROJECT_ID" \
    --arg agent "$AGENT_ID" '
      [.data.items[] | select(
        .issue_id == $issue and .project_id == $project and .agent_id == $agent)] | length
    ' <<<"$payload" 2>/dev/null)" || return 1

  for ((index = 0; index < candidate_count; index++)); do
    if ! jq -e --argjson index "$index" --arg issue "$ISSUE_ID" \
      --arg project "$PROJECT_ID" --arg agent "$AGENT_ID" '
        [.data.items[] | select(
          .issue_id == $issue and .project_id == $project and .agent_id == $agent)][$index] as $item |
        ($item | type) == "object" and
        ($item.id | type) == "string" and
        ($item.id | test("^[A-Za-z0-9_-]{1,128}$")) and
        ($item.issue_agent_instance_id | type) == "string" and
        ($item.issue_agent_instance_id | test("^[A-Za-z0-9_-]{1,128}$"))
      ' <<<"$payload" >/dev/null 2>&1; then
      cleanup_failed=1
      continue
    fi
    discovered_run_id="$(jq -er --argjson index "$index" --arg issue "$ISSUE_ID" \
      --arg project "$PROJECT_ID" --arg agent "$AGENT_ID" '
        [.data.items[] | select(
          .issue_id == $issue and .project_id == $project and .agent_id == $agent)][$index].id
      ' <<<"$payload" 2>/dev/null)" || {
      cleanup_failed=1
      continue
    }
    discovered_instance_id="$(jq -er --argjson index "$index" --arg issue "$ISSUE_ID" \
      --arg project "$PROJECT_ID" --arg agent "$AGENT_ID" '
        [.data.items[] | select(
          .issue_id == $issue and .project_id == $project and .agent_id == $agent)][$index].issue_agent_instance_id
      ' <<<"$payload" 2>/dev/null)" || {
      cleanup_failed=1
      continue
    }

    if [[ -n "$expected_run_id" && "$discovered_run_id" == "$expected_run_id" ]]; then
      known_run_found=true
      if [[ -n "$expected_instance_id" && \
        "$discovered_instance_id" != "$expected_instance_id" ]]; then
        cleanup_failed=1
      fi
    fi
    # Use each row's own instance ID so the detailed Run read verifies the
    # exact instance/run binding before that Run is stopped.
    if ! best_effort_stop_run "$discovered_run_id" "$ISSUE_ID" "$discovered_instance_id"; then
      cleanup_failed=1
    fi
  done

  if [[ -n "$expected_run_id" && "$known_run_found" != true ]]; then
    cleanup_failed=1
  fi
  ((cleanup_failed == 0))
}

discover_and_stop_scheduled_runs() {
  local payload="" occurrence_count="" occurrence_id="" occurrence_status="" run_id=""
  local index cleanup_failed=0 known_run_found=false
  [[ -n "$TASK_ID" && -n "$PROJECT_ID" && -n "$AGENT_ID" ]] || return 1
  [[ "$TASK_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]] || return 1
  payload="$(cleanup_get "/v1/scheduled-tasks/$TASK_ID/runs")" || return 1
  jq -e '(.data.items | type) == "array"' <<<"$payload" >/dev/null 2>&1 || return 1
  occurrence_count="$(jq -er '.data.items | length' <<<"$payload" 2>/dev/null)" || return 1

  for ((index = 0; index < occurrence_count; index++)); do
    if ! jq -e --argjson index "$index" --arg task "$TASK_ID" '
      .data.items[$index] as $item |
      $item.scheduled_task_id == $task and
      ($item.id | type) == "string" and
      ($item.id | test("^[A-Za-z0-9_-]{1,128}$")) and
      (($item.run_id // "") | type) == "string" and
      ($item.status | IN(
        "pending", "running", "succeeded", "failed", "cancelled", "lost",
        "skipped_misfire", "skipped_concurrency"))
    ' <<<"$payload" >/dev/null 2>&1; then
      cleanup_failed=1
      continue
    fi
    occurrence_id="$(jq -er --argjson index "$index" '.data.items[$index].id' \
      <<<"$payload" 2>/dev/null)" || {
      cleanup_failed=1
      continue
    }
    occurrence_status="$(jq -er --argjson index "$index" '.data.items[$index].status' \
      <<<"$payload" 2>/dev/null)" || {
      cleanup_failed=1
      continue
    }
    run_id="$(jq -r --argjson index "$index" '.data.items[$index].run_id // ""' \
      <<<"$payload" 2>/dev/null)" || {
      cleanup_failed=1
      continue
    }
    [[ -n "$occurrence_id" && -n "$occurrence_status" ]] || {
      cleanup_failed=1
      continue
    }
    if [[ ! "$run_id" =~ ^[A-Za-z0-9_-]{1,128}$ ]]; then
      cleanup_failed=1
      continue
    fi
    if [[ -n "$SCHEDULE_RUN_ID" && "$run_id" == "$SCHEDULE_RUN_ID" ]]; then
      known_run_found=true
    fi
    if ! best_effort_stop_run "$run_id" '' ''; then
      cleanup_failed=1
    fi
  done

  if [[ -n "$SCHEDULE_RUN_ID" && "$known_run_found" != true ]]; then
    cleanup_failed=1
  fi
  ((cleanup_failed == 0))
}

recycle_idle_provider_runtime() {
  local payload="" attempt recycled="" active="" remaining=""
  [[ -n "$COMPUTER_ID" && "$COMPUTER_ID" =~ ^[A-Za-z0-9_-]{1,128}$ && \
    ("$PROVIDER" == codex || "$PROVIDER" == opencode) ]] || return 1

  for ((attempt = 0; attempt < 100; attempt++)); do
    if payload="$(cleanup_post \
      "/v1/computers/$COMPUTER_ID/programming-tools/$PROVIDER/recycle-idle" '{}' 20)" &&
      jq -e --arg provider "$PROVIDER" '
        .data.provider == $provider and
        (.data.recycled_instances | type) == "number" and
        (.data.recycled_instances | floor) == .data.recycled_instances and
        .data.recycled_instances >= 0 and
        (.data.active_instances | type) == "number" and
        (.data.active_instances | floor) == .data.active_instances and
        .data.active_instances >= 0 and
        (.data.remaining_instances | type) == "number" and
        (.data.remaining_instances | floor) == .data.remaining_instances and
        .data.remaining_instances >= .data.active_instances
      ' <<<"$payload" >/dev/null 2>&1; then
      recycled="$(jq -er '.data.recycled_instances' <<<"$payload" 2>/dev/null)" || return 1
      active="$(jq -er '.data.active_instances' <<<"$payload" 2>/dev/null)" || return 1
      remaining="$(jq -er '.data.remaining_instances' <<<"$payload" 2>/dev/null)" || return 1
      if [[ "$active" -eq 0 && "$remaining" -eq 0 ]]; then
        [[ "$recycled" -ge 0 ]] || return 1
        return 0
      fi
    fi
    sleep 0.1
  done
  return 1
}

data() { jq -er ".data$1" <<<"$RESPONSE"; }

api() {
  local method="$1" path="$2" expected="$3" body="${4:-}" idempotency="${5:-}"
  local -a args=(--noproxy '*' --connect-timeout 2 --max-time 15 -sS
    -o "$RESPONSE_FILE" -w '%{http_code}' -X "$method"
    -b "$COOKIE_JAR" -c "$COOKIE_JAR" -H 'Accept: application/json'
    -H "Origin: $BASE_URL" "$BASE_URL$path")
  : >"$RESPONSE_FILE"
  [[ -z "$body" ]] || args+=(-H 'Content-Type: application/json' --data-binary @-)
  if [[ "$method" != GET && "$method" != HEAD && "$path" != "/v1/auth/local-session" ]]; then
    [[ -s "$CURL_SECRET_CONFIG" ]] || fail 'CSRF curl configuration is unavailable'
    args+=(--config "$CURL_SECRET_CONFIG")
  fi
  [[ -z "$idempotency" ]] || args+=(-H "Idempotency-Key: $idempotency")
  local status safe_error=""
  if [[ -n "$body" ]]; then
    status="$(curl -q "${args[@]}" <<<"$body")"
  else
    status="$(curl -q "${args[@]}")"
  fi
  RESPONSE="$(<"$RESPONSE_FILE")"
  if [[ "|$expected|" != *"|$status|"* ]]; then
    if jq -e . >/dev/null 2>&1 <<<"$RESPONSE"; then
      safe_error="$(jq -r '.error.code // empty' <<<"$RESPONSE")"
    fi
    [[ -z "$safe_error" ]] || safe_error=" ($safe_error)"
    fail "$method $path expected $expected, got $status$safe_error"
  fi
  [[ -z "$RESPONSE" ]] || jq -e . >/dev/null <<<"$RESPONSE" || \
    fail "$method $path returned invalid JSON"
  printf 'PASS %-6s %-64s %s\n' "$method" "$path" "$status"
}

assert_run_binding() {
  local run_id="$1" issue_id="$2" label="$3" instance_id="${4:-}"
  jq -e --arg run "$run_id" --arg issue "$issue_id" --arg project "$PROJECT_ID" \
    --arg instance "$instance_id" --arg agent "$AGENT_ID" '
      .data.id == $run and (.data.issue_id // "") == $issue and
      (.data.issue_agent_instance_id // "") == $instance and
      .data.project_id == $project and .data.agent_id == $agent and
      (.data | has("git_repository_id") | not)
    ' <<<"$RESPONSE" >/dev/null || fail "$label Run binding is incorrect"
}

assert_no_pending_approvals() {
  local run_id="$1" issue_id="$2" label="$3" count
  api GET '/v1/approvals?status=pending' 200 >/dev/null
  jq -e '(.data.items | type) == "array"' <<<"$RESPONSE" >/dev/null || \
    fail 'pending approvals response is malformed'
  count="$(jq -er '.data.items | length' <<<"$RESPONSE")"
  [[ "$count" -eq 0 ]] && return 0
  jq -e --arg run "$run_id" --arg issue "$issue_id" --arg project "$PROJECT_ID" '
    all(.data.items[];
      .run_id == $run and (.issue_id // "") == $issue and .project_id == $project)
  ' <<<"$RESPONSE" >/dev/null || fail 'a pending approval did not match the exact Run, Issue, and Project'
  fail "$label requested an unexpected native action; no approval was granted"
}

capture_git_fingerprint() {
  local destination="$1"
  mkdir "$destination" 2>/dev/null || return 1
  safe_git -C "$RESOURCE_PATH" rev-parse --verify HEAD >"$destination/head" 2>/dev/null &&
    safe_git -C "$RESOURCE_PATH" status --porcelain=v2 -z --untracked-files=all \
      >"$destination/porcelain-v2-z" 2>/dev/null &&
    safe_git -C "$RESOURCE_PATH" diff --binary --full-index --no-color --no-ext-diff \
      --no-textconv --cached -- >"$destination/cached.diff" 2>/dev/null &&
    safe_git -C "$RESOURCE_PATH" diff --binary --full-index --no-color --no-ext-diff \
      --no-textconv -- >"$destination/unstaged.diff" 2>/dev/null
}

assert_git_fingerprint_unchanged() {
  local label="$1" destination="$2" component
  capture_git_fingerprint "$destination" || fail "$label Git fingerprint could not be captured"
  for component in head porcelain-v2-z cached.diff unstaged.diff; do
    cmp -s "$GIT_FINGERPRINT_BASELINE/$component" "$destination/$component" || \
      fail "$label changed the isolated Git fixture"
  done
}

resolve_project_workspace() {
  local workspace_root="$LOCAL_DATA_DIR/runtime/workspaces/projects"
  local directory canonical="" candidate="" count=0
  [[ -d "$workspace_root" && ! -L "$workspace_root" ]] || return 1
  for directory in "$workspace_root"/*; do
    [[ -d "$directory" && ! -L "$directory" && -f "$directory/AGENTS.md" && \
      ! -L "$directory/AGENTS.md" ]] || continue
    grep -Fq -- "$RESOURCE_PATH" "$directory/AGENTS.md" 2>/dev/null || continue
    canonical="$(CDPATH='' cd -- "$directory" 2>/dev/null && pwd -P)" || return 1
    [[ "$canonical" == "$workspace_root/"* ]] || return 1
    candidate="$canonical"
    count=$((count + 1))
  done
  [[ "$count" -eq 1 ]] || return 1
  PROJECT_WORKSPACE_PATH="$candidate"
}

assert_project_workspace_manifest() {
  resolve_project_workspace || fail 'exact project Workspace could not be resolved from its repository map'
  local manifest_json
  manifest_json="$("$NODE_BIN" - "$PROJECT_WORKSPACE_PATH/AGENTS.md" <<'NODE'
const fs = require("fs");
const value = fs.readFileSync(process.argv[2], "utf8");
const start = value.indexOf("[\n");
if (start < 0) process.exit(1);
const parsed = JSON.parse(value.slice(start));
process.stdout.write(JSON.stringify(parsed));
NODE
  )" || fail 'project Workspace repository map is not valid JSON data'
  jq -e --arg full "$RESOURCE_PATH" --arg partial_a "$PARTIAL_RESOURCE_A" \
    --arg partial_b "$PARTIAL_RESOURCE_B" --arg parent "$PARTIAL_REPOSITORY_ROOT" '
    type == "array" and length == 2 and
    any(.[]; .writable_roots == [$full]) and
    any(.[]; .writable_roots == [$partial_a, $partial_b]) and
    all(.[]; (.writable_roots | index($parent)) == null)
  ' <<<"$manifest_json" >/dev/null 2>&1 || \
  fail 'project Workspace repository map widened or omitted an exact writable root'
}

assert_schedule_workspace_writes() {
  local expected="$WORKSPACE_WRITE_CONTENT"
  [[ -f "$RESOURCE_PATH/$WORKSPACE_WRITE_MARKER" && ! -L "$RESOURCE_PATH/$WORKSPACE_WRITE_MARKER" && \
    "$(<"$RESOURCE_PATH/$WORKSPACE_WRITE_MARKER")" == "$expected" ]] || \
    fail 'Schedule Run did not write the full repository root'
  [[ -f "$PARTIAL_RESOURCE_A/$WORKSPACE_WRITE_MARKER" && \
    ! -L "$PARTIAL_RESOURCE_A/$WORKSPACE_WRITE_MARKER" && \
    "$(<"$PARTIAL_RESOURCE_A/$WORKSPACE_WRITE_MARKER")" == "$expected" ]] || \
    fail 'Schedule Run did not write the first exact partial root'
  [[ -f "$PARTIAL_RESOURCE_B/$WORKSPACE_WRITE_MARKER" && \
    ! -L "$PARTIAL_RESOURCE_B/$WORKSPACE_WRITE_MARKER" && \
    "$(<"$PARTIAL_RESOURCE_B/$WORKSPACE_WRITE_MARKER")" == "$expected" ]] || \
    fail 'Schedule Run did not write the second exact partial root'
  [[ ! -e "$PARTIAL_REPOSITORY_ROOT/$WORKSPACE_WRITE_MARKER" ]] || \
    fail 'Schedule Run widened write access to the partial repository root'
}

wait_for_git_snapshots() {
  local run_id="$1" label="$2" mode="${3:-clean}" expected_repositories=2 attempt snapshot_count
  for ((attempt = 0; attempt < 150; attempt++)); do
    api GET "/v1/runs/$run_id/git/changes?limit=100" 200 >/dev/null
    if jq -e 'any(.data.snapshots[]?; (.warnings | type) != "array" or (.warnings | length) > 0)' \
      <<<"$RESPONSE" >/dev/null 2>&1; then
      fail "$label Git snapshot reported a warning"
    fi
    snapshot_count="$(jq -r 'if (.data.snapshots | type) == "array" then (.data.snapshots | length) else -1 end' \
      <<<"$RESPONSE")"
    if [[ "$snapshot_count" -ge $((expected_repositories * 2)) ]]; then
      jq -e --arg run "$run_id" --arg mode "$mode" --arg marker "$WORKSPACE_WRITE_MARKER" \
        --arg full_repository "$GIT_REPOSITORY_ID" \
        --arg partial_repository "$PARTIAL_GIT_REPOSITORY_ID" \
        --arg full_label "$RESOURCE_LABEL" --arg full_display "$RESOURCE_DISPLAY" \
        --arg partial_label "$PARTIAL_REPOSITORY_LABEL" \
        --argjson expected "$expected_repositories" '
        def timestamp:
          capture("^(?<whole>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?<fraction>\\.[0-9]+)?Z$") as $parts |
          (($parts.whole + "Z") | fromdateiso8601) as $validated |
          $parts.whole + (((($parts.fraction // ".0") + "000000000") | .[0:10]));
        def valid_time: (type == "string") and ((try timestamp catch null) != null);
        .data.run_id == $run and
        (.data.snapshots | type) == "array" and
        (.data.snapshots | length) == ($expected * 2) and
        ([.data.snapshots[].repository_id] | unique | length) == $expected and
        ([.data.snapshots[] | select(.repository_id == $full_repository) | .phase] == ["start", "end"]) and
        ([.data.snapshots[] | select(.repository_id == $partial_repository) | .phase] == ["start", "end"]) and
        ($full_repository | test("^repo_[A-Za-z0-9_-]{43}$")) and
        ($partial_repository | test("^repo_[A-Za-z0-9_-]{43}$")) and
        any(.data.snapshots[];
          .repository_id == $full_repository and .label == $full_label and
          .root_display == $full_display) and
        any(.data.snapshots[];
          .repository_id == $partial_repository and .label == $partial_label) and
        all(.data.snapshots[];
          (.captured_at | valid_time) and
          (.content_hash | type) == "string" and
          (.content_hash | test("^[0-9a-f]{64}$")) and
          .truncated == false and
          ([.areas[].area] == ["conflict", "staged", "unstaged"]) and
          all(.areas[];
            ($mode == "written" or (((.patch // "") == "") and .paths == [] and
              .binary_paths == [])) and
            .truncated == false) and
          ($mode == "written" or .untracked == []) and
          .warnings == []) and
        ($mode != "written" or
          ([.data.snapshots[] | select(.phase == "start") |
            (.untracked | any(.path == $marker or (.path | endswith("/" + $marker))))] |
            all(. == false)) and
          ([.data.snapshots[] | select(.phase == "end") |
            (.untracked | any(.path == $marker or (.path | endswith("/" + $marker))))] |
            all(. == true))) and
        (.data.provider_reports | type) == "array" and
        all(.data.provider_reports[];
          .truncated == false and
          ($mode == "written" or (.paths == [] and ((.patch // "") == "")))) and
        .data.provider_reports_truncated == false and
        ((.data.next_provider_cursor // "") == "")
      ' <<<"$RESPONSE" >/dev/null || \
        fail "$label Git start/end snapshots were not exact clean, safe observations"
      return 0
    fi
    sleep 0.1
  done
  fail "$label Git start/end snapshots were not persisted within the bounded wait"
}

capture_git_fingerprint "$GIT_FINGERPRINT_BASELINE" || fail 'initial Git fingerprint could not be captured'
[[ ! -s "$GIT_FINGERPRINT_BASELINE/porcelain-v2-z" && \
  ! -s "$GIT_FINGERPRINT_BASELINE/cached.diff" && \
  ! -s "$GIT_FINGERPRINT_BASELINE/unstaged.diff" ]] || \
  fail 'isolated Git fixture must be clean before runtime smoke'

curl -q --noproxy '*' --connect-timeout 2 --max-time 10 -fsS "$BASE_URL/readyz" >/dev/null || \
  fail 'local runtime is not ready'
api POST /v1/auth/local-session 200 '{}'
CSRF_TOKEN="$(data '.csrf_token')"
[[ "$CSRF_TOKEN" =~ ^[A-Za-z0-9_-]{43}$ ]] || fail 'bootstrap returned an invalid CSRF token'
if ! (umask 077 && printf 'header = "X-CSRF-Token: %s"\n' "$CSRF_TOKEN" \
  >"$CURL_SECRET_CONFIG") || ! chmod 600 "$CURL_SECRET_CONFIG" 2>/dev/null; then
  fail 'CSRF curl configuration could not be secured'
fi
CSRF_TOKEN=""
api GET /v1/computers 200
[[ "$(data '.items | length')" == 1 ]] || fail 'local mode did not expose exactly one computer'
COMPUTER_ID="$(data '.items[0].id')"
api GET "/v1/computers/$COMPUTER_ID/agent-services" 200
SERVICE_ID="$(jq -er --arg provider "$PROVIDER" \
  '.data.items[] | select(.provider == $provider and .status == "ready") | .id' <<<"$RESPONSE")"
MODEL="$(jq -er --arg provider "$PROVIDER" --arg preferred "$PREFERRED_MODEL" '
  [.data.items[] | select(.provider == $provider) | .capabilities.models[]] as $models |
  if $preferred != "" and ($models | index($preferred)) then $preferred
  elif $provider == "opencode" and ($models | index("deepseek/deepseek-v4-flash")) then "deepseek/deepseek-v4-flash"
  else $models[0]
  end
' <<<"$RESPONSE")"

api POST /v1/projects 201 "$(jq -cn --arg computer "$COMPUTER_ID" \
  '{computer_id:$computer,name:"本地 Runtime 验收",description:"真实 Adapter E2E"}')"
PROJECT_ID="$(data '.id')"

api POST "/v1/computers/$COMPUTER_ID/resource-register" 202 \
  "$(jq -cn --arg path "$RESOURCE_PATH" '{path:$path}')"
COMMAND_ID="$(data '.command_id')"
api GET "/v1/computers/$COMPUTER_ID/directory-list/$COMMAND_ID" 200
[[ "$(data '.status')" == completed ]] || fail 'resource registration did not finish synchronously'
REGISTERED_PATH="$(data '.result.path')"
if ! REGISTERED_PATH="$(CDPATH='' cd -- "$REGISTERED_PATH" 2>/dev/null && pwd -P)"; then
  fail 'registered resource path cannot be canonicalized'
fi
[[ "$REGISTERED_PATH" == "$RESOURCE_PATH" ]] || fail 'resource registration changed the selected Git root'
api POST "/v1/projects/$PROJECT_ID/resources" 201 \
  "$(jq -cn --arg path "$REGISTERED_PATH" --arg name "$RESOURCE_DISPLAY" \
    '{kind:"code_directory",name:$name,path:$path}')"
RESOURCE_ID="$(data '.id')"
RESOURCE_VERSION="$(data '.version')"
api POST "/v1/projects/$PROJECT_ID/resources/$RESOURCE_ID/validate" 202 \
  "$(jq -cn --argjson version "$RESOURCE_VERSION" '{expected_version:$version}')"
[[ "$(data '.status')" == ready && "$(data '.git_detected')" == true ]] || \
  fail 'local resource is not a ready Git resource'

for partial_spec in \
  "$PARTIAL_RESOURCE_A|$PARTIAL_RESOURCE_A_DISPLAY|a" \
  "$PARTIAL_RESOURCE_B|$PARTIAL_RESOURCE_B_DISPLAY|b"; do
  IFS='|' read -r partial_path partial_name partial_key <<<"$partial_spec"
  api POST "/v1/computers/$COMPUTER_ID/resource-register" 202 \
    "$(jq -cn --arg path "$partial_path" '{path:$path}')"
  COMMAND_ID="$(data '.command_id')"
  api GET "/v1/computers/$COMPUTER_ID/directory-list/$COMMAND_ID" 200
  [[ "$(data '.status')" == completed && "$(data '.result.path')" == "$partial_path" ]] || \
    fail 'partial resource registration did not preserve its exact root'
  api POST "/v1/projects/$PROJECT_ID/resources" 201 \
    "$(jq -cn --arg path "$partial_path" --arg name "$partial_name" \
      '{kind:"code_directory",name:$name,path:$path}')"
  partial_id="$(data '.id')"
  partial_version="$(data '.version')"
  api POST "/v1/projects/$PROJECT_ID/resources/$partial_id/validate" 202 \
    "$(jq -cn --argjson version "$partial_version" '{expected_version:$version}')"
  [[ "$(data '.status')" == ready && "$(data '.git_detected')" == true ]] || \
    fail 'partial resource is not a ready Git resource'
  if [[ "$partial_key" == a ]]; then
    PARTIAL_RESOURCE_A_ID="$partial_id"
  else
    PARTIAL_RESOURCE_B_ID="$partial_id"
  fi
done

api GET "/v1/projects/$PROJECT_ID/git/repositories?refresh=true" 200
jq -e --arg resource "$RESOURCE_ID" --arg partial_a "$PARTIAL_RESOURCE_A_ID" \
  --arg partial_b "$PARTIAL_RESOURCE_B_ID" --arg label "$RESOURCE_LABEL" \
  --arg root_display "$RESOURCE_DISPLAY" --arg partial_label "$PARTIAL_REPOSITORY_LABEL" '
  .data.truncated == false and (.data.warnings | type) == "array" and
  (.data.warnings | length) == 0 and
  (.data.repositories | type) == "array" and (.data.repositories | length) == 2 and
  any(.data.repositories[];
    .scope == "full" and .discovery_origin == "resource" and
    .label == $label and .root_display == $root_display and
    .resource_ids == [$resource] and (.id | test("^repo_[A-Za-z0-9_-]{43}$"))) and
  any(.data.repositories[];
    .scope == "resource_prefix" and .discovery_origin == "ancestor" and
    .label == $partial_label and
    (.resource_ids | sort) == ([$partial_a, $partial_b] | sort) and
    (.id | test("^repo_[A-Za-z0-9_-]{43}$")))
' <<<"$RESPONSE" >/dev/null || fail 'resources did not discover one full and one exact partial repository'
GIT_REPOSITORY_ID="$(jq -er --arg resource "$RESOURCE_ID" \
  '.data.repositories[] | select(.resource_ids == [$resource]) | .id' <<<"$RESPONSE")"
PARTIAL_GIT_REPOSITORY_ID="$(jq -er --arg partial_a "$PARTIAL_RESOURCE_A_ID" \
  '.data.repositories[] | select(.resource_ids | index($partial_a)) | .id' <<<"$RESPONSE")"

api POST "/v1/computers/$COMPUTER_ID/agents" 201 \
  "$(jq -cn --arg service "$SERVICE_ID" --arg model "$MODEL" \
    --arg marker "$MCP_COMMENT_MARKER" '
      {agent_service_id:$service,model:$model,name:"本地开发助手",skill_version_ids:[],
       system_prompt:(
         "严格按任务要求工作且不要修改代码。Issue Run 必须调用 teamva-issue MCP server 的 issue_comment_create 工具；" +
         "body 必须精确等于固定非秘密 marker `" + $marker + "`，mentioned_agent_ids 必须精确为 []；不得改用其他工具。"
       )}
    ')"
AGENT_ID="$(data '.id')"

api POST "/v1/projects/$PROJECT_ID/issues" 201 \
  "$(jq -cn --arg marker "$MCP_COMMENT_MARKER" '
      {title:"真实本地执行",status:"open",
       description:(
         "读取上下文且不修改代码。必须调用 teamva-issue MCP server 的 issue_comment_create 工具；" +
         "body 必须精确为固定非秘密 marker `" + $marker + "`，mentioned_agent_ids 必须为 []。"
       )}
    ')"
ISSUE_ID="$(data '.id')"
jq -e --arg project "$PROJECT_ID" '
  .data.project_id == $project and (.data | has("git_repository_id") | not) and
  (.data | has("git_repository_scope") | not)
' <<<"$RESPONSE" >/dev/null || fail 'Issue did not use project Workspace semantics'
ISSUE_ACTIVATION_REQUESTED=true
api POST "/v1/issues/$ISSUE_ID/comments" 201 \
  "$(jq -cn --arg agent "$AGENT_ID" '{body:"@本地开发助手 开始工作。",mentioned_agent_ids:[$agent]}')"

for ((attempt = 0; attempt < 180; attempt++)); do
  api GET "/v1/issues/$ISSUE_ID/runs" 200 >/dev/null
  candidate_count="$(jq -er --arg issue "$ISSUE_ID" --arg project "$PROJECT_ID" \
    --arg agent "$AGENT_ID" '
      [.data.items[] | select(
        .issue_id == $issue and .project_id == $project and .agent_id == $agent and
        (. | has("git_repository_id") | not) and
        ((.issue_agent_instance_id // "") | test("^[A-Za-z0-9_-]{1,128}$")))] | length
    ' <<<"$RESPONSE")"
  [[ "$candidate_count" -le 1 ]] || fail 'Issue runs endpoint returned more than one matching activation Run'
  if [[ "$candidate_count" -eq 1 ]]; then
    RUN_ID="$(jq -er --arg issue "$ISSUE_ID" --arg project "$PROJECT_ID" \
      --arg agent "$AGENT_ID" '
        .data.items[] | select(
          .issue_id == $issue and .project_id == $project and .agent_id == $agent and
          (. | has("git_repository_id") | not) and
          ((.issue_agent_instance_id // "") | test("^[A-Za-z0-9_-]{1,128}$"))) | .id
      ' <<<"$RESPONSE")"
    ISSUE_AGENT_INSTANCE_ID="$(jq -er --arg run "$RUN_ID" '
      .data.items[] | select(.id == $run) | .issue_agent_instance_id
    ' <<<"$RESPONSE")"
    break
  fi
  sleep 0.1
done
[[ -n "$RUN_ID" ]] || fail 'Issue runs endpoint did not expose the exact activation Run'

AGENT_COMMENT_ID=""
status=""
for ((attempt = 0; attempt < 300; attempt++)); do
  api GET "/v1/runs/$RUN_ID" 200 >/dev/null
  assert_run_binding "$RUN_ID" "$ISSUE_ID" 'Issue' "$ISSUE_AGENT_INSTANCE_ID"
  status="$(data '.status')"
  assert_no_pending_approvals "$RUN_ID" "$ISSUE_ID" 'Issue Run'
  api GET "/v1/issues/$ISSUE_ID/comments" 200 >/dev/null
  AGENT_COMMENT_ID="$(jq -r --arg run "$RUN_ID" --arg agent "$AGENT_ID" '
    [.data.items[] | select(
      .author_kind == "agent" and .author_agent_id == $agent and .run_id == $run and
      .status == "published")][0].id // ""
  ' <<<"$RESPONSE")"
  case "$status" in
    succeeded)
      [[ -z "$AGENT_COMMENT_ID" ]] || break
      ;;
    failed|cancelled|lost) fail "Issue Run ended as $status" ;;
  esac
  sleep 0.5
done
[[ "$status" == succeeded ]] || fail 'Issue Run did not succeed'
[[ -n "$AGENT_COMMENT_ID" ]] || fail 'Agent did not publish an Issue comment for the exact Run'
jq -e --arg comment "$AGENT_COMMENT_ID" --arg marker "$MCP_COMMENT_MARKER" '
  any(.data.items[]; .id == $comment and .body == $marker)
' <<<"$RESPONSE" >/dev/null || fail 'Agent comment did not contain the exact MCP marker'
wait_for_git_snapshots "$RUN_ID" 'Issue Run'
assert_git_fingerprint_unchanged 'Issue Run' "$GIT_FINGERPRINT_ISSUE"

SCHEDULE_NAME="runtime-smoke-$AGENT_ID"
SCHEDULE_CREATION_REQUESTED=true
api POST "/v1/projects/$PROJECT_ID/scheduled-tasks" 201 \
  "$(jq -cn --arg name "$SCHEDULE_NAME" --arg agent "$AGENT_ID" \
    --arg full "$RESOURCE_PATH" --arg partial_a "$PARTIAL_RESOURCE_A" \
    --arg partial_b "$PARTIAL_RESOURCE_B" --arg parent "$PARTIAL_REPOSITORY_ROOT" \
    --arg marker "$WORKSPACE_WRITE_MARKER" --arg content "$WORKSPACE_WRITE_CONTENT" '
    {name:$name,schedule_kind:"cron",schedule_expression:"0 0 1 1 *",timezone:"UTC",
     prompt:[{type:"text",text:(
       "这是项目 Workspace 写权限验收。只执行以下文件操作，不调用 Issue 工具，不创建其他文件：" +
       "分别在精确绝对目录 `" + $full + "`、`" + $partial_a + "`、`" + $partial_b +
       "` 中创建文件 `" + $marker + "`，文件内容都必须精确为 `" + $content +
       "`（结尾换行可有可无）。禁止在祖先目录 `" + $parent + "` 直接创建该文件。完成后只回复一句执行成功。"
     )}],agent_id:$agent,concurrency_policy:"forbid",misfire_policy:"skip"}')"
TASK_ID="$(data '.id')"
api GET "/v1/scheduled-tasks/$TASK_ID" 200
jq -e --arg project "$PROJECT_ID" '
  .data.project_id == $project and (.data | has("git_repository_id") | not) and
  (.data | has("git_repository_scope") | not)
' <<<"$RESPONSE" >/dev/null || fail 'Schedule did not use project Workspace semantics'
api POST "/v1/scheduled-tasks/$TASK_ID/run-now" 202 '' "schedule-$TASK_ID"
SCHEDULE_RUN_ID="$(data '.run_id')"
[[ -n "$SCHEDULE_RUN_ID" ]] || fail 'schedule trigger did not create a Run'

api GET "/v1/runs/$SCHEDULE_RUN_ID" 200
assert_run_binding "$SCHEDULE_RUN_ID" '' 'Schedule'
status="$(data '.status')"
for ((attempt = 0; attempt < 600; attempt++)); do
  assert_no_pending_approvals "$SCHEDULE_RUN_ID" '' 'Schedule Run'
  case "$status" in
    succeeded) break ;;
    failed|cancelled|lost) fail "Schedule Run ended as $status" ;;
  esac
  sleep 0.5
  api GET "/v1/runs/$SCHEDULE_RUN_ID" 200 >/dev/null
  assert_run_binding "$SCHEDULE_RUN_ID" '' 'Schedule'
  status="$(data '.status')"
done
[[ "$status" == succeeded ]] || fail 'Schedule Run did not succeed'
assert_schedule_workspace_writes
assert_project_workspace_manifest
api GET "/v1/scheduled-tasks/$TASK_ID/runs" 200
jq -e --arg run "$SCHEDULE_RUN_ID" --arg task "$TASK_ID" \
  --arg trigger "manual:schedule-$TASK_ID" '
  ([.data.items[] | select(
    .scheduled_task_id == $task and .trigger_key == $trigger and
    .run_id == $run and .status == "succeeded")] | length) == 1
' <<<"$RESPONSE" >/dev/null || fail 'scheduled task occurrence did not advance for the exact Run'
wait_for_git_snapshots "$SCHEDULE_RUN_ID" 'Schedule Run' written

printf -v SUCCESS_SUMMARY 'Local runtime smoke passed: provider=%s model=%s run=%s' \
  "$PROVIDER" "$MODEL" "$RUN_ID"
