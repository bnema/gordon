package main

import (
	"os"

	"github.com/labstack/echo/v4"
	root "gogs.bnema.dev/gordon-echo"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/routes"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func main() {
	// Port = os.Getenv("PORT") or fallback to default port (1323)
	port := os.Getenv("PORT")
	if port == "" {
		port = "1323"
	}
	// Initialize Gordon
	gordon, err := app.NewApp(
		&app.Config{
			TemplateFS: root.TemplateFS,
			PublicFS:   root.PublicFS,
			ModelFS:    root.ModelFS,
		},
	)
	if err != nil {
		// Handle initialization error and exit
		gordon.AppLogger.Error().Err(err).Msg("Failed to initialize app")
		return
	}
	utils.CaptureSTDOUT(gordon.AppLogger)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Logger = utils.NewEchoLoggerWrapper(gordon.AppLogger)
	e = routes.ConfigureRouter(e, gordon, gordon.Config)

	startFunc := func() error {
		gordon.AppLogger.Info().Msgf("Server is listening on port %s", port)
		return e.Start(":" + port)
	}
	utils.RunAppCatchSIGINT(startFunc, &gordon.AppLogger.Logger)

}
