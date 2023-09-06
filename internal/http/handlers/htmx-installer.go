package handlers

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/pkg/scripts"
)

var eventsChan = make(chan string)

// TraefikInstallerHandler handles the installation of Traefik
func TraefikInstallerHandler(c echo.Context, a *app.App) error {
	logger := a.AppLogger
	eventsChan <- "Starting Traefik installation..."
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
		eventsChan <- "Traefik installation completed!"
		return c.String(200, "Traefik installed successfully")
	}
}

func TraefikInstallerSSEHandler(c echo.Context, a *app.App) error {
	w := c.Response().Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return c.String(http.StatusInternalServerError, "Streaming unsupported!")
	}

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")

	for {
		// Listen for a new message to be sent to the client
		msg, open := <-eventsChan
		if !open {
			fmt.Println("Connection closed")
			break
		}
		_, err := fmt.Fprintf(w, "data: %s\n\n", msg)
		if err != nil {
			return err
		}
		flusher.Flush()
	}
	return nil
}
