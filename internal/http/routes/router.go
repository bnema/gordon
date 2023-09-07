package routes

import (
	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/handlers"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
)

func ConfigureRouter(e *echo.Echo, a *app.App, ac *app.Config) *echo.Echo {

	// SetLogger is a middleware that sets the logger in the context
	e.Use(middlewares.SetLogger(a.HttpLogger))
	// HTTPAccessLogger is a middleware that logs HTTP requests
	e.Use(middlewares.HTTPAccessLogger(a.HttpLogger))
	e.Use(middlewares.LanguageDetection)
	e.Use(middlewares.ColorSchemeDetection)
	//e.Use(middlewares.SecureMiddleware()) // Need testing

	// Register routes
	e = bindAdminRoutes(e, a, ac)
	e = bindHtmxRoutes(e, a, ac)
	e = bindStaticRoutes(e, a.Config)
	e.HTTPErrorHandler = handlers.ErrorNumberHandler
	return e
}
func bindAdminRoutes(e *echo.Echo, a *app.App, ac *app.Config) *echo.Echo {
	// We pass the embed config to the routes so we can parse the strings.yml file
	e.GET("/admin", func(c echo.Context) error {
		return AdminRoute(c, ac, a)
	})
	e.GET("/admin/install", func(c echo.Context) error {
		return InstallRoute(c, ac, a)
	})

	e.POST("/admin/install/traefik", func(c echo.Context) error {
		return handlers.TraefikInstallerHandler(c, a)
	})
	e.GET("/admin/install/traefik/sse", func(c echo.Context) error {
		return handlers.TraefikInstallerSSEHandler(c, a)
	})
	return e
}

func bindHtmxRoutes(e *echo.Echo, a *app.App, ac *app.Config) *echo.Echo {
	e.GET("/htmx", func(c echo.Context) error {
		return handlers.HTMXHandler(c, a, ac)
	})
	e.GET("/htmx/fragment", func(c echo.Context) error {
		return handlers.HTMXHandler(c, a, ac)
	})
	e.POST("/htmx", func(c echo.Context) error {
		return handlers.HTMXHandler(c, a, ac)
	})
	e.POST("/htmx/fragment", func(c echo.Context) error {
		return handlers.HTMXHandler(c, a, ac)
	})
	return e
}

func bindStaticRoutes(e *echo.Echo, config *app.Config) *echo.Echo {
	e.GET("/*", func(c echo.Context) error {
		return StaticRoute(c, config)
	})
	return e
}
