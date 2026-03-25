# Plan: Dynamic Worker Discovery via DynamoWorkerMetadata

## Context

The swap-aware router currently takes `--endpoint swap-qwen3-1-d5eaa276.backend.generate` with a hardcoded namespace hash. This hash (`sha256(worker_spec)[:8]`) changes whenever any worker pod-template field changes (image, env, args). During rolling updates, old workers register under hash A and new workers under hash B — the router can only reach one set, breaking routing until manually updated.

**Goal:** Replace the hardcoded endpoint with dynamic discovery via DynamoWorkerMetadata CRs, so the router automatically finds all active worker namespaces and routes to all workers (old + new) during rollouts.

## Approach: Multi-Namespace KvRouter with DWM Polling

Since each `KvRouter` (Rust-backed) binds to exactly one `namespace.component.endpoint` path and cannot be changed after creation, the router will manage **multiple KvRouters** — one per discovered worker namespace — via a new aggregator class.

```
DWMWatcher (polls K8s API every 15s)
  └─ discovers namespaces: {swap-qwen3-1-d5eaa276, swap-qwen3-1-abc12345}
       └─ MultiNamespaceKvRouter
            ├─ KvRouter("swap-qwen3-1-d5eaa276.backend.generate")  ← old workers
            └─ KvRouter("swap-qwen3-1-abc12345.backend.generate")  ← new workers
```

**Request flow in DWM mode:**
1. `rank_workers(token_ids)` called on ALL KvRouters → merge results
2. Merged list sent to SwapCoordinator → selects worker
3. `generate_from_request()` dispatched to the KvRouter that owns the selected worker

## Implementation Steps

### Step 1: CLI Flags & Config

**File:** `components/src/dynamo/swap_aware_router/__main__.py`

Add to `SwapAwareRouterArgGroup` (after line 318) and `SwapAwareRouterConfig` (after line 252):

| Flag | Env Var | Default | Purpose |
|------|---------|---------|---------|
| `--discover-from-dwm` | `DYN_DISCOVER_FROM_DWM` | `False` | Enable DWM-based discovery |
| `--dgd-prefix` | `DYN_DGD_PREFIX` | `None` | DGD name prefix for filtering (e.g., `swap-qwen3-1`) |
| `--worker-component` | `DYN_WORKER_COMPONENT` | `"backend"` | Worker component name to match |
| `--worker-endpoint` | `DYN_WORKER_ENDPOINT` | `"generate"` | Worker endpoint name to match |
| `--dwm-poll-interval` | `DYN_DWM_POLL_INTERVAL` | `15` | Polling interval in seconds |

Validation: if `--discover-from-dwm` is set, `--dgd-prefix` is required. `--endpoint` becomes optional (but can still be provided as bootstrap).

Use existing `add_argument` and `add_negatable_bool_argument` helpers (already imported).

### Step 2: DWMWatcher Class

**File:** `components/src/dynamo/swap_aware_router/__main__.py` (inline, consistent with current single-file pattern)

**K8s API access:** Raw HTTP via `aiohttp` (already a dependency — no new packages needed).
- Token: `/var/run/secrets/kubernetes.io/serviceaccount/token`
- Namespace: `/var/run/secrets/kubernetes.io/serviceaccount/namespace`
- CA cert: `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`
- API URL: `https://kubernetes.default.svc/apis/nvidia.com/v1alpha1/namespaces/{ns}/dynamoworkermetadatas`

