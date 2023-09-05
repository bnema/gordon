package middlewares

import (
	"fmt"

	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

// ColorSchemeKey is a key for setting and getting the color scheme from the context
const ColorSchemeKey = "CurrentColorScheme"

// ColorSchemeDetection detects the current color scheme and sets it in the context
func ColorSchemeDetection(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		colorScheme := detectCurrentColorScheme(c)
		fmt.Println("Color scheme detected:", colorScheme)
		c.Set(ColorSchemeKey, colorScheme)
		return next(c)
	}
}

func detectCurrentColorScheme(c echo.Context) string {
	// Check if the color scheme key is set in the session storage
	sess, err := session.Get("session", c)
	if err != nil {
		// If session storage is not available, proceed with "Sec-CH-Prefers-Color-Scheme" header detection
		return "light"
	}

	if colorSchemeValue, ok := sess.Values["colorScheme"]; ok && colorSchemeValue != nil {
		return colorSchemeValue.(string) // Return the color scheme value from the session storage
	}

	// If color scheme key is not set, proceed with "Sec-CH-Prefers-Color-Scheme" header detection
	header := c.Request().Header.Get("Sec-CH-Prefers-Color-Scheme")
	if header == "dark" {
		return "dark"
	}
	return "light" // Default to light mode
}
