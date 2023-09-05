package routes

import (
	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/handlers"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
)

func ConfigureRouter(e *echo.Echo, app *app.App) *echo.Echo {
	// SetLogger is a middleware that sets the logger in the context
	e.Use(middlewares.SetLogger(app.HttpLogger))
	// HTTPAccessLogger is a middleware that logs HTTP requests
	e.Use(middlewares.HTTPAccessLogger(app.HttpLogger))
	e.Use(middlewares.LanguageDetection)
	e.Use(middlewares.ColorSchemeDetection)
	//e.Use(middlewares.SecureMiddleware()) // Need testing

	// Register routes
	e = bindAdminRoutes(e)
	e = bindHtmxRoutes(e)
	e = bindStaticRoutes(e)
	e.HTTPErrorHandler = handlers.ErrorNumberHandler
	return e
}
func bindAdminRoutes(e *echo.Echo) *echo.Echo {
	e.GET("/admin", AdminRoute)
	e.GET("/admin/install", InstallRoute)
	e.POST("/admin/install/traefik", handlers.TraefikInstallerHandler)
	return e
}

func bindHtmxRoutes(e *echo.Echo) *echo.Echo {
	e.GET("/htmx", handlers.HTMXHandler)
	e.GET("/htmx/fragment", handlers.HTMXHandler)
	e.POST("/htmx", handlers.HTMXHandler)
	e.POST("/htmx/fragment", handlers.HTMXHandler)
	return e
}

func bindStaticRoutes(e *echo.Echo) *echo.Echo {
	e.GET("/*", StaticRoute)
	return e
}
