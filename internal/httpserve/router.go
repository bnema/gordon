package httpserve

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/httpserve/handler"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/labstack/echo/v4"
)

// RegisterRoutes registers all routes for the application
func RegisterRoutes(e *echo.Echo, a *app.App) *echo.Echo {
	AdminPath := a.AdminPath
	e.Use(middleware.ErrorHandler)
	// Use middlewares
	// e.Use(middleware.SecureRoutes())
	e.Use(middleware.ColorSchemeDetection)
	e.Use(middleware.LanguageDetection)

	// Register routes

	// Serve admin routes
	bindAdminRoute(e, a, AdminPath)
	// Serve static files
	bindStaticRoute(e, a, "/*")
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
