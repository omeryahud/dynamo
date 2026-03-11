// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
)

const swapGroupLabelKey = "run.ai/swap-group-instance-uuid"
const dgdNameLabelKey = "nvidia.com/dynamo-graph-deployment-name"
const frontendMetricsPort = 8000

var dgdGVR = schema.GroupVersionResource{
	Group:    "nvidia.com",
	Version:  "v1alpha1",
	Resource: "dynamographdeployments",
}

// instanceIDPattern matches instance_id in worker logs.
// Covers both structured tracing fields (instance_id=N) and
// JSON log format ("instance_id":N).
var instanceIDPattern = regexp.MustCompile(`instance_id[=:][\s"]*(\d+)`)

// ansiEscape strips ANSI escape sequences from log output.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// PodReconciler reconciles Pods that carry the swap-group-instance-uuid label
type PodReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	StateManager  *state.Manager
}

func (r *PodReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Pod not found, unregistering worker", "podName", req.Name)
			if unregErr := r.StateManager.UnregisterWorkerByPodName(req.Name); unregErr != nil {
				logger.Info("Failed to unregister worker (may not exist)", "podName", req.Name, "error", unregErr)
			}
			// Also unregister as frontend if it was one
			r.StateManager.UnregisterFrontend(req.Name)
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to get Pod")
		return reconcile.Result{}, err
	}

	// Check if this is a frontend pod (name contains "frontend")
	if isFrontendPod(&pod) {
		dgdName := pod.Labels[dgdNameLabelKey]
		if dgdName != "" && pod.Status.PodIP != "" {
			r.StateManager.RegisterFrontend(pod.Name, pod.Status.PodIP, frontendMetricsPort, dgdName, pod.Namespace)
			logger.Info("Registered frontend pod for TTFT scraping",
				"podName", pod.Name, "podIP", pod.Status.PodIP, "dgdName", dgdName)
		}
	}

	swapGroupInstanceUUID, exists := pod.Labels[swapGroupLabelKey]
	if !exists || swapGroupInstanceUUID == "" {
		// Not a swap-group worker pod — skip worker registration
		// (might be a frontend-only pod, which was handled above)
		return reconcile.Result{}, nil
	}

	// Extract the real instance_id from the worker's logs
	instanceID, err := r.extractInstanceIDFromLogs(ctx, &pod)
	if err != nil {
		logger.Info("Could not extract instance_id from pod logs, will retry",
			"podName", pod.Name, "error", err)
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Read DGD name from pod label and fetch DGD annotations
	dgdName := pod.Labels[dgdNameLabelKey]
	dgdNamespace := pod.Namespace
	if dgdName != "" && r.DynamicClient != nil {
		r.fetchAndStoreDGDConfig(ctx, dgdName, dgdNamespace)
	}

	if err := r.StateManager.RegisterWorker(instanceID, swapGroupInstanceUUID, pod.Name, pod.Namespace, dgdName, dgdNamespace); err != nil {
		logger.Error(err, "Failed to register worker",
			"instanceID", instanceID,
			"swapGroupInstanceUUID", swapGroupInstanceUUID,
			"podName", pod.Name,
			"namespace", pod.Namespace)
		return reconcile.Result{}, err
	}

	logger.Info("Successfully registered worker",
		"instanceID", instanceID,
		"swapGroupInstanceUUID", swapGroupInstanceUUID,
		"podName", pod.Name,
		"namespace", pod.Namespace,
		"dgdName", dgdName)

	return reconcile.Result{}, nil
}

