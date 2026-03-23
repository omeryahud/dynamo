// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dynamov1alpha1 "github.com/ai-dynamo/dynamo/deploy/operator/api/v1alpha1"
	dynamov1 "github.com/ai-dynamo/dynamo/swap-coordinator/api/v1"
	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/api"
	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/controller"
	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

func main() {
	// Setup logging
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "INFO"
	}

	// Configure logger with appropriate level
	opts := zap.Options{
		Development: logLevel == "DEBUG",
	}
	logger := zap.New(zap.UseFlagOptions(&opts))
	ctrl.SetLogger(logger)

	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("Starting swap-coordinator", "logLevel", logLevel)

	// Get HTTP port from environment
	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = "8080"
	}
	setupLog.Info("HTTP API will listen on port", "port", httpPort)

	// Create runtime scheme and register core Kubernetes types
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dynamov1.AddToScheme(scheme))
	utilruntime.Must(grovev1alpha1.AddToScheme(scheme))
	utilruntime.Must(dynamov1alpha1.AddToScheme(scheme))
	setupLog.Info("Registered types with scheme")

	// Get Kubernetes configuration
	config := ctrl.GetConfigOrDie()
	setupLog.Info("Loaded Kubernetes configuration")

	// Create controller-runtime Manager
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: ":8082"},
		LeaderElection: false,
	})
	if err != nil {
		setupLog.Error(err, "Failed to create controller manager")
		os.Exit(1)
	}
	setupLog.Info("Created controller manager", "metricsAddr", ":8081")

	// Create dynamic client for DGD annotation patching and DGD watcher
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "Failed to create dynamic client")
		os.Exit(1)
	}
	setupLog.Info("Created dynamic client")

	// Create state manager
	stateManager := state.NewManager()
	setupLog.Info("Initialized state manager")

	// Create and register the Pod reconciler
	reconciler := &controller.PodReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		StateManager: stateManager,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to setup PodReconciler")
		os.Exit(1)
	}
	setupLog.Info("Registered PodReconciler")

	// Create and start HTTP API server in a goroutine
	apiServer := api.NewServer(stateManager, dynamicClient)
	go func() {
		setupLog.Info("Starting HTTP API server", "port", httpPort)
		if err := apiServer.Start(); err != nil {
			setupLog.Error(err, "HTTP API server error")
		}
		setupLog.Info("HTTP API server stopped")
	}()

	// Start the DGD watcher to react to annotation changes
	dgdWatcher := &controller.DGDWatcher{
		DynamicClient: dynamicClient,
		StateManager:  stateManager,
	}
	ctx := ctrl.SetupSignalHandler()
	go func() {
		if err := dgdWatcher.Start(ctx); err != nil {
			setupLog.Error(err, "DGD watcher error")
		}
	}()
	setupLog.Info("Started DGD watcher")

	// Start the TTFT metrics scraper
	go api.StartScraper(ctx, stateManager, 1*time.Second)
	setupLog.Info("Started TTFT scraper")

	// Start the controller manager
	setupLog.Info("Starting controller manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Controller manager error")
		os.Exit(1)
	}

	setupLog.Info("Swap coordinator shut down cleanly")
}
