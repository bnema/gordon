package handlers

import (
	"fmt"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/pkg/scripts"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// TraefikInstallerHandler handles the installation of Traefik
func TraefikInstallerHandler(c echo.Context) error {
	// Retrieve the logger from the context
	logger := c.Get("logger").(*utils.Logger)
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

	fmt.Println("POST request PAYLOAD=", topDomain, adminEmail)

	// Call to create the YAML files
	err = scripts.CreateYAMLFiles(topDomain, adminEmail, logger)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create YAML files")
		return err
	}
	return nil

}

// use the handler at the route /admin/install/traefik
