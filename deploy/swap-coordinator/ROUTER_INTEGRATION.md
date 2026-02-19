# Swap-Aware Router Integration Guide

This guide explains how to integrate the Dynamo swap-aware router with the SwapCoordinator service for GPU-aware routing decisions in Run:AI swap environments.

---

## Overview

The swap-aware router can optionally integrate with the SwapCoordinator service to make routing decisions based on which workers share GPU hardware (swap group instances). This enables:

- **Phase 1 (Current):** Foundation for swap-aware routing (SwapCoordinator discovery only)
- **Phase 2 (Future):** Intelligent routing that prefers workers with GPU memory already loaded (swapped-in) to avoid costly swap operations

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Dynamo Request Flow                           │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│              Swap-Aware Router (Python)                          │
│  1. Receive generation request                                   │
│  2. Query KV cache state for all workers                         │
│  3. Build worker candidate list with potential loads             │
│  4. Call SwapCoordinator for selection (if configured)           │
│  5. Route request to selected worker                             │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼ HTTP POST /select_worker
┌─────────────────────────────────────────────────────────────────┐
│            SwapCoordinator Service (Go)                          │
│  - Receives worker candidates with KV cache metrics              │
│  - Maps worker_id → swap_group_instance_uuid                     │
│  - [Phase 2] Selects worker based on swap state                  │
│  - Returns selected worker + reason                              │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼ Selection decision
┌─────────────────────────────────────────────────────────────────┐
│              Swap-Aware Router (Python)                          │
│  - Applies SwapCoordinator's selection                           │
│  - Falls back to local KV-cache selection if unavailable         │
│  - Routes request to selected worker                             │
└─────────────────────────────────────────────────────────────────┘
```

---

## Configuration

### Router Command-Line Arguments

#### Required for Swap-Aware Routing

```bash
--swap-aware-routing
```
- **Type:** Flag (boolean)
- **Default:** `False`
- **Description:** Enable swap-aware routing logic. When enabled, the router considers KV cache warmth when selecting workers.

#### Optional SwapCoordinator Integration

```bash
--swap-coordinator-url <URL>
```
- **Type:** String
- **Default:** `None`
- **Description:** URL of the SwapCoordinator service (e.g., `http://swap-coordinator-service:8080`). If not specified, the router uses local KV-cache based selection without GPU swap awareness.
- **Example:** `http://swap-coordinator-service.default.svc.cluster.local:8080`

```bash
--swap-coordinator-timeout <SECONDS>
```
- **Type:** Float
- **Default:** `1.0`
- **Description:** Timeout in seconds for SwapCoordinator API calls. If the SwapCoordinator doesn't respond within this time, the router falls back to local selection.

---

## Usage Examples

### Example 1: Local KV-Cache Selection (No SwapCoordinator)

```bash
python -m dynamo.swap_aware_router \
  --endpoint dynamo.prefill.generate \
  --block-size 128 \
  --swap-aware-routing
```

**Behavior:**
- Swap-aware routing enabled
- Uses local KV-cache metrics only
- Selects worker with minimum `potential_prefill_tokens` (warmest cache)
- No GPU swap group awareness

**Use Case:** Testing swap-aware routing logic without SwapCoordinator infrastructure

### Example 2: SwapCoordinator Integration (Phase 1 - Discovery Only)

```bash
python -m dynamo.swap_aware_router \
  --endpoint dynamo.prefill.generate \
  --block-size 128 \
  --swap-aware-routing \
  --swap-coordinator-url http://swap-coordinator-service:8080 \
  --swap-coordinator-timeout 1.0
```

**Behavior:**
- Swap-aware routing enabled
- Calls SwapCoordinator `/select_worker` endpoint
- SwapCoordinator returns 501 (Phase 1 stub - selection not implemented)
- Falls back to local KV-cache selection
- Logs: "SwapCoordinator returned 501 (Phase 1 - selection not implemented). Falling back to local selection."

**Use Case:** Preparing for Phase 2 - validates API integration and deployment

### Example 3: SwapCoordinator Integration (Phase 2 - Full Swap Awareness)

