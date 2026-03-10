#!/usr/bin/env bash
set -euo pipefail

DYNAMO_IMAGE="dynamo:latest-vllm-runtime"
SC_IMAGE="swap-coordinator:latest"
EXPORT_DIR="$HOME/dynamo"
SERVERS=("gpu-node-1" "gpu-node-2" "gpu-node-3" "gpu-node-4")
NFS_MOUNT="/mnt/dynamo"
MAX_RETRIES=5
RETRY_DELAY=3

REPO_ROOT=~/go/src/github.com/omeryahud/dynamo

# Import an image on a remote server, retrying until it verifies present.
# Args: $1=server, $2=tar filename, $3=expected image ref (e.g. docker.io/library/swap-coordinator:latest)
import_on_server() {
    local server="$1"
    local tar_name="$2"
    local expected_ref="$3"
    local nfs_path="$NFS_MOUNT/$tar_name"
    local log_prefix="[$server][$tar_name]"

    for attempt in $(seq 1 "$MAX_RETRIES"); do
        echo "$log_prefix Attempt $attempt/$MAX_RETRIES: importing..."
        ssh "$server" "sudo ctr --namespace k8s.io images import $nfs_path" 2>&1 || true

        # Verify the image is actually present
        if ssh "$server" "sudo ctr --namespace k8s.io images ls -q 2>/dev/null | grep -qF '$expected_ref'"; then
            echo "$log_prefix Verified image present."
            return 0
        fi

        echo "$log_prefix Image not found after import, retrying in ${RETRY_DELAY}s..." >&2
        sleep "$RETRY_DELAY"
    done

    echo "$log_prefix FAILED: image not present after $MAX_RETRIES attempts" >&2
    return 1
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

SC_REF="docker.io/library/$SC_IMAGE"
pids=()
for server in "${SERVERS[@]}"; do
    import_on_server "$server" "swap-coordinator.tar" "$SC_REF" &
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

DYNAMO_REF="docker.io/library/$DYNAMO_IMAGE"
pids=()
for server in "${SERVERS[@]}"; do
    import_on_server "$server" "dynamo-vllm-runtime.tar" "$DYNAMO_REF" &
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
