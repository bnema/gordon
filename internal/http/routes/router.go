package routes

import (
	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/handlers"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
)

func NewRouter(app *app.App) *echo.Echo {
	e := echo.New()

	// Register middlewares
	e.Use(middlewares.NewRequestLoggerMiddleware(app.HTTPLogger.Logger))

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
