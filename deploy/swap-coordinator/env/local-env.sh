#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -e
trap 'echo "Cleaning up..."; kill 0' EXIT

# Set deterministic hash for KV event IDs
export PYTHONHASHSEED=0

MODEL="Qwen/Qwen3-0.6B"
BLOCK_SIZE=64
SWAP_COORDINATOR_PORT=8080

# Namespace used by the router (frontend-visible)
ROUTER_NAMESPACE="dynamo"
# Namespace used by the backend workers (not directly visible to the frontend)
WORKER_NAMESPACE="backend"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SWAP_COORDINATOR_DIR="$SCRIPT_DIR/.."

# Start etcd
docker stop etcd 2>/dev/null || true
docker rm etcd 2>/dev/null || true
docker run -d --name etcd \
    -p 2379:2379 \
    quay.io/coreos/etcd:v3.5.0 \
    etcd \
    --advertise-client-urls http://0.0.0.0:2379 \
    --listen-client-urls http://0.0.0.0:2379

sleep 2

# Build and run swap-coordinator
# NOTE: requires a kubeconfig (~/.kube/config) pointing to a running Kubernetes cluster.
# It uses the Kubernetes API to watch DynamoWorkerMetadata CRDs.
# The swap-aware router will fall back to local KV-cache selection if it is unavailable.
echo "Building swap-coordinator..."
(cd "$SWAP_COORDINATOR_DIR" && go build -o bin/swap-coordinator .)

HTTP_PORT=$SWAP_COORDINATOR_PORT "$SWAP_COORDINATOR_DIR/bin/swap-coordinator" &

# Frontend: HTTP API on port 8000
# Uses round-robin routing, forwarding to the swap-aware router which is registered
# in the discovery service at ${ROUTER_NAMESPACE}.router.generate
python -m dynamo.frontend \
    --router-mode round-robin \
    --namespace $ROUTER_NAMESPACE &

# Swap-aware router: KV-cache-aware routing to backend workers
# Registers itself with the discovery service so the frontend can find it.
# Workers are in the $WORKER_NAMESPACE namespace (not visible to the frontend).
python -m dynamo.swap_aware_router \
    --endpoint $WORKER_NAMESPACE.backend.generate \
    --router-namespace $ROUTER_NAMESPACE \
    --block-size $BLOCK_SIZE \
    --swap-aware-routing \
    --swap-coordinator-url "http://localhost:$SWAP_COORDINATOR_PORT" \
    --swap-coordinator-timeout 1.0 \
    --register-model \
    --model-name $MODEL &

# vLLM worker: aggregated (no prefill/decode split), KV events enabled for routing
# Runs in $WORKER_NAMESPACE so the frontend routes through the swap-aware router,
# not directly to this worker.
DYN_SYSTEM_PORT=${DYN_SYSTEM_PORT:-8081} \
DYN_NAMESPACE=$WORKER_NAMESPACE \
CUDA_VISIBLE_DEVICES=0 python3 -m dynamo.vllm \
    --model $MODEL \
    --block-size $BLOCK_SIZE \
    --enforce-eager \
    --connector none \
    --kv-events-config '{"publisher":"zmq","topic":"kv-events","endpoint":"tcp://*:20080","enable_kv_cache_events":true}'

wait
