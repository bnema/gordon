package handlers

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/pkg/templating"
)

func RejectPOSTPolicy(c echo.Context) error {
	// Maximum POST size in bytes
	const maxPOSTSize = 1024 * 1024 // = 1 MB
	// Check the request method
	if c.Request().Method != http.MethodPost {
		return fmt.Errorf("invalid request method")
	}
	// Check for unusually large POST bodies, which might indicate an attempt at a DoS attack
	if c.Request().ContentLength > maxPOSTSize {
		return fmt.Errorf("request too large: %d bytes", c.Request().ContentLength)
	}

	// Check for other suspicious behaviors, e.g., missing user agent, too many request headers, etc.
	if c.Request().UserAgent() == "" {
		return fmt.Errorf("missing user agent")
	}

	// If all checks pass
	return nil
}

func SanitizeUserInput(input string) (string, error) {
	sanitized, err := templating.SanitizeHTML(input)
	if err != nil {
		return "", fmt.Errorf("Failed to sanitize user input: %w", err)
	}
	return sanitized, nil
}
