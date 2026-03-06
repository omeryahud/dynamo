// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/api"
	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/controller"
	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
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
	setupLog.Info("Registered client-go types with scheme")

	// Get Kubernetes configuration
	config := ctrl.GetConfigOrDie()
	setupLog.Info("Loaded Kubernetes configuration")

	// Create controller-runtime Manager
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: ":8082",
		LeaderElection:     false,
	})
	if err != nil {
		setupLog.Error(err, "Failed to create controller manager")
		os.Exit(1)
	}
	setupLog.Info("Created controller manager", "metricsAddr", ":8081")

	// Create Kubernetes clientset for pod log access
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "Failed to create Kubernetes clientset")
		os.Exit(1)
	}
	setupLog.Info("Created Kubernetes clientset")

	// Create state manager
	stateManager := state.NewManager()
	setupLog.Info("Initialized state manager")

	// Create and register the Pod reconciler
	reconciler := &controller.PodReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Clientset:    clientset,
		StateManager: stateManager,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to setup PodReconciler")
		os.Exit(1)
	}
	setupLog.Info("Registered PodReconciler")

	// Create and start HTTP API server in a goroutine
	apiServer := api.NewServer(stateManager)
	go func() {
		setupLog.Info("Starting HTTP API server", "port", httpPort)
		if err := apiServer.Start(); err != nil {
			setupLog.Error(err, "HTTP API server error")
			// Note: Start() handles graceful shutdown on signals, so errors here
			// are unexpected. We log but don't exit as the controller might still be useful.
		}
		setupLog.Info("HTTP API server stopped")
	}()

	// Start the controller manager
	setupLog.Info("Starting controller manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Controller manager error")
		os.Exit(1)
	}

	setupLog.Info("Swap coordinator shut down cleanly")
}
