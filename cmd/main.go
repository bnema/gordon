package main

import (
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/routes"
	"gogs.bnema.dev/gordon-echo/pkg/docker"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func setRawMode() {
	cmd := exec.Command("stty", "raw", "-echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

func restoreTerminal() {
	cmd := exec.Command("stty", "-raw", "echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

func main() {
	// Port = os.Getenv("PORT") or fallback to default port (1323)
	port := os.Getenv("PORT")
	if port == "" {
		port = "1323"
	}

	// Initialize Gordon
	gordon, err := app.NewApp()
	if err != nil {
		// Handle initialization error and exit
		gordon.APPLogger.Error().Err(err).Msg("Failed to initialize app")
		return
	}

	docker.ListDocker() // List docker containers

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Logger = utils.NewEchoLoggerWrapper(gordon.APPLogger)
	e = routes.ConfigureRouter(e, gordon)

	// Start the server in a separate goroutine
	go func() {
		gordon.APPLogger.Info().Msgf("Server is listening on port %s", port)
		if err := e.Start(":" + port); err != nil && err != http.ErrServerClosed {
			gordon.APPLogger.Fatal().Err(err).Msg("Failed to start the server")
		}
	}()

	// Catch SIGINT and SIGTERM signals without raw mode initially
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c

	gordon.APPLogger.Warn().Msg("Graceful shutdown initiated")
	gordon.APPLogger.Warn().Msg("Server will shutdown in 5 seconds press ENTER to cancel")

	// Now, set raw mode
	setRawMode()
	defer restoreTerminal() // Ensure terminal is restored even if program exits unexpectedly

	// Channel to signal a key press
	keypress := make(chan struct{})

	// Goroutine to listen for a key press
	go func() {
		var b []byte = make([]byte, 1)
		os.Stdin.Read(b)
		keypress <- struct{}{}
	}()

	// Wait for either a key press or the 5-second timer
	select {
	case <-time.After(5 * time.Second):
		gordon.APPLogger.Info().Msg("Server shutdown")
	case <-keypress:
		gordon.APPLogger.Info().Msg("Shutdown cancelled by user")
	}

}
