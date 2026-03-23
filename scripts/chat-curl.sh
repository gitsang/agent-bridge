#!/usr/bin/env bash

set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8192}"
SESSION_ID="${SESSION_ID:-1}"

if [[ $# -lt 1 ]]; then
  printf 'Usage: %s <message> [session_id]\n' "$0"
  printf 'Example: %s "hello world" 1\n' "$0"
  exit 1
fi

MESSAGE="$1"
if [[ $# -ge 2 ]]; then
  SESSION_ID="$2"
fi

curl -sS -X POST "${BASE_URL}/chat/completions" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"opencode-connect\",\"messages\":[{\"role\":\"user\",\"content\":$(printf '%s' "$MESSAGE" | jq -Rs .)}],\"user\":$(printf '%s' "$SESSION_ID" | jq -Rs .)}"
