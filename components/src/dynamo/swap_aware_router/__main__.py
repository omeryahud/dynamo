# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""
Swap-Aware KV Router Service

Usage: python -m dynamo.swap_aware_router --endpoint <namespace.component.endpoint> [args]

Extends the standard Dynamo KV-aware router with GPU swap-awareness.
When --swap-aware-routing is enabled, the router consults an external
SwapCoordinator service to select workers based on GPU swap state,
preferring workers whose model is already warm on their GPU.
"""

import asyncio
import logging
from typing import Optional

import aiohttp
import uvloop

from dynamo.llm import KvRouter, KvRouterConfig, ModelInput, ModelType, register_model
from dynamo.router.args import DynamoRouterConfig, DynamoRouterArgGroup, build_kv_router_config
from dynamo.common.configuration.utils import add_argument, add_negatable_bool_argument
from dynamo.runtime import Client, DistributedRuntime, dynamo_worker
from dynamo.runtime.logging import configure_dynamo_logging

configure_dynamo_logging()
logger = logging.getLogger(__name__)


class SwapCoordinatorRejectedError(Exception):
    """Raised when the SwapCoordinator rejects a request (e.g., max_warm_workers=0)."""
    pass


class SwapAwareRouterHandler:
    """Handles routing requests to workers using KV-aware + swap-aware routing."""

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
        self.kv_router: Optional[KvRouter] = None
        self.worker_client: Optional[Client] = None
        self._http_session: Optional[aiohttp.ClientSession] = None

    async def initialize(self):
        """Initialize the KV router for workers."""
        try:
            parts = self.worker_endpoint_path.split(".")
            if len(parts) != 3:
                raise ValueError(
                    f"Invalid endpoint path format: {self.worker_endpoint_path}. "
                    "Expected format: namespace.component.endpoint"
                )
            namespace, component, endpoint = parts

            worker_endpoint = self.runtime.endpoint(
                f"{namespace}.{component}.{endpoint}"
            )
            self.worker_client = await worker_endpoint.client()

            self.kv_router = KvRouter(
                endpoint=worker_endpoint,
                block_size=self.block_size,
                kv_router_config=self.kv_router_config,
            )

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

    async def _call_swap_coordinator(self, workers: list, request_id: str) -> dict:
        """Call SwapCoordinator service to select the best worker."""
        if not self.swap_coordinator_url or not self._http_session:
            raise RuntimeError("SwapCoordinator URL not configured")

        worker_candidates = [
            {
                "instance_id": w["worker_id"],
                "dp_rank": w["dp_rank"],
                "potential_prefill_tokens": w["potential_prefill_tokens"],
                "potential_decode_blocks": w["potential_decode_blocks"],
                "logit": w["logit"],
            }
            for w in workers
        ]

        payload = {"workers": worker_candidates, "request_id": request_id}
        url = f"{self.swap_coordinator_url}/select_worker"

        async with self._http_session.post(url, json=payload) as response:
            if response.status == 200:
                result = await response.json()
                logger.debug(
                    f"SwapCoordinator selected: instance_id={result['selected_instance_id']}, "
                    f"dp_rank={result['selected_dp_rank']}, reason={result['reason']}"
                )
                return result
            elif response.status in (403, 503):
                error_text = await response.text()
                logger.warning(
                    f"SwapCoordinator rejected request ({response.status}): {error_text}"
                )
                raise SwapCoordinatorRejectedError(error_text)
            else:
                error_text = await response.text()
                raise RuntimeError(
                    f"SwapCoordinator returned status {response.status}: {error_text}"
                )

    async def _apply_swap_aware_routing(self, request, routing):
        """Apply swap-aware routing if enabled and no explicit routing is set."""
        if not self.swap_aware_routing or routing is not None:
            return routing

        token_ids = request.get("token_ids", [])
        ranked_workers = await self.kv_router.rank_workers(token_ids)

        if not ranked_workers:
            raise RuntimeError("Swap-aware routing enabled but no workers available")

        request_id = request.get("request_id", f"req-{id(request)}")
        coordinator_result = await self._call_swap_coordinator(
            ranked_workers, request_id
        )

        selected_instance_id = coordinator_result["selected_instance_id"]
        selected_dp_rank = coordinator_result["selected_dp_rank"]

        best_worker = None
        for worker in ranked_workers:
            if (worker["worker_id"] == selected_instance_id and
                    worker["dp_rank"] == selected_dp_rank):
                best_worker = worker
                break

        if not best_worker:
            raise RuntimeError(
                f"SwapCoordinator selected instance_id={selected_instance_id} "
                f"dp_rank={selected_dp_rank}, but worker not found in ranked_workers"
            )

        logger.info(
            f"Swap-aware routing: Selected worker "
            f"{best_worker['worker_id']} (dp_rank={best_worker['dp_rank']}) with "
            f"{best_worker['potential_prefill_tokens']} prefill tokens, "
            f"{best_worker['potential_decode_blocks']} decode blocks"
        )

        return {
            "backend_instance_id": best_worker["worker_id"],
            "dp_rank": best_worker["dp_rank"],
        }

    async def generate(self, request):
        """Generate tokens using the KV-aware router with optional swap-awareness."""
        if self.kv_router is None:
            logger.error("KvRouter not initialized - cannot process request")
            raise RuntimeError("Router not initialized")

        routing = request.get("routing")
        dp_rank = request.get("dp_rank")
        if routing is None and dp_rank is not None:
            routing = {"dp_rank": dp_rank}

        # Apply swap-aware routing (no-op if disabled or routing already set)
        routing = await self._apply_swap_aware_routing(request, routing)

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

        async for worker_output in await self.kv_router.generate_from_request(
            preprocessed_request  # type: ignore[arg-type]
        ):
            llm_engine_output = {
                "token_ids": worker_output.get("token_ids", []),  # type: ignore[attr-defined]
                "tokens": worker_output.get("tokens"),  # type: ignore[attr-defined]
                "text": worker_output.get("text"),  # type: ignore[attr-defined]
                "cum_log_probs": worker_output.get("cum_log_probs"),  # type: ignore[attr-defined]
                "log_probs": worker_output.get("log_probs"),  # type: ignore[attr-defined]
                "top_logprobs": worker_output.get("top_logprobs"),  # type: ignore[attr-defined]
                "finish_reason": worker_output.get("finish_reason"),  # type: ignore[attr-defined]
                "stop_reason": worker_output.get("stop_reason"),  # type: ignore[attr-defined]
                "index": worker_output.get("index"),  # type: ignore[attr-defined]
                "disaggregated_params": worker_output.get("disaggregated_params"),  # type: ignore[attr-defined]
                "extra_args": worker_output.get("extra_args"),  # type: ignore[attr-defined]
                "completion_usage": worker_output.get("completion_usage"),  # type: ignore[attr-defined]
            }
            yield llm_engine_output

    async def best_worker_id(self, token_ids, router_config_override=None):
        """Get the best worker ID for a given set of tokens without routing."""
        if self.kv_router is None:
            logger.error("KvRouter not initialized - cannot get best worker")
            raise RuntimeError("Router not initialized")

        (worker_id, _dp_rank, _overlap_blocks) = await self.kv_router.best_worker(
            token_ids, router_config_override
        )
        yield worker_id


class SwapAwareRouterConfig(DynamoRouterConfig):
    """Configuration for the swap-aware router (extends DynamoRouterConfig)."""

    swap_aware_routing: bool
    swap_coordinator_url: Optional[str]
    swap_coordinator_timeout: float
    router_namespace: Optional[str]
    register_model: bool
    model_name: Optional[str]


class SwapAwareRouterArgGroup:
    """CLI argument group for swap-aware router options."""

    def add_arguments(self, parser) -> None:
        g = parser.add_argument_group("Swap-Aware Router Options")

        add_negatable_bool_argument(
            g,
            flag_name="--swap-aware-routing",
            env_var="DYN_SWAP_AWARE_ROUTING",
            default=False,
            help="Enable swap-aware routing via external SwapCoordinator.",
        )

        add_argument(
            g,
            flag_name="--swap-coordinator-url",
            env_var="DYN_SWAP_COORDINATOR_URL",
            default=None,
            help=(
                "SwapCoordinator service URL (e.g., http://swap-coordinator-service:8080). "
                "Required when --swap-aware-routing is enabled."
            ),
        )

        add_argument(
            g,
            flag_name="--swap-coordinator-timeout",
            env_var="DYN_SWAP_COORDINATOR_TIMEOUT",
            default=1.0,
            help="Timeout in seconds for SwapCoordinator API calls.",
            arg_type=float,
        )

        add_argument(
            g,
            flag_name="--router-namespace",
            env_var="DYN_ROUTER_NAMESPACE",
            default=None,
            help=(
                "Namespace the router registers in. Decouples the router's own registration "
                "namespace from the worker endpoint namespace. Defaults to the namespace "
                "derived from --endpoint."
            ),
        )

        add_negatable_bool_argument(
            g,
            flag_name="--register-model",
            env_var="DYN_REGISTER_MODEL",
            default=False,
            help=(
                "Register the router endpoint with the discovery service so the frontend "
                "can route to it. Requires --model-name."
            ),
        )

        add_argument(
            g,
            flag_name="--model-name",
            env_var="DYN_MODEL_NAME",
            default=None,
            help="Model name/path to advertise when using --register-model (e.g. Qwen/Qwen3-0.6B).",
        )


def parse_args(argv=None) -> SwapAwareRouterConfig:
    """Parse CLI arguments: standard router args + swap-aware extensions."""
    import argparse

    parser = argparse.ArgumentParser(
        description="Dynamo Swap-Aware Router Service: KV-aware routing with GPU swap coordination",
        formatter_class=argparse.RawTextHelpFormatter,
    )

    # Standard router args (endpoint, block-size, all 17 KV router params)
    DynamoRouterArgGroup().add_arguments(parser)

    # Swap-aware extensions
    SwapAwareRouterArgGroup().add_arguments(parser)

    args = parser.parse_args(argv)
    config = SwapAwareRouterConfig.from_cli_args(args)
    config.validate()
    return config


@dynamo_worker()
async def worker(runtime: DistributedRuntime):
    """Main worker function for the swap-aware router service."""

    config = parse_args()
    namespace = config.router_namespace if config.router_namespace else config.namespace

    logger.info("Starting Swap-Aware Router Service")
    logger.debug(
        f"Configuration: endpoint={config.endpoint}, "
        f"router_block_size={config.router_block_size}, "
        f"overlap_score_weight={config.overlap_score_weight}, "
        f"router_temperature={config.router_temperature}, "
        f"use_kv_events={config.use_kv_events}, "
        f"durable_kv_events={config.durable_kv_events}, "
        f"router_replica_sync={config.router_replica_sync}, "
        f"router_reset_states={config.router_reset_states}, "
        f"router_track_active_blocks={config.router_track_active_blocks}, "
        f"router_track_output_blocks={config.router_track_output_blocks}, "
        f"router_assume_kv_reuse={config.router_assume_kv_reuse}, "
        f"router_ttl_secs={config.router_ttl_secs}, "
        f"router_max_tree_size={config.router_max_tree_size}, "
        f"router_prune_target_ratio={config.router_prune_target_ratio}, "
        f"router_queue_threshold={config.router_queue_threshold}, "
        f"router_event_threads={config.router_event_threads}, "
        f"router_queue_policy={config.router_queue_policy}, "
        f"remote_indexer_component={config.remote_indexer_component}, "
        f"swap_aware_routing={config.swap_aware_routing}, "
        f"swap_coordinator_url={config.swap_coordinator_url}, "
        f"swap_coordinator_timeout={config.swap_coordinator_timeout}"
    )

    kv_router_config = build_kv_router_config(config)

    # Expose endpoints and register model BEFORE initializing the handler.
    # handler.initialize() blocks on worker discovery (await worker_endpoint.client()),
    # so register_model must happen first to make the model visible to the frontend.
    generate_endpoint = runtime.endpoint(f"{namespace}.router.generate")
    best_worker_endpoint = runtime.endpoint(f"{namespace}.router.best_worker_id")

    if config.register_model:
        if not config.model_name:
            raise ValueError("--model-name is required when --register-model is set")
        logger.info(f"Registering router endpoint as model '{config.model_name}'")
        await register_model(
            ModelInput.Tokens,
            ModelType.Chat | ModelType.Completions,
            generate_endpoint,
            config.model_name,
            kv_cache_block_size=config.router_block_size,
        )

    # Create handler — initialize() blocks until workers are discovered
    handler = SwapAwareRouterHandler(
        runtime,
        config.endpoint,
        config.router_block_size,
        kv_router_config,
        swap_aware_routing=config.swap_aware_routing,
        swap_coordinator_url=config.swap_coordinator_url,
        swap_coordinator_timeout=config.swap_coordinator_timeout,
    )
    await handler.initialize()

    logger.debug("Starting to serve endpoints...")

    try:
        await asyncio.gather(
            generate_endpoint.serve_endpoint(
                handler.generate,
                graceful_shutdown=True,
                metrics_labels=[("service", "router")],
            ),
            best_worker_endpoint.serve_endpoint(
                handler.best_worker_id,
                graceful_shutdown=True,
                metrics_labels=[("service", "router")],
            ),
        )
    except Exception as e:
        logger.error(f"Failed to serve endpoint: {e}")
        raise
    finally:
        await handler.cleanup()
        logger.info("Swap-Aware Router Service shutting down")


def main():
    """Entry point for the swap-aware router service."""
    uvloop.run(worker())


if __name__ == "__main__":
    main()