**Core logic:**
```python
class DWMWatcher:
    async def poll_namespaces(self) -> set[str]:
        """List DWM CRs, extract unique Dynamo namespaces matching prefix."""
        dwm_list = await self._list_dwm_crs()
        namespaces = set()
        for dwm in dwm_list["items"]:
            for ep_data in dwm["spec"]["data"].get("endpoints", {}).values():
                ns = ep_data.get("namespace", "")
                comp = ep_data.get("component", "")
                ep = ep_data.get("endpoint", "")
                if (ns.startswith(self.dgd_prefix)
                        and comp == self.worker_component
                        and ep == self.worker_endpoint):
                    namespaces.add(ns)
        return namespaces

    async def run(self, on_change: Callable[[set[str]], Awaitable[None]]):
        """Poll loop — calls on_change when namespace set changes."""
        last = set()
        while True:
            try:
                current = await self.poll_namespaces()
                if current != last:
                    await on_change(current)
                    last = current
            except Exception as e:
                logger.warning(f"DWM poll failed: {e}")
            await asyncio.sleep(self.poll_interval)
```

**Design choice — polling over watching:** 15s poll latency is acceptable for rolling updates (which take minutes). Avoids WebSocket complexity and reconnection logic.

### Step 3: MultiNamespaceKvRouter Class

**File:** `components/src/dynamo/swap_aware_router/__main__.py`

**State:**
- `_routers: dict[str, KvRouter]` — namespace → KvRouter
- `_worker_ns_map: dict[int, str]` — worker_id → namespace (for routing to correct KvRouter)
- `_lock: asyncio.Lock` — protects `_routers` during sync

**Key methods:**

1. **`sync_namespaces(active: set[str])`** — Called by DWMWatcher on change.
   - New namespaces: `KvRouter(endpoint=runtime.endpoint(f"{ns}.{comp}.{ep}"), ...)` — no `await endpoint.client()` needed (KvRouter does its own internal discovery).
   - Removed namespaces: delete KvRouter + clean stale entries from `_worker_ns_map`.

2. **`rank_workers(token_ids)`** — Call `rank_workers()` on each KvRouter, merge all results, sort by logit descending. Update `_worker_ns_map` with each worker's namespace.

3. **`generate_from_request(request)`** — Look up `routing.backend_instance_id` in `_worker_ns_map` to find the correct KvRouter, delegate to it. Fallback: first available router (for non-swap-aware requests where KvRouter picks its own worker).

4. **`best_worker(token_ids)`** — Aggregate `rank_workers()` and return top result (for the `best_worker_id` endpoint).

**Worker ID uniqueness:** Instance IDs are globally unique (Rust runtime connection-based counters), so no collisions across namespaces.

### Step 4: Integrate into SwapAwareRouterHandler

**File:** `components/src/dynamo/swap_aware_router/__main__.py`

- Add `multi_ns_router: Optional[MultiNamespaceKvRouter] = None` parameter to `__init__`.
- In `_apply_swap_aware_routing` (line 146): use `self.multi_ns_router.rank_workers()` when set, otherwise `self.kv_router.rank_workers()`.
- In `generate` (line 213): use `self.multi_ns_router.generate_from_request()` when set, otherwise `self.kv_router.generate_from_request()`.
- In `best_worker_id` (line 232): use `self.multi_ns_router.best_worker()` when set, otherwise `self.kv_router.best_worker()`.
- In `initialize()`: skip single-KvRouter creation when `multi_ns_router` is set (but still create HTTP session for SwapCoordinator).

### Step 5: Wire Up in worker() Entry Point

**File:** `components/src/dynamo/swap_aware_router/__main__.py` (line 343)

Two code paths based on `config.discover_from_dwm`:

**DWM mode:**
```python
if config.discover_from_dwm:
    multi_ns_router = MultiNamespaceKvRouter(runtime, ...)
    dwm_watcher = DWMWatcher(dgd_prefix, component, endpoint, poll_interval)

    # Bootstrap: if --endpoint also provided, seed initial namespace
    if config.endpoint:
        await multi_ns_router.sync_namespaces({config.endpoint.split(".")[0]})

    handler = SwapAwareRouterHandler(..., multi_ns_router=multi_ns_router)
    # Only initialize HTTP session, skip single-KvRouter init
    handler._init_http_session()

    watcher_task = asyncio.create_task(dwm_watcher.run(multi_ns_router.sync_namespaces))
    try:
        await asyncio.gather(generate_endpoint.serve_endpoint(...), ...)
    finally:
        watcher_task.cancel()
        await handler.cleanup()
```

