# Swap-Aware Routing Feature — Deep Dive Review

## Overview

This feature introduces **GPU swap-aware routing** to Dynamo's KV-cache router. The core problem: when multiple LLM models share GPUs via model swapping using Run:ai's Swap feature, the router has no awareness of which model is physically loaded ("warm") on which GPUs. This leads to unnecessary model swaps that increase model responsivene.

The solution is a two-component architecture with a centralized SwapCoordinator making global swap decisions.

## Architecture

```
┌─────────────────┐  ② POST /select_worker  ┌───────────────────────┐
│  Swap-Aware     │ ───────────────────►   │  SwapCoordinator      │
│  Router (Python) │  ③ selected worker    │  (Go, K8s controller) │
│  per DGD         │ ◄───────────────────  │  cluster singleton     │
└─────────────────┘                        └───────────────────────┘
     │          │                                   │
     │          │ ① rank_workers()                  │ watches pods via
     │          ▼   (local Rust call)               │ controller-runtime
     │   ┌────────────┐                             ▼
     │   │ KvRouter   │                      ┌──────────────┐
     │   │ (Rust/lib) │                      │ K8s Pod logs  │
     │   └────────────┘                      │ DGD CRDs      │
     │                                       └──────────────┘
     │ ④ routes to worker
     ▼
┌─────────┐   Run:ai's Swap feature warms
│ Workers  │   the worker if not already warm
│ (vLLM)   │
└─────────┘
```

---

## Component 1: Rust — `rank_workers` API (lib/llm) - Requires Dynamo maintainers review

**Files:** `lib/llm/src/kv_router.rs`, `lib/llm/src/kv_router/scheduler.rs`, `lib/bindings/python/rust/llm/kv.rs`

Added a new `rank_workers()` method to `KvRouter` that exposes the full scoring pipeline to Python:

1. **Block hashing** — computes block hashes from token IDs
2. **Overlap finding** — queries the indexer for KV cache overlap scores
3. **Logit computation** — `logit = overlap_weight * (prefill_tokens / block_size) + decode_blocks` (lower is better)
4. **Softmax sampling** — selects best worker probabilistically (temperature-controlled)
5. **Tie breaking** — uses tree sizes, then random selection

The key struct `RankedWorker` packages `worker_id`, `dp_rank`, `potential_prefill_tokens`, `potential_decode_blocks`, and `logit` — everything the SwapCoordinator needs to make swap-aware decisions.

The `softmax_sample` function handles edge cases well: single worker, zero temperature (deterministic argmin), tied logits, and uniform distributions. Unit tests cover these cases.

The `rank_workers` function's logic was extracted and re-used from the `best_worker` function in order to provide an ordered list of workers instead of automatically sending requests to the best worker.

---

## Component 2: Python — Standalone Swap-Aware Router

**File:** `components/src/dynamo/swap_aware_router/__main__.py` (536 lines)

A standalone Dynamo service that:

- Registers as a router endpoint (`{namespace}.router.generate`)
- On each request: calls `rank_workers()` to get all workers ranked by KV-cache affinity, then sends the ranked list to the SwapCoordinator
- The SwapCoordinator returns the selected worker (factoring in swap state)
- The router pins the request to that worker via `routing.backend_instance_id`
- Respects explicit user routing — swap-aware logic only applies when `routing` is `None`
- Handles rejection (403/503) from the coordinator when `max_warm_workers=0`

---

## Component 3: Go — SwapCoordinator (Kubernetes Controller)

**Files:** `deploy/swap-coordinator/` (~2,800 lines of Go)

A Kubernetes controller-runtime application with:

### State Management (`pkg/state/`)

- Thread-safe `Manager` with `sync.RWMutex` protecting all maps
- Tracks: worker metadata, swap group instances, instance-to-swapgroup mappings, DGD configs, worker logits, TTFT samples, frontend pods
- Each swap group instance represents a group of physical GPUs — only one model can be warm per group of GPUs
- Rolling TTFT window with configurable sample pruning

### Pod Controller (`pkg/controller/`)

- Watches pods with `run.ai/swap-group-instance-uuid` label (Run:ai swap groups - not yet implemented)
- Extracts `instance_id` from worker pod logs via regex (handles both structured and JSON formats, for POC purposes. A robust alternative is a requirement for production use)
- Reads DGD annotations for min/max warm worker config and TTFT thresholds
- Also watches frontend pods for TTFT metrics scraping

### DGD Watcher (`pkg/controller/dgd_watcher.go`)

- Watches DynamoGraphDeployment CRDs for annotation changes
- Propagates min/max warm worker config updates to state manager in real-time

### Worker Selection (`pkg/api/handlers.go` — `SelectWorkerHandler`)

The core algorithm is a **3-tier selection with eviction safety**:


| Tier   | Strategy                                                           | When                                                       |
| ------ | ------------------------------------------------------------------ | ---------------------------------------------------------- |
| **1**  | Warm match — pick a worker that is already warm                    | Default path, skipped if below `min_warm` or TTFT exceeded |
| **2a** | Cold swap group — pick a worker that runs on unclaimed GPUs        | Avoids swaps entirely                                      |
| **2b** | Safe eviction — evict a warm model if the victim DGD can afford it | Checks victim's `min_warm` and TTFT pressure               |
| **3**  | Last resort — fall back to reusing own warm worker                 | When all eviction targets are protected                    |


The `canEvict` function enforces:

- Never evict if it drops the victim's DGD below `min_warm`
- Never evict from a DGD under TTFT pressure
- Allow eviction if victim exceeds `max_warm` (oversubscribed)

### TTFT Auto-Scaling

- Scrapes TTFT histograms directly from frontend pods (`pkg/api/scraper.go`)
- Computes rolling average TTFT per DGD within configurable time windows
- When TTFT exceeds threshold, the coordinator forces warming additional GPUs (skips Tier 1)
- This provides automatic scaling of warm workers based on actual latency

### Dashboard (`pkg/api/dashboard.go`)

- Embedded HTML dashboard showing swap groups, warm states, DGD configs, logits
- Color-coded visualization of warm/cold workers
- Live-editable min/max warm worker values (patches DGD annotations in K8s)
- Shows last-routed worker and TTFT metrics per DGD

### Deployment

- Helm charts for the full stack: dynamo CRDs, dynamo operator, Grove, Kai scheduler, etcd, NATS
- Build scripts, Dockerfiles, K8s manifests for 3 Qwen3 model DGDs
- Request loop scripts for testing

---

## Key Design Decisions

1. **Centralized coordinator** — a single SwapCoordinator makes global swap decisions, avoiding race conditions where multiple routers independently decide to warm conflicting models on the same GPUs.
2. **Annotations as config** — DGD min/max warm workers are stored as Kubernetes annotations, making them editable via `kubectl` or the dashboard without redeployment.
3. **Log-based instance ID extraction** — extracts the Dynamo `instance_id` from pod logs rather than requiring a registration protocol. Pragmatic for a POC, though fragile for production.
4. **Direct frontend scraping** — scrapes TTFT metrics directly from frontend pod `/metrics` endpoints rather than going through Prometheus, reducing latency for auto-scaling decisions. Pragmatic for a POC, though fragile for production.
5. **Reject-on-zero-max** — when `max_warm_workers=0`, the coordinator returns 403, causing the router to reject the request entirely. This enables scale-to-zero per model.

