package handler

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bnema/gordon/internal/httpserve"
	"github.com/bnema/gordon/internal/proxy"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
)

// Global server instance for shutdown handling
var (
	globalServer     *server.App
	globalServerLock sync.Mutex
	// Add a lock for server initialization
	serverInitLock sync.Mutex
)

// SetGlobalServer sets the global server instance for shutdown handling
func SetGlobalServer(s *server.App) {
	globalServerLock.Lock()
	defer globalServerLock.Unlock()
	globalServer = s
}

// GetGlobalServer gets the global server instance
func GetGlobalServer() *server.App {
	globalServerLock.Lock()
	defer globalServerLock.Unlock()
	return globalServer
}

// execute cmd/srv/main.go main function

func StartServer(a *server.App, port string) error {
	// Use lock to ensure only one server initialization happens at a time
	serverInitLock.Lock()
	defer serverInitLock.Unlock()

	// Set the global server instance for shutdown handling
	SetGlobalServer(a)

	_, err := server.InitializeDB(a)
	if err != nil {
		logger.Fatal("Failed to initialize database:", err)
	}

	// Start the session cleaner cron job
	a.StartSessionCleaner()

	_, err = server.HandleNewTokenInitialization(a)
	if err != nil {
		logger.Error("Token initialization error", "error", err)
	}

	// Create a channel to signal when the main server is ready
	serverReady := make(chan struct{})

	logger.Info("Initializing main HTTP server...")
	e := echo.New()
	e.Server.ReadTimeout = 10 * time.Minute
	e.Server.WriteTimeout = 10 * time.Minute
	e.HideBanner = true
	e.HidePort = true

	logger.Debug("About to register routes to main HTTP server")
	e = httpserve.RegisterRoutes(e, a)
	logger.Info("Routes registered to main HTTP server")

	// Setup a channel to capture termination signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Start the server in a goroutine
	go func() {
		logger.Info("Starting main server on port", "port", a.Config.Http.Port)

		// Create a separate goroutine that will signal after a short delay
		// This ensures the server has time to initialize and bind to the port
		go func() {
			// Wait for server to start binding
			time.Sleep(1 * time.Second)
			logger.Debug("Signaling that main server is ready")
			close(serverReady)
		}()

		// Attempt to start the server
		if err := e.Start(fmt.Sprintf(":%s", a.Config.Http.Port)); err != nil {
			if err.Error() != "http: Server closed" {
				logger.Error("Server error", "error", err)
			}
		}
	}()

	// Log information about accessing the admin WebUI
	adminPath := a.Config.Admin.Path
	if adminPath == "" {
		adminPath = "/admin" // Default if not set
	}

	// Log access information
	protocol := "https"
	if !a.Config.Http.Https {
		protocol = "http"
	}

	fullDomain := a.Config.Http.FullDomain()
	logger.Info("Access the Admin WebUI at", "url", fmt.Sprintf("%s://%s%s", protocol, fullDomain, adminPath))

	// Wait for the main server to be ready before initializing the proxy
	<-serverReady

	// Initialize and start the reverse proxy after the main server is ready
	var p *proxy.Proxy
	logger.Info("Starting reverse proxy initialization...")
	p, err = httpserve.InitializeProxy(a)
	if err != nil {
		logger.Error("Failed to initialize reverse proxy", "error", err, "solution", "Check logs for specific HTTPS binding errors and container capabilities")
		// Alert the user that this is a critical issue
		fmt.Println("[gordon] | ⚠️  WARNING: Reverse proxy failed to initialize. HTTPS server will not be available!")
		fmt.Println("[gordon] | ⚠️  Check logs for details and verify container has correct permissions.")
	} else {
		logger.Info("Reverse proxy initialization complete")
	}

	// Wait for interrupt signal
	<-sigs
	logger.Info("Received shutdown signal")

	// Create a deadline for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Gracefully shutdown the server
	logger.Info("Shutting down server...")
	if err := e.Shutdown(ctx); err != nil {
		logger.Error("Server shutdown error", "error", err)
	}

	// Perform application shutdown (including database closure)
	if err := a.Shutdown(); err != nil {
		logger.Error("Application shutdown error", "error", err)
	}

	// Stop the proxy if it was started
	if p != nil {
		logger.Info("Stopping reverse proxy...")
		if err := p.Stop(); err != nil {
			logger.Error("Failed to stop reverse proxy", "error", err)
		}
	}

	logger.Info("Shutdown complete")
	return nil
}
