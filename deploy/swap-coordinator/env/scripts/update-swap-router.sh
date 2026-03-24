#!/usr/bin/env bash
# Updates the swap-router-code ConfigMap from local source and restarts router pods.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

NAMESPACE="swap"
CONFIGMAP_NAME="swap-router-code"
ROUTER_SRC="$REPO_ROOT/components/src/dynamo/swap_aware_router"

echo "==> Updating ConfigMap '$CONFIGMAP_NAME' from $ROUTER_SRC"
kubectl create configmap "$CONFIGMAP_NAME" \
    --namespace "$NAMESPACE" \
    --from-file="__init__.py=$ROUTER_SRC/__init__.py" \
    --from-file="__main__.py=$ROUTER_SRC/__main__.py" \
    --from-file="_version.py=$ROUTER_SRC/_version.py" \
    --dry-run=client -o yaml | kubectl apply -f -

echo "==> Deleting router pods"
kubectl get pods -n "$NAMESPACE" -o name | grep -i router | while read -r pod; do
    echo "    Deleting $pod"
    kubectl delete -n "$NAMESPACE" "$pod"
done

echo "==> Done. Router pods will be recreated by the operator."
