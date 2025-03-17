package handlers

import (
	"net/http"

	"github.com/bnema/gordon/internal/server"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// GetConfigAPI handles the /api/config endpoint
// It returns the admin path from the configuration
func GetConfigAPI(c echo.Context, a *server.App) error {
	log.Debug("GetConfigAPI: Returning admin path", "path", a.Config.Admin.Path)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":   true,
		"adminPath": a.Config.Admin.Path,
	})
}
