package middlewares

import (
	"strings"

	"github.com/labstack/echo/v4"
)

// LangKey is a key for setting and getting the language from the context
const LangKey = "CurrentLang"

// LanguageDetectionMiddleware detects the current language and sets it in the context
func LanguageDetectionMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		lang := detectCurrentLanguage(c)
		c.Set(LangKey, lang)
		return next(c)
	}
}

func detectCurrentLanguage(c echo.Context) string {
	header := c.Request().Header.Get("Accept-Language")
	if strings.Contains(header, "fr") {
		return "fr"
	}
	return "en" // Default to English
}