```bash
python -m dynamo.swap_aware_router \
  --endpoint dynamo.prefill.generate \
  --block-size 128 \
  --swap-aware-routing \
  --swap-coordinator-url http://swap-coordinator-service:8080 \
  --swap-coordinator-timeout 2.0
```

**Behavior (when Phase 2 is implemented):**
- Swap-aware routing enabled
- Calls SwapCoordinator `/select_worker` endpoint
- SwapCoordinator returns worker selection based on:
  - GPU swap group instance membership
  - Recent routing history
  - KV cache warmth
- Router uses SwapCoordinator's selection
- Logs: "Swap-aware routing (swap-coordinator): Selected worker X..."

**Use Case:** Production deployment with full GPU swap awareness

### Example 4: Kubernetes Deployment with Service Discovery

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: swap-aware-router
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: router
        image: swap-aware-router:latest
        command:
        - python
        - -m
        - dynamo.swap_aware_router
        args:
        - --endpoint=dynamo.prefill.generate
        - --block-size=128
        - --swap-aware-routing
        - --swap-coordinator-url=http://swap-coordinator-service.default.svc.cluster.local:8080
        - --swap-coordinator-timeout=1.0
        env:
        - name: LOG_LEVEL
          value: INFO
```

**Kubernetes Service Discovery:**
- SwapCoordinator service: `swap-coordinator-service.default.svc.cluster.local`
- DNS resolves to ClusterIP automatically
- No need for external load balancers

---

## Fallback Behavior

The router implements robust fallback logic to ensure reliability:

### 1. SwapCoordinator Unavailable
**Trigger:** SwapCoordinator service not reachable, network error, timeout

**Action:**
- Router falls back to local KV-cache selection
- Selects worker with minimum `potential_prefill_tokens`
- Logs: "Failed to call SwapCoordinator: <error>. Falling back to local selection."

**Result:** Request continues normally with KV-aware (but not swap-aware) routing

### 2. SwapCoordinator Returns 501 (Phase 1)
**Trigger:** SwapCoordinator Phase 1 stub (selection not implemented yet)

**Action:**
- Router recognizes 501 as expected Phase 1 behavior
- Falls back to local KV-cache selection
- Logs: "SwapCoordinator returned 501 (Phase 1 - selection not implemented). Falling back to local selection."

**Result:** Graceful degradation to KV-aware routing

### 3. SwapCoordinator Returns Error (4xx/5xx)
**Trigger:** SwapCoordinator internal error, invalid request, etc.

**Action:**
- Router falls back to local KV-cache selection
- Logs: "SwapCoordinator returned status <code>: <error>"

**Result:** Request continues with local selection

### 4. SwapCoordinator Selection Not Found
**Trigger:** SwapCoordinator selects a worker not in the candidate list

**Action:**
- Router falls back to local KV-cache selection
- Logs: "SwapCoordinator selected worker_id=X dp_rank=Y, but worker not found in potential_loads. Falling back to local selection."

**Result:** Request routed to locally-selected worker

### 5. No Workers Available
**Trigger:** KV router returns empty `potential_loads` list

**Action:**
- Router skips swap-aware routing entirely
- Uses default routing (round-robin or other configured policy)
- Logs: "Swap-aware routing enabled but no workers available. Falling back to default routing."

**Result:** Request handled by default routing mechanism

---

## API Contract

### Request: POST /select_worker

The router sends worker candidates to SwapCoordinator:

```json
{
  "workers": [
    {
      "instance_id": "worker-1",
      "worker_id": 0,
      "dp_rank": 0,
      "potential_prefill_tokens": 100,
      "potential_decode_blocks": 50
    },
    {
      "instance_id": "worker-2",
      "worker_id": 1,
      "dp_rank": 1,
      "potential_prefill_tokens": 500,
      "potential_decode_blocks": 200
    }
  ],
  "request_id": "req-12345"
}
```

**Field Descriptions:**
- `workers`: List of worker candidates sorted by `potential_prefill_tokens` (ascending = warmer cache)
- `instance_id`: DynamoWorkerMetadata instance ID (extracted from worker metadata if available)
- `worker_id`: Runtime worker ID
- `dp_rank`: Data parallel rank
- `potential_prefill_tokens`: Number of tokens that would need to be prefilled (lower = warmer cache)
- `potential_decode_blocks`: Number of KV cache blocks available for decode
- `request_id`: Unique request ID for logging/debugging

### Response: 200 OK (Phase 2)

SwapCoordinator returns the selected worker:

```json
{
  "selected_instance_id": "worker-1",
  "selected_worker_id": 0,
  "selected_dp_rank": 0,
  "reason": "Same swap group as recent request (swap-group-uuid: abc-123)"
}
```

**Field Descriptions:**
- `selected_instance_id`: Instance ID of selected worker
- `selected_worker_id`: Worker ID to route to
- `selected_dp_rank`: Data parallel rank of selected worker
- `reason`: Human-readable explanation of selection (for logging/debugging)

### Response: 501 Not Implemented (Phase 1)

SwapCoordinator Phase 1 stub:

```json
{
  "error": "Worker selection not implemented yet (Phase 2)"
}
```

**Router Behavior:** Falls back to local KV-cache selection

---

## Logging

### Router Logs

**Successful SwapCoordinator Selection (Phase 2):**
```
INFO: Swap-aware routing (swap-coordinator): Selected worker 0 (dp_rank=0) with 100 prefill tokens, 50 decode blocks
```

**SwapCoordinator Phase 1 Fallback:**
```
DEBUG: SwapCoordinator returned 501 (Phase 1 - selection not implemented). Falling back to local selection.
INFO: Swap-aware routing (local): Selected worker 0 (dp_rank=0) with 100 prefill tokens, 50 decode blocks
```

**SwapCoordinator Timeout:**
```
WARNING: SwapCoordinator request timed out after 1.0s
INFO: Swap-aware routing (local): Selected worker 0 (dp_rank=0) with 100 prefill tokens, 50 decode blocks
```

**SwapCoordinator Error:**
```
WARNING: SwapCoordinator returned status 500: Internal server error
INFO: Swap-aware routing (local): Selected worker 0 (dp_rank=0) with 100 prefill tokens, 50 decode blocks
```

---

## Deployment Checklist

### Phase 1 Deployment (Discovery Only)

- [ ] Deploy SwapCoordinator service with RBAC, Service, Deployment manifests
- [ ] Verify SwapCoordinator is discovering workers: `kubectl logs -l app=swap-coordinator | grep "Registered worker"`
- [ ] Test SwapCoordinator health endpoint: `curl http://swap-coordinator-service:8080/health`
- [ ] Deploy swap-aware router with `--swap-coordinator-url` flag
- [ ] Verify router logs show SwapCoordinator integration enabled
- [ ] Send test requests and verify fallback to local selection (501 response expected)
- [ ] Verify swap-aware routing works correctly in fallback mode

