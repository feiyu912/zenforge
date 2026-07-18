#!/usr/bin/env bash

# Verify a deployed agent-platform endpoint without embedding credentials or a
# deployment address in the ZenForge repository. --run-query is intentionally
# opt-in because it creates a real model-backed run.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ZENFORGE_PLATFORM_BASE_URL=https://platform.example ./scripts/verify-platform-deployment.sh [--run-query]

Environment:
  ZENFORGE_PLATFORM_BASE_URL       Required Platform base URL.
  ZENFORGE_PLATFORM_TOKEN          Optional bearer token.
  ZENFORGE_PLATFORM_AGENT_KEY      Required with --run-query.
  ZENFORGE_PLATFORM_CANARY_MESSAGE Optional query message.
  ZENFORGE_PLATFORM_TIMEOUT_SECONDS Maximum query duration (default: 90).

Without --run-query, the script only verifies the deployed catalog endpoint.
EOF
}

fail() {
  printf 'platform deployment check failed: %s\n' "$*" >&2
  exit 1
}

json_string() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '"%s"' "$value"
}

run_query=false
case "${1:-}" in
  '') ;;
  --run-query) run_query=true ;;
  --help|-h) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

base_url=${ZENFORGE_PLATFORM_BASE_URL:-}
base_url=${base_url%/}
[[ -n "$base_url" ]] || fail "ZENFORGE_PLATFORM_BASE_URL is required"

headers=(-H 'Accept: application/json')
if [[ -n ${ZENFORGE_PLATFORM_TOKEN:-} ]]; then
  headers+=(-H "Authorization: Bearer ${ZENFORGE_PLATFORM_TOKEN}")
fi

catalog=$(mktemp)
stream=''
cleanup() {
  rm -f "$catalog"
  [[ -z "$stream" ]] || rm -f "$stream"
}
trap cleanup EXIT

if ! curl --fail --silent --show-error --location "${headers[@]}" \
  "$base_url/api/agents" >"$catalog"; then
  fail "GET $base_url/api/agents was unsuccessful"
fi
if ! grep -Eq '"code"[[:space:]]*:[[:space:]]*0' "$catalog"; then
  cat "$catalog" >&2
  fail "catalog response did not contain a successful Platform envelope"
fi
printf 'catalog: reachable and successful\n'

if [[ "$run_query" != true ]]; then
  printf 'query: skipped (pass --run-query to create a real ZenForge canary)\n'
  exit 0
fi

agent_key=${ZENFORGE_PLATFORM_AGENT_KEY:-}
[[ -n "$agent_key" ]] || fail "ZENFORGE_PLATFORM_AGENT_KEY is required with --run-query"

timeout=${ZENFORGE_PLATFORM_TIMEOUT_SECONDS:-90}
[[ "$timeout" =~ ^[1-9][0-9]*$ ]] || fail "ZENFORGE_PLATFORM_TIMEOUT_SECONDS must be a positive integer"

nonce="$(date -u +%Y%m%dT%H%M%SZ)-$$"
chat_id="zenforge-deploy-${nonce}"
run_id="zenforge-deploy-${nonce}"
request_id="zenforge-deploy-${nonce}"
message=${ZENFORGE_PLATFORM_CANARY_MESSAGE:-'Reply with exactly: ZenForge deployment canary OK.'}
payload=$(printf '{"message":%s,"agentKey":%s,"chatId":%s,"runId":%s,"requestId":%s}' \
  "$(json_string "$message")" \
  "$(json_string "$agent_key")" \
  "$(json_string "$chat_id")" \
  "$(json_string "$run_id")" \
  "$(json_string "$request_id")")

stream=$(mktemp)
if ! curl --fail --silent --show-error --no-buffer --max-time "$timeout" --location \
  "${headers[@]}" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  --data "$payload" \
  "$base_url/api/query" >"$stream"; then
  cat "$stream" >&2
  fail "POST $base_url/api/query was unsuccessful"
fi

for event in request.query run.start content.delta run.complete; do
  if ! grep -Fq "\"type\":\"$event\"" "$stream"; then
    cat "$stream" >&2
    fail "SSE stream did not contain $event"
  fi
done
if ! grep -Fq 'data: [DONE]' "$stream"; then
  cat "$stream" >&2
  fail "SSE stream did not contain the completion sentinel"
fi

printf 'query: ZenForge SSE lifecycle verified (chatId=%s runId=%s)\n' "$chat_id" "$run_id"
