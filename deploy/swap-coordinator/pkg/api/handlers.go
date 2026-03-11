package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
)

var handlerLog = ctrl.Log.WithName("select-worker")

// StateHandler handles GET /state requests
// Returns the full swap group state for visualization
func StateHandler(stateManager *state.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		snapshot := stateManager.Snapshot()

		groups := make([]StateSwapGroup, 0, len(snapshot))
		for uuid, sg := range snapshot {
			workers := make([]StateWorker, 0, len(sg.Workers))
			for _, wid := range sg.Workers {
				sw := StateWorker{
					InstanceID: fmt.Sprintf("%d", wid),
					IsWarm:     wid == sg.WarmInstanceID,
				}
				if meta := stateManager.GetWorkerMetadata(wid); meta != nil {
					sw.PodName = meta.PodName
					sw.Namespace = meta.Namespace
					sw.DGDName = meta.DGDName
				}
				if logit, ok := stateManager.GetWorkerLogit(wid); ok {
					sw.Logit = &logit
				}
				workers = append(workers, sw)
			}
			groups = append(groups, StateSwapGroup{
				SwapGroupUUID: uuid,
				Workers:       workers,
			})
		}

		c.IndentedJSON(http.StatusOK, groups)
	}
}

// DashboardHandler serves the visualization HTML page
func DashboardHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(dashboardHTML))
	}
}

// SetWarmHandler handles PUT /state/warm requests
// Manually sets the warm worker for a swap group
func SetWarmHandler(stateManager *state.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request SetWarmRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: "Invalid request: " + err.Error(),
			})
			return
		}

		instanceID, err := strconv.ParseUint(request.InstanceID, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: "Invalid instance_id: " + err.Error(),
			})
			return
		}

		sg := stateManager.GetSwapGroupState(request.SwapGroupUUID)
		if sg == nil {
			c.JSON(http.StatusNotFound, ErrorResponse{
				Error: "Swap group not found",
			})
			return
		}

		stateManager.SetWarmInstance(request.SwapGroupUUID, instanceID)
		handlerLog.Info("Manually set warm worker",
			"swapGroup", request.SwapGroupUUID,
			"instanceID", instanceID)

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// HealthHandler handles GET /health requests
// Returns the service status and count of discovered workers
func HealthHandler(stateManager *state.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		workers := stateManager.ListWorkers()

		response := HealthResponse{
			Status:            "ok",
			DiscoveredWorkers: len(workers),
		}

		c.JSON(http.StatusOK, response)
	}
}

// DGDsHandler handles GET /dgds requests
// Returns all DGD configurations with current warm counts
func DGDsHandler(stateManager *state.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		configs := stateManager.ListDGDConfigs()

		dgds := make([]StateDGD, 0, len(configs))
		for _, cfg := range configs {
			dgdKey := cfg.Namespace + "/" + cfg.Name
			avgTTFT, sampleCount := stateManager.GetRollingAvgTTFT(dgdKey)
			dgds = append(dgds, StateDGD{
				Name:              cfg.Name,
				Namespace:         cfg.Namespace,
				MinWarmWorkers:    cfg.MinWarmWorkers,
				MaxWarmWorkers:    cfg.MaxWarmWorkers,
				CurrentWarm:       stateManager.CountWarmWorkersForDGD(cfg.Name, cfg.Namespace),
				TTFTThresholdMS:   cfg.TTFTThresholdMS,
				TTFTWindowSeconds: cfg.TTFTWindowSeconds,
				AvgTTFTMS:         avgTTFT,
				TTFTSampleCount:   sampleCount,
				TTFTExceeded:      stateManager.IsTTFTExceeded(cfg.Name, cfg.Namespace),
			})
		}

		c.JSON(http.StatusOK, dgds)
	}
}

var dgdGVR = schema.GroupVersionResource{
	Group:    "nvidia.com",
	Version:  "v1alpha1",
	Resource: "dynamographdeployments",
}

