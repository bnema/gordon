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
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
)

// Global server instance for shutdown handling
var (
	globalServer     *server.App
	globalServerLock sync.Mutex
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

	// Initialize and start the reverse proxy using the already loaded configuration
	p, err := httpserve.InitializeProxy(a)
	if err != nil {
		logger.Error("Failed to initialize reverse proxy", "error", err)
		// Continue even if proxy fails, as the main server should still work
	}

	e := echo.New()
	e.Server.ReadTimeout = 10 * time.Minute
	e.Server.WriteTimeout = 10 * time.Minute
	e.HideBanner = true
	e.HidePort = true
	e = httpserve.RegisterRoutes(e, a)

	// Setup a channel to capture termination signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Start server in a goroutine
	go func() {
		logger.Info("Starting server", "port", port)
		if err := e.Start(fmt.Sprintf(":%s", a.Config.Http.Port)); err != nil {
			if err.Error() != "http: Server closed" {
				logger.Error("Server error", "error", err)
			}
		}
	}()

	// Test admin connections after the server has started (in a separate goroutine)
	if p != nil {
		go func() {
			// Add a delay to ensure the server has fully started
			time.Sleep(2 * time.Second)
			p.TestAdminConnectionLater()
		}()
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
