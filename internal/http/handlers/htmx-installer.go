package handlers

import (
	"fmt"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// Create a RejectPOSTPolicy function to reject POST requests if they are not allowed

func traefikInstallerHandler(c echo.Context, logger *utils.Logger) error {
	// Reject POST requests if they are not allowed with the RejectPOSTPolicy function
	RejectPOSTPolicy(c)

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

	fmt.Println(topDomain, adminEmail)

	// stop here for now
	return nil

}
