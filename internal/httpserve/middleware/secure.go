package middleware

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/bnema/gordon/internal/server"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func SecureRoutes(a *server.App) echo.MiddlewareFunc {
	proxyURL := a.Config.Build.ProxyURL
	urlCheckVersion := a.Config.Build.ProxyURL + "/version"

	// Base directives
	defaultSrc := "'self'"
	styleSrc := "'self' 'unsafe-inline'"
	fontSrc := "'self' data:"
	imgSrc := "'self' data:"
	connectSrc := fmt.Sprintf("'self' %s %s", proxyURL, urlCheckVersion)

	// Start building script-src
	scriptSrc := "'self' 'unsafe-inline' 'unsafe-eval'"

	// Conditionally add Cloudflare Insights if SkipCertificates is true
	if a.Config.ReverseProxy.SkipCertificates {
		scriptSrc += " https://static.cloudflareinsights.com"
	}

	// Construct the final CSP string
	csp := fmt.Sprintf(
		"default-src %s; style-src %s; font-src %s; img-src %s; script-src %s; connect-src %s",
		defaultSrc, styleSrc, fontSrc, imgSrc, scriptSrc, connectSrc,
	)

	return middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "SAMEORIGIN",
		HSTSMaxAge:            3600,
		HSTSExcludeSubdomains: false,
		ContentSecurityPolicy: csp, // Use the dynamically constructed CSP
	})
}

// InitSessionMiddleware initializes the session middleware with secure options
func InitSessionMiddleware(a *server.App) echo.MiddlewareFunc {
	isHttps := a.Config.Http.Https
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		log.Fatal("Environment variable SESSION_SECRET is not set or cannot be read")
	}
	store := sessions.NewCookieStore([]byte(secret))
	store.Options = &sessions.Options{
		Path:     "/",
		HttpOnly: true,
		Secure:   isHttps,
		MaxAge:   86400,
		SameSite: http.SameSiteLaxMode,
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := session.Middleware(store)(next)(c)
			if err != nil {
				log.Printf("Error in session middleware: %v", err)
				return err
			}
			_, err = session.Get("session", c)
			if err != nil {
				log.Printf("Could not retrieve session: %v", err)
			}
			return nil
		}
	}
}
