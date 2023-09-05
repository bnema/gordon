package routes

import (
	"os"

	"github.com/labstack/echo/v4"
)

func (a *AppConfig) StaticRoute(c echo.Context) error {

	// Set the cache-control header to cache the static files for 1 day if PROD env is set to true

	if os.Getenv("PROD") == "true" {
		c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		c.Response().Header().Set("Cache-Control", "no-cache")
	}
	return echo.StaticDirectoryHandler(a.Config.GetPublicFS(), false)(c)
}
