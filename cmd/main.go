package main

import (
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/routes"
)

func main() {
	// Initialize Gordon
	gordon, err := app.NewApp()
	if err != nil {
		// Handle initialization error and exit
		gordon.APPLogger.Error().Err(err).Msg("Failed to initialize app")
		return
	}

	// Initialize echo server with routes and middlewares
	e := routes.NewRouter(gordon.APPLogger.Logger, gordon.HTTPLogger.Logger)

	if err := e.Start(":1323"); err != nil {
		gordon.APPLogger.Error().Err(err).Msg("Failed to start the server")
	}
	// Graceful shutdown
	gordon.APPLogger.Info().Msg("Server is shutting down...")
}
