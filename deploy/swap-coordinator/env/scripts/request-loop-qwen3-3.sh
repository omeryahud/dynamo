#!/usr/bin/env bash
# Sends requests to qwen3-3 in a loop simulating multi-turn conversations.
# Each conversation: random 100-char initial prompt, then 5 follow-ups appending 50 chars each.
set -euo pipefail

MODEL="Qwen/Qwen3-0.6B"
URL="http://ec2-3-238-76-242.compute-1.amazonaws.com:30703/v1/chat/completions"
FOLLOWUPS=20

rand_str() { head -c "$1" /dev/urandom | base64 | tr -dc 'a-zA-Z0-9 ' | head -c "$1"; }

while true; do
  prompt="$(rand_str 100)"

  for turn in $(seq 0 "$FOLLOWUPS"); do
    if [ "$turn" -gt 0 ]; then
      prompt="${prompt} $(rand_str 50)"
    fi
    echo -n "[qwen3-3] req #$turn (${#prompt} chars): "
    curl -s --max-time 30 "$URL" \
      -H 'Content-Type: application/json' \
      -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"$prompt\"}],\"max_tokens\":10}" \
      | python3 -c "import sys,json; r=json.load(sys.stdin); print(r['choices'][0]['message']['content'][:60])" 2>/dev/null \
      || echo "FAILED"
  done
  echo "[qwen3-3] Conversation done, starting new one..."
done
