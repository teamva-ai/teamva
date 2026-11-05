#!/usr/bin/env bash

set -euo pipefail

STATE_FILE="${TEAMVA_LOCAL_STATE_FILE:?set TEAMVA_LOCAL_STATE_FILE to runtime.json}"
RESOURCE_PATH="${TEAMVA_LOCAL_RESOURCE_PATH:?set TEAMVA_LOCAL_RESOURCE_PATH to a disposable test directory}"
PROVIDER="${TEAMVA_LOCAL_PROVIDER:?set TEAMVA_LOCAL_PROVIDER to opencode or codex}"
PREFERRED_MODEL="${TEAMVA_LOCAL_MODEL:-}"
TIMEOUT_SECONDS="${TEAMVA_LOCAL_GOAL_TIMEOUT_SECONDS:-900}"
TOKEN_BUDGET="${TEAMVA_LOCAL_GOAL_TOKEN_BUDGET:-2000000}"
BASE_URL="$(jq -er '.origin' "$STATE_FILE")"
COOKIE_JAR="$(mktemp -t teamva-local-goal-cookie.XXXXXX)"
RESPONSE_FILE="$(mktemp -t teamva-local-goal-response.XXXXXX)"
trap 'rm -f "$COOKIE_JAR" "$RESPONSE_FILE"' EXIT

RESPONSE=""
CSRF_TOKEN=""

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
data() { jq -er ".data$1" <<<"$RESPONSE"; }

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
  local status error_summary
  status="$(curl "${args[@]}")"
  RESPONSE="$(<"$RESPONSE_FILE")"
  if [[ "|$expected|" != *"|$status|"* ]]; then
    error_summary="$(jq -r '(.error.code // "unknown") + ": " + (.error.message // "request failed")' <<<"$RESPONSE" 2>/dev/null || true)"
    fail "$method $path expected $expected, got $status ($error_summary)"
  fi
  printf 'PASS %-6s %-64s %s\n' "$method" "$path" "$status"
}

approve_pending() {
  api GET '/v1/approvals?status=pending' 200 >/dev/null
  while IFS=$'\t' read -r approval_id approval_version digest; do
    [[ -n "$approval_id" ]] || continue
    api POST "/v1/approvals/$approval_id/decisions" 200 \
      "$(jq -cn --argjson version "$approval_version" --arg digest "$digest" \
        '{expected_version:$version,action_digest:$digest,decision:"approved",scope:"once"}')" >/dev/null
  done < <(jq -r '.data.items[] | [.id,.version,.action_digest] | @tsv' <<<"$RESPONSE")
}

[[ "$PROVIDER" == opencode || "$PROVIDER" == codex ]] || fail 'provider must be opencode or codex'
[[ "$TIMEOUT_SECONDS" =~ ^[0-9]+$ && "$TIMEOUT_SECONDS" -ge 60 ]] || fail 'timeout must be at least 60 seconds'
[[ "$TOKEN_BUDGET" =~ ^[0-9]+$ && "$TOKEN_BUDGET" -ge 1000 && "$TOKEN_BUDGET" -le 100000000 ]] \
  || fail 'Goal token budget must be between 1000 and 100000000'
[[ -d "$RESOURCE_PATH" ]] || fail "resource directory does not exist: $RESOURCE_PATH"
for reserved in verify-goal.sh goal-stage-one.txt goal-stage-two.unlock goal-stage-two.txt goal-result.json; do
  [[ ! -e "$RESOURCE_PATH/$reserved" ]] || fail "resource contains reserved test path: $reserved"
done

cat >"$RESOURCE_PATH/verify-goal.sh" <<'VERIFY_SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
provider="${1:?provider is required}"
unlock="$(<goal-stage-two.unlock)"
[[ "$unlock" == "unlock:$provider:"* ]]
[[ "$(<goal-stage-one.txt)" == "stage-one:$provider" ]]
[[ "$(<goal-stage-two.txt)" == "stage-two:$provider" ]]
jq -e --arg provider "$provider" --arg unlock "$unlock" \
  '.provider == $provider and .status == "completed" and .stages == 2 and .unlock == $unlock' \
  goal-result.json >/dev/null
