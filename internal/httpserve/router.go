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
	// List all images component
	e.GET("/htmx/image-manager", func(c echo.Context) error {
		return handler.ImageManagerComponent(c, a)
	})
	// Delete an image
	e.DELETE("/htmx/image-manager/delete/:ID", func(c echo.Context) error {
		return handler.ImageManagerDelete(c, a)
	})

	// List all containers
	e.GET("/htmx/container-manager", func(c echo.Context) error {
		return handler.ContainerManagerComponent(c, a)
	})
	// Stop a container
	e.POST("/htmx/container-manager/stop/:ID", func(c echo.Context) error {
		return handler.ContainerManagerStop(c, a)
	})
	// Delete a container
	e.DELETE("/htmx/container-manager/delete/:ID", func(c echo.Context) error {
		return handler.ContainerManagerDelete(c, a)
	})
	// Start a container
	e.POST("/htmx/container-manager/start/:ID", func(c echo.Context) error {
		return handler.ContainerManagerStart(c, a)
	})
	// Edit a container view
	e.GET("/htmx/container-manager/edit/:ID", func(c echo.Context) error {
		return handler.ContainerManagerEditGET(c, a)
	})
	// Edit a container action
	e.POST("/htmx/container-manager/edit/:ID", func(c echo.Context) error {
		return handler.ContainerManagerEditPOST(c, a)
	})

	// Display upload-image component
	e.GET("/htmx/upload-image", func(c echo.Context) error {
		return handler.UploadImageGETHandler(c, a)
	})
	// Upload image
	e.POST("/htmx/upload-image", func(c echo.Context) error {
		return handler.UploadImagePOSTHandler(c, a)
	})
	// Display create-container component
	e.GET("htmx/create-container/:ID", func(c echo.Context) error {
		return handler.CreateContainerGET(c, a)
	})
	// Create container
	e.POST("htmx/create-container/:ID", func(c echo.Context) error {
		return handler.CreateContainerPOST(c, a)
	})
}
