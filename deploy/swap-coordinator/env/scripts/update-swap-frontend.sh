#!/usr/bin/env bash
# Updates the swap-frontend-code ConfigMap from local source and restarts frontend pods.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

NAMESPACE="swap"
CONFIGMAP_NAME="swap-frontend-code"
FRONTEND_SRC="$REPO_ROOT/components/src/dynamo/frontend"

echo "==> Updating ConfigMap '$CONFIGMAP_NAME' from $FRONTEND_SRC"
kubectl create configmap "$CONFIGMAP_NAME" \
    --namespace "$NAMESPACE" \
    --from-file="frontend_args.py=$FRONTEND_SRC/frontend_args.py" \
    --from-file="vllm_processor.py=$FRONTEND_SRC/vllm_processor.py" \
    --from-file="sglang_processor.py=$FRONTEND_SRC/sglang_processor.py" \
    --from-file="swap_routing.py=$FRONTEND_SRC/swap_routing.py" \
    --dry-run=client -o yaml | kubectl apply -f -

echo "==> Deleting frontend pods"
kubectl get pods -n "$NAMESPACE" -o name | grep -i frontend | while read -r pod; do
    echo "    Deleting $pod"
    kubectl delete -n "$NAMESPACE" "$pod"
done

echo "==> Done. Frontend pods will be recreated by the operator."
