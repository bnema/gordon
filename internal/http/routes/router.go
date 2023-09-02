package routes

import (
	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/internal/http/handlers"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
)

func NewRouter(appLogger, echoLogger zerolog.Logger) *echo.Echo {
	e := echo.New()

	// Register middlewares
	e.Use(middlewares.NewRequestLoggerMiddleware(echoLogger))

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