**Legacy mode** (unchanged): existing `--endpoint` single-KvRouter path.

### Step 6: RBAC Manifest

**New file:** `deploy/swap-coordinator/env/manifests/deployment/router-rbac.yaml`

Pattern follows existing `rbac.yaml` (lines 1-91). The router pod needs `list` on `dynamoworkermetadatas` in its namespace.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: swap-router-dwm-reader
  namespace: swap
rules:
- apiGroups: ["nvidia.com"]
  resources: ["dynamoworkermetadatas"]
  verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: swap-router-dwm-reader-binding
  namespace: swap
subjects:
- kind: ServiceAccount
  name: default   # The operator-created SA for the router component
  namespace: swap
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: swap-router-dwm-reader
```

Note: the `subjects.name` needs to match the actual SA the operator assigns to the router pod. Verify with `kubectl get pod <router-pod> -o jsonpath='{.spec.serviceAccountName}'`.

### Step 7: Update DGD Manifest

**File:** `deploy/swap-coordinator/env/manifests/dgds/qwen3-1.yaml` (line 67-82)

Replace router args:
```yaml
args:
- --discover-from-dwm
- --dgd-prefix
- swap-qwen3-1
- --endpoint                            # optional bootstrap
- swap-qwen3-1-d5eaa276.backend.generate
- --router-namespace
- swap-qwen3-1-router
- --swap-aware-routing
- --swap-coordinator-url
- http://swap-coordinator-service.swap.svc.cluster.local:8080
- --swap-coordinator-timeout
- "1.0"
- --register-model
- --model-name
- Qwen/Qwen3-0.6B
- --block-size
- "16"
```

Also update `update-swap-router.sh` script to include any new files if DWMWatcher is in a separate file.

### Step 8: Update ConfigMap Hot-Swap Script

**File:** `deploy/swap-coordinator/env/scripts/update-swap-router.sh`

No changes needed — the DWMWatcher is inline in `__main__.py`, which is already included in the ConfigMap.

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| Router starts before any workers | DWM poll returns empty set. If `--endpoint` provided, bootstrap KvRouter handles initial traffic. Otherwise, requests fail with "No KvRouters available" until first workers appear. |
| All workers of one hash disappear | Next DWM poll detects empty namespace → KvRouter removed. In-flight requests complete normally (NATS handles delivery). |
| DWM poll fails (API timeout, RBAC) | Warning logged, previous namespace set retained. Routing continues with last-known workers. |
| Multiple DGDs in same K8s namespace | `--dgd-prefix` scoping ensures only matching workers are discovered. |

## Files Summary

| File | Change |
|------|--------|
| `components/src/dynamo/swap_aware_router/__main__.py` | Add DWMWatcher, MultiNamespaceKvRouter, CLI flags, handler integration |
| `deploy/swap-coordinator/env/manifests/dgds/qwen3-1.yaml` | Replace `--endpoint` with `--discover-from-dwm --dgd-prefix swap-qwen3-1` |
| `deploy/swap-coordinator/env/manifests/deployment/router-rbac.yaml` | New file: Role/RoleBinding for DWM list |

## Verification

1. **Unit test:** Run router with `--discover-from-dwm --dgd-prefix test` in a mock environment, verify DWMWatcher parses sample DWM JSON correctly
2. **Single-namespace deploy:** Deploy with `--discover-from-dwm` + `--endpoint` (bootstrap), verify router discovers workers and serves requests identically to legacy mode
3. **Rolling update simulation:** Change a worker env var to trigger a new hash. Verify both old and new workers appear in `rank_workers()` output. Verify requests route to both sets. Scale old workers to 0, verify they're cleaned up.
4. **RBAC:** Verify `kubectl auth can-i list dynamoworkermetadatas --as=system:serviceaccount:swap:<router-sa>` returns `yes`