printf 'GOAL_ACCEPTANCE_OK provider=%s\n' "$provider"
VERIFY_SCRIPT
chmod +x "$RESOURCE_PATH/verify-goal.sh"

api POST /v1/auth/local-session 200 '{}'
CSRF_TOKEN="$(data '.csrf_token')"
api GET /v1/computers 200
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
[[ -n "$MODEL" ]] || fail "$PROVIDER did not report an available model"

api POST /v1/projects 201 "$(jq -cn --arg computer "$COMPUTER_ID" --arg provider "$PROVIDER" \
  '{computer_id:$computer,name:("持续目标真实验收-" + $provider),description:"Disposable real-provider goal E2E"}')"
PROJECT_ID="$(data '.id')"
api POST "/v1/computers/$COMPUTER_ID/resource-register" 202 \
  "$(jq -cn --arg path "$RESOURCE_PATH" '{path:$path}')"
COMMAND_ID="$(data '.command_id')"
api GET "/v1/computers/$COMPUTER_ID/directory-list/$COMMAND_ID" 200
[[ "$(data '.status')" == completed ]] || fail 'resource registration did not finish synchronously'
REGISTERED_PATH="$(data '.result.path')"
api POST "/v1/projects/$PROJECT_ID/resources" 201 \
  "$(jq -cn --arg path "$REGISTERED_PATH" '{kind:"code_directory",name:"Goal E2E Workspace",path:$path}')"
RESOURCE_ID="$(data '.id')"
RESOURCE_VERSION="$(data '.version')"
api POST "/v1/projects/$PROJECT_ID/resources/$RESOURCE_ID/validate" 202 \
  "$(jq -cn --argjson version "$RESOURCE_VERSION" '{expected_version:$version}')"
[[ "$(data '.status')" == ready ]] || fail 'isolated Goal resource is not ready'

SYSTEM_PROMPT='你是持续目标真实验收 Agent。必须在当前隔离工作目录实际操作并验证结果，不能只描述计划或伪造结果。严格遵守用户目标中的轮次条件；每个工作轮次结束前必须调用 issue_comment_create 更新进度。如果系统随后明确说明独立审核已经确认完成并要求最终总结，最终评论必须包含 [GOAL_FINAL_COMPLETE]。'
api POST "/v1/computers/$COMPUTER_ID/agents" 201 \
  "$(jq -cn --arg service "$SERVICE_ID" --arg model "$MODEL" --arg prompt "$SYSTEM_PROMPT" \
    '{agent_service_id:$service,model:$model,name:"持续目标验收助手",system_prompt:$prompt,skill_version_ids:[]}')"
AGENT_ID="$(data '.id')"
api POST "/v1/projects/$PROJECT_ID/issues" 201 \
  '{"title":"真实 Provider 持续目标验收","description":"在已注册的隔离代码目录中完成两阶段文件任务，并提供真实验证证据。","status":"open"}'
ISSUE_ID="$(data '.id')"

# A structured mention creates the conversation that the side-sheet Goal API
# later continues. It must not modify the staged Goal artifacts.
api POST "/v1/issues/$ISSUE_ID/comments" 201 \
  "$(jq -cn --arg agent "$AGENT_ID" \
    '{body:"@持续目标验收助手 只确认当前目录可访问，并调用 issue_comment_create 回复 SESSION_READY；不要创建或修改文件。",mentioned_agent_ids:[$agent]}')"

