#!/usr/bin/env bash
# Shared constants and helpers for build/deploy scripts.
#
# Node IPs/hostnames can be supplied in three ways (highest priority first):
#   1. Positional arguments:  ./build-swap-coordinator.sh 10.0.0.1 10.0.0.2
#   2. Environment variable:  NODES="10.0.0.1,10.0.0.2" ./build-swap-coordinator.sh
#   3. Default list below.

EXPORT_DIR="$HOME/dynamo"

if [ "$#" -gt 0 ]; then
    SERVERS=("$@")
elif [ -n "${NODES:-}" ]; then
    IFS=',' read -ra SERVERS <<< "$NODES"
else
    echo "ERROR: No target nodes specified." >&2
    echo "Provide nodes as arguments or via the NODES env var (comma-separated)." >&2
    exit 1
fi

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

# Build, save, and deploy an image to all GPU nodes in parallel.
# Args: $1=image tag, $2=tar filename
build_save_deploy() {
    local image="$1"
    local tar_name="$2"
    local tar_path="$EXPORT_DIR/$tar_name"
    local ref="docker.io/library/$image"

    mkdir -p "$EXPORT_DIR"

    echo "==> Saving $image to $tar_path..."
    docker save "$image" -o "$tar_path"
    echo "==> Save complete ($(du -sh "$tar_path" | cut -f1))"

    local pids=()
    for server in "${SERVERS[@]}"; do
        import_on_server "$server" "$tar_name" "$ref" &
        pids+=($!)
    done

    local failed=0
    for i in "${!pids[@]}"; do
        if ! wait "${pids[$i]}"; then
            echo "ERROR: Deployment to ${SERVERS[$i]} failed" >&2
            failed=1
        fi
    done

    if [ "$failed" -ne 0 ]; then
        echo "==> Deployment of $image failed."
        exit $failed
    fi
    echo "==> $image deployed to all nodes."
}
