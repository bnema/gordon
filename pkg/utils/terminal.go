package utils

import (
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
)

// SetRawMode sets the terminal to raw mode.
func SetRawMode() {
	cmd := exec.Command("stty", "raw", "-echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

// RestoreTerminal restores the terminal settings.
func RestoreTerminal() {
	cmd := exec.Command("stty", "-raw", "echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

func RunAppCatchSIGINT(startServerFunc func() error, logger *zerolog.Logger) {
	// Start the server in a separate goroutine
	go func() {
		if err := startServerFunc(); err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("Failed to start the server")
		}
	}()

	// Catch SIGINT and SIGTERM signals without raw mode initially
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c

	logger.Warn().Msg("Graceful shutdown initiated")
	logger.Warn().Msg("Server will shutdown in 5 seconds press ENTER to cancel")

	// Now, set raw mode
	SetRawMode()
	defer RestoreTerminal()

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
		logger.Info().Msg("Server shutdown")
	case <-keypress:
		logger.Info().Msg("Shutdown cancelled by user")
	}
}
