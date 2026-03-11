#!/usr/bin/env bash
# Sends requests to qwen3-2 in a loop simulating multi-turn conversations.
# Each conversation: random 100-char initial prompt, then 5 follow-ups appending 50 chars each.
set -euo pipefail

LOCAL_PORT=8001
MODEL="Qwen/Qwen3-0.6B"
SVC="svc/qwen3-2-frontend"
NS="swap"
PF_PID=""

cleanup() { kill $PF_PID 2>/dev/null; }
trap cleanup EXIT

ensure_port_forward() {
  if [ -n "$PF_PID" ] && kill -0 "$PF_PID" 2>/dev/null; then
    return
  fi
  echo "[qwen3-2] Starting port-forward $SVC -> localhost:$LOCAL_PORT..."
  kubectl port-forward -n "$NS" "$SVC" "$LOCAL_PORT":8000 &>/dev/null &
  PF_PID=$!
  sleep 2
}

URL="http://localhost:$LOCAL_PORT/v1/chat/completions"
i=0
FOLLOWUPS=5

rand_str() { head -c "$1" /dev/urandom | base64 | tr -dc 'a-zA-Z0-9 ' | head -c "$1"; }

while true; do
  ensure_port_forward
  prompt="$(rand_str 100)"

  for turn in $(seq 0 "$FOLLOWUPS"); do
    if [ "$turn" -gt 0 ]; then
      prompt="${prompt} $(rand_str 50)"
    fi
    i=$((i + 1))
    echo -n "[qwen3-2] req #$turn (${#prompt} chars): "
    curl -s --max-time 30 "$URL" \
      -H 'Content-Type: application/json' \
      -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"$prompt\"}],\"max_tokens\":10}" \
      | python3 -c "import sys,json; r=json.load(sys.stdin); print(r['choices'][0]['message']['content'][:60])" 2>/dev/null \
      || echo "FAILED"
  done
  echo "[qwen3-2] Conversation done, starting new one..."
done
