# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""
Standalone KV Router Service

Usage: python -m dynamo.router --endpoint <namespace.component.endpoint> [args]

This service provides a standalone KV-aware router for any set of workers
in a Dynamo deployment. It can be used for disaggregated serving (e.g., routing
to prefill workers) or any other scenario requiring intelligent KV cache-aware
routing decisions.
"""

import argparse
import asyncio
import logging
from typing import Optional

import aiohttp
import uvloop

from dynamo.llm import KvRouter, KvRouterConfig, ModelInput, ModelType, register_model
from dynamo.runtime import Client, DistributedRuntime, dynamo_worker
from dynamo.runtime.logging import configure_dynamo_logging

configure_dynamo_logging()
logger = logging.getLogger(__name__)


class StandaloneRouterHandler:
    """Handles routing requests to workers using KV-aware routing."""

    def __init__(
        self,
        runtime: DistributedRuntime,
        worker_endpoint_path: str,
        block_size: int,
        kv_router_config: KvRouterConfig,
        swap_aware_routing: bool = False,
        swap_coordinator_url: Optional[str] = None,
        swap_coordinator_timeout: float = 1.0,
    ):
        self.runtime = runtime
        self.worker_endpoint_path = worker_endpoint_path
        self.block_size = block_size
        self.kv_router_config = kv_router_config
        self.swap_aware_routing = swap_aware_routing
        self.swap_coordinator_url = swap_coordinator_url
        self.swap_coordinator_timeout = swap_coordinator_timeout
        self.kv_push_router: Optional[KvRouter] = None
        self.worker_client: Optional[Client] = None
        self._http_session: Optional[aiohttp.ClientSession] = None

    async def initialize(self):
        """Initialize the KV router for workers."""
        try:
            # Parse endpoint path (format: namespace.component.endpoint)
            parts = self.worker_endpoint_path.split(".")
            if len(parts) != 3:
                raise ValueError(
                    f"Invalid endpoint path format: {self.worker_endpoint_path}. "
                    "Expected format: namespace.component.endpoint"
                )
            namespace, component, endpoint = parts

            # Get worker endpoint
            worker_endpoint = self.runtime.endpoint(
                f"{namespace}.{component}.{endpoint}"
            )

            self.worker_client = await worker_endpoint.client()

            # Create KvRouter with specified configuration
            self.kv_push_router = KvRouter(
                endpoint=worker_endpoint,
                block_size=self.block_size,
                kv_router_config=self.kv_router_config,
            )

            # Create HTTP session for SwapCoordinator communication if URL is provided
            if self.swap_coordinator_url:
                self._http_session = aiohttp.ClientSession(
                    timeout=aiohttp.ClientTimeout(total=self.swap_coordinator_timeout)
                )
                logger.info(
                    f"SwapCoordinator integration enabled: {self.swap_coordinator_url}"
                )

        except Exception as e:
            logger.error(f"Failed to initialize KvRouter: {e}")
            raise

    async def cleanup(self):
        """Cleanup resources."""
        if self._http_session:
            await self._http_session.close()
            self._http_session = None

    async def _call_swap_coordinator(self, workers: list, request_id: str) -> Optional[dict]:
        """
        Call SwapCoordinator service to select the best worker.

        Args:
            workers: List of worker candidates with potential loads
            request_id: Unique request ID for tracking

        Returns:
            Selected worker info dict or None if call fails
        """
        if not self.swap_coordinator_url or not self._http_session:
            return None

        try:
            # Build request payload matching SwapCoordinator API contract
            # Extract instance_id from worker metadata (if available)
            worker_candidates = []
            for worker in workers:
                candidate = {
                    "instance_id": worker["worker_id"],
                    "dp_rank": worker["dp_rank"],
                    "potential_prefill_tokens": worker["potential_prefill_tokens"],
                    "potential_decode_blocks": worker["potential_decode_blocks"],
                }
                worker_candidates.append(candidate)

            payload = {
                "workers": worker_candidates,
                "request_id": request_id,
            }

            # Call SwapCoordinator /select_worker endpoint
            url = f"{self.swap_coordinator_url}/select_worker"
            async with self._http_session.post(url, json=payload) as response:
                if response.status == 200:
                    result = await response.json()
                    logger.debug(
                        f"SwapCoordinator selected: instance_id={result['selected_instance_id']}, "
                        f"dp_rank={result['selected_dp_rank']}, reason={result['reason']}"
                    )
                    return result
                elif response.status == 501:
                    # Phase 1 stub - selection not implemented yet
                    logger.debug(
                        "SwapCoordinator returned 501 (Phase 1 - selection not implemented). "
                        "Falling back to local selection."
                    )
                    return None
                else:
                    error_text = await response.text()
                    logger.warning(
                        f"SwapCoordinator returned status {response.status}: {error_text}"
                    )
                    return None

        except asyncio.TimeoutError:
            logger.warning(
                f"SwapCoordinator request timed out after {self.swap_coordinator_timeout}s"
            )
            return None
        except Exception as e:
            logger.warning(f"Failed to call SwapCoordinator: {e}")
            return None

    async def generate(self, request):
        """
        Generate tokens using the KV-aware router.

        This endpoint routes the request to the best worker and streams back results.
        Wraps the request into PreprocessedRequest format and wraps worker responses
        into LLMEngineOutput format.
        """
        if self.kv_push_router is None:
            logger.error("KvRouter not initialized - cannot process request")
            raise RuntimeError("Router not initialized")

        # Wrap incoming request into PreprocessedRequest format for KvRouter
        # The request should already have most fields, but we ensure it has the structure
        # Build routing hints from request (supports both nested routing object and legacy dp_rank)
        routing = request.get("routing")
        dp_rank = request.get("dp_rank")
        if routing is None and dp_rank is not None:
            routing = {"dp_rank": dp_rank}

        # Apply Swap-aware routing only if:
        # 1. Feature is enabled (--swap-aware-routing flag)
        # 2. User hasn't specified explicit routing (respects user choice)
        if self.swap_aware_routing and routing is None:
            try:
                token_ids = request.get("token_ids", [])

                # Query potential loads for all workers
                potential_loads = await self.kv_push_router.get_potential_loads(token_ids)

                if not potential_loads:
                    logger.warning(
                        "Swap-aware routing enabled but no workers available. "
                        "Falling back to default routing."
                    )
                else:
                    best_worker = None
                    selection_source = "local"

                    # Try SwapCoordinator if configured
                    if self.swap_coordinator_url:
                        request_id = request.get("request_id", f"req-{id(request)}")
                        coordinator_result = await self._call_swap_coordinator(
                            potential_loads, request_id
                        )

                        if coordinator_result:
                            # SwapCoordinator made a selection
                            selected_instance_id = coordinator_result["selected_instance_id"]
                            selected_dp_rank = coordinator_result["selected_dp_rank"]

                            # Find the matching worker in potential_loads
                            for worker in potential_loads:
                                if (worker["worker_id"] == selected_instance_id and
                                    worker["dp_rank"] == selected_dp_rank):
                                    best_worker = worker
                                    selection_source = "swap-coordinator"
                                    break

                            if not best_worker:
                                logger.warning(
                                    f"SwapCoordinator selected instance_id={selected_instance_id} "
                                    f"dp_rank={selected_dp_rank}, but worker not found in "
                                    f"potential_loads. Falling back to local selection."
                                )

                    # Fall back to local selection if SwapCoordinator unavailable or failed
                    if not best_worker:
                        best_worker = min(
                            potential_loads,
                            key=lambda x: x['potential_prefill_tokens']
                        )
                        selection_source = "local"

                    routing = {
                        "worker_id": best_worker['worker_id'],
                        "dp_rank": best_worker['dp_rank']
                    }

                    logger.info(
                        f"Swap-aware routing ({selection_source}): Selected worker "
                        f"{best_worker['worker_id']} (dp_rank={best_worker['dp_rank']}) with "
                        f"{best_worker['potential_prefill_tokens']} prefill tokens, "
                        f"{best_worker['potential_decode_blocks']} decode blocks"
                    )

            except Exception as e:
                logger.error(
                    f"Failed to apply swap-aware routing: {e}. "
                    f"Falling back to default routing."
                )
                # Continue with routing=None to use default behavior
                routing = None

        preprocessed_request = {
            "model": request.get("model", "unknown"),
            "token_ids": request["token_ids"],
            "stop_conditions": request.get("stop_conditions", {}),
            "sampling_options": request.get("sampling_options", {}),
            "output_options": request.get("output_options", {}),
            "eos_token_ids": request.get("eos_token_ids", []),
            "annotations": request.get("annotations", []),
            "routing": routing,
            "router_config_override": request.get("router_config_override"),
            "prefill_result": request.get("prefill_result"),
            "bootstrap_info": request.get("bootstrap_info"),
            "extra_args": request.get("extra_args"),
        }

        # Route and process through KvRouter
        async for worker_output in await self.kv_push_router.generate_from_request(
            preprocessed_request
        ):
            # Wrap worker output into LLMEngineOutput format
            # Worker should return dict with at minimum kv_transfer_params in extra_args
            llm_engine_output = {
                "token_ids": worker_output.get("token_ids", []),
                "tokens": worker_output.get("tokens"),
                "text": worker_output.get("text"),
                "cum_log_probs": worker_output.get("cum_log_probs"),
                "log_probs": worker_output.get("log_probs"),
                "top_logprobs": worker_output.get("top_logprobs"),
                "finish_reason": worker_output.get("finish_reason"),
                "stop_reason": worker_output.get("stop_reason"),
                "index": worker_output.get("index"),
                "disaggregated_params": worker_output.get("disaggregated_params"),
                "extra_args": worker_output.get("extra_args"),
                "completion_usage": worker_output.get("completion_usage"),
            }
            yield llm_engine_output


def parse_args():
    parser = argparse.ArgumentParser(
        description="Dynamo Standalone Router Service: Configurable KV-aware routing for any worker endpoint",
        formatter_class=argparse.RawTextHelpFormatter,
    )

    parser.add_argument(
        "--endpoint",
        type=str,
        required=True,
        help=(
            "Full endpoint path for workers in the format namespace.component.endpoint\n"
            "(e.g., dynamo.prefill.generate for prefill workers)"
        ),
    )

    parser.add_argument(
        "--block-size",
        type=int,
        default=128,
        help="KV cache block size for routing decisions (default: 128)",
    )

    parser.add_argument(
        "--kv-overlap-score-weight",
        type=float,
        default=1.0,
        help="KV Router: Weight for overlap score in worker selection. Higher values prioritize KV cache reuse (default: 1.0)",
    )

    parser.add_argument(
        "--router-temperature",
        type=float,
        default=0.0,
        help="KV Router: Temperature for worker sampling via softmax. Higher values promote more randomness, and 0 fallbacks to deterministic (default: 0.0)",
    )

    parser.add_argument(
        "--no-kv-events",
        action="store_false",
        dest="use_kv_events",
        default=True,
        help="KV Router: Disable KV events. When set, the router predicts cache state based on routing decisions with TTL-based expiration and pruning, rather than receiving events from workers. By default, KV events are enabled.",
    )

    parser.add_argument(
        "--router-replica-sync",
        action="store_true",
        default=False,
        help="KV Router: Enable replica synchronization across multiple router instances. When true, routers will publish and subscribe to events to maintain consistent state (default: False)",
    )

    parser.add_argument(
        "--router-snapshot-threshold",
        type=int,
        default=1000000,
        help="KV Router: Number of messages in stream before triggering a snapshot (default: 1000000)",
    )

    parser.add_argument(
        "--router-reset-states",
        action="store_true",
        dest="router_reset_states",
        default=False,
        help="KV Router: Reset router state on startup, purging stream and object store. By default, states are persisted. WARNING: This can affect existing router replicas (default: False)",
    )

    parser.add_argument(
        "--no-track-active-blocks",
        action="store_false",
        dest="router_track_active_blocks",
        default=True,
        help="KV Router: Disable tracking of active blocks (blocks being used for ongoing generation). By default, active blocks are tracked for load balancing (default: True)",
    )

    parser.add_argument(
        "--track-output-blocks",
        action="store_true",
        dest="router_track_output_blocks",
        default=False,
        help="KV Router: Track output blocks during generation. When enabled, the router adds placeholder blocks as tokens are generated and applies fractional decay based on progress toward expected_output_tokens (default: False)",
    )

    parser.add_argument(
        "--router-ttl-secs",
        type=float,
        default=120.0,
        help="KV Router: TTL for blocks in seconds. Only used when --no-kv-events is set. Controls how long cached blocks are considered valid without explicit events (default: 120.0)",
    )

    parser.add_argument(
        "--router-max-tree-size",
        type=int,
        default=2**20,
        help="KV Router: Maximum tree size before pruning. Only used when --no-kv-events is set. When the indexer tree exceeds this size, pruning is triggered (default: 1048576, which is 2^20)",
    )

    parser.add_argument(
        "--router-prune-target-ratio",
        type=float,
        default=0.8,
        help="KV Router: Target size ratio after pruning (0.0-1.0). Only used when --no-kv-events is set. Determines how aggressively to prune the tree (default: 0.8)",
    )

    parser.add_argument(
        "--swap-aware-routing",
        action="store_true",
        default=False,
        help="Make the router swap-aware (default: False)",
    )

    parser.add_argument(
        "--swap-coordinator-url",
        type=str,
        default=None,
        help=(
            "SwapCoordinator service URL for swap-aware routing decisions "
            "(e.g., http://swap-coordinator-service:8080). If not specified, "
            "uses local KV-cache based selection. Only used when --swap-aware-routing is enabled."
        ),
    )

    parser.add_argument(
        "--swap-coordinator-timeout",
        type=float,
        default=1.0,
        help="Timeout in seconds for SwapCoordinator API calls (default: 1.0)",
    )

    parser.add_argument(
        "--router-namespace",
        type=str,
        default=None,
        help=(
            "Namespace the router registers in (e.g. 'dynamo'). "
            "Decouples the router's own registration namespace from the worker "
            "endpoint namespace. Defaults to the namespace derived from --endpoint."
        ),
    )

    parser.add_argument(
        "--register-model",
        action="store_true",
        default=False,
        help=(
            "Register the router endpoint with the discovery service so the frontend "
            "can route to it. Requires --model-name."
        ),
    )

    parser.add_argument(
        "--model-name",
        type=str,
        default=None,
        help="Model name/path to advertise when using --register-model (e.g. Qwen/Qwen3-0.6B).",
    )

    return parser.parse_args()


@dynamo_worker()
async def worker(runtime: DistributedRuntime):
    """Main worker function for the standalone router service."""

    args = parse_args()

    # Parse endpoint path to get namespace for service registration
    endpoint_parts = args.endpoint.split(".")
    if len(endpoint_parts) != 3:
        raise ValueError(
            f"Invalid endpoint path format: {args.endpoint}. "
            "Expected format: namespace.component.endpoint"
        )
    worker_namespace = endpoint_parts[0]
    namespace = args.router_namespace if args.router_namespace else worker_namespace

    logger.info("Starting Standalone Router Service")
    logger.debug(
        f"Configuration: endpoint={args.endpoint}, block_size={args.block_size}, "
        f"overlap_score_weight={args.kv_overlap_score_weight}, "
        f"router_temperature={args.router_temperature}, "
        f"use_kv_events={args.use_kv_events}, "
        f"router_replica_sync={args.router_replica_sync}, "
        f"router_reset_states={args.router_reset_states}, "
        f"router_track_active_blocks={args.router_track_active_blocks}, "
        f"router_track_output_blocks={args.router_track_output_blocks}, "
        f"router_ttl_secs={args.router_ttl_secs}, "
        f"router_max_tree_size={args.router_max_tree_size}, "
        f"router_prune_target_ratio={args.router_prune_target_ratio}, "
        f"swap_aware_routing={args.swap_aware_routing}, "
        f"swap_coordinator_url={args.swap_coordinator_url}, "
        f"swap_coordinator_timeout={args.swap_coordinator_timeout}"
    )

    # Create KvRouter configuration
    kv_router_config = KvRouterConfig(
        overlap_score_weight=args.kv_overlap_score_weight,
        router_temperature=args.router_temperature,
        use_kv_events=args.use_kv_events,
        router_replica_sync=args.router_replica_sync,
        router_snapshot_threshold=args.router_snapshot_threshold,
        router_reset_states=args.router_reset_states,
        router_track_active_blocks=args.router_track_active_blocks,
        router_track_output_blocks=args.router_track_output_blocks,
        router_ttl_secs=args.router_ttl_secs,
        router_max_tree_size=args.router_max_tree_size,
        router_prune_target_ratio=args.router_prune_target_ratio,
    )

    # Create handler
    handler = StandaloneRouterHandler(
        runtime,
        args.endpoint,
        args.block_size,
        kv_router_config,
        swap_aware_routing=args.swap_aware_routing,
        swap_coordinator_url=args.swap_coordinator_url,
        swap_coordinator_timeout=args.swap_coordinator_timeout,
    )
    await handler.initialize()

    # Expose endpoints — get the generate endpoint then derive the component from it
    generate_endpoint = runtime.endpoint(f"{namespace}.router.generate")
    component = generate_endpoint.component()

    if args.register_model:
        if not args.model_name:
            raise ValueError("--model-name is required when --register-model is set")
        logger.info(f"Registering router endpoint as model '{args.model_name}'")
        await register_model(
            ModelInput.Tokens,
            ModelType.Chat | ModelType.Completions,
            generate_endpoint,
            args.model_name,
            kv_cache_block_size=args.block_size,
        )

    logger.debug("Starting to serve endpoints...")

    # Serve both endpoints concurrently
    try:
        await asyncio.gather(
            generate_endpoint.serve_endpoint(
                handler.generate,
                graceful_shutdown=True,
                metrics_labels=[("service", "router")],
            )
        )
    except Exception as e:
        logger.error(f"Failed to serve endpoint: {e}")
        raise
    finally:
        await handler.cleanup()
        logger.info("Standalone Router Service shutting down")


def main():
    """Entry point for the standalone router service."""
    uvloop.run(worker())


if __name__ == "__main__":
    main()
