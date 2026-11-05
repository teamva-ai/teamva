#!/usr/bin/env bash

set -euo pipefail

STATE_FILE="${TEAMVA_LOCAL_STATE_FILE:?set TEAMVA_LOCAL_STATE_FILE to runtime.json}"
[[ -s "$STATE_FILE" ]] || { printf 'runtime state not found: %s\n' "$STATE_FILE" >&2; exit 1; }
BASE_URL="$(jq -er '.origin' "$STATE_FILE")"
COOKIE_JAR="$(mktemp -t teamva-local-cookie.XXXXXX)"
RESPONSE_FILE="$(mktemp -t teamva-local-response.XXXXXX)"
trap 'rm -f "$COOKIE_JAR" "$RESPONSE_FILE"' EXIT

RESPONSE=""
CSRF_TOKEN=""

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

api() {
  local method="$1" path="$2" expected="$3" body="${4:-}" idempotency="${5:-}"
  local -a args=(-sS -o "$RESPONSE_FILE" -w '%{http_code}' -X "$method" \
    -b "$COOKIE_JAR" -c "$COOKIE_JAR" -H 'Accept: application/json' \
    -H "Origin: $BASE_URL" "$BASE_URL$path")
  [[ -z "$body" ]] || args+=(-H 'Content-Type: application/json' --data "$body")
  if [[ -n "$CSRF_TOKEN" && "$method" != GET && "$method" != HEAD ]]; then
    args+=(-H "X-CSRF-Token: $CSRF_TOKEN")
  fi
  [[ -z "$idempotency" ]] || args+=(-H "Idempotency-Key: $idempotency")
  local status
  status="$(curl "${args[@]}")"
  RESPONSE="$(<"$RESPONSE_FILE")"
  [[ "|$expected|" == *"|$status|"* ]] || fail "$method $path expected $expected, got $status: $RESPONSE"
  printf 'PASS %-6s %-68s %s\n' "$method" "$path" "$status"
}

data() { jq -er ".data$1" <<<"$RESPONSE"; }

curl -fsS "$BASE_URL/readyz" | jq -e '.status == "ok"' >/dev/null
api POST /v1/auth/local-session 200 '{}'
CSRF_TOKEN="$(data '.csrf_token')"
api GET /v1/system/capabilities 200
[[ "$(data '.mode')" == local ]] || fail 'runtime mode is not local'
api GET /v1/computers 200
COMPUTER_ID="$(data '.items[0].id')"
[[ "$(data '.items | length')" == 1 ]] || fail 'local mode did not provision exactly one computer'

api GET /v1/me/preferences 200
PREF_VERSION="$(data '.version')"
api PATCH /v1/me/preferences 200 "$(jq -cn --argjson version "$PREF_VERSION" \
  '{expected_version:$version,theme:"dark",allow_write:true}')"
api PATCH /v1/me/preferences 409 "$(jq -cn --argjson version "$PREF_VERSION" \
  '{expected_version:$version,theme:"light"}')"

api POST /v1/projects 201 "$(jq -cn --arg computer "$COMPUTER_ID" \
  '{computer_id:$computer,name:"本地 API 验收",description:"localhost SQLite"}')"
PROJECT_ID="$(data '.id')"
PROJECT_VERSION="$(data '.version')"
api PATCH "/v1/projects/$PROJECT_ID" 200 "$(jq -cn --argjson version "$PROJECT_VERSION" \
  '{expected_version:$version,name:"本地 API 验收（更新）",description:"updated"}')"

api POST "/v1/projects/$PROJECT_ID/resources" 201 \
  '{"kind":"url","name":"文档","url":"https://example.test/docs"}'
RESOURCE_ID="$(data '.id')"
RESOURCE_VERSION="$(data '.version')"
api PATCH "/v1/projects/$PROJECT_ID/resources/$RESOURCE_ID" 200 \
  "$(jq -cn --argjson version "$RESOURCE_VERSION" '{expected_version:$version,name:"文档（更新）"}')"
RESOURCE_VERSION="$(data '.version')"

api POST "/v1/projects/$PROJECT_ID/issues" 201 \
  '{"title":"本地任务","description":"验证评论与修订","status":"open","worktree_hint":false}'
ISSUE_ID="$(data '.id')"
ISSUE_VERSION="$(data '.version')"
api POST "/v1/issues/$ISSUE_ID/comments" 201 '{"body":"第一条评论","mentioned_agent_ids":[]}'
COMMENT_ID="$(data '.id')"
COMMENT_VERSION="$(data '.version')"
api PATCH "/v1/comments/$COMMENT_ID" 200 \
  "$(jq -cn --argjson version "$COMMENT_VERSION" '{expected_version:$version,body:"修正后的评论"}')"
api GET "/v1/comments/$COMMENT_ID/revisions" 200
[[ "$(data '.items | length')" == 1 ]] || fail 'comment revision was not recorded'
api PATCH "/v1/issues/$ISSUE_ID" 200 \
  "$(jq -cn --argjson version "$ISSUE_VERSION" '{expected_version:$version,status:"resolved"}')"

api POST "/v1/projects/$PROJECT_ID/memories" 201 \
  '{"key":"architecture","kind":"fact","title":"架构","content":"本地优先","source_type":"user"}'
MEMORY_ID="$(data '.id')"
MEMORY_VERSION="$(data '.version')"
api PATCH "/v1/projects/$PROJECT_ID/memories/$MEMORY_ID" 200 \
  "$(jq -cn --argjson version "$MEMORY_VERSION" '{expected_version:$version,title:"架构",content:"本地优先，HTTP 边界"}')"
MEMORY_VERSION="$(data '.version')"
api POST "/v1/projects/$PROJECT_ID/memories/$MEMORY_ID/archive" 204 \
  "$(jq -cn --argjson version "$MEMORY_VERSION" '{expected_version:$version}')"
api POST "/v1/projects/$PROJECT_ID/memories/$MEMORY_ID/restore" 200 \
  "$(jq -cn --argjson version "$((MEMORY_VERSION + 1))" '{expected_version:$version}')"
api GET "/v1/projects/$PROJECT_ID/memories/$MEMORY_ID/versions" 200

api POST "/v1/projects/$PROJECT_ID/policy-rules" 201 \
  '{"action_kind":"shell","effect":"require_approval","conditions":{}}'
RULE_ID="$(data '.id')"
RULE_VERSION="$(data '.version')"
api PATCH "/v1/policy-rules/$RULE_ID" 200 \
  "$(jq -cn --argjson version "$RULE_VERSION" '{expected_version:$version,effect:"allow",enabled:true}')"
RULE_VERSION="$(data '.version')"
api DELETE "/v1/policy-rules/$RULE_ID?expected_version=$RULE_VERSION" 204

api POST /v1/scheduled-tasks/preview 200 \
  '{"schedule_kind":"cron","schedule_expression":"0 9 * * 1-5","timezone":"Asia/Shanghai","count":3}'
[[ "$(data '.times | length')" == 3 ]] || fail 'schedule preview count mismatch'

api GET "/v1/projects/$PROJECT_ID/issues" 200
api GET "/v1/projects/$PROJECT_ID/resources" 200
api GET "/v1/projects/$PROJECT_ID/memories" 200
api GET "/v1/audit-events?project_id=$PROJECT_ID" 200

api DELETE "/v1/projects/$PROJECT_ID/resources/$RESOURCE_ID?expected_version=$RESOURCE_VERSION" 204
printf 'Local API smoke passed: %s\n' "$BASE_URL"
