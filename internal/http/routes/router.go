package routes

import (
	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/config"
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
	e = bindAdminRoutes(e, app.Config)
	e = bindHtmxRoutes(e, app.Config)
	e = bindStaticRoutes(e, app.Config)
	e.HTTPErrorHandler = handlers.ErrorNumberHandler
	return e
}
func bindAdminRoutes(e *echo.Echo, config config.Provider) *echo.Echo {
	// We pass the embed config to the routes so we can parse the strings.yml file
	e.GET("/admin", func(c echo.Context) error {
		return AdminRoute(c, config)
	})
	e.GET("/admin/install", func(c echo.Context) error {
		return InstallRoute(c, config)
	})

	e.POST("/admin/install/traefik", handlers.TraefikInstallerHandler)
	return e
}

func bindHtmxRoutes(e *echo.Echo, config config.Provider) *echo.Echo {
	e.GET("/htmx", handlers.HTMXHandler)
	e.GET("/htmx/fragment", handlers.HTMXHandler)
	e.POST("/htmx", handlers.HTMXHandler)
	e.POST("/htmx/fragment", handlers.HTMXHandler)
	return e
}

func bindStaticRoutes(e *echo.Echo, config config.Provider) *echo.Echo {

	e.GET("/*", func(c echo.Context) error {
		return StaticRoute(c, config)
	})
	return e
}
