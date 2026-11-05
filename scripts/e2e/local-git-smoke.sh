#!/usr/bin/env bash

set -euo pipefail

STATE_FILE="${TEAMVA_LOCAL_STATE_FILE:-}"

RESPONSE=""
CSRF_TOKEN=""
FIXTURE_ROOT=""
FIXTURE_REAL_ROOT=""
SSE_PID=""
PARALLEL_DIR=""
SECRET_MARKER="OUTSIDE_SYMLINK_CONTENT_MUST_NOT_LEAK"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -n "$STATE_FILE" && -s "$STATE_FILE" ]] || fail 'runtime state is unavailable'

for required_command in curl git jq mktemp tr; do
  command -v "$required_command" >/dev/null 2>&1 || fail "missing command: $required_command"
done

BASE_URL="$(jq -er '.origin' "$STATE_FILE")"
case "$BASE_URL" in
  http://127.0.0.1:*|http://localhost:*|http://\[::1\]:*) ;;
  *) fail 'runtime origin is not loopback HTTP' ;;
esac

COOKIE_JAR="$(mktemp -t teamva-local-git-cookie.XXXXXX)"
RESPONSE_FILE="$(mktemp -t teamva-local-git-response.XXXXXX)"
HEADER_FILE="$(mktemp -t teamva-local-git-headers.XXXXXX)"
SSE_FILE="$(mktemp -t teamva-local-git-sse.XXXXXX)"
SSE_HEADER_FILE="$(mktemp -t teamva-local-git-sse-headers.XXXXXX)"
SSE_ERROR_FILE="$(mktemp -t teamva-local-git-sse-error.XXXXXX)"

stop_sse() {
  if [[ -n "$SSE_PID" ]]; then
    kill "$SSE_PID" 2>/dev/null || true
    wait "$SSE_PID" 2>/dev/null || true
    SSE_PID=""
  fi
}

cleanup() {
  stop_sse
  rm -f -- "$COOKIE_JAR" "$RESPONSE_FILE" "$HEADER_FILE" \
    "$SSE_FILE" "$SSE_HEADER_FILE" "$SSE_ERROR_FILE"
  if [[ -n "$FIXTURE_ROOT" && -d "$FIXTURE_ROOT" ]]; then
    rm -rf -- "$FIXTURE_ROOT"
  fi
  if [[ -n "$PARALLEL_DIR" && -d "$PARALLEL_DIR" ]]; then
    rm -rf -- "$PARALLEL_DIR"
  fi
}
trap cleanup EXIT INT TERM

data() {
  jq -er ".data$1" <<<"$RESPONSE"
}

error_code() {
  jq -r '.error.code // ""' <<<"$RESPONSE"
}

urlencode() {
  jq -rn --arg value "$1" '$value | @uri'
}

response_header_from() {
  local file="$1" wanted="$2" line name value found=""
  wanted="$(printf '%s' "$wanted" | tr '[:upper:]' '[:lower:]')"
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" == *:* ]] || continue
    name="${line%%:*}"
    name="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
    [[ "$name" == "$wanted" ]] || continue
    value="${line#*:}"
    value="${value#"${value%%[![:space:]]*}"}"
    found="$value"
  done <"$file"
  printf '%s' "$found"
}

http_status_from_headers() {
  local file="$1" line rest status=""
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" == HTTP/* ]] || continue
    rest="${line#* }"
    status="${rest%% *}"
  done <"$file"
  printf '%s' "$status"
}

assert_no_store_header() {
  local file="$1" cache_control
  cache_control="$(response_header_from "$file" 'cache-control')"
  cache_control="$(printf '%s' "$cache_control" | tr '[:upper:]' '[:lower:]')"
  [[ "$cache_control" == *no-store* ]] || fail 'Git response is missing Cache-Control: no-store'
}

api() {
  local method="$1" path="$2" expected="$3" body="${4:-}" idempotency="${5:-}"
  local -a args=(-sS -o "$RESPONSE_FILE" -D "$HEADER_FILE" -w '%{http_code}' -X "$method"
    -b "$COOKIE_JAR" -c "$COOKIE_JAR" -H 'Accept: application/json'
    -H "Origin: $BASE_URL" "$BASE_URL$path")
  : >"$RESPONSE_FILE"
  : >"$HEADER_FILE"
  [[ -z "$body" ]] || args+=(-H 'Content-Type: application/json' --data "$body")
  if [[ -n "$CSRF_TOKEN" && "$method" != GET && "$method" != HEAD ]]; then
    args+=(-H "X-CSRF-Token: $CSRF_TOKEN")
  fi
  [[ -z "$idempotency" ]] || args+=(-H "Idempotency-Key: $idempotency")
  local status
  status="$(curl "${args[@]}")"
  RESPONSE="$(<"$RESPONSE_FILE")"
  if [[ "|$expected|" != *"|$status|"* ]]; then
    local safe_error_code=""
    if jq -e . >/dev/null 2>&1 <<<"$RESPONSE"; then
      safe_error_code="$(jq -r '.error.code // ""' <<<"$RESPONSE")"
    fi
    if [[ -n "$safe_error_code" ]]; then
      fail "$method $path returned unexpected HTTP status $status ($safe_error_code)"
    fi
    fail "$method $path returned unexpected HTTP status $status"
  fi
  if [[ -n "$RESPONSE" ]]; then
    jq -e . >/dev/null <<<"$RESPONSE" || fail "$method $path returned invalid JSON"
  fi
  printf 'PASS %-6s %-68s %s\n' "$method" "$path" "$status"
}

