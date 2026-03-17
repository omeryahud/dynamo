#!/usr/bin/env bash
# Builds and deploys both swap-coordinator and dynamo vllm runtime images.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

"$SCRIPT_DIR/build-swap-coordinator.sh"
"$SCRIPT_DIR/build-dynamo-runtime.sh"
