#!/usr/bin/env bash
# Sends requests to qwen3-1 in a loop
# Maintains port-forwarding automatically
set -euo pipefail

LOCAL_PORT=8000
MODEL="Qwen/Qwen3-0.6B"
SVC="svc/qwen3-1-frontend"
NS="swap"
PF_PID=""

cleanup() { kill $PF_PID 2>/dev/null; }
trap cleanup EXIT

ensure_port_forward() {
  if [ -n "$PF_PID" ] && kill -0 "$PF_PID" 2>/dev/null; then
    return
  fi
  echo "[qwen3-1] Starting port-forward $SVC -> localhost:$LOCAL_PORT..."
  kubectl port-forward -n "$NS" "$SVC" "$LOCAL_PORT":8000 &>/dev/null &
  PF_PID=$!
  sleep 2
}

URL="http://localhost:$LOCAL_PORT/v1/chat/completions"
i=0

while true; do
  ensure_port_forward
  i=$((i + 1))
  echo -n "[qwen3-1] Request #$i: "
  curl -s --max-time 30 "$URL" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Count to $i\"}],\"max_tokens\":10}" \
    | python3 -c "import sys,json; r=json.load(sys.stdin); print(r['choices'][0]['message']['content'][:60])" 2>/dev/null \
    || echo "FAILED"
done
