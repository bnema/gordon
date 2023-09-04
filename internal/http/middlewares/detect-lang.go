package middlewares

import (
	"strings"

	"github.com/labstack/echo-contrib/session"
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
	// Check if the lang key is set in the session storage
	sess, _ := session.Get("session", c) // Assuming you have a session named "session"
	if langValue, ok := sess.Values["lang"]; ok {
		return langValue.(string) // Return the lang value from the session storage
	}

	// If lang key is not set, proceed with "Accept-Language" header detection
	header := c.Request().Header.Get("Accept-Language")
	if strings.Contains(header, "fr") {
		return "fr"
	}
	return "en" // Default to English
}
