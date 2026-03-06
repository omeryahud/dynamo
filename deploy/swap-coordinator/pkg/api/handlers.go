package api

import (
	"net/http"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
	"github.com/gin-gonic/gin"
)

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

		// Try to find a warm match
		var selected *WorkerCandidate
		var selectedSwapGroupUUID string
		reason := "no-warm-match"

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
			if swapGroupState.WarmInstanceID == candidate.InstanceID {
				selected = candidate
				selectedSwapGroupUUID = swapGroupUUID
				reason = "warm-model"
				break
			}
		}

		// Fall back to first candidate if no warm match
		if selected == nil {
			selected = &request.Workers[0]
			swapGroupUUID, err := stateManager.GetSwapGroupInstance(selected.InstanceID)
			if err == nil {
				selectedSwapGroupUUID = swapGroupUUID
			}
		}

		// Update the warm instance for the selected worker's swap group
		if selectedSwapGroupUUID != "" {
			stateManager.SetWarmInstance(selectedSwapGroupUUID, selected.InstanceID)
		}

		c.JSON(http.StatusOK, SelectWorkerResponse{
			SelectedInstanceID: selected.InstanceID,
			SelectedWorkerID:   selected.WorkerID,
			SelectedDPRank:     selected.DPRank,
			Reason:             reason,
		})
	}
}
