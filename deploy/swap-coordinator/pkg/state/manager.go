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

	// dgdConfigs maps "namespace/name" to DGD configuration
	dgdConfigs map[string]*DGDConfig

	// instanceToDGD maps worker instance IDs to their DGD key ("namespace/name")
	instanceToDGD map[uint64]string

	// workerLogits stores the last-known routing logit per worker instance
	workerLogits map[uint64]float64

	// ttftSamples stores rolling TTFT samples per DGD key ("namespace/name")
	ttftSamples map[string][]TTFTSample

	// frontendPods maps pod name to frontend pod info for metrics scraping
	frontendPods map[string]*FrontendPod

	// lastScrape stores the last scraped histogram values per frontend pod
	lastScrape map[string]struct {
		Sum   float64
		Count uint64
	}

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
		dgdConfigs:          make(map[string]*DGDConfig),
		instanceToDGD:       make(map[uint64]string),
		workerLogits:        make(map[uint64]float64),
		ttftSamples:         make(map[string][]TTFTSample),
		frontendPods:        make(map[string]*FrontendPod),
		lastScrape: make(map[string]struct {
			Sum   float64
			Count uint64
		}),
	}
}

// RegisterWorker registers a new worker instance or updates an existing one
// It associates the worker with a swap group instance and updates the last seen timestamp
func (m *Manager) RegisterWorker(instanceID uint64, swapGroupInstanceUUID, podName, namespace, dgdName, dgdNamespace string) error {
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
		metadata.DGDName = dgdName
		metadata.DGDNamespace = dgdNamespace
		metadata.LastSeenAt = now
	} else {
		// Register new worker
		m.workerMetadata[instanceID] = &WorkerMetadata{
			InstanceID:            instanceID,
			SwapGroupInstanceUUID: swapGroupInstanceUUID,
			PodName:               podName,
			Namespace:             namespace,
			DGDName:               dgdName,
			DGDNamespace:          dgdNamespace,
			LastSeenAt:            now,
		}
	}

	// Update instance to DGD mapping
	if dgdName != "" {
		m.instanceToDGD[instanceID] = dgdNamespace + "/" + dgdName
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

	// Remove from instance to DGD mapping
	delete(m.instanceToDGD, instanceID)

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

// SetWarmInstance updates the warm worker for a swap group instance.
// Only one worker can be warm per swap-group-instance (one GPU).
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

// Snapshot returns a point-in-time copy of the full state for visualization
func (m *Manager) Snapshot() map[string]*SwapGroupInstanceState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := make(map[string]*SwapGroupInstanceState, len(m.swapGroupInstances))
	for uuid, sg := range m.swapGroupInstances {
		workers := make([]uint64, len(sg.Workers))
		copy(workers, sg.Workers)
		snap[uuid] = &SwapGroupInstanceState{
			SwapGroupInstanceUUID: sg.SwapGroupInstanceUUID,
			Workers:               workers,
			WarmInstanceID:        sg.WarmInstanceID,
		}
	}
	return snap
}

// GetWorkerMetadata returns metadata for a specific worker instance
func (m *Manager) GetWorkerMetadata(instanceID uint64) *WorkerMetadata {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if meta, ok := m.workerMetadata[instanceID]; ok {
		copy := *meta
		return &copy
	}
	return nil
}

// SetDGDConfig stores or updates the min/max warm worker configuration for a DGD
func (m *Manager) SetDGDConfig(name, namespace string, minWarm, maxWarm int, ttftThresholdMS float64, ttftWindowSeconds int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ttftWindowSeconds <= 0 {
		ttftWindowSeconds = 60
	}

	key := namespace + "/" + name
	m.dgdConfigs[key] = &DGDConfig{
		Name:              name,
		Namespace:         namespace,
		MinWarmWorkers:    minWarm,
		MaxWarmWorkers:    maxWarm,
		TTFTThresholdMS:   ttftThresholdMS,
		TTFTWindowSeconds: ttftWindowSeconds,
	}
}

// GetDGDConfig returns the DGD configuration for the given name and namespace
func (m *Manager) GetDGDConfig(name, namespace string) *DGDConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := namespace + "/" + name
	if cfg, ok := m.dgdConfigs[key]; ok {
		copy := *cfg
		return &copy
	}
	return nil
}

// ListDGDConfigs returns all DGD configurations
func (m *Manager) ListDGDConfigs() []*DGDConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	configs := make([]*DGDConfig, 0, len(m.dgdConfigs))
	for _, cfg := range m.dgdConfigs {
		copy := *cfg
		configs = append(configs, &copy)
	}
	return configs
}

// CountWarmWorkersForDGD counts how many swap group instances currently have a warm
// worker that belongs to the given DGD
func (m *Manager) CountWarmWorkersForDGD(name, namespace string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	dgdKey := namespace + "/" + name
	count := 0
	for _, sg := range m.swapGroupInstances {
		if sg.WarmInstanceID == 0 {
			continue
		}
		if workerDGDKey, ok := m.instanceToDGD[sg.WarmInstanceID]; ok && workerDGDKey == dgdKey {
			count++
		}
	}
	return count
}