INSTANCE_ID=""
BASELINE_RUN_ID=""
deadline=$((SECONDS + TIMEOUT_SECONDS))
while (( SECONDS < deadline )); do
  approve_pending
  api GET "/v1/issues/$ISSUE_ID/agent-instances" 200 >/dev/null
  INSTANCE_ID="$(jq -r '.data.items[0].id // ""' <<<"$RESPONSE")"
  if [[ -n "$INSTANCE_ID" ]]; then
    api GET "/v1/agent-instances/$INSTANCE_ID/runs" 200 >/dev/null
    BASELINE_RUN_ID="$(jq -r '[.data.items[] | select((.goal_id // "") == "")][0].id // ""' <<<"$RESPONSE")"
    baseline_status="$(jq -r --arg id "$BASELINE_RUN_ID" '.data.items[] | select(.id == $id) | .status' <<<"$RESPONSE")"
    [[ "$baseline_status" != failed && "$baseline_status" != cancelled ]] || fail "session bootstrap run ended as $baseline_status"
    [[ "$baseline_status" != succeeded ]] || break
  fi
  sleep 0.5
done
[[ -n "$BASELINE_RUN_ID" ]] || fail 'session bootstrap did not create a Run'
[[ "$baseline_status" == succeeded ]] || fail 'session bootstrap Run did not succeed'
for forbidden in goal-stage-one.txt goal-stage-two.unlock goal-stage-two.txt goal-result.json; do
  [[ ! -e "$RESOURCE_PATH/$forbidden" ]] || fail "session bootstrap modified Goal artifact: $forbidden"
done

api GET "/v1/issues/$ISSUE_ID" 200
ISSUE_VERSION="$(data '.version')"
OBJECTIVE="$(cat <<EOF
完成一个必须经过两个工作轮次的真实本地文件任务。严格按照当前目录状态执行：

重要控制约束：goal-stage-two.unlock 只能由外部验收控制器在第一轮结束并进入独立审核后创建。你永远不得创建、修改、删除、重命名或 chmod 这个 unlock 文件。缺少 unlock 时第二阶段在事实上不可执行，不能猜测其内容。

1. 如果 goal-stage-two.unlock 不存在，这是第一工作轮次。只能创建 goal-stage-one.txt，内容必须精确为 stage-one:${PROVIDER}。不得创建或修改 goal-stage-two.txt 和 goal-result.json，也不得提前完成第二阶段。验证第一阶段文件后，调用 issue_comment_create 更新进度，并在评论与本轮最终回答中包含 [GOAL_STAGE_ONE_ONLY]，明确说明正在等待外部控制器提供 unlock，目标尚未完成、需要下一轮。
2. 只有进入后续工作轮次且 goal-stage-one.txt 与 goal-stage-two.unlock 都已存在时，才能读取 unlock 的精确内容，创建 goal-stage-two.txt（内容精确为 stage-two:${PROVIDER}）和 goal-result.json（JSON 字段 provider=${PROVIDER}、status=completed、stages=2、unlock=刚读取的完整 unlock 内容）。不得修改 verify-goal.sh 和 unlock 文件。随后执行 ./verify-goal.sh ${PROVIDER}，并确认输出 GOAL_ACCEPTANCE_OK。
3. 第二阶段完成后，调用 issue_comment_create 更新进度，并在本轮最终回答中包含 [GOAL_STAGE_TWO_COMPLETE] 和真实验证结果。
只有三个产物内容正确且验收脚本成功才算完成；不得跳过第一轮的强制停点。
EOF
)"
api POST "/v1/issues/$ISSUE_ID/runs" '200|202' \
  "$(jq -cn --arg instance "$INSTANCE_ID" --arg input "$OBJECTIVE" --argjson budget "$TOKEN_BUDGET" \
    --argjson version "$ISSUE_VERSION" \
    '{issue_agent_instance_id:$instance,input:$input,goal:true,goal_token_budget:$budget,expected_issue_version:$version}')" \
  "goal-real-$PROVIDER"
GOAL_RUN_ID="$(data '.id')"
GOAL_ID="$(data '.goal_id')"
[[ -n "$GOAL_ID" ]] || fail 'Goal Run response did not include goal_id'

