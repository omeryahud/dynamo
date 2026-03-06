package state

import (
	"fmt"
	"sync"
	"time"
)

// Manager manages the state of worker instances and their swap group assignments
// It provides thread-safe operations for registering, unregistering, and querying workers
type Manager struct {
	// workerMetadata maps instance IDs to their metadata
	workerMetadata map[uint64]*WorkerMetadata

	// swapGroupInstances maps swap group instance UUIDs to their state
	swapGroupInstances map[string]*SwapGroupInstanceState

	// instanceToSwapGroup maps instance IDs to their swap group instance UUID
	// This provides O(1) lookup for GetSwapGroupInstance
	instanceToSwapGroup map[uint64]string

	// podNameToInstanceID maps pod names to their instance IDs
	// This provides O(1) reverse lookup for UnregisterWorkerByPodName
	podNameToInstanceID map[string]uint64

	// mu protects all maps from concurrent access
	mu sync.RWMutex
}

// NewManager creates and initializes a new state Manager
func NewManager() *Manager {
	return &Manager{
		workerMetadata:      make(map[uint64]*WorkerMetadata),
		swapGroupInstances:  make(map[string]*SwapGroupInstanceState),
		instanceToSwapGroup: make(map[uint64]string),
		podNameToInstanceID: make(map[string]uint64),
	}
}

// RegisterWorker registers a new worker instance or updates an existing one
// It associates the worker with a swap group instance and updates the last seen timestamp
func (m *Manager) RegisterWorker(instanceID uint64, swapGroupInstanceUUID, podName, namespace string) error {
	if instanceID == 0 {
		return fmt.Errorf("instanceID cannot be zero")
	}
	if swapGroupInstanceUUID == "" {
		return fmt.Errorf("swapGroupInstanceUUID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Check if worker is already registered to a different swap group
	if existingSwapGroupUUID, exists := m.instanceToSwapGroup[instanceID]; exists {
		if existingSwapGroupUUID != swapGroupInstanceUUID {
			// Worker is moving to a different swap group, clean up old association
			if oldSwapGroup, ok := m.swapGroupInstances[existingSwapGroupUUID]; ok {
				oldSwapGroup.RemoveWorker(instanceID)
				// Clean up empty swap group states
				if !oldSwapGroup.HasWorkers() {
					delete(m.swapGroupInstances, existingSwapGroupUUID)
				}
			}
		}
	}

	// Update or create worker metadata
	if metadata, exists := m.workerMetadata[instanceID]; exists {
		// Update existing worker
		metadata.SwapGroupInstanceUUID = swapGroupInstanceUUID
		metadata.PodName = podName
		metadata.Namespace = namespace
		metadata.LastSeenAt = now
	} else {
		// Register new worker
		m.workerMetadata[instanceID] = &WorkerMetadata{
			InstanceID:            instanceID,
			SwapGroupInstanceUUID: swapGroupInstanceUUID,
			PodName:               podName,
			Namespace:             namespace,
			LastSeenAt:            now,
		}
	}

	// Update instance to swap group mapping
	m.instanceToSwapGroup[instanceID] = swapGroupInstanceUUID

	// Update pod name to instance ID mapping
	m.podNameToInstanceID[podName] = instanceID

	// Add worker to swap group instance
	swapGroup, exists := m.swapGroupInstances[swapGroupInstanceUUID]
	if !exists {
		swapGroup = &SwapGroupInstanceState{
			SwapGroupInstanceUUID: swapGroupInstanceUUID,
			Workers:               make([]uint64, 0),
		}
		m.swapGroupInstances[swapGroupInstanceUUID] = swapGroup
	}
	swapGroup.AddWorker(instanceID)

	return nil
}

// UnregisterWorker removes a worker instance from the state manager
// It cleans up all associated data structures and removes empty swap groups
func (m *Manager) UnregisterWorker(instanceID uint64) error {
	if instanceID == 0 {
		return fmt.Errorf("instanceID cannot be zero")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if worker exists
	metadata, exists := m.workerMetadata[instanceID]
	if !exists {
		return fmt.Errorf("worker %d not found", instanceID)
	}

	// Get the swap group UUID before deleting
	swapGroupUUID := metadata.SwapGroupInstanceUUID

	// Remove from pod name mapping
	delete(m.podNameToInstanceID, metadata.PodName)

	// Remove from worker metadata
	delete(m.workerMetadata, instanceID)

	// Remove from instance to swap group mapping
	delete(m.instanceToSwapGroup, instanceID)

	// Remove from swap group instance
	if swapGroup, ok := m.swapGroupInstances[swapGroupUUID]; ok {
		swapGroup.RemoveWorker(instanceID)
		// Clean up empty swap group states
		if !swapGroup.HasWorkers() {
			delete(m.swapGroupInstances, swapGroupUUID)
		}
	}

	return nil
}

// UnregisterWorkerByPodName removes a worker instance using the pod name as a key
func (m *Manager) UnregisterWorkerByPodName(podName string) error {
	m.mu.Lock()
	instanceID, exists := m.podNameToInstanceID[podName]
	m.mu.Unlock()

	if !exists {
		return fmt.Errorf("worker with pod name %s not found", podName)
	}

	return m.UnregisterWorker(instanceID)
}

// GetSwapGroupInstance returns the swap group instance UUID for a given worker instance ID
func (m *Manager) GetSwapGroupInstance(instanceID uint64) (string, error) {
	if instanceID == 0 {
		return "", fmt.Errorf("instanceID cannot be zero")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	swapGroupUUID, exists := m.instanceToSwapGroup[instanceID]
	if !exists {
		return "", fmt.Errorf("worker %d not found", instanceID)
	}

	return swapGroupUUID, nil
}

// GetSwapGroupState returns the full swap group state for a given swap group instance UUID
func (m *Manager) GetSwapGroupState(swapGroupInstanceUUID string) *SwapGroupInstanceState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.swapGroupInstances[swapGroupInstanceUUID]
}

// SetWarmInstance updates the warm worker for a swap group instance
func (m *Manager) SetWarmInstance(swapGroupInstanceUUID string, instanceID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if swapGroup, ok := m.swapGroupInstances[swapGroupInstanceUUID]; ok {
		swapGroup.WarmInstanceID = instanceID
	}
}

// ListWorkers returns a list of all registered workers
// The returned slice is a copy to prevent external modifications
func (m *Manager) ListWorkers() []*WorkerMetadata {
	m.mu.RLock()
	defer m.mu.RUnlock()

	workers := make([]*WorkerMetadata, 0, len(m.workerMetadata))
	for _, metadata := range m.workerMetadata {
		// Create a copy of the metadata to prevent external modifications
		workerCopy := *metadata
		workers = append(workers, &workerCopy)
	}

	return workers
}

// GetWorkersInSwapGroup returns a list of instance IDs for all workers in a given swap group
// The returned slice is a copy to prevent external modifications
func (m *Manager) GetWorkersInSwapGroup(swapGroupInstanceUUID string) []uint64 {
	if swapGroupInstanceUUID == "" {
		return []uint64{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	swapGroup, exists := m.swapGroupInstances[swapGroupInstanceUUID]
	if !exists {
		return []uint64{}
	}

	// Return a copy to prevent external modifications
	workers := make([]uint64, len(swapGroup.Workers))
	copy(workers, swapGroup.Workers)

	return workers
}
