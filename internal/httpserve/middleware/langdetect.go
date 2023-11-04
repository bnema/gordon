package middleware

import (
	"strings"

	"github.com/labstack/echo/v4"
)

// LanguageDetectiondetects the current language and sets it in the context
func LanguageDetection(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		lang := detectCurrentLanguage(c)
		c.Set("LangKey", lang)
		return next(c)
	}
}

func detectCurrentLanguage(c echo.Context) string {
	// If lang key is not set, proceed with "Accept-Language" header detection
	header := c.Request().Header.Get("Accept-Language")
	if strings.Contains(header, "fr") {
		return "fr"
	}
	return "en" // Default to English
}
