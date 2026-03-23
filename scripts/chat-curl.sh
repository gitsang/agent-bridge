#!/usr/bin/env bash

set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8192}"
SESSION_ID="${SESSION_ID:-}"

if [[ $# -lt 1 ]]; then
  printf 'Usage: %s <message> [session_id]\n' "$0"
  printf 'Example: %s "hello world"\n' "$0"
  printf 'Example: %s "hello world" ses_xxx\n' "$0"
  exit 1
fi

MESSAGE="$1"
if [[ $# -ge 2 ]]; then
  SESSION_ID="$2"
fi

PAYLOAD=$(jq -n \
  --arg model "opencode-connect" \
  --arg message "$MESSAGE" \
  --arg sessionID "$SESSION_ID" \
  '{model: $model, messages: [{role: "user", content: $message}]} + (if $sessionID == "" then {} else {user: $sessionID} end)')

curl -sS -X POST "${BASE_URL}/chat/completions" \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD"
