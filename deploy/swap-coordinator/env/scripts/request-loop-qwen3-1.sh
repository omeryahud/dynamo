#!/usr/bin/env bash
# Sends requests to qwen3-1 (port 8000) in a loop
set -euo pipefail

URL="http://localhost:8000/v1/chat/completions"
MODEL="Qwen/Qwen3-0.6B"
i=0

while true; do
  i=$((i + 1))
  echo -n "[qwen3-1] Request #$i: "
  curl -s --max-time 30 "$URL" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Count to $i\"}],\"max_tokens\":10}" \
    | python3 -c "import sys,json; r=json.load(sys.stdin); print(r['choices'][0]['message']['content'][:60])" 2>/dev/null \
    || echo "FAILED"
  sleep 1
done