### Phase 2 Deployment (Full Swap Awareness)

- [ ] Upgrade SwapCoordinator to Phase 2 (worker selection logic implemented)
- [ ] Test `/select_worker` endpoint returns 200 with selection decision
- [ ] Restart swap-aware router (no config changes needed)
- [ ] Send test requests and verify SwapCoordinator selection is used
- [ ] Monitor selection decisions: `kubectl logs -l app=router | grep "swap-coordinator"`
- [ ] Verify routing prefers workers in same swap group instance
- [ ] Test fallback behavior by stopping SwapCoordinator temporarily

---

## Performance Considerations

### Latency

- **SwapCoordinator call:** Adds ~1-10ms to routing decision (HTTP round-trip)
- **Timeout:** Configurable via `--swap-coordinator-timeout` (default: 1.0s)
- **Fallback:** If SwapCoordinator slow/unavailable, falls back to local selection immediately

**Recommendation:** Set timeout to 1-2 seconds for production to balance swap awareness with request latency.

### Caching

Currently, the router calls SwapCoordinator on **every request**. Future optimizations:
- Cache swap group instance mappings (TTL-based)
- Batch requests to SwapCoordinator
- Use server-sent events (SSE) for real-time swap state updates

### Reliability

- **No single point of failure:** SwapCoordinator unavailable → fallback to local selection
- **Graceful degradation:** Phase 1 stub → fallback to local selection
- **Error resilience:** All SwapCoordinator errors caught and logged

---

## Troubleshooting

### Issue: Router not calling SwapCoordinator

