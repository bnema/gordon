package handlers

import (
	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/pkg/scripts"
)

// TraefikInstallerHandler handles the installation of Traefik
func TraefikInstallerHandler(c echo.Context, a *app.App) error {
	logger := a.AppLogger
	// Retrieve the logger from the context
	// Reject POST requests if they are not allowed with the RejectPOSTPolicy function
	if err := RejectPOSTPolicy(c); err != nil {
		logger.Error().Err(err).Msg("POST request rejected")
		return err
	}

	// Get data from the form install-traefik-form (input fields : topdomain, adminemail) and sanitize it
	topDomain, err := SanitizeUserInput(c.FormValue("topdomain"))
	if err != nil {
		logger.Error().Err(err).Msg("Failed to sanitize user input")
		return err
	}
	adminEmail, err := SanitizeUserInput(c.FormValue("adminemail"))
	if err != nil {
		logger.Error().Err(err).Msg("Failed to sanitize user input")
		return err
	}

	// Call the InstallTraefik function from pkg/scripts/install-traefik.go
	if success, err := scripts.InstallTraefik(topDomain, adminEmail, a); err != nil {
		logger.Error().Err(err).Msg("Failed to install Traefik")
		return err
	} else if !success {
		logger.Error().Msg("Failed to install Traefik")
		return err
	} else {
		logger.Info().Msg("Traefik installed successfully")
		return c.String(200, "Traefik installed successfully")
	}
}

// use the handler at the route /admin/install/traefik
