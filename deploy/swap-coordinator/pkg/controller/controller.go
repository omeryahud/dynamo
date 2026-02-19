package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dynamov1 "github.com/ai-dynamo/dynamo/swap-coordinator/api/v1"
	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
)

// DynamoWorkerMetadataReconciler reconciles a DynamoWorkerMetadata object
type DynamoWorkerMetadataReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	StateManager *state.Manager
}

// Reconcile handles the reconciliation loop for DynamoWorkerMetadata resources
// It watches for CRD changes and updates the state manager accordingly
func (r *DynamoWorkerMetadataReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the DynamoWorkerMetadata resource
	var dynamoWorkerMetadata dynamov1.DynamoWorkerMetadata
	if err := r.Get(ctx, req.NamespacedName, &dynamoWorkerMetadata); err != nil {
		if errors.IsNotFound(err) {
			// Resource was deleted, unregister the worker
			logger.Info("DynamoWorkerMetadata resource not found, unregistering worker", "instanceID", req.Name)

			// Use the resource name as the instance ID
			instanceID := req.Name
			if err := r.StateManager.UnregisterWorker(instanceID); err != nil {
				// Log the error but don't fail - worker might already be unregistered
				logger.Info("Failed to unregister worker (may not exist)", "instanceID", instanceID, "error", err)
			}

			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request
		logger.Error(err, "Failed to get DynamoWorkerMetadata")
		return reconcile.Result{}, err
	}

	// Extract instance ID from the CRD metadata name
	instanceID := dynamoWorkerMetadata.Name

	// Get the owner Pod
	pod, err := r.getOwnerPod(ctx, &dynamoWorkerMetadata)
	if err != nil {
		// Pod not found - this will trigger a requeue after 5s
		logger.Error(err, "Failed to get owner Pod", "instanceID", instanceID)
		return reconcile.Result{}, err
	}

	// Extract the swap group instance UUID from the Pod annotation
	swapGroupInstanceUUID, exists := pod.Annotations["run.ai/swap-group-instance-uuid"]
	if !exists || swapGroupInstanceUUID == "" {
		// Missing annotation - log warning and return success (don't register)
		logger.Info("Pod missing swap-group-instance-uuid annotation, skipping registration",
			"instanceID", instanceID,
			"podName", pod.Name,
			"namespace", pod.Namespace)
		return reconcile.Result{}, nil
	}

	// Register the worker with the state manager
	err = r.StateManager.RegisterWorker(instanceID, swapGroupInstanceUUID, pod.Name, pod.Namespace)
	if err != nil {
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

// getOwnerPod retrieves the owner Pod for the given DynamoWorkerMetadata resource
func (r *DynamoWorkerMetadataReconciler) getOwnerPod(ctx context.Context, dwm *dynamov1.DynamoWorkerMetadata) (*corev1.Pod, error) {
	// Get owner references
	ownerRefs := dwm.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return nil, fmt.Errorf("no owner references found for DynamoWorkerMetadata %s", dwm.Name)
	}

	// Find the Pod owner
	var podOwner *corev1.Pod
	for _, ownerRef := range ownerRefs {
		if ownerRef.Kind == "Pod" {
			// Fetch the Pod
			pod := &corev1.Pod{}
			podName := types.NamespacedName{
				Name:      ownerRef.Name,
				Namespace: dwm.Namespace,
			}
			if err := r.Get(ctx, podName, pod); err != nil {
				if errors.IsNotFound(err) {
					return nil, fmt.Errorf("owner Pod %s not found in namespace %s", ownerRef.Name, dwm.Namespace)
				}
				return nil, fmt.Errorf("failed to get owner Pod %s: %w", ownerRef.Name, err)
			}
			podOwner = pod
			break
		}
	}

	if podOwner == nil {
		return nil, fmt.Errorf("no Pod owner reference found for DynamoWorkerMetadata %s", dwm.Name)
	}

	return podOwner, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *DynamoWorkerMetadataReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dynamov1.DynamoWorkerMetadata{}).
		Complete(r)
}