**Symptoms:** Logs show "Swap-aware routing (local)" instead of "swap-coordinator"

**Possible Causes:**
1. `--swap-coordinator-url` not specified
2. SwapCoordinator service not reachable
3. SwapCoordinator returns 501 (Phase 1 stub)

**Solution:**
- Check router startup logs for "SwapCoordinator integration enabled: <URL>"
- Verify SwapCoordinator service: `kubectl get svc swap-coordinator-service`
- Test connectivity: `curl http://swap-coordinator-service:8080/health`
- Check SwapCoordinator logs: `kubectl logs -l app=swap-coordinator`

### Issue: SwapCoordinator timeouts

**Symptoms:** Logs show "SwapCoordinator request timed out after Xs"

**Possible Causes:**
1. SwapCoordinator under heavy load
2. Network latency
3. Timeout too low

**Solution:**
- Increase `--swap-coordinator-timeout` (e.g., 2.0 seconds)
- Scale SwapCoordinator: `kubectl scale deployment swap-coordinator --replicas=3`
- Check SwapCoordinator performance metrics

### Issue: Selected worker not found

**Symptoms:** Logs show "SwapCoordinator selected worker_id=X dp_rank=Y, but worker not found in potential_loads"

**Possible Causes:**
1. Worker state changed between KV query and SwapCoordinator call
2. instance_id mismatch between router and SwapCoordinator

**Solution:**
- Verify DynamoWorkerMetadata CRDs match worker IDs
- Check that instance_id extraction in router is correct
- Enable DEBUG logging to see full worker candidate list

---

## Phase 2 Enhancements

When SwapCoordinator Phase 2 is implemented, the following will be available:

### 1. Swap Group Instance Awareness
- SwapCoordinator knows which workers share GPU hardware
- Routing prefers workers in the same swap group instance as recent requests
- Avoids costly GPU memory swaps

### 2. Routing History Tracking
- SwapCoordinator tracks recent routing decisions per swap group instance
- Prefers workers that were recently used (likely still swapped-in)
- Configurable time window (e.g., last 30 seconds)

### 3. Selection Reasons
- SwapCoordinator returns human-readable reason for selection
- Examples:
  - "Same swap group as recent request (swap-group-uuid: abc-123)"
  - "Warmest cache in swap group instance abc-123"
  - "No swap history, selected by KV cache warmth"

### 4. Metrics and Observability
- Prometheus metrics for swap-aware routing decisions
- Grafana dashboard showing swap efficiency
- Selection reason breakdown (swap-aware vs KV-aware)

---

## Migration Guide

### Migrating from Local Selection to SwapCoordinator

**Step 1:** Deploy SwapCoordinator (Phase 1)
```bash
kubectl apply -f deploy/swap-coordinator/rbac.yaml
kubectl apply -f deploy/swap-coordinator/service.yaml
kubectl apply -f deploy/swap-coordinator/deployment.yaml
```

**Step 2:** Update router configuration (no downtime)
```bash
# Add to router deployment:
--swap-coordinator-url=http://swap-coordinator-service:8080
```

**Step 3:** Rolling restart of router pods
```bash
kubectl rollout restart deployment swap-aware-router
```

**Step 4:** Verify integration
```bash
kubectl logs -l app=router | grep "SwapCoordinator integration enabled"
```

**Step 5:** Monitor fallback behavior (Phase 1)
```bash
kubectl logs -l app=router | grep "501"
# Should see: "SwapCoordinator returned 501 (Phase 1 - selection not implemented)"
```

**Step 6:** Wait for SwapCoordinator Phase 2 upgrade
- No router changes needed
- SwapCoordinator upgrade transparent to router
- Routing automatically uses swap-aware selection when available

---

## Summary

- **Integration:** Optional, enabled via `--swap-coordinator-url` flag
- **Fallback:** Robust fallback to local KV-cache selection if SwapCoordinator unavailable
- **Phase 1:** Validates API integration, prepares for Phase 2
- **Phase 2:** Enables GPU swap-aware routing for optimal performance in Run:AI environments
- **Zero Downtime:** Can enable/disable integration without service interruption
- **Production Ready:** Comprehensive error handling, logging, and monitoring support