// UpdateDGDHandler handles PUT /dgds requests
// Updates the min/max warm worker annotations on the DGD resource in Kubernetes
// and also updates the in-memory state manager
func UpdateDGDHandler(stateManager *state.Manager, dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request UpdateDGDRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: "Invalid request: " + err.Error(),
			})
			return
		}

		if request.MinWarmWorkers < 0 || request.MaxWarmWorkers < 0 {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: "min_warm_workers and max_warm_workers must be >= 0",
			})
			return
		}

		if request.MaxWarmWorkers > 0 && request.MinWarmWorkers > request.MaxWarmWorkers {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: "min_warm_workers cannot exceed max_warm_workers",
			})
			return
		}

		// Patch the DGD annotations in Kubernetes
		annotations := map[string]string{
			"swap-coordinator/min-warm-workers": strconv.Itoa(request.MinWarmWorkers),
			"swap-coordinator/max-warm-workers": strconv.Itoa(request.MaxWarmWorkers),
		}
		if request.TTFTThresholdMS > 0 {
			annotations["swap-coordinator/ttft-threshold-ms"] = fmt.Sprintf("%.1f", request.TTFTThresholdMS)
		} else {
			annotations["swap-coordinator/ttft-threshold-ms"] = "0"
		}
		if request.TTFTWindowSeconds > 0 {
			annotations["swap-coordinator/ttft-window-seconds"] = strconv.Itoa(request.TTFTWindowSeconds)
		}
		patch := map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": annotations,
			},
		}
		patchBytes, err := json.Marshal(patch)
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error: "Failed to marshal patch: " + err.Error(),
			})
			return
		}

		_, err = dynamicClient.Resource(dgdGVR).Namespace(request.Namespace).Patch(
			c.Request.Context(),
			request.Name,
			k8stypes.MergePatchType,
			patchBytes,
			metav1.PatchOptions{},
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error: "Failed to patch DGD annotations: " + err.Error(),
			})
			return
		}

		// Also update in-memory state immediately
		stateManager.SetDGDConfig(request.Name, request.Namespace, request.MinWarmWorkers, request.MaxWarmWorkers, request.TTFTThresholdMS, request.TTFTWindowSeconds)

		handlerLog.Info("Updated DGD config",
			"dgdName", request.Name,
			"namespace", request.Namespace,
			"minWarm", request.MinWarmWorkers,
			"maxWarm", request.MaxWarmWorkers,
			"ttftThresholdMS", request.TTFTThresholdMS,
			"ttftWindowSeconds", request.TTFTWindowSeconds)

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// SelectWorkerHandler handles POST /select_worker requests
// Implements swap-group-aware worker selection: prefers workers whose model
// is already warm on their swap-group-instance.
// Enforces DGD min/max warm worker annotations.
func SelectWorkerHandler(stateManager *state.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request SelectWorkerRequest

		// Validate request body
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: "Invalid request: " + err.Error(),
			})
			return
		}

		// Validate that workers list is not empty
		if len(request.Workers) == 0 {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: "Invalid request: workers list cannot be empty",
			})
			return
		}

		logger := handlerLog.WithValues("requestID", request.RequestID, "candidates", len(request.Workers))

		// Store latest logits from the router for dashboard display
		logits := make(map[uint64]float64, len(request.Workers))
		for _, w := range request.Workers {
			if w.Logit != 0 {
				logits[w.InstanceID] = w.Logit
			}
		}
		if len(logits) > 0 {
			stateManager.UpdateWorkerLogits(logits)
		}

		// Identify DGD from the first registered candidate
		var dgdName, dgdNamespace string
		var dgdConfig *state.DGDConfig
		warmCount := 0
		for _, w := range request.Workers {
			dgdName, dgdNamespace = stateManager.GetWorkerDGD(w.InstanceID)
			if dgdName != "" {
				break
			}
		}
		if dgdName != "" {
			dgdConfig = stateManager.GetDGDConfig(dgdName, dgdNamespace)
			warmCount = stateManager.CountWarmWorkersForDGD(dgdName, dgdNamespace)
		}

		maxWarm := 0
		minWarm := 0
		if dgdConfig != nil {
			maxWarm = dgdConfig.MaxWarmWorkers
			minWarm = dgdConfig.MinWarmWorkers
		}

		// When max=0, this DGD must never warm a swap group.
		// Pick the first candidate without updating warm state.
		zeroMax := dgdConfig != nil && dgdConfig.MaxWarmWorkers == 0

		// Tier 1: Try to find a warm match
		// Skip if below min-warm — force Tier 2 to warm up additional swap groups
		var selected *WorkerCandidate
		var selectedSwapGroupUUID string
		reason := "no-warm-match"
		belowMin := minWarm > 0 && warmCount < minWarm

		// Check if TTFT threshold is exceeded and we should warm more workers
		ttftWantsMoreWarm := false
		if dgdConfig != nil && !belowMin && warmCount < maxWarm {
			ttftWantsMoreWarm = stateManager.IsTTFTExceeded(dgdName, dgdNamespace)
		}
		needMoreWarm := belowMin || ttftWantsMoreWarm

		if zeroMax {
			// max=0 means this DGD is not allowed to run. Reject the request.
			logger.Info("DGD max_warm_workers=0, rejecting request",
				"dgdName", dgdName)
			c.JSON(http.StatusForbidden, ErrorResponse{
				Error: fmt.Sprintf("DGD %s/%s has max_warm_workers=0, no workers allowed", dgdNamespace, dgdName),
			})
			return
		} else if !needMoreWarm {
			for i := range request.Workers {
				candidate := &request.Workers[i]
				swapGroupUUID, err := stateManager.GetSwapGroupInstance(candidate.InstanceID)
				if err != nil {
					logger.V(1).Info("Candidate not registered with coordinator",
						"instanceID", candidate.InstanceID, "dpRank", candidate.DPRank)
					continue
				}
				swapGroupState := stateManager.GetSwapGroupState(swapGroupUUID)
				if swapGroupState != nil && swapGroupState.WarmInstanceID == candidate.InstanceID {
					selected = candidate
					selectedSwapGroupUUID = swapGroupUUID
					reason = "warm-model"
					logger.Info("Selected warm worker",
						"instanceID", candidate.InstanceID,
						"dpRank", candidate.DPRank,
						"swapGroup", swapGroupUUID)
					break
				}
			}
		} else {
			skipReason := "below-min"
			if ttftWantsMoreWarm {
				skipReason = "ttft-exceeded"
			}
			logger.Info("Skipping Tier 1 to warm additional workers",
				"dgdName", dgdName, "warmCount", warmCount, "minWarm", minWarm,
				"reason", skipReason)
		}

		// canEvict checks whether evicting the warm instance from a swap group
		// is safe — i.e., it won't drop the victim's DGD below its min-warm
		// and the victim isn't already under TTFT pressure.
		// Returns true if the swap group is cold, the warm instance is one of
		// our own candidates, or the victim's DGD can afford to lose a warm worker.
		canEvict := func(swapGroupState *state.SwapGroupInstanceState) bool {
			if swapGroupState.WarmInstanceID == 0 {
				return true // already cold
			}
			victimDGDName, victimDGDNS := stateManager.GetWorkerDGD(swapGroupState.WarmInstanceID)
			if victimDGDName == "" {
				return true // no DGD, safe to evict
			}
			if victimDGDName == dgdName && victimDGDNS == dgdNamespace {
				return true // same DGD, not losing a warm worker
			}
			// Check victim's min-warm constraint
			victimConfig := stateManager.GetDGDConfig(victimDGDName, victimDGDNS)
			victimWarmCount := stateManager.CountWarmWorkersForDGD(victimDGDName, victimDGDNS)

			if victimConfig != nil && victimConfig.MinWarmWorkers > 0 && victimWarmCount <= victimConfig.MinWarmWorkers {
				return false // would violate victim's min-warm
			}

			// Don't evict from a DGD that's already under TTFT pressure
			if stateManager.IsTTFTExceeded(victimDGDName, victimDGDNS) {
				return false
			}

			return true
		}

		// Tier 2: Fall back — prefer cold swap groups, with max-warm enforcement
		if selected == nil {
			// Build set of our candidate instance IDs for quick lookup
			candidateSet := make(map[uint64]bool, len(request.Workers))
			for _, w := range request.Workers {
				candidateSet[w.InstanceID] = true
			}

			// If max > 0 and warmCount >= max, skip cold-swap-group tier
			// and only allow reusing a swap group that already has one of our DGD's workers warm
			maxReached := maxWarm > 0 && warmCount >= maxWarm

			if maxReached {
				// Find a candidate whose swap group already has one of our DGD's workers warm
				for i := range request.Workers {
					candidate := &request.Workers[i]
					swapGroupUUID, err := stateManager.GetSwapGroupInstance(candidate.InstanceID)
					if err != nil {
						continue
					}
					swapGroupState := stateManager.GetSwapGroupState(swapGroupUUID)
					if swapGroupState == nil {
						continue
					}
					if swapGroupState.WarmInstanceID != 0 {
						warmDGDName, warmDGDNS := stateManager.GetWorkerDGD(swapGroupState.WarmInstanceID)
						if warmDGDName == dgdName && warmDGDNS == dgdNamespace {
							selected = candidate
							selectedSwapGroupUUID = swapGroupUUID
							reason = "max-warm-reuse"
							break
						}
					}
				}
			} else {
				// Pass 1: prefer truly cold swap groups, or same-model groups when not needing more warm.
				// When we need more warm workers, same-model reuse doesn't increase warm count,
				// so skip it — we need to pick a swap group where we'd actually create a NEW warm instance.
				for i := range request.Workers {
					candidate := &request.Workers[i]
					swapGroupUUID, err := stateManager.GetSwapGroupInstance(candidate.InstanceID)
					if err != nil {
						continue
					}
					swapGroupState := stateManager.GetSwapGroupState(swapGroupUUID)
					if swapGroupState == nil {
						continue
					}
					if swapGroupState.WarmInstanceID == 0 {
						// Truly cold — always good
						selected = candidate
						selectedSwapGroupUUID = swapGroupUUID
						reason = "cold-swap-group"
						if ttftWantsMoreWarm {
							reason = "cold-swap-group-ttft"
						}
						break
					}
					if !needMoreWarm && candidateSet[swapGroupState.WarmInstanceID] {
						// Same model already warm — reuse only if not trying to grow warm count
						selected = candidate
						selectedSwapGroupUUID = swapGroupUUID
						reason = "cold-swap-group"
						break
					}
				}
				// Pass 2: if no cold/same-model found, pick one where eviction is safe
				if selected == nil {
					for i := range request.Workers {
						candidate := &request.Workers[i]
						swapGroupUUID, err := stateManager.GetSwapGroupInstance(candidate.InstanceID)
						if err != nil {
							continue
						}
						swapGroupState := stateManager.GetSwapGroupState(swapGroupUUID)
						if swapGroupState == nil {
							continue
						}
						if canEvict(swapGroupState) {
							selected = candidate
							selectedSwapGroupUUID = swapGroupUUID
							reason = "safe-eviction"
							break
						}
					}
				}
			}

			// Tier 3: Last resort — no safe eviction possible, must evict anyway
			if selected == nil {
				// If we needed more warm workers but couldn't find a safe eviction,
				// fall back to Tier 1 (use existing warm worker) rather than
				// violating another DGD's min.
				if needMoreWarm {
					for i := range request.Workers {
						candidate := &request.Workers[i]
						swapGroupUUID, err := stateManager.GetSwapGroupInstance(candidate.InstanceID)
						if err != nil {
							continue
						}
						swapGroupState := stateManager.GetSwapGroupState(swapGroupUUID)
						if swapGroupState != nil && swapGroupState.WarmInstanceID == candidate.InstanceID {
							selected = candidate
							selectedSwapGroupUUID = swapGroupUUID
							reason = "warm-model-min-blocked"
							break
						}
					}
				}
				// Absolute last resort — try to reuse a swap group that already
				// has one of our own workers warm (avoids evicting a victim under pressure).
				if selected == nil {
					for i := range request.Workers {
						candidate := &request.Workers[i]
						swapGroupUUID, err := stateManager.GetSwapGroupInstance(candidate.InstanceID)
						if err != nil {
							continue
						}
						swapGroupState := stateManager.GetSwapGroupState(swapGroupUUID)
						if swapGroupState != nil && swapGroupState.WarmInstanceID == candidate.InstanceID {
							selected = candidate
							selectedSwapGroupUUID = swapGroupUUID
							reason = "reuse-own-warm"
							break
						}
					}
				}
			}

			logger.Info("No warm match, selected fallback",
				"reason", reason,
				"instanceID", selected.InstanceID,
				"dpRank", selected.DPRank,
				"swapGroup", selectedSwapGroupUUID,
				"dgdName", dgdName,
				"warmCount", warmCount,
				"minWarm", minWarm,
				"maxWarm", maxWarm)
		}

		if selected == nil {
			logger.Info("No worker selected, all eviction targets are protected",
				"dgdName", dgdName,
				"warmCount", warmCount,
				"minWarm", minWarm,
				"maxWarm", maxWarm)
			c.JSON(http.StatusForbidden, gin.H{
				"error":   "no safe worker available",
				"dgdName": dgdName,
			})
			return
		}

		// Update the warm instance for the selected worker's swap group
		// Skip if max=0 — this DGD must never warm a swap group
		if selectedSwapGroupUUID != "" && !zeroMax {
			stateManager.SetWarmInstance(selectedSwapGroupUUID, selected.InstanceID)
		}

		c.JSON(http.StatusOK, SelectWorkerResponse{
			SelectedInstanceID: selected.InstanceID,
			SelectedDPRank:     selected.DPRank,
			Reason:             reason,
			DGDName:            dgdName,
			WarmCount:          warmCount,
			MinWarm:            minWarm,
			MaxWarm:            maxWarm,
		})
	}
}