// fetchAndStoreDGDConfig fetches the DGD resource via dynamic client and stores
// the min/max warm worker annotations in the state manager
func (r *PodReconciler) fetchAndStoreDGDConfig(ctx context.Context, dgdName, dgdNamespace string) {
	logger := log.FromContext(ctx)

	dgd, err := r.DynamicClient.Resource(dgdGVR).Namespace(dgdNamespace).Get(ctx, dgdName, metav1.GetOptions{})
	if err != nil {
		logger.Info("Failed to fetch DGD resource", "dgdName", dgdName, "namespace", dgdNamespace, "error", err)
		return
	}

	annotations := dgd.GetAnnotations()
	minWarm := 0
	maxWarm := 0
	ttftThresholdMS := 0.0
	ttftWindowSeconds := 60

	if v, ok := annotations["swap-coordinator/min-warm-workers"]; ok {
		if parsed, err := strconv.Atoi(v); err == nil {
			minWarm = parsed
		}
	}
	if v, ok := annotations["swap-coordinator/max-warm-workers"]; ok {
		if parsed, err := strconv.Atoi(v); err == nil {
			maxWarm = parsed
		}
	}
	if v, ok := annotations["swap-coordinator/ttft-threshold-ms"]; ok {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			ttftThresholdMS = parsed
		}
	}
	if v, ok := annotations["swap-coordinator/ttft-window-seconds"]; ok {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			ttftWindowSeconds = parsed
		}
	}

	r.StateManager.SetDGDConfig(dgdName, dgdNamespace, minWarm, maxWarm, ttftThresholdMS, ttftWindowSeconds)
	logger.Info("Loaded DGD config", "dgdName", dgdName, "namespace", dgdNamespace,
		"minWarm", minWarm, "maxWarm", maxWarm,
		"ttftThresholdMS", ttftThresholdMS, "ttftWindowSeconds", ttftWindowSeconds)
}

// extractInstanceIDFromLogs fetches the pod's logs and parses the instance_id
// that the worker logs during discovery registration.
// It tries each container in the pod since multi-container pods require
// specifying the container name.
func (r *PodReconciler) extractInstanceIDFromLogs(ctx context.Context, pod *corev1.Pod) (uint64, error) {
	containers := pod.Spec.Containers
	if len(containers) == 0 {
		return 0, fmt.Errorf("pod has no containers")
	}

	for _, container := range containers {
		id, err := r.extractInstanceIDFromContainerLogs(ctx, pod.Namespace, pod.Name, container.Name)
		if err == nil {
			return id, nil
		}
	}

	return 0, fmt.Errorf("instance_id not found in any container logs")
}

func (r *PodReconciler) extractInstanceIDFromContainerLogs(ctx context.Context, namespace, podName, containerName string) (uint64, error) {
	logReq := r.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
	})
	stream, err := logReq.Stream(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get log stream for container %s: %w", containerName, err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		line := ansiEscape.ReplaceAllString(scanner.Text(), "")
		matches := instanceIDPattern.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		id, err := strconv.ParseUint(matches[1], 10, 64)
		if err != nil {
			continue
		}
		if id == 0 {
			continue
		}
		return id, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("error reading logs for container %s: %w", containerName, err)
	}

	return 0, fmt.Errorf("instance_id not found in container %s logs", containerName)
}

// isFrontendPod checks if a pod is a frontend pod based on naming convention
// or the DYN_COMPONENT=frontend environment variable
func isFrontendPod(pod *corev1.Pod) bool {
	// Check naming convention first (e.g., qwen3-{N}-0-frontend-*)
	if strings.Contains(pod.Name, "frontend") {
		return true
	}
	// Check environment variable
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "DYN_COMPONENT" && env.Value == "frontend" {
				return true
			}
		}
	}
	return false
}

// SetupWithManager watches Pods that have the swap-group-instance-uuid label
// or are frontend pods (for TTFT metrics scraping)
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	isRelevantPod := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		// Match swap-group worker pods
		if v, ok := obj.GetLabels()[swapGroupLabelKey]; ok && v != "" {
			return true
		}
		// Match frontend pods by DGD label (they need to have the DGD label to be useful)
		if _, ok := obj.GetLabels()[dgdNameLabelKey]; ok {
			return true
		}
		return false
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(isRelevantPod).
		Complete(r)
}