assert_git_payload_safe() {
  local payload="$1"
  if jq -e --arg root "$FIXTURE_ROOT" --arg real_root "$FIXTURE_REAL_ROOT" \
    --arg marker "$SECRET_MARKER" \
    '[.. | strings] | any(.[]; contains($root) or contains($real_root) or contains($marker))' \
    >/dev/null <<<"$payload"; then
    fail 'Git HTTP response leaked a fixture path or external symlink content'
  fi
}

git_api() {
  local method="$1"
  api "$@"
  assert_git_payload_safe "$RESPONSE"
  if [[ "$method" == GET || "$method" == HEAD ]]; then
    assert_no_store_header "$HEADER_FILE"
  fi
}

expect_error_code() {
  local expected="$1"
  [[ "$(error_code)" == "$expected" ]] || fail "expected error code $expected"
}

curl -fsS "$BASE_URL/readyz" | jq -e '.status == "ok"' >/dev/null
api POST /v1/auth/local-session 200 '{}'
CSRF_TOKEN="$(data '.csrf_token')"
api GET /v1/system/capabilities 200
[[ "$(data '.mode')" == local ]] || fail 'runtime mode is not local'
api GET /v1/computers 200
[[ "$(data '.items | length')" == 1 ]] || fail 'local mode did not provision exactly one computer'
COMPUTER_ID="$(data '.items[0].id')"

FIXTURE_ROOT="$(mktemp -d -t teamva-local-git-fixture.XXXXXX)"
FIXTURE_REAL_ROOT="$(cd "$FIXTURE_ROOT" && pwd -P)"
MONO_ROOT="$FIXTURE_ROOT/mono"
SCOPE_ROOT="$MONO_ROOT/scope"
NESTED_ROOT="$SCOPE_ROOT/depth-one/nested"
SECOND_ROOT="$FIXTURE_ROOT/second"
NO_HOOKS_ROOT="$FIXTURE_ROOT/no-hooks"
EXTERNAL_FILE="$FIXTURE_ROOT/outside-symlink-target.txt"

mkdir -p "$NESTED_ROOT" "$SECOND_ROOT" "$NO_HOOKS_ROOT" "$SCOPE_ROOT/lazy"
export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_MERGE_AUTOEDIT=no

init_repository() {
  local directory="$1"
  git -C "$directory" init -q --template=
  git -C "$directory" symbolic-ref HEAD refs/heads/main
  git -C "$directory" config user.name 'Teamva Git Smoke'
  git -C "$directory" config user.email 'git-smoke@teamva.invalid'
  git -C "$directory" config commit.gpgsign false
  git -C "$directory" config core.hooksPath "$NO_HOOKS_ROOT"
}

init_repository "$MONO_ROOT"
printf 'scope/depth-one/nested/\n' >"$MONO_ROOT/.gitignore"
printf 'outside base\n' >"$MONO_ROOT/outside.txt"
printf 'partial base\n' >"$SCOPE_ROOT/partial.txt"
printf 'staged base\n' >"$SCOPE_ROOT/staged-only.txt"
printf 'unstaged base\n' >"$SCOPE_ROOT/unstaged-only.txt"
printf 'event base\n' >"$SCOPE_ROOT/event.txt"
printf 'conflict a base\n' >"$SCOPE_ROOT/conflict-a.txt"
printf 'conflict b base\n' >"$SCOPE_ROOT/conflict-b.txt"
git -C "$MONO_ROOT" add -- .
git -C "$MONO_ROOT" commit -q -m 'initial parent fixture'
git -C "$MONO_ROOT" checkout -q -b conflict-side
printf 'conflict a side\n' >"$SCOPE_ROOT/conflict-a.txt"
printf 'conflict b side\n' >"$SCOPE_ROOT/conflict-b.txt"
git -C "$MONO_ROOT" add -- scope/conflict-a.txt scope/conflict-b.txt
git -C "$MONO_ROOT" commit -q -m 'side conflicts'
git -C "$MONO_ROOT" checkout -q main
printf 'conflict a main\n' >"$SCOPE_ROOT/conflict-a.txt"
printf 'conflict b main\n' >"$SCOPE_ROOT/conflict-b.txt"
git -C "$MONO_ROOT" add -- scope/conflict-a.txt scope/conflict-b.txt
git -C "$MONO_ROOT" commit -q -m 'main conflicts'
if git -C "$MONO_ROOT" merge --no-edit conflict-side >/dev/null 2>&1; then
  fail 'fixture merge unexpectedly completed without conflicts'
