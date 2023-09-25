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
	AdminPath := a.Config.Admin.Path
	e.Use(middleware.SetCommonDataMiddleware(a))
	e.Use(middleware.ErrorHandler)
	// Add session middleware
	e.Use(session.Middleware(sessions.NewCookieStore([]byte(os.Getenv("SESSION_SECRET")))))
	e.Use(middleware.ColorSchemeDetection)
	e.Use(middleware.LanguageDetection)

	// Register routes
	bindAdminRoute(e, a, AdminPath)
	bindStaticRoute(e, a, "/*")
	bindLoginRoute(e, a, AdminPath)
	bindHTMXEndpoints(e, a)
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

func bindAdminRoute(e *echo.Echo, a *app.App, adminPath string) {
	e.GET(adminPath, func(c echo.Context) error {
		return handler.AdminRoute(c, a)
	}, middleware.RequireLogin) // Require login

	e.GET(adminPath+"/manager", func(c echo.Context) error {
		return handler.AdminManagerRoute(c, a)
	})
}

func bindLoginRoute(e *echo.Echo, a *app.App, adminPath string) {
	e.GET(adminPath+"/login", func(c echo.Context) error {
		return handler.RenderLoginPage(c, a)
	})
	e.GET(adminPath+"/login/oauth/github", func(c echo.Context) error {
		return handler.StartOAuthGithub(c, a)
	})
	e.GET(adminPath+"/login/oauth/callback", func(c echo.Context) error {
		return handler.OAuthCallback(c, a)
	})
	e.GET(adminPath+"/logout", func(c echo.Context) error {
		return handler.Logout(c, a)
	})
}

func bindHTMXEndpoints(e *echo.Echo, a *app.App) {
	e.GET("/htmx/image-manager", func(c echo.Context) error {
		return handler.ImageManagerHandler(c, a)
	})
	e.DELETE("/htmx/image-manager/delete/:ID", func(c echo.Context) error {
		return handler.ImageManagerDeleteHandler(c, a)
	})
	e.GET("/htmx/container-manager", func(c echo.Context) error {
		return handler.ContainerManagerHandler(c, a)
	})
	e.POST("/htmx/container-manager/stop/:ID", func(c echo.Context) error {
		return handler.ContainerManagerStopHandler(c, a)
	})
	e.DELETE("/htmx/container-manager/delete/:ID", func(c echo.Context) error {
		return handler.ContainerManagerDeleteHandler(c, a)
	})
	e.GET("/htmx/upload-image", func(c echo.Context) error {
		return handler.UploadImageGETHandler(c, a)
	})
	e.POST("/htmx/upload-image", func(c echo.Context) error {
		return handler.UploadImagePOSTHandler(c, a)
	})
	e.GET("htmx/create-container/:ID", func(c echo.Context) error {
		return handler.CreateContainerGETHandler(c, a)
	})
}
