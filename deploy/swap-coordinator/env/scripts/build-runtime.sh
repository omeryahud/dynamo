#!/usr/bin/env bash
set -euo pipefail

DYNAMO_IMAGE="dynamo:latest-vllm-runtime"
SC_IMAGE="swap-coordinator:latest"
EXPORT_DIR="$HOME/dynamo"
SERVERS=("gpu-node-1" "gpu-node-2" "gpu-node-3" "gpu-node-4")
NFS_MOUNT="/mnt/dynamo"

REPO_ROOT=~/go/src/github.com/omeryahud/dynamo

import_on_server() {
    local server="$1"
    local tar_name="$2"
    local nfs_path="$NFS_MOUNT/$tar_name"
    local log_prefix="[$server][$tar_name]"

    echo "$log_prefix Importing from NFS mount..."
    ssh "$server" "sudo ctr --namespace k8s.io images import $nfs_path"

    echo "$log_prefix Done."
}

mkdir -p "$EXPORT_DIR"

# --- Build and deploy swap-coordinator image ---
echo "==> Building swap-coordinator image..."
cd "$REPO_ROOT/deploy/swap-coordinator"
docker build -t "$SC_IMAGE" -f Dockerfile .

SC_TAR="$EXPORT_DIR/swap-coordinator.tar"
echo "==> Saving $SC_IMAGE to $SC_TAR..."
docker save "$SC_IMAGE" -o "$SC_TAR"
echo "==> Save complete ($(du -sh "$SC_TAR" | cut -f1))"

pids=()
for server in "${SERVERS[@]}"; do
    import_on_server "$server" "swap-coordinator.tar" &
    pids+=($!)
done

failed=0
for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
        echo "ERROR: Deployment to ${SERVERS[$i]} failed" >&2
        failed=1
    fi
done

if [ "$failed" -ne 0 ]; then
    echo "==> swap-coordinator deployment failed, aborting."
    exit $failed
fi
echo "==> swap-coordinator deployed to all nodes."

# --- Build and deploy dynamo vllm runtime image ---
echo "==> Building dynamo vllm runtime image..."
cd "$REPO_ROOT"
python3 container/render.py --framework vllm --target runtime --cuda-version 13.0 --output-short-filename
docker build -t "$DYNAMO_IMAGE" -f container/rendered.Dockerfile .

DYNAMO_TAR="$EXPORT_DIR/dynamo-vllm-runtime.tar"
echo "==> Saving $DYNAMO_IMAGE to $DYNAMO_TAR..."
docker save "$DYNAMO_IMAGE" -o "$DYNAMO_TAR"
echo "==> Save complete ($(du -sh "$DYNAMO_TAR" | cut -f1))"

pids=()
for server in "${SERVERS[@]}"; do
    import_on_server "$server" "dynamo-vllm-runtime.tar" &
    pids+=($!)
done

failed=0
for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
        echo "ERROR: Deployment to ${SERVERS[$i]} failed" >&2
        failed=1
    fi
done

if [ "$failed" -eq 0 ]; then
    echo "==> All images successfully deployed to all nodes."
fi

exit $failed
