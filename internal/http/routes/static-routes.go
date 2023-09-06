package routes

import (
	"io/fs"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/config"
)

func StaticRoute(c echo.Context, config config.Provider) error {

	// Set the cache-control header based on PROD environment variable
	if os.Getenv("PROD") == "true" {
		c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		c.Response().Header().Set("Cache-Control", "no-cache")
	}

	// Serve the file from the embedded filesystem
	httpFs := http.FileServer(http.FS(fs.FS(config.GetPublicFS())))
	httpFs.ServeHTTP(c.Response(), c.Request())
	return nil
}
