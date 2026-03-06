package api

// WorkerCandidate represents a worker instance that could be selected for a request
type WorkerCandidate struct {
	InstanceID             uint64 `json:"instance_id"`
	WorkerID               string `json:"worker_id"`
	DPRank                 int    `json:"dp_rank"`
	PotentialPrefillTokens int    `json:"potential_prefill_tokens"`
	PotentialDecodeBlocks  int    `json:"potential_decode_blocks"`
}

// SelectWorkerRequest contains the list of worker candidates and request metadata
type SelectWorkerRequest struct {
	Workers   []WorkerCandidate `json:"workers" binding:"required"`
	RequestID string            `json:"request_id" binding:"required"`
}

// SelectWorkerResponse contains the selected worker information and selection reason
type SelectWorkerResponse struct {
	SelectedInstanceID uint64 `json:"selected_instance_id"`
	SelectedWorkerID   string `json:"selected_worker_id"`
	SelectedDPRank     int    `json:"selected_dp_rank"`
	Reason             string `json:"reason"`
}

// HealthResponse contains the health status of the swap coordinator
type HealthResponse struct {
	Status            string `json:"status"`
	DiscoveredWorkers int    `json:"discovered_workers"`
}

// ErrorResponse contains error information for failed requests
type ErrorResponse struct {
	Error string `json:"error"`
}