fi

printf 'partial staged\n' >"$SCOPE_ROOT/partial.txt"
git -C "$MONO_ROOT" add -- scope/partial.txt
printf 'partial worktree\n' >>"$SCOPE_ROOT/partial.txt"
printf 'staged changed\n' >"$SCOPE_ROOT/staged-only.txt"
git -C "$MONO_ROOT" add -- scope/staged-only.txt
printf 'unstaged changed\n' >"$SCOPE_ROOT/unstaged-only.txt"
printf 'outside must remain hidden\n' >"$MONO_ROOT/outside.txt"
printf 'lazy a\n' >"$SCOPE_ROOT/lazy/a.txt"
printf 'lazy b\n' >"$SCOPE_ROOT/lazy/b.txt"
for ((line_number = 1; line_number <= 720; line_number++)); do
  printf 'large line %04d\n' "$line_number"
done >"$SCOPE_ROOT/large.txt"
printf '%s\n' "$SECRET_MARKER" >"$EXTERNAL_FILE"
ln -s "$EXTERNAL_FILE" "$SCOPE_ROOT/outside-link"

init_repository "$NESTED_ROOT"
printf 'nested base\n' >"$NESTED_ROOT/nested.txt"
git -C "$NESTED_ROOT" add -- nested.txt
git -C "$NESTED_ROOT" commit -q -m 'initial nested fixture'
printf 'nested changed\n' >"$NESTED_ROOT/nested.txt"

init_repository "$SECOND_ROOT"
printf 'second base\n' >"$SECOND_ROOT/second.txt"
git -C "$SECOND_ROOT" add -- second.txt
git -C "$SECOND_ROOT" commit -q -m 'initial second fixture'
printf 'second changed\n' >"$SECOND_ROOT/second.txt"

api POST /v1/projects 201 "$(jq -cn --arg computer "$COMPUTER_ID" \
  '{computer_id:$computer,name:"本地 Git 验收",description:"真实 Git fixture，不调用 Provider"}')"
PROJECT_ID="$(data '.id')"

register_code_resource() {
  local path="$1" name="$2"
  api POST "/v1/computers/$COMPUTER_ID/resource-register" 202 \
    "$(jq -cn --arg path "$path" '{path:$path}')"
  local command_id registered_path
  command_id="$(data '.command_id')"
  api GET "/v1/computers/$COMPUTER_ID/directory-list/$command_id" 200
  [[ "$(data '.status')" == completed ]] || fail 'resource registration did not finish synchronously'
  registered_path="$(data '.result.path')"
  api POST "/v1/projects/$PROJECT_ID/resources" 201 \
    "$(jq -cn --arg path "$registered_path" --arg name "$name" \
      '{kind:"code_directory",name:$name,path:$path}')"
  REGISTERED_RESOURCE_ID="$(data '.id')"
  REGISTERED_RESOURCE_VERSION="$(data '.version')"
  api POST "/v1/projects/$PROJECT_ID/resources/$REGISTERED_RESOURCE_ID/validate" 202 \
    "$(jq -cn --argjson version "$REGISTERED_RESOURCE_VERSION" '{expected_version:$version}')"
  [[ "$(data '.status')" == ready ]] || fail 'code resource is not ready'
  [[ "$(data '.git_detected')" == true ]] || fail 'code resource Git detection failed'
  REGISTERED_RESOURCE_VERSION="$(data '.version')"
}

REGISTERED_RESOURCE_ID=""
REGISTERED_RESOURCE_VERSION=""
register_code_resource "$SCOPE_ROOT" 'Scoped'
SCOPE_RESOURCE_ID="$REGISTERED_RESOURCE_ID"
register_code_resource "$SECOND_ROOT" 'Second'
SECOND_RESOURCE_ID="$REGISTERED_RESOURCE_ID"

api GET "/v1/projects/$PROJECT_ID/resources" 200
jq -e --arg scope "$SCOPE_RESOURCE_ID" --arg second "$SECOND_RESOURCE_ID" '
  (.data.items | length) == 2 and
  ([.data.items[] | select(.id == $scope and .name == "Scoped")] | length) == 1 and
  ([.data.items[] | select(.id == $second and .name == "Second")] | length) == 1
