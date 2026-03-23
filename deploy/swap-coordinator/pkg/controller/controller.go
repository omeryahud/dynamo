// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	dynamov1alpha1 "github.com/ai-dynamo/dynamo/deploy/operator/api/v1alpha1"
	dynamov1 "github.com/ai-dynamo/dynamo/swap-coordinator/api/v1"
	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

const swapGroupLabelKey = "run.ai/swap-group-instance-uuid"
const dgdNameLabelKey = "nvidia.com/dynamo-graph-deployment-name"
const frontendMetricsPort = 8000

// PodReconciler reconciles Pods that carry the swap-group-instance-uuid label
type PodReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
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
			// Also unregister as frontend if it was one
			r.StateManager.UnregisterFrontend(req.Name)
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to get Pod")
		return reconcile.Result{}, err
	}

	// Check if this is a frontend pod (name contains "frontend")
	// TODO: migrate to ownership chain approach (PodClique roleName or component labels)
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

	// Extract instance_id from the DynamoWorkerMetadata CRD
	instanceID, err := r.extractInstanceIDFromDWM(ctx, pod.Namespace, pod.Name)
	if err != nil {
		logger.Info("Could not extract instance_id from DynamoWorkerMetadata, will retry",
			"podName", pod.Name, "error", err)
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Resolve DGD via ownership chain: Pod → PodClique → PodCliqueSet → DGD
	dgdName, dgdNamespace := r.resolveDGDFromOwnerChain(ctx, &pod)
	if dgdName != "" {
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

// extractInstanceIDFromDWM fetches the DynamoWorkerMetadata CRD for the given pod
// and extracts the instance_id from its spec.data JSON blob.
// The CRD name matches the pod name by convention.
func (r *PodReconciler) extractInstanceIDFromDWM(ctx context.Context, namespace, podName string) (uint64, error) {
	var dwm dynamov1.DynamoWorkerMetadata
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &dwm); err != nil {
		return 0, err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(dwm.Spec.Data.Raw, &data); err != nil {
		return 0, fmt.Errorf("failed to parse DWM data: %w", err)
	}

	// Extract instance_id from the first endpoint entry.
	// All endpoints for a single worker share the same instance_id.
	endpoints, ok := data["endpoints"].(map[string]interface{})
	if !ok || len(endpoints) == 0 {
		return 0, fmt.Errorf("no endpoints in DWM data")
	}
	for _, v := range endpoints {
		ep, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := ep["instance_id"].(float64); ok {
			return uint64(id), nil
		}
	}
	return 0, fmt.Errorf("instance_id not found in DWM endpoints")
}

// resolveDGDFromOwnerChain traverses the Kubernetes ownership chain from a Pod
// up to the DynamoGraphDeployment. Supports three deployment paths:
//
//	Grove path:          Pod → PodClique → PodCliqueSet → DynamoGraphDeployment
//	Component path:      Pod → ReplicaSet → Deployment → DynamoComponentDeployment → DynamoGraphDeployment
//	Component LWS path:  Pod → StatefulSet → LeaderWorkerSet → DynamoComponentDeployment → DynamoGraphDeployment
//
// Returns the DGD name and namespace, or empty strings if the chain cannot be resolved.
func (r *PodReconciler) resolveDGDFromOwnerChain(ctx context.Context, pod *corev1.Pod) (string, string) {
	if name, ns := r.resolveDGDViaGrove(ctx, pod); name != "" {
		return name, ns
	}
	if name, ns := r.resolveDGDViaComponent(ctx, pod); name != "" {
		return name, ns
	}
	return r.resolveDGDViaLWS(ctx, pod)
}

// resolveDGDViaGrove tries: Pod → PodClique → PodCliqueSet → DGD
func (r *PodReconciler) resolveDGDViaGrove(ctx context.Context, pod *corev1.Pod) (string, string) {
	logger := log.FromContext(ctx)

	pcOwner := findOwnerRef(pod.OwnerReferences, "PodClique")
	if pcOwner == nil {
		return "", ""
	}

	var pc grovev1alpha1.PodClique
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pcOwner.Name}, &pc); err != nil {
		logger.V(1).Info("Failed to get PodClique", "name", pcOwner.Name, "error", err)
		return "", ""
	}

	pcsOwner := findOwnerRef(pc.OwnerReferences, "PodCliqueSet")
	if pcsOwner == nil {
		return "", ""
	}

	var pcs grovev1alpha1.PodCliqueSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pcsOwner.Name}, &pcs); err != nil {
		logger.V(1).Info("Failed to get PodCliqueSet", "name", pcsOwner.Name, "error", err)
		return "", ""
	}

	dgdOwner := findOwnerRef(pcs.OwnerReferences, "DynamoGraphDeployment")
	if dgdOwner == nil {
		return "", ""
	}

	return dgdOwner.Name, pod.Namespace
}

