package main

import (
	"os"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/routes"
	"gogs.bnema.dev/gordon-echo/pkg/docker"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

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

	startFunc := func() error {
		gordon.APPLogger.Info().Msgf("Server is listening on port %s", port)
		return e.Start(":" + port)
	}
	utils.RunAppCatchSIGINT(startFunc, &gordon.APPLogger.Logger)
}
