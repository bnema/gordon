package middleware

import (
	"log"
	"os"

	"github.com/bnema/gordon/internal/server"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func SecureRoutes(a *server.App) echo.MiddlewareFunc {
	proxyURL := a.Config.Build.ProxyURL // <-- https://gordon-proxy.bnema.dev
	urlCheckVersion := a.Config.Build.ProxyURL + "/version"
	return middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "SAMEORIGIN",
		HSTSMaxAge:            3600,
		HSTSExcludeSubdomains: false,
		ContentSecurityPolicy: "default-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data:; script-src 'self' 'unsafe-inline' 'unsafe-eval'; connect-src 'self' " + proxyURL + " " + urlCheckVersion,
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
