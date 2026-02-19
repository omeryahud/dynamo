// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	dynamov1 "github.com/ai-dynamo/dynamo/swap-coordinator/api/v1"
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

	// Create runtime scheme and register our types
	scheme := runtime.NewScheme()
	if err := dynamov1.AddToScheme(scheme); err != nil {
		setupLog.Error(err, "Failed to register DynamoWorkerMetadata types to scheme")
		os.Exit(1)
	}
	setupLog.Info("Registered DynamoWorkerMetadata types with scheme")

	// Get Kubernetes configuration
	config := ctrl.GetConfigOrDie()
	setupLog.Info("Loaded Kubernetes configuration")

	// Create controller-runtime Manager
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: ":8081",
		LeaderElection:     false,
	})
	if err != nil {
		setupLog.Error(err, "Failed to create controller manager")
		os.Exit(1)
	}
	setupLog.Info("Created controller manager", "metricsAddr", ":8081")

	// Create state manager
	stateManager := state.NewManager()
	setupLog.Info("Initialized state manager")

	// Create and register the DynamoWorkerMetadata reconciler
	reconciler := &controller.DynamoWorkerMetadataReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		StateManager: stateManager,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to setup DynamoWorkerMetadataReconciler")
		os.Exit(1)
	}
	setupLog.Info("Registered DynamoWorkerMetadataReconciler")

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