' >/dev/null <<<"$RESPONSE" || fail 'project registered anything other than scope and second resources'

git_api GET "/v1/projects/$PROJECT_ID/git/repositories?refresh=true" 200
REPOSITORIES_RESPONSE="$RESPONSE"
jq -e --arg scope "$SCOPE_RESOURCE_ID" --arg second "$SECOND_RESOURCE_ID" '
  (.data.snapshot_id | type == "string" and length > 0) and
  (.data.truncated == false) and
  ((.data.repositories | length) == 3) and
  ([.data.repositories[] | select(
    .scope == "resource_prefix" and .root_display == "Scoped" and
    .discovery_origin == "ancestor" and
    ((.resource_ids | length) == 1) and .resource_ids[0] == $scope
  )] | length) == 1 and
  ([.data.repositories[] | select(
    .scope == "full" and .root_display == "Scoped/depth-one/nested" and
    .discovery_origin == "nested" and
    ((.resource_ids | length) == 1) and .resource_ids[0] == $scope
  )] | length) == 1 and
  ([.data.repositories[] | select(
    .scope == "full" and .root_display == "Second" and
    .discovery_origin == "resource" and
    ((.resource_ids | length) == 1) and .resource_ids[0] == $second
  )] | length) == 1
' >/dev/null <<<"$RESPONSE" || fail 'depth-two repository discovery or repository scopes are incorrect'

PARENT_ID="$(jq -er '.data.repositories[] | select(.scope == "resource_prefix") | .id' \
  <<<"$REPOSITORIES_RESPONSE")"
NESTED_ID="$(jq -er --arg resource "$SCOPE_RESOURCE_ID" \
  '.data.repositories[] | select(.scope == "full" and (.resource_ids | index($resource))) | .id' \
  <<<"$REPOSITORIES_RESPONSE")"
SECOND_ID="$(jq -er --arg resource "$SECOND_RESOURCE_ID" \
  '.data.repositories[] | select(.scope == "full" and (.resource_ids | index($resource))) | .id' \
  <<<"$REPOSITORIES_RESPONSE")"
PARENT_GENERATION="$(jq -er --arg id "$PARENT_ID" \
  '.data.repositories[] | select(.id == $id) | .generation' <<<"$REPOSITORIES_RESPONSE")"
jq -e --arg id "$PARENT_ID" '
  .data.repositories[] | select(.id == $id) |
  .operation.kind == "merge" and .counts.conflict >= 2 and
  .counts.staged >= 2 and .counts.unstaged >= 2 and .counts.untracked >= 3
' >/dev/null <<<"$REPOSITORIES_RESPONSE" || fail 'parent repository summary missed fixture changes or merge state'

