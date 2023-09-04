package routes

import (
	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/handlers"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
)

func ConfigureRouter(e *echo.Echo, app *app.App) *echo.Echo {
	// The logger is already set in main.go, so we don't need to set it again here.

	// Register middlewares
	e.Use(middlewares.NewRequestLoggerMiddleware(app.HTTPLogger.Logger))
	e.Use(middlewares.LanguageDetectionMiddleware)

	// Register routes
	e = bindStaticAdminUI(e)

	return e
}
func bindStaticAdminUI(e *echo.Echo) *echo.Echo {
	e.GET("/admin", AdminRoute)
	e.GET("/htmx", handlers.HTMXHandler)
	e.GET("/*", StaticRoute)
	e.HTTPErrorHandler = handlers.ErrorNumberHandler
	return e
}
