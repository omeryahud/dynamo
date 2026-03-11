// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
)

var dgdWatcherLog = ctrl.Log.WithName("dgd-watcher")

// DGDWatcher watches DynamoGraphDeployment resources and syncs their
// swap-coordinator annotations into the state manager.
type DGDWatcher struct {
	DynamicClient dynamic.Interface
	StateManager  *state.Manager
}

// Start begins watching DGD resources. It blocks until ctx is cancelled.
func (w *DGDWatcher) Start(ctx context.Context) error {
	factory := dynamicinformer.NewDynamicSharedInformerFactory(w.DynamicClient, 30*time.Second)
	informer := factory.ForResource(dgdGVR).Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			w.handleDGD(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			w.handleDGD(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			// Could remove the config, but keeping it is harmless
			// since it won't match any workers once the DGD is gone
		},
	})

	dgdWatcherLog.Info("Starting DGD informer")
	informer.Run(ctx.Done())
	return nil
}

func (w *DGDWatcher) handleDGD(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}

	name := u.GetName()
	namespace := u.GetNamespace()
	annotations := u.GetAnnotations()

	minWarm := 0
	maxWarm := 0
	ttftThresholdMS := 0.0
	ttftWindowSeconds := 60
	if v, exists := annotations["swap-coordinator/min-warm-workers"]; exists {
		if parsed, err := strconv.Atoi(v); err == nil {
			minWarm = parsed
		}
	}
	if v, exists := annotations["swap-coordinator/max-warm-workers"]; exists {
		if parsed, err := strconv.Atoi(v); err == nil {
			maxWarm = parsed
		}
	}
	if v, exists := annotations["swap-coordinator/ttft-threshold-ms"]; exists {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			ttftThresholdMS = parsed
		}
	}
	if v, exists := annotations["swap-coordinator/ttft-window-seconds"]; exists {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			ttftWindowSeconds = parsed
		}
	}

	w.StateManager.SetDGDConfig(name, namespace, minWarm, maxWarm, ttftThresholdMS, ttftWindowSeconds)
	dgdWatcherLog.Info("Updated DGD config from watch event",
		"dgdName", name, "namespace", namespace,
		"minWarm", minWarm, "maxWarm", maxWarm,
		"ttftThresholdMS", ttftThresholdMS, "ttftWindowSeconds", ttftWindowSeconds)
}