// resolveDGDViaComponent tries: Pod → ReplicaSet → Deployment → DynamoComponentDeployment → DGD
func (r *PodReconciler) resolveDGDViaComponent(ctx context.Context, pod *corev1.Pod) (string, string) {
	logger := log.FromContext(ctx)

	rsOwner := findOwnerRef(pod.OwnerReferences, "ReplicaSet")
	if rsOwner == nil {
		return "", ""
	}

	var rs appsv1.ReplicaSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: rsOwner.Name}, &rs); err != nil {
		logger.V(1).Info("Failed to get ReplicaSet", "name", rsOwner.Name, "error", err)
		return "", ""
	}

	deplOwner := findOwnerRef(rs.OwnerReferences, "Deployment")
	if deplOwner == nil {
		return "", ""
	}

	var depl appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: deplOwner.Name}, &depl); err != nil {
		logger.V(1).Info("Failed to get Deployment", "name", deplOwner.Name, "error", err)
		return "", ""
	}

	dcdOwner := findOwnerRef(depl.OwnerReferences, "DynamoComponentDeployment")
	if dcdOwner == nil {
		return "", ""
	}

	var dcd dynamov1alpha1.DynamoComponentDeployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: dcdOwner.Name}, &dcd); err != nil {
		logger.V(1).Info("Failed to get DynamoComponentDeployment", "name", dcdOwner.Name, "error", err)
		return "", ""
	}

	dgdOwner := findOwnerRef(dcd.OwnerReferences, "DynamoGraphDeployment")
	if dgdOwner == nil {
		return "", ""
	}

	return dgdOwner.Name, pod.Namespace
}

// resolveDGDViaLWS tries: Pod → StatefulSet → LeaderWorkerSet → DynamoComponentDeployment → DGD
func (r *PodReconciler) resolveDGDViaLWS(ctx context.Context, pod *corev1.Pod) (string, string) {
	logger := log.FromContext(ctx)

	stsOwner := findOwnerRef(pod.OwnerReferences, "StatefulSet")
	if stsOwner == nil {
		return "", ""
	}

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: stsOwner.Name}, &sts); err != nil {
		logger.V(1).Info("Failed to get StatefulSet", "name", stsOwner.Name, "error", err)
		return "", ""
	}

	lwsOwner := findOwnerRef(sts.OwnerReferences, "LeaderWorkerSet")
	if lwsOwner == nil {
		return "", ""
	}

	var lws leaderworkersetv1.LeaderWorkerSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: lwsOwner.Name}, &lws); err != nil {
		logger.V(1).Info("Failed to get LeaderWorkerSet", "name", lwsOwner.Name, "error", err)
		return "", ""
	}

	dcdOwner := findOwnerRef(lws.OwnerReferences, "DynamoComponentDeployment")
	if dcdOwner == nil {
		return "", ""
	}

	var dcd dynamov1alpha1.DynamoComponentDeployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: dcdOwner.Name}, &dcd); err != nil {
		logger.V(1).Info("Failed to get DynamoComponentDeployment", "name", dcdOwner.Name, "error", err)
		return "", ""
	}

	dgdOwner := findOwnerRef(dcd.OwnerReferences, "DynamoGraphDeployment")
	if dgdOwner == nil {
		return "", ""
	}

	return dgdOwner.Name, pod.Namespace
}

// findOwnerRef finds an owner reference of the given kind in the list.
func findOwnerRef(refs []metav1.OwnerReference, kind string) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Kind == kind {
			return &refs[i]
		}
	}
	return nil
}

// fetchAndStoreDGDConfig fetches the DynamoGraphDeployment resource and stores
// the swap-coordinator annotations in the state manager.
func (r *PodReconciler) fetchAndStoreDGDConfig(ctx context.Context, dgdName, dgdNamespace string) {
	logger := log.FromContext(ctx)

	var dgd dynamov1alpha1.DynamoGraphDeployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: dgdNamespace, Name: dgdName}, &dgd); err != nil {
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

// isFrontendPod checks if a pod is a frontend pod based on naming convention
// or the DYN_COMPONENT=frontend environment variable.
// TODO: migrate to ownership chain approach (PodClique roleName or component labels)
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
// or are frontend pods (for TTFT metrics scraping).
// Also watches DynamoWorkerMetadata CRDs via Owns() so that CRD creation
// triggers immediate re-reconciliation of the owning Pod.
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
		Owns(&dynamov1.DynamoWorkerMetadata{}).
		WithEventFilter(isRelevantPod).
		Complete(r)
}