observed_initial_audit=false
observed_continuation=false
UNLOCK_VALUE="unlock:${PROVIDER}:$(uuidgen | tr '[:upper:]' '[:lower:]')"
last_state=""
FINAL_GOAL_JSON=""
deadline=$((SECONDS + TIMEOUT_SECONDS))
while (( SECONDS < deadline )); do
  approve_pending
  api GET "/v1/agent-instances/$INSTANCE_ID/goal" 200 >/dev/null
  goal_json="$(jq -c '.data.goal' <<<"$RESPONSE")"
  [[ "$goal_json" != null ]] || { sleep 0.25; continue; }
  status="$(jq -r '.status' <<<"$goal_json")"
  phase="$(jq -r '.phase' <<<"$goal_json")"
  continuations="$(jq -r '.continuations_used' <<<"$goal_json")"
  state="$status/$phase/continuations=$continuations"
  if [[ "$state" != "$last_state" ]]; then
    printf 'GOAL %-10s %s\n' "$PROVIDER" "$state"
    last_state="$state"
  fi
  if [[ "$phase" == evaluating && "$continuations" == 0 ]]; then
    [[ -f "$RESOURCE_PATH/goal-stage-one.txt" ]] || fail 'first work Run reached audit without stage-one artifact'
    [[ ! -e "$RESOURCE_PATH/goal-stage-two.txt" ]] || fail 'first work Run illegally created stage-two artifact'
    [[ ! -e "$RESOURCE_PATH/goal-result.json" ]] || fail 'first work Run illegally created final result'
    if [[ "$observed_initial_audit" == false ]]; then
      [[ ! -e "$RESOURCE_PATH/goal-stage-two.unlock" ]] || fail 'first work Run illegally created the controller unlock'
      printf '%s\n' "$UNLOCK_VALUE" >"$RESOURCE_PATH/goal-stage-two.unlock"
      chmod 444 "$RESOURCE_PATH/goal-stage-two.unlock"
      observed_initial_audit=true
    else
      [[ "$(<"$RESOURCE_PATH/goal-stage-two.unlock")" == "$UNLOCK_VALUE" ]] || fail 'controller unlock changed during audit'
    fi
  fi
  (( continuations > 0 )) && observed_continuation=true
  case "$status" in
    completed)
      FINAL_GOAL_JSON="$goal_json"
      break
      ;;
    blocked|budget_reached|cancelled|paused)
      reason="$(jq -r '.status_reason // .note // "unknown"' <<<"$goal_json")"
      fail "Goal stopped as $status: $reason"
      ;;
  esac
  sleep 0.25
done
[[ -n "$FINAL_GOAL_JSON" ]] || fail 'Goal did not complete before timeout'
[[ "$observed_initial_audit" == true ]] || fail 'did not observe the first isolated audit before continuation'
[[ "$observed_continuation" == true ]] || fail 'Goal never triggered a continuation Run'

api GET "/v1/issues/$ISSUE_ID/runs" 200 >/dev/null
GOAL_RUNS_JSON="$(jq -c --arg goal "$GOAL_ID" '[.data.items[] | select(.goal_id == $goal)]' <<<"$RESPONSE")"
work_count="$(jq '[.[] | select(.purpose == "work")] | length' <<<"$GOAL_RUNS_JSON")"
continuation_count="$(jq '[.[] | select(.purpose == "goal_continuation")] | length' <<<"$GOAL_RUNS_JSON")"
audit_count="$(jq '[.[] | select(.purpose == "goal_audit")] | length' <<<"$GOAL_RUNS_JSON")"
finalize_count="$(jq '[.[] | select(.purpose == "goal_finalize")] | length' <<<"$GOAL_RUNS_JSON")"
[[ "$work_count" == 1 ]] || fail "expected one initial Goal work Run, got $work_count"
(( continuation_count >= 1 )) || fail 'expected at least one continuation Run'
(( audit_count >= 2 )) || fail 'expected at least two independent audit Runs'
[[ "$finalize_count" == 1 ]] || fail "expected one finalize Run, got $finalize_count"
jq -e 'all(.[]; .status == "succeeded")' <<<"$GOAL_RUNS_JSON" >/dev/null || fail 'not every Goal Run succeeded'
jq -e 'all(.[] | select(.purpose == "goal_audit"); .read_only == true)' <<<"$GOAL_RUNS_JSON" >/dev/null \
  || fail 'an independent audit Run was not read-only'

