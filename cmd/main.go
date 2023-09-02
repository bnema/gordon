package main

import (
	"gogs.bnema.dev/gordon-echo/internal"
	"gogs.bnema.dev/gordon-echo/internal/http/routes"
)

func main() {
	// Setup logging
	internal.SetupLogging()

	// Initialize loggers
	appLogger := internal.InitializeAppLogger()
	echoLogger := internal.InitializeHTTPLogger()

	// Initialize echo server with routes and middlewares
	e := routes.NewRouter(appLogger, echoLogger)

	if err := e.Start(":1323"); err != nil {
		appLogger.Error().Err(err).Msg("Failed to start the server")
	}
}
