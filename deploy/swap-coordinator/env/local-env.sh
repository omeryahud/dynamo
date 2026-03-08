#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -e
trap 'echo "Cleaning up..."; kill 0' EXIT

# Set deterministic hash for KV event IDs
export PYTHONHASHSEED=0

MODEL="Qwen/Qwen3-0.6B"
SWAP_COORDINATOR_PORT=8080
ROUTER_NAMESPACE="dynamo"   # namespace the router registers in (frontend-visible)
WORKER_NAMESPACE="backend"  # namespace the workers run in (not visible to frontend)

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SWAP_COORDINATOR_DIR="$SCRIPT_DIR/.."

# # Start etcd
# docker stop etcd 2>/dev/null || true
# docker rm etcd 2>/dev/null || true
# docker run -d --name etcd \
#     -p 2379:2379 \
#     quay.io/coreos/etcd:v3.5.0 \
#     etcd \
#     --advertise-client-urls http://0.0.0.0:2379 \
#     --listen-client-urls http://0.0.0.0:2379

# # Start NATS
# docker stop nats 2>/dev/null || true
# docker rm nats 2>/dev/null || true
# docker run -d --name nats -p 4222:4222 nats:latest

# sleep 2

# # Build and run swap-coordinator
# # NOTE: requires a kubeconfig (~/.kube/config) pointing to a running Kubernetes cluster.
# # It watches Pods with the run.ai/swap-group-instance-uuid label directly (no CRDs needed).
# # The swap-aware router will fall back to local KV-cache selection if it is unavailable.
# echo "Building swap-coordinator..."
# (cd "$SWAP_COORDINATOR_DIR" && go build -o bin/swap-coordinator .)

# HTTP_PORT=$SWAP_COORDINATOR_PORT "$SWAP_COORDINATOR_DIR/bin/swap-coordinator" &

# # Frontend: HTTP API on port 8000 (round-robin within ROUTER_NAMESPACE)
# python -m dynamo.frontend \
#     --namespace $ROUTER_NAMESPACE &

# Swap-aware router: KV-cache-aware routing to workers in WORKER_NAMESPACE,
# registers itself in ROUTER_NAMESPACE so the frontend can discover it
python -m dynamo.swap_aware_router \
    --endpoint $WORKER_NAMESPACE.backend.generate \
    --router-namespace $ROUTER_NAMESPACE \
    --swap-aware-routing \
    --swap-coordinator-url "http://localhost:$SWAP_COORDINATOR_PORT" \
    --swap-coordinator-timeout 1.0 \
    --register-model \
    --model-name $MODEL &

# # vLLM worker: runs in WORKER_NAMESPACE so the frontend doesn't discover it directly
# # CUDA_HOME is required for FlashInfer's JIT kernel compilation (nvcc is in the cu13 pip package)
# DYN_SYSTEM_PORT=${DYN_SYSTEM_PORT:-8081} \
# DYN_NAMESPACE=$WORKER_NAMESPACE \
# CUDA_HOME=/opt/pytorch/cuda \
# CUDA_VISIBLE_DEVICES=0 python3 -m dynamo.vllm \
#     --connector none \
#     --model $MODEL

wait
