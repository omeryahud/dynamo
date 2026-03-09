package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
	"github.com/gin-gonic/gin"
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
				}
				workers = append(workers, sw)
			}
			groups = append(groups, StateSwapGroup{
				SwapGroupUUID: uuid,
				Workers:       workers,
			})
		}

		c.JSON(http.StatusOK, groups)
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

// SelectWorkerHandler handles POST /select_worker requests
// Implements swap-group-aware worker selection: prefers workers whose model
// is already warm on their swap-group-instance.
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

		// Try to find a warm match
		var selected *WorkerCandidate
		var selectedSwapGroupUUID string
		reason := "no-warm-match"

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

		// Fall back: prefer a swap group with no warm instance (cold) to avoid
		// evicting another model's warm worker and causing unnecessary swaps.
		if selected == nil {
			// Build set of our candidate instance IDs for quick lookup
			candidateSet := make(map[uint64]bool, len(request.Workers))
			for _, w := range request.Workers {
				candidateSet[w.InstanceID] = true
			}

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
				// Prefer a swap group that is cold (no warm instance) or where
				// the warm instance is one of our own candidates (same model)
				if swapGroupState.WarmInstanceID == 0 || candidateSet[swapGroupState.WarmInstanceID] {
					selected = candidate
					selectedSwapGroupUUID = swapGroupUUID
					reason = "cold-swap-group"
					break
				}
			}

			// Last resort: all swap groups have a different model warm, must evict
			if selected == nil {
				selected = &request.Workers[0]
				swapGroupUUID, err := stateManager.GetSwapGroupInstance(selected.InstanceID)
				if err == nil {
					selectedSwapGroupUUID = swapGroupUUID
				}
				reason = "forced-swap"
			}

			logger.Info("No warm match, selected fallback",
				"reason", reason,
				"instanceID", selected.InstanceID,
				"dpRank", selected.DPRank,
				"swapGroup", selectedSwapGroupUUID)
		}

		// Update the warm instance for the selected worker's swap group
		if selectedSwapGroupUUID != "" {
			stateManager.SetWarmInstance(selectedSwapGroupUUID, selected.InstanceID)
		}

		c.JSON(http.StatusOK, SelectWorkerResponse{
			SelectedInstanceID: selected.InstanceID,
			SelectedDPRank:     selected.DPRank,
			Reason:             reason,
		})
	}
}
