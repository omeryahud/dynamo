# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Swap-aware routing for the Dynamo frontend.

When enabled, ranks workers via KvRouter then asks an external
SwapCoordinator to select the best worker based on GPU swap state.
"""

import logging
from typing import Optional

import aiohttp

from dynamo.llm import KvRouter

logger = logging.getLogger(__name__)


class SwapAwareRouter:
    """Wraps a KvRouter with SwapCoordinator-based worker selection."""

    def __init__(
        self,
        router: KvRouter,
        swap_coordinator_url: str,
        swap_coordinator_timeout: float = 1.0,
    ):
        self.router = router
        self.swap_coordinator_url = swap_coordinator_url
        self._session = aiohttp.ClientSession(
            timeout=aiohttp.ClientTimeout(total=swap_coordinator_timeout)
        )
        logger.info("Swap-aware routing enabled: %s", swap_coordinator_url)

    async def select_worker(self, token_ids: list[int]) -> tuple[int, int]:
        """Rank workers via KvRouter, then ask SwapCoordinator to pick one.

        Returns:
            (worker_id, dp_rank)
        """
        ranked = await self.router.rank_workers(token_ids)
        if not ranked:
            raise RuntimeError("No workers available for swap-aware routing")

        candidates = [
            {
                "instance_id": w["worker_id"],
                "dp_rank": w["dp_rank"],
                "potential_prefill_tokens": w["potential_prefill_tokens"],
                "potential_decode_blocks": w["potential_decode_blocks"],
                "logit": w["logit"],
            }
            for w in ranked
        ]

        async with self._session.post(
            f"{self.swap_coordinator_url}/select_worker",
            json={"workers": candidates, "request_id": f"req-{id(token_ids)}"},
        ) as resp:
            if resp.status != 200:
                error = await resp.text()
                raise RuntimeError(
                    f"SwapCoordinator returned {resp.status}: {error}"
                )
            result = await resp.json()
            logger.debug(
                "SwapCoordinator selected: worker_id=%s dp_rank=%s reason=%s",
                result["selected_instance_id"],
                result["selected_dp_rank"],
                result.get("reason", "unknown"),
            )
            return result["selected_instance_id"], result["selected_dp_rank"]

    async def close(self):
        await self._session.close()


def create_swap_router(
    router: KvRouter,
    swap_coordinator_url: Optional[str],
    swap_coordinator_timeout: float = 1.0,
) -> Optional[SwapAwareRouter]:
    """Create a SwapAwareRouter if swap_coordinator_url is set and router is a KvRouter."""
    if not swap_coordinator_url or not isinstance(router, KvRouter):
        return None
    return SwapAwareRouter(router, swap_coordinator_url, swap_coordinator_timeout)
