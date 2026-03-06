package state

import "time"

// WorkerMetadata represents metadata about a registered worker instance
type WorkerMetadata struct {
	// InstanceID is the hashed unique identifier for the worker instance
	InstanceID uint64

	// SwapGroupInstanceUUID is the UUID of the swap group instance this worker belongs to
	SwapGroupInstanceUUID string

	// PodName is the Kubernetes pod name of the worker
	PodName string

	// Namespace is the Kubernetes namespace where the worker pod is running
	Namespace string

	// LastSeenAt is the timestamp when the worker was last seen (registered or heartbeat)
	LastSeenAt time.Time
}

// SwapGroupInstanceState represents the state of a swap group instance
// and tracks all workers belonging to it
type SwapGroupInstanceState struct {
	// SwapGroupInstanceUUID is the unique identifier for this swap group instance
	SwapGroupInstanceUUID string

	// Workers is a list of instance IDs that belong to this swap group instance
	Workers []uint64

	// WarmInstanceID is the instance_id of the worker that was last routed to on this swap group
	WarmInstanceID uint64
}

// AddWorker adds a worker instance ID to the swap group instance state
func (s *SwapGroupInstanceState) AddWorker(instanceID uint64) {
	// Check if worker already exists to avoid duplicates
	for _, w := range s.Workers {
		if w == instanceID {
			return
		}
	}
	s.Workers = append(s.Workers, instanceID)
}

// RemoveWorker removes a worker instance ID from the swap group instance state
func (s *SwapGroupInstanceState) RemoveWorker(instanceID uint64) {
	for i, w := range s.Workers {
		if w == instanceID {
			// Remove by replacing with last element and truncating
			s.Workers[i] = s.Workers[len(s.Workers)-1]
			s.Workers = s.Workers[:len(s.Workers)-1]
			return
		}
	}
}

// HasWorkers returns true if the swap group instance has any registered workers
func (s *SwapGroupInstanceState) HasWorkers() bool {
	return len(s.Workers) > 0
}