check_area_pagination() {
  local area="$1" cursor encoded_cursor first_id snapshot generation
  git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=$area&limit=1" 200
  jq -e --arg area "$area" --arg repository "$PARENT_ID" '
    .data.repository_id == $repository and .data.area == $area and
    ((.data.items | length) == 1) and .data.items[0].area == $area and
    (((.data.next_cursor // "") | length) > 0)
  ' >/dev/null <<<"$RESPONSE" || fail "$area first page is not paginated"
  first_id="$(data '.items[0].id')"
  snapshot="$(data '.snapshot_id')"
  generation="$(data '.repository_generation')"
  cursor="$(jq -er '.data.next_cursor' <<<"$RESPONSE")"
  [[ "$cursor" != *scope/* && "$cursor" != *outside.txt* ]] || fail "$area cursor is not opaque"
  encoded_cursor="$(urlencode "$cursor")"
  git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=$area&limit=1&cursor=$encoded_cursor" 200
  jq -e --arg area "$area" --arg repository "$PARENT_ID" --arg first "$first_id" \
    --arg snapshot "$snapshot" --argjson generation "$generation" '
    .data.repository_id == $repository and .data.area == $area and
    .data.snapshot_id == $snapshot and .data.repository_generation == $generation and
    ((.data.items | length) == 1) and .data.items[0].area == $area and
    .data.items[0].id != $first
  ' >/dev/null <<<"$RESPONSE" || fail "$area second page is inconsistent"
}

# Start all four status/numstat reads together, retain every first-page cursor,
# and only consume them after all reads have completed. Pure reads in one area
# must not invalidate a snapshot created concurrently by another area.
PARALLEL_DIR="$(mktemp -d -t teamva-local-git-parallel.XXXXXX)"
PARALLEL_AREAS=(conflict staged unstaged untracked)
PARALLEL_PIDS=()
for area in "${PARALLEL_AREAS[@]}"; do
  (
    output_file="$PARALLEL_DIR/$area.json"
    header_file="$PARALLEL_DIR/$area.headers"
    status_file="$PARALLEL_DIR/$area.status"
    status="$(curl -sS -o "$output_file" -D "$header_file" -w '%{http_code}' \
      -X GET -b "$COOKIE_JAR" -H 'Accept: application/json' -H "Origin: $BASE_URL" \
      "$BASE_URL/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=$area&limit=1")"
    printf '%s' "$status" >"$status_file"
  ) &
  PARALLEL_PIDS+=("$!")
done
for pid in "${PARALLEL_PIDS[@]}"; do
  wait "$pid" || fail 'parallel Git area request failed'
done
for area in "${PARALLEL_AREAS[@]}"; do
  parallel_status="$(<"$PARALLEL_DIR/$area.status")"
  [[ "$parallel_status" == 200 ]] || fail "$area parallel first page returned HTTP $parallel_status"
  parallel_response="$(<"$PARALLEL_DIR/$area.json")"
  jq -e --arg area "$area" --arg repository "$PARENT_ID" '
    .data.repository_id == $repository and .data.area == $area and
    ((.data.items | length) == 1) and (((.data.next_cursor // "") | length) > 0)
  ' >/dev/null <<<"$parallel_response" || fail "$area parallel first page is not paginated"
  assert_git_payload_safe "$parallel_response"
  assert_no_store_header "$PARALLEL_DIR/$area.headers"
done
for area in "${PARALLEL_AREAS[@]}"; do
  parallel_response="$(<"$PARALLEL_DIR/$area.json")"
  parallel_cursor="$(jq -er '.data.next_cursor' <<<"$parallel_response")"
  parallel_snapshot="$(jq -er '.data.snapshot_id' <<<"$parallel_response")"
  parallel_generation="$(jq -er '.data.repository_generation' <<<"$parallel_response")"
  encoded_parallel_cursor="$(urlencode "$parallel_cursor")"
  git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=$area&limit=1&cursor=$encoded_parallel_cursor" 200
  jq -e --arg snapshot "$parallel_snapshot" --argjson generation "$parallel_generation" '
    .data.snapshot_id == $snapshot and .data.repository_generation == $generation and
    ((.data.items | length) == 1)
  ' >/dev/null <<<"$RESPONSE" || fail "$area concurrent cursor changed after cross-area reads"
done

for area in conflict staged unstaged untracked; do
  check_area_pagination "$area"
done

load_area() {
  local area="$1"
  git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=$area&limit=200" 200
  jq -e --arg area "$area" --arg repository "$PARENT_ID" '
    .data.repository_id == $repository and .data.area == $area and
    ((.data.items | length) >= 2) and
    all(.data.items[]; .area == $area and (.display_path | startswith("scope/")))
  ' >/dev/null <<<"$RESPONSE" || fail "$area area crossed the registered parent prefix"
  AREA_RESPONSE="$RESPONSE"
}

AREA_RESPONSE=""
load_area conflict
CONFLICT_RESPONSE="$AREA_RESPONSE"
load_area staged
STAGED_RESPONSE="$AREA_RESPONSE"
load_area unstaged
UNSTAGED_RESPONSE="$AREA_RESPONSE"
load_area untracked
UNTRACKED_RESPONSE="$AREA_RESPONSE"

# Retain a cursor across several pure Diff reads below. This specifically
# guards against Darwin NOTE_ATTRIB events caused by source-file access time.
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=untracked&limit=1" 200
PURE_DIFF_CURSOR="$(data '.next_cursor')"
PURE_DIFF_SNAPSHOT="$(data '.snapshot_id')"
PURE_DIFF_GENERATION="$(data '.repository_generation')"
[[ -n "$PURE_DIFF_CURSOR" ]] || fail 'pure Diff cursor fixture is not paginated'

jq -e '
  ([.data.items[] | select(.display_path == "scope/conflict-a.txt" or .display_path == "scope/conflict-b.txt")] | length) == 2 and
  all(.data.items[]; ((.conflict_code // "") | length) > 0 and (.conflict | type) == "object")
' >/dev/null <<<"$CONFLICT_RESPONSE" || fail 'real conflict entries were not exposed in the conflict area'
jq -e '[.data.items[] | select(.display_path == "scope/partial.txt")] | length == 1' \
  >/dev/null <<<"$STAGED_RESPONSE" || fail 'partially staged file is missing from staged area'
jq -e '[.data.items[] | select(.display_path == "scope/partial.txt")] | length == 1' \
  >/dev/null <<<"$UNSTAGED_RESPONSE" || fail 'partially staged file is missing from unstaged area'
for scoped_payload in "$CONFLICT_RESPONSE" "$STAGED_RESPONSE" "$UNSTAGED_RESPONSE" "$UNTRACKED_RESPONSE"; do
  [[ "$scoped_payload" != *outside.txt* ]] || fail 'parent change outside the registered scope was exposed'
done

LAZY_TOKEN="$(jq -er '.data.items[] | select(.display_path == "scope/lazy" and .kind == "directory") | .path.token' \
  <<<"$UNTRACKED_RESPONSE")"
[[ -n "$LAZY_TOKEN" && "$LAZY_TOKEN" != 'scope/lazy' && "$LAZY_TOKEN" != *scope/lazy* ]] || \
  fail 'untracked directory prefix is not opaque'
ENCODED_LAZY_TOKEN="$(urlencode "$LAZY_TOKEN")"
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=untracked&prefix=$ENCODED_LAZY_TOKEN&expand_untracked=true&limit=200" 200
jq -e '
  ([.data.items[] | select(.display_path == "scope/lazy/a.txt")] | length) == 1 and
  ([.data.items[] | select(.display_path == "scope/lazy/b.txt")] | length) == 1 and
  all(.data.items[]; .area == "untracked" and (.display_path | startswith("scope/lazy/")))
' >/dev/null <<<"$RESPONSE" || fail 'opaque untracked prefix did not expand to its files'

SYMLINK_CHANGE_ID="$(jq -er '.data.items[] | select(.display_path == "scope/outside-link" and .kind == "symlink") | .id' \
  <<<"$UNTRACKED_RESPONSE")"
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes/$SYMLINK_CHANGE_ID/diff" 200
jq -e --arg repository "$PARENT_ID" --arg change "$SYMLINK_CHANGE_ID" '
  .data.repository_id == $repository and .data.change_id == $change and
  .data.kind == "symlink" and .data.comparison == "empty_to_worktree" and
  ((.data.patch // "") == "") and .data.confirm_required == false and
  .data.message_code == "untracked_symlink_summary"
' >/dev/null <<<"$RESPONSE" || fail 'untracked symlink was followed or rendered as file content'

LARGE_CHANGE_ID="$(jq -er '.data.items[] | select(.display_path == "scope/large.txt" and .kind == "file") | .id' \
  <<<"$UNTRACKED_RESPONSE")"
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes/$LARGE_CHANGE_ID/diff" 200
jq -e --arg repository "$PARENT_ID" --arg change "$LARGE_CHANGE_ID" '
  .data.repository_id == $repository and .data.change_id == $change and
  .data.stats.additions > 700 and .data.stats.lines > 700 and
  .data.confirm_required == true and ((.data.patch // "") == "") and
  .data.message_code == "diff_line_confirmation_required"
' >/dev/null <<<"$RESPONSE" || fail 'large diff did not require confirmation with an empty patch'
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes/$LARGE_CHANGE_ID/diff?allow_large=true" 200
jq -e --arg repository "$PARENT_ID" --arg change "$LARGE_CHANGE_ID" '
  .data.repository_id == $repository and .data.change_id == $change and
  .data.stats.additions > 700 and .data.confirm_required == false and
  ((.data.patch // "") | length) > 0
' >/dev/null <<<"$RESPONSE" || fail 'confirmed large diff did not return a patch'
ENCODED_PURE_DIFF_CURSOR="$(urlencode "$PURE_DIFF_CURSOR")"
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=untracked&limit=1&cursor=$ENCODED_PURE_DIFF_CURSOR" 200
jq -e --arg snapshot "$PURE_DIFF_SNAPSHOT" --argjson generation "$PURE_DIFF_GENERATION" '
  .data.snapshot_id == $snapshot and .data.repository_generation == $generation and
  ((.data.items | length) == 1)
' >/dev/null <<<"$RESPONSE" || fail 'pure Diff reads invalidated an unchanged cursor'

api GET "/v1/computers/$COMPUTER_ID/agent-services" 200
SERVICE_ID="$(jq -er '.data.items[0].id' <<<"$RESPONSE")"
api POST "/v1/computers/$COMPUTER_ID/agents" 201 \
  "$(jq -cn --arg service "$SERVICE_ID" \
    '{agent_service_id:$service,name:"Git Workspace smoke only",system_prompt:"Do not run.",skill_version_ids:[]}')"
AGENT_ID="$(data '.id')"

api POST "/v1/projects/$PROJECT_ID/issues" 400 \
  "$(jq -cn --arg repository "$PARENT_ID" '{title:"legacy repository issue",git_repository_id:$repository}')"
expect_error_code invalid_argument
api POST "/v1/projects/$PROJECT_ID/issues" 201 '{"title":"project workspace issue"}'
WORKSPACE_ISSUE_ID="$(data '.id')"
jq -e --arg project "$PROJECT_ID" '
  .data.project_id == $project and .data.title == "project workspace issue" and
  (.data | has("git_repository_id") | not) and
  (.data | has("git_repository_scope") | not)
' >/dev/null <<<"$RESPONSE" || fail 'Issue did not use project Workspace semantics'
api GET "/v1/issues/$WORKSPACE_ISSUE_ID" 200
jq -e --arg issue "$WORKSPACE_ISSUE_ID" '
  .data.id == $issue and (.data | has("git_repository_id") | not) and
  (.data | has("git_repository_scope") | not)
' >/dev/null <<<"$RESPONSE" || fail 'Issue response exposed a legacy repository binding'

SCHEDULE_BASE="$(jq -cn --arg agent "$AGENT_ID" '
  {name:"project workspace schedule",schedule_kind:"cron",
   schedule_expression:"0 0 1 1 *",timezone:"UTC",
   prompt:[{type:"text",text:"This schedule must not run during smoke validation."}],
   agent_id:$agent,concurrency_policy:"forbid",misfire_policy:"skip"}
')"
api POST "/v1/projects/$PROJECT_ID/scheduled-tasks" 400 \
  "$(jq -c --arg repository "$PARENT_ID" '. + {name:"legacy repository schedule",git_repository_id:$repository}' \
    <<<"$SCHEDULE_BASE")"
expect_error_code invalid_argument
api POST "/v1/projects/$PROJECT_ID/scheduled-tasks" 201 "$SCHEDULE_BASE"
WORKSPACE_SCHEDULE_ID="$(data '.id')"
jq -e --arg project "$PROJECT_ID" '
  .data.project_id == $project and (.data | has("git_repository_id") | not) and
  (.data | has("git_repository_scope") | not)
' >/dev/null <<<"$RESPONSE" || fail 'Schedule did not use project Workspace semantics'
api GET "/v1/scheduled-tasks/$WORKSPACE_SCHEDULE_ID" 200
jq -e --arg task "$WORKSPACE_SCHEDULE_ID" '
  .data.id == $task and (.data | has("git_repository_id") | not) and
  (.data | has("git_repository_scope") | not)
' >/dev/null <<<"$RESPONSE" || fail 'Schedule response exposed a legacy repository binding'

extract_sse_event() {
  local repository="$1" line payload
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" == data:* ]] || continue
    payload="${line#data:}"
    payload="${payload# }"
    if jq -e --arg repository "$repository" '.repository_id == $repository' \
      >/dev/null 2>&1 <<<"$payload"; then
      printf '%s' "$payload"
      return 0
    fi
  done <"$SSE_FILE"
  return 1
}

: >"$SSE_FILE"
: >"$SSE_HEADER_FILE"
: >"$SSE_ERROR_FILE"
curl -sS -N --max-time 20 -D "$SSE_HEADER_FILE" -o "$SSE_FILE" \
  -b "$COOKIE_JAR" -H 'Accept: text/event-stream' -H "Origin: $BASE_URL" \
  "$BASE_URL/v1/projects/$PROJECT_ID/git/events" 2>"$SSE_ERROR_FILE" &
SSE_PID="$!"
SSE_READY=false
for ((attempt = 0; attempt < 200; attempt++)); do
  if [[ "$(http_status_from_headers "$SSE_HEADER_FILE")" == 200 ]]; then
    SSE_READY=true
    break
  fi
  kill -0 "$SSE_PID" 2>/dev/null || break
  sleep 0.05
done
[[ "$SSE_READY" == true ]] || fail 'Git SSE stream did not open'
assert_no_store_header "$SSE_HEADER_FILE"
SSE_CONTENT_TYPE="$(response_header_from "$SSE_HEADER_FILE" 'content-type')"
[[ "$SSE_CONTENT_TYPE" == text/event-stream* ]] || fail 'Git SSE content type is incorrect'

git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=untracked&limit=1" 200
PRE_MUTATION_CURSOR="$(data '.next_cursor')"
[[ -n "$PRE_MUTATION_CURSOR" ]] || fail 'pre-mutation cursor fixture is not paginated'
printf 'event changed\n' >"$SCOPE_ROOT/event.txt"
EVENT_JSON=""
for ((attempt = 0; attempt < 200; attempt++)); do
  EVENT_JSON="$(extract_sse_event "$PARENT_ID" || true)"
  [[ -z "$EVENT_JSON" ]] || break
  kill -0 "$SSE_PID" 2>/dev/null || break
  sleep 0.05
done
[[ -n "$EVENT_JSON" ]] || fail 'Git SSE stream did not emit the parent generation event'
stop_sse
SSE_PAYLOAD="$(<"$SSE_FILE")"
[[ "$SSE_PAYLOAD" == *event:git.changed* || "$SSE_PAYLOAD" == *'event: git.changed'* ]] || \
  fail 'Git SSE event type is missing'
if [[ "$SSE_PAYLOAD" == *"$FIXTURE_ROOT"* || "$SSE_PAYLOAD" == *"$FIXTURE_REAL_ROOT"* || \
  "$SSE_PAYLOAD" == *"$SECRET_MARKER"* ]]; then
  fail 'Git SSE response leaked a fixture path or external symlink content'
fi
jq -e --arg repository "$PARENT_ID" --argjson previous "$PARENT_GENERATION" '
  .repository_id == $repository and (.generation | type) == "number" and
  .generation > $previous and (.reason | type) == "string" and (.reason | length) > 0 and
  ((keys | sort) == ["generation","reason","repository_id"])
' >/dev/null <<<"$EVENT_JSON" || fail 'Git SSE event included paths or an invalid generation'
EVENT_GENERATION="$(jq -er '.generation' <<<"$EVENT_JSON")"
ENCODED_PRE_MUTATION_CURSOR="$(urlencode "$PRE_MUTATION_CURSOR")"
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=untracked&limit=1&cursor=$ENCODED_PRE_MUTATION_CURSOR" 400
expect_error_code invalid_cursor

git_api GET "/v1/projects/$PROJECT_ID/git/repositories?refresh=true" 200
POST_EVENT_REPOSITORIES="$RESPONSE"
POST_EVENT_PARENT_GENERATION="$(jq -er --arg id "$PARENT_ID" \
  '.data.repositories[] | select(.id == $id) | .generation' <<<"$POST_EVENT_REPOSITORIES")"
[[ "$POST_EVENT_PARENT_GENERATION" -gt "$EVENT_GENERATION" ]] || fail 'refresh did not advance the parent generation'
jq -e --arg parent "$PARENT_ID" --arg nested "$NESTED_ID" --arg second "$SECOND_ID" '
  ([.data.repositories[].id] | sort) == ([$parent,$nested,$second] | sort)
' >/dev/null <<<"$POST_EVENT_REPOSITORIES" || fail 'refresh changed stable repository IDs'
git_api GET "/v1/projects/$PROJECT_ID/git/repositories/$PARENT_ID/changes?area=unstaged&limit=200" 200
jq -e '
  ([.data.items[] | select(.display_path == "scope/event.txt")] | length) == 1 and
  ([.data.items[] | select(.display_path == "outside.txt")] | length) == 0
' >/dev/null <<<"$RESPONSE" || fail 'refresh did not reconcile the post-SSE change within scope'

RETIRED_NESTED_ROOT="$FIXTURE_ROOT/retired-nested"
mv "$NESTED_ROOT" "$RETIRED_NESTED_ROOT"
mkdir -p "$NESTED_ROOT"
init_repository "$NESTED_ROOT"
printf 'rebuilt nested\n' >"$NESTED_ROOT/nested.txt"
git -C "$NESTED_ROOT" add -- nested.txt
git -C "$NESTED_ROOT" commit -q -m 'rebuilt nested fixture'

git_api GET "/v1/projects/$PROJECT_ID/git/repositories?refresh=true" 200
REBUILT_REPOSITORIES="$RESPONSE"
NEW_NESTED_ID="$(jq -er --arg resource "$SCOPE_RESOURCE_ID" \
  '.data.repositories[] | select(.scope == "full" and (.resource_ids | index($resource))) | .id' \
  <<<"$REBUILT_REPOSITORIES")"
[[ "$NEW_NESTED_ID" != "$NESTED_ID" ]] || fail 'rebuilt nested repository retained its old stable ID'
jq -e --arg parent "$PARENT_ID" --arg second "$SECOND_ID" --arg old "$NESTED_ID" --arg new "$NEW_NESTED_ID" '
  ((.data.repositories | length) == 3) and
  ([.data.repositories[].id] | index($parent)) != null and
  ([.data.repositories[].id] | index($second)) != null and
  ([.data.repositories[].id] | index($old)) == null and
  ([.data.repositories[].id] | index($new)) != null
' >/dev/null <<<"$REBUILT_REPOSITORIES" || fail 'repository identities after nested rebuild are incorrect'
api GET "/v1/issues/$WORKSPACE_ISSUE_ID" 200
jq -e '(.data | has("git_repository_id") | not) and (.data | has("git_repository_scope") | not)' \
  >/dev/null <<<"$RESPONSE" || fail 'repository rebuild introduced a product binding'
api GET "/v1/scheduled-tasks/$WORKSPACE_SCHEDULE_ID" 200
jq -e '(.data | has("git_repository_id") | not) and (.data | has("git_repository_scope") | not)' \
  >/dev/null <<<"$RESPONSE" || fail 'repository rebuild introduced a Schedule binding'

printf 'Local Git smoke passed: real fixture, Git HTTP/SSE, project Workspace contracts; no Run or Provider invoked.\n'