non_audit_tokens=0
audit_tokens=0
first_output=""
continuation_output=""
while IFS=$'\t' read -r run_id purpose; do
  api GET "/v1/runs/$run_id/events" 200 >/dev/null
  run_usage="$(jq '[.data.items[] | select(.event_type == "usage.updated") | (.payload.total_tokens // 0 | floor)] | add // 0' <<<"$RESPONSE")"
  output="$(jq -jr '[.data.items[] | select(.event_type == "output.delta") | (.payload.text // "")] | join("")' <<<"$RESPONSE")"
  if [[ "$purpose" == goal_audit ]]; then
    audit_tokens=$((audit_tokens + run_usage))
  else
    non_audit_tokens=$((non_audit_tokens + run_usage))
  fi
  [[ "$purpose" != work ]] || first_output+="$output"
  [[ "$purpose" != goal_continuation ]] || continuation_output+="$output"
done < <(jq -r '.[] | [.id,.purpose] | @tsv' <<<"$GOAL_RUNS_JSON")

goal_tokens="$(jq -r '.tokens_used' <<<"$FINAL_GOAL_JSON")"
(( goal_tokens > 0 )) || fail 'Goal did not record real Provider token usage'
[[ "$goal_tokens" == "$non_audit_tokens" ]] || fail "Goal tokens $goal_tokens did not match non-audit usage $non_audit_tokens"
(( audit_tokens > 0 )) || fail 'independent audit Runs did not report real Provider token usage'
[[ "$first_output" == *'[GOAL_STAGE_ONE_ONLY]'* ]] || fail 'first work Run did not emit the stage-one marker'
[[ "$continuation_output" == *'[GOAL_STAGE_TWO_COMPLETE]'* ]] || fail 'continuation Run did not emit the completion marker'

[[ "$(<"$RESOURCE_PATH/goal-stage-one.txt")" == "stage-one:$PROVIDER" ]] || fail 'stage-one artifact content mismatch'
[[ "$(<"$RESOURCE_PATH/goal-stage-two.unlock")" == "$UNLOCK_VALUE" ]] || fail 'controller unlock was modified'
[[ "$(<"$RESOURCE_PATH/goal-stage-two.txt")" == "stage-two:$PROVIDER" ]] || fail 'stage-two artifact content mismatch'
jq -e --arg provider "$PROVIDER" --arg unlock "$UNLOCK_VALUE" \
  '.provider == $provider and .status == "completed" and .stages == 2 and .unlock == $unlock' \
  "$RESOURCE_PATH/goal-result.json" >/dev/null || fail 'goal-result.json failed acceptance'
(cd "$RESOURCE_PATH" && ./verify-goal.sh "$PROVIDER") >/dev/null || fail 'final artifact verifier failed'

SUMMARY_COMMENT_ID="$(jq -r '.summary_comment_id' <<<"$FINAL_GOAL_JSON")"
api GET "/v1/issues/$ISSUE_ID/comments" 200 >/dev/null
summary_status="$(jq -r --arg id "$SUMMARY_COMMENT_ID" '.data.items[] | select(.id == $id) | .status' <<<"$RESPONSE")"
summary_body="$(jq -r --arg id "$SUMMARY_COMMENT_ID" '.data.items[] | select(.id == $id) | .body' <<<"$RESPONSE")"
[[ "$summary_status" == published ]] || fail "final Goal summary status is $summary_status"
[[ "$summary_body" == *'[GOAL_FINAL_COMPLETE]'* ]] || fail 'final published Goal summary is missing its completion marker'

printf 'Real Goal runtime smoke passed: provider=%s model=%s goal=%s initial_run=%s continuations=%s tokens=%s audit_tokens=%s\n' \
  "$PROVIDER" "$MODEL" "$GOAL_ID" "$GOAL_RUN_ID" "$continuation_count" "$goal_tokens" "$audit_tokens"
