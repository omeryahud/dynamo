package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
	"github.com/gin-gonic/gin"
	"k8s.io/client-go/dynamic"
)

// Server wraps the HTTP server and provides lifecycle management
type Server struct {
	stateManager  *state.Manager
	dynamicClient dynamic.Interface
	httpServer    *http.Server
	router        *gin.Engine
}

// NewServer creates a new API server with the given state manager and dynamic client
func NewServer(stateManager *state.Manager, dynamicClient dynamic.Interface) *Server {
	// Create Gin router with default middleware (logger and recovery)
	router := gin.Default()

	// Create server instance
	server := &Server{
		stateManager:  stateManager,
		dynamicClient: dynamicClient,
		router:        router,
	}

	// Register routes
	server.registerRoutes()

	// Get port from environment variable or use default
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}

	// Create HTTP server
	server.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: router,
	}

	return server
}

// registerRoutes sets up all HTTP routes
func (s *Server) registerRoutes() {
	// Dashboard
	s.router.GET("/", DashboardHandler())

	// State endpoints for visualization
	s.router.GET("/state", StateHandler(s.stateManager))
	s.router.PUT("/state/warm", SetWarmHandler(s.stateManager))

	// Health check endpoint
	s.router.GET("/health", HealthHandler(s.stateManager))

	// DGD configuration endpoints
	s.router.GET("/dgds", DGDsHandler(s.stateManager))
	s.router.PUT("/dgds", UpdateDGDHandler(s.stateManager, s.dynamicClient))

	// Worker selection endpoint
	s.router.POST("/select_worker", SelectWorkerHandler(s.stateManager))
}

// Start begins serving HTTP requests and blocks until shutdown
// It handles graceful shutdown on SIGINT and SIGTERM signals
func (s *Server) Start() error {
	// Channel to listen for errors from the server
	serverErrors := make(chan error, 1)

	// Start the server in a goroutine
	go func() {
		fmt.Printf("Starting HTTP server on %s\n", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- err
		}
	}()

	// Channel to listen for interrupt signals
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	// Block until we receive a signal or an error
	select {
	case err := <-serverErrors:
		return fmt.Errorf("server error: %w", err)
	case sig := <-stop:
		fmt.Printf("\nReceived signal %v, shutting down gracefully...\n", sig)

		// Create a context with timeout for graceful shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Attempt graceful shutdown
		if err := s.httpServer.Shutdown(ctx); err != nil {
			return fmt.Errorf("error during shutdown: %w", err)
		}

		fmt.Println("Server stopped gracefully")
		return nil
	}
}
