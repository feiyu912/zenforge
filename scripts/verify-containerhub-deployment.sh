#!/usr/bin/env bash

# Verify a deployed Container Hub without embedding an endpoint or credential
# in this repository. --run-session is opt-in because it creates a disposable
# remote sandbox session.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ZENFORGE_CONTAINERHUB_URL=https://hub.example ./scripts/verify-containerhub-deployment.sh [--run-session]

Environment:
  ZENFORGE_CONTAINERHUB_URL               Required Container Hub base URL.
  ZENFORGE_CONTAINERHUB_TOKEN             Optional bearer token.
  ZENFORGE_CONTAINERHUB_ENVIRONMENT       Environment name (default: shell).
  ZENFORGE_CONTAINERHUB_TIMEOUT_SECONDS   Request timeout (default: 45).

Without --run-session, the script only verifies the runtime-info endpoint.
EOF
}

fail() {
  printf 'container hub deployment check failed: %s\n' "$*" >&2
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

run_session=false
case "${1:-}" in
  '') ;;
  --run-session) run_session=true ;;
  --help|-h) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

base_url=${ZENFORGE_CONTAINERHUB_URL:-}
base_url=${base_url%/}
[[ -n "$base_url" ]] || fail "ZENFORGE_CONTAINERHUB_URL is required"

timeout=${ZENFORGE_CONTAINERHUB_TIMEOUT_SECONDS:-45}
[[ "$timeout" =~ ^[1-9][0-9]*$ ]] || fail "ZENFORGE_CONTAINERHUB_TIMEOUT_SECONDS must be a positive integer"

headers=(-H 'Accept: application/json')
if [[ -n ${ZENFORGE_CONTAINERHUB_TOKEN:-} ]]; then
  headers+=(-H "Authorization: Bearer ${ZENFORGE_CONTAINERHUB_TOKEN}")
fi

runtime_info=$(mktemp)
session_id=''
stop_session() {
  curl --fail --silent --show-error --max-time "$timeout" --location \
    "${headers[@]}" \
    -H 'Content-Type: application/json' \
    --data '{}' \
    "$base_url/api/sessions/$session_id/stop" >/dev/null
}

cleanup() {
  if [[ -n "$session_id" ]]; then
    stop_session || \
      printf 'warning: failed to stop canary session %s\n' "$session_id" >&2
  fi
  rm -f "$runtime_info"
}
trap cleanup EXIT

if ! curl --fail --silent --show-error --max-time "$timeout" --location \
  "${headers[@]}" \
  "$base_url/api/runtime-info" >"$runtime_info"; then
  fail "GET $base_url/api/runtime-info was unsuccessful"
fi
if [[ ! -s "$runtime_info" ]]; then
  fail "runtime-info response was empty"
fi
printf 'runtime-info: reachable and non-empty\n'

if [[ "$run_session" != true ]]; then
  printf 'session: skipped (pass --run-session to create a disposable sandbox session)\n'
  exit 0
fi

environment=${ZENFORGE_CONTAINERHUB_ENVIRONMENT:-shell}
nonce="$(date -u +%Y%m%dT%H%M%SZ)-$$"
session_id="zenforge-deploy-${nonce}"
create_payload=$(printf '{"session_id":%s,"environment_name":%s,"cwd":"/workspace","labels":{"source":"zenforge-deployment-canary"},"mounts":[]}' \
  "$(json_string "$session_id")" \
  "$(json_string "$environment")")

if ! curl --fail --silent --show-error --max-time "$timeout" --location \
  "${headers[@]}" \
  -H 'Content-Type: application/json' \
  --data "$create_payload" \
  "$base_url/api/sessions/create" >/dev/null; then
  fail "POST $base_url/api/sessions/create was unsuccessful"
fi

execute_output=$(mktemp)
execute_payload='{"command":"/bin/sh","args":["-lc","printf zenforge-containerhub-ok"],"cwd":"/workspace","timeout_ms":30000}'
if ! curl --fail --silent --show-error --max-time "$timeout" --location \
  "${headers[@]}" \
  -H 'Content-Type: application/json' \
  --data "$execute_payload" \
  "$base_url/api/sessions/$session_id/execute" >"$execute_output"; then
  cat "$execute_output" >&2
  rm -f "$execute_output"
  fail "POST $base_url/api/sessions/$session_id/execute was unsuccessful"
fi
if ! grep -Fq 'zenforge-containerhub-ok' "$execute_output"; then
  cat "$execute_output" >&2
  rm -f "$execute_output"
  fail "session command did not return the expected output"
fi
rm -f "$execute_output"

completed_session_id=$session_id
if ! stop_session; then
  fail "POST $base_url/api/sessions/$session_id/stop was unsuccessful"
fi
session_id=''
printf 'session: create, execute, and cleanup verified (sessionId=%s)\n' "$completed_session_id"
