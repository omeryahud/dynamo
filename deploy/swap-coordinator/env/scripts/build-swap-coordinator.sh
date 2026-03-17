#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/common.sh"

SC_IMAGE="swap-coordinator:latest"

echo "==> Building swap-coordinator image..."
cd "$REPO_ROOT/deploy/swap-coordinator"
docker build -t "$SC_IMAGE" -f Dockerfile .

build_save_deploy "$SC_IMAGE" "swap-coordinator.tar"
