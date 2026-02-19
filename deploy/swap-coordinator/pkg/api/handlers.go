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
// Currently returns 501 Not Implemented as this is a Phase 2 feature
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

		// Return 501 Not Implemented for Phase 1
		c.JSON(http.StatusNotImplemented, ErrorResponse{
			Error: "Worker selection not implemented yet (Phase 2)",
		})
	}
}
