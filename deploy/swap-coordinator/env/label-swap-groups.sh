#!/usr/bin/env bash
# Labels worker pods in the swap namespace with their node name as the swap-group-instance-uuid
set -euo pipefail

NAMESPACE="swap"
LABEL_KEY="run.ai/swap-group-instance-uuid"

kubectl get pods -n "$NAMESPACE" -l nvidia.com/dynamo-component-type=worker -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.nodeName}{"\n"}{end}' | while read -r pod node; do
  echo "Labeling $pod with $LABEL_KEY=$node"
  kubectl label pod -n "$NAMESPACE" "$pod" "$LABEL_KEY=$node" --overwrite
done
