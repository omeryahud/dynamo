 #!/usr/bin/env bash
 set -euo pipefail

 IMAGE="dynamo:latest-vllm-runtime"
 EXPORT_DIR="$HOME/dynamo"
 EXPORT_FILE="$EXPORT_DIR/dynamo-vllm-runtime.tar"
 SERVERS=("gpu-node-1" "gpu-node-2")
 REMOTE_DIR="/tmp/dynamo"
 REMOTE_FILE="$REMOTE_DIR/dynamo-vllm-runtime.tar"
 
cd ~/go/src/github.com/omeryahud/dynamo

python3 container/render.py --framework vllm --target runtime --cuda-version 13.0 --output-short-filename 

docker build -t $IMAGE -f container/rendered.Dockerfile .

mkdir -p "$EXPORT_DIR"

echo "==> Saving $IMAGE to $EXPORT_FILE..."
docker save "$IMAGE" -o "$EXPORT_FILE"
echo "==> Save complete ($(du -sh "$EXPORT_FILE" | cut -f1))"

deploy_to_server() {
    local server="$1"
    local log_prefix="[$server]"

    echo "$log_prefix Creating remote directory..."
    ssh "$server" "mkdir -p $REMOTE_DIR"

    echo "$log_prefix Copying image via rsync..."
    rsync -az --info=progress2 "$EXPORT_FILE" "$server:$REMOTE_FILE"

    echo "$log_prefix Loading into k8s.io namespace..."
    ssh "$server" "sudo nerdctl --namespace k8s.io load -i $REMOTE_FILE"

    echo "$log_prefix Cleaning up remote tar..."
    ssh "$server" "rm -f $REMOTE_FILE"

    echo "$log_prefix Done."
}

pids=()
for server in "${SERVERS[@]}"; do
    deploy_to_server "$server" &
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
    echo "==> Image successfully deployed to all nodes."
fi

exit $failed