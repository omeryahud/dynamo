package api

// WorkerCandidate represents a worker instance that could be selected for a request
type WorkerCandidate struct {
	InstanceID             uint64 `json:"instance_id"`
	DPRank                 int    `json:"dp_rank"`
	PotentialPrefillTokens int    `json:"potential_prefill_tokens"`
	PotentialDecodeBlocks  int      `json:"potential_decode_blocks"`
	Logit                  float64 `json:"logit,omitempty"`
}

// SelectWorkerRequest contains the list of worker candidates and request metadata
type SelectWorkerRequest struct {
	Workers   []WorkerCandidate `json:"workers" binding:"required"`
	RequestID string            `json:"request_id" binding:"required"`
}

// SelectWorkerResponse contains the selected worker information and selection reason
type SelectWorkerResponse struct {
	SelectedInstanceID uint64 `json:"selected_instance_id"`
	SelectedDPRank     int    `json:"selected_dp_rank"`
	Reason             string `json:"reason"`
}

// HealthResponse contains the health status of the swap coordinator
type HealthResponse struct {
	Status            string `json:"status"`
	DiscoveredWorkers int    `json:"discovered_workers"`
}

// StateWorker represents a worker in the state snapshot
// InstanceID is serialized as a string to preserve precision in JavaScript
type StateWorker struct {
	InstanceID string `json:"instance_id"`
	PodName    string `json:"pod_name"`
	Namespace  string `json:"namespace"`
	IsWarm     bool   `json:"is_warm"`
}

// StateSwapGroup represents a swap group in the state snapshot
type StateSwapGroup struct {
	SwapGroupUUID string        `json:"swap_group_uuid"`
	Workers       []StateWorker `json:"workers"`
}

// SetWarmRequest contains the swap group and instance to mark as warm
// InstanceID is a string to preserve precision from JavaScript
type SetWarmRequest struct {
	SwapGroupUUID string `json:"swap_group_uuid" binding:"required"`
	InstanceID    string `json:"instance_id" binding:"required"`
}

// ErrorResponse contains error information for failed requests
type ErrorResponse struct {
	Error string `json:"error"`
}