// GetWorkerDGD returns the DGD name and namespace for a worker instance
func (m *Manager) GetWorkerDGD(instanceID uint64) (name, namespace string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if meta, ok := m.workerMetadata[instanceID]; ok {
		return meta.DGDName, meta.DGDNamespace
	}
	return "", ""
}

// UpdateWorkerLogits stores the latest routing logits for a batch of workers
func (m *Manager) UpdateWorkerLogits(logits map[uint64]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, logit := range logits {
		m.workerLogits[id] = logit
	}
}

// GetWorkerLogit returns the last-known routing logit for a worker, and whether one exists
func (m *Manager) GetWorkerLogit(instanceID uint64) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	logit, ok := m.workerLogits[instanceID]
	return logit, ok
}

// RecordTTFTSample appends a TTFT sample for a DGD and prunes samples outside the window
func (m *Manager) RecordTTFTSample(dgdKey string, avgTTFTMS float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sample := TTFTSample{
		AvgTTFTMS: avgTTFTMS,
		Timestamp: time.Now(),
	}
	m.ttftSamples[dgdKey] = append(m.ttftSamples[dgdKey], sample)

	// Prune stale samples based on the DGD's window
	m.pruneTTFTSamplesLocked(dgdKey)
}

// pruneTTFTSamplesLocked removes samples older than the DGD's window. Must hold mu.
func (m *Manager) pruneTTFTSamplesLocked(dgdKey string) {
	windowSeconds := 60 // default
	if cfg, ok := m.dgdConfigs[dgdKey]; ok && cfg.TTFTWindowSeconds > 0 {
		windowSeconds = cfg.TTFTWindowSeconds
	}

	cutoff := time.Now().Add(-time.Duration(windowSeconds) * time.Second)
	samples := m.ttftSamples[dgdKey]
	i := 0
	for i < len(samples) && samples[i].Timestamp.Before(cutoff) {
		i++
	}
	if i > 0 {
		m.ttftSamples[dgdKey] = samples[i:]
	}
}

// GetRollingAvgTTFT returns the rolling average TTFT in ms and sample count for a DGD
func (m *Manager) GetRollingAvgTTFT(dgdKey string) (float64, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	samples := m.ttftSamples[dgdKey]
	if len(samples) == 0 {
		return 0, 0
	}

	// Filter to window
	windowSeconds := 60
	if cfg, ok := m.dgdConfigs[dgdKey]; ok && cfg.TTFTWindowSeconds > 0 {
		windowSeconds = cfg.TTFTWindowSeconds
	}
	cutoff := time.Now().Add(-time.Duration(windowSeconds) * time.Second)

	sum := 0.0
	count := 0
	for _, s := range samples {
		if !s.Timestamp.Before(cutoff) {
			sum += s.AvgTTFTMS
			count++
		}
	}
	if count == 0 {
		return 0, 0
	}
	return sum / float64(count), count
}

// IsTTFTExceeded returns true if the DGD's rolling average TTFT exceeds its threshold
func (m *Manager) IsTTFTExceeded(name, namespace string) bool {
	key := namespace + "/" + name

	m.mu.RLock()
	defer m.mu.RUnlock()

	cfg, ok := m.dgdConfigs[key]
	if !ok || cfg.TTFTThresholdMS <= 0 {
		return false
	}

	samples := m.ttftSamples[key]
	if len(samples) == 0 {
		return false
	}

	windowSeconds := cfg.TTFTWindowSeconds
	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	cutoff := time.Now().Add(-time.Duration(windowSeconds) * time.Second)

	sum := 0.0
	count := 0
	for _, s := range samples {
		if !s.Timestamp.Before(cutoff) {
			sum += s.AvgTTFTMS
			count++
		}
	}
	if count == 0 {
		return false
	}
	return (sum / float64(count)) > cfg.TTFTThresholdMS
}

// RegisterFrontend registers a frontend pod for metrics scraping
func (m *Manager) RegisterFrontend(podName, podIP string, port int, dgdName, namespace string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.frontendPods[podName] = &FrontendPod{
		PodName:   podName,
		PodIP:     podIP,
		Port:      port,
		DGDName:   dgdName,
		Namespace: namespace,
	}
}

// UnregisterFrontend removes a frontend pod from tracking
func (m *Manager) UnregisterFrontend(podName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.frontendPods, podName)
	delete(m.lastScrape, podName)
}

// ListFrontends returns a copy of all registered frontend pods
func (m *Manager) ListFrontends() []*FrontendPod {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pods := make([]*FrontendPod, 0, len(m.frontendPods))
	for _, pod := range m.frontendPods {
		p := *pod
		pods = append(pods, &p)
	}
	return pods
}

// GetLastScrape returns the last scraped histogram values for a frontend pod
func (m *Manager) GetLastScrape(podName string) (sum float64, count uint64, exists bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	v, ok := m.lastScrape[podName]
	if !ok {
		return 0, 0, false
	}
	return v.Sum, v.Count, true
}

// SetLastScrape stores the last scraped histogram values for a frontend pod
func (m *Manager) SetLastScrape(podName string, sum float64, count uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastScrape[podName] = struct {
		Sum   float64
		Count uint64
	}{Sum: sum, Count: count}
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
