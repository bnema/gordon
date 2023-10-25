package handler

import (
	"net/http"
	"os"

	"github.com/bnema/gordon/internal/server"
	"github.com/labstack/echo/v4"
)

// StaticRoute serves static files from the embedded filesystem
func StaticRoute(c echo.Context, a *server.App) error {
	// Set the cache-control header based on PROD environment variable
	if os.Getenv("PROD") == "true" {
		c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		c.Response().Header().Set("Cache-Control", "no-cache")
	}

	// Serve the file from the embedded filesystem
	publicFS := http.FileServer(http.FS(a.PublicFS))
	publicFS.ServeHTTP(c.Response(), c.Request())
	return nil
}
