#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/common.sh"

DYNAMO_IMAGE="vllm-runtime:1.0.1-swap"

echo "==> Building dynamo vllm runtime image..."
cd "$REPO_ROOT"
python3 container/render.py --framework vllm --target runtime --cuda-version 13.0 --output-short-filename
docker build -t "$DYNAMO_IMAGE" -f container/rendered.Dockerfile .

build_save_deploy "$DYNAMO_IMAGE" "dynamo-vllm-runtime.tar"
