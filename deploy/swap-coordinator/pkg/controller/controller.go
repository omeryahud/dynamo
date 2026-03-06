// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
)

const swapGroupLabelKey = "run.ai/swap-group-instance-uuid"

// instanceIDPattern matches instance_id in worker logs.
// Covers both structured tracing fields (instance_id=N) and
// JSON log format ("instance_id":N).
var instanceIDPattern = regexp.MustCompile(`instance_id[=:][\s"]*(\d+)`)

// ansiEscape strips ANSI escape sequences from log output.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// PodReconciler reconciles Pods that carry the swap-group-instance-uuid label
type PodReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Clientset    kubernetes.Interface
	StateManager *state.Manager
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
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to get Pod")
		return reconcile.Result{}, err
	}

	swapGroupInstanceUUID, exists := pod.Labels[swapGroupLabelKey]
	if !exists || swapGroupInstanceUUID == "" {
		logger.Info("Pod missing swap-group-instance-uuid label, skipping", "podName", pod.Name)
		return reconcile.Result{}, nil
	}

	// Extract the real instance_id from the worker's logs
	instanceID, err := r.extractInstanceIDFromLogs(ctx, &pod)
	if err != nil {
		logger.Info("Could not extract instance_id from pod logs, will retry",
			"podName", pod.Name, "error", err)
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if err := r.StateManager.RegisterWorker(instanceID, swapGroupInstanceUUID, pod.Name, pod.Namespace); err != nil {
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
		"namespace", pod.Namespace)

	return reconcile.Result{}, nil
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

// SetupWithManager watches Pods that have the swap-group-instance-uuid label
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	hasSwapGroupLabel := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		v, ok := obj.GetLabels()[swapGroupLabelKey]
		return ok && v != ""
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(hasSwapGroupLabel).
		Complete(r)
}
