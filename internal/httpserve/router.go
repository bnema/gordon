package httpserve

import (
	"os"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/httpserve/handler"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

// RegisterRoutes registers all routes for the application
func RegisterRoutes(e *echo.Echo, a *app.App) *echo.Echo {
	AdminPath := a.AdminPath
	e.Use(middleware.ErrorHandler)
	// Use session middleware
	// Add session middleware
	e.Use(session.Middleware(sessions.NewCookieStore([]byte(os.Getenv("SECRET")))))
	e.Use(middleware.ColorSchemeDetection)
	e.Use(middleware.LanguageDetection)

	// Register routes
	bindAdminRoute(e, a, AdminPath)
	bindStaticRoute(e, a, "/*")
	bindLoginRoute(e, a, AdminPath)

	// Protect the root path with a 403
	e.GET("/", func(c echo.Context) error {
		return c.String(403, "403 Forbidden")
	})

	return e
}

func bindStaticRoute(e *echo.Echo, a *app.App, path string) {
	e.GET(path, func(c echo.Context) error {
		return handler.StaticRoute(c, a)
	})
}

func bindAdminRoute(e *echo.Echo, a *app.App, path string) {
	e.GET(path, func(c echo.Context) error {
		return handler.AdminRoute(c, a)
	})
}

func bindLoginRoute(e *echo.Echo, a *app.App, adminPath string) {
	e.GET(adminPath+"/login", func(c echo.Context) error {
		return handler.RenderLoginPage(c, a)
	})
	e.GET(adminPath+"/login/:provider", func(c echo.Context) error {
		return handler.StartOAuth(c, a)
	})
	e.GET(a.OauthCallbackURL, func(c echo.Context) error {
		return handler.OAuthCallback(c, a)
	})
	e.GET(adminPath+"/logout", func(c echo.Context) error {
		return handler.Logout(c, a)
	})
}
