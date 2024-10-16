package httpserve

import (
	"github.com/bnema/gordon/internal/httpserve/handlers"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/server"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo/v4"
	echomid "github.com/labstack/echo/v4/middleware"
)

// RegisterRoutes registers all routes and middlewares
func RegisterRoutes(e *echo.Echo, a *server.App) *echo.Echo {
	// e.Use(echomid.Logger()) too verbose
	e.Use(echomid.Recover())
	// Initiate the session middleware
	e.Use(middleware.InitSessionMiddleware(a))

	// Language detection
	e.Use(middleware.LanguageDetection)

	AdminPath := a.Config.Admin.Path
	bindLoginRoute(e, a, AdminPath)
	bindAPIEndpoints(e, a)

	// SetCommonDataMiddleware will pass data to the renderer
	e.Use(middleware.SetCommonDataMiddleware(a))

	// Error handler middleware
	e.Use(middleware.ErrorHandler)

	// Use middlewares
	e.Use(middleware.SecureRoutes(a))

	// Color scheme detection for dark/light mode
	e.Use(middleware.ColorSchemeDetection)

	bindAdminRoute(e, a, AdminPath)
	bindStaticRoute(e, a, "/*")
	bindHTMXEndpoints(e, a)

	// Protect the root path with a 403
	e.GET("/", func(c echo.Context) error {
		return c.String(403, "403 Forbidden")
	})

	return e
}

// bindAPIEndpoints expose /api endpoints and speaks only JSON
func bindAPIEndpoints(e *echo.Echo, a *server.App) {
	apiGroup := e.Group("/api")

	// Device flow endpoints (without RequireToken middleware)
	apiGroup.POST("/device/code", func(c echo.Context) error {
		return handlers.DeviceCodeRequest(c, a)
	})
	apiGroup.POST("/device/token", func(c echo.Context) error {
		return handlers.DeviceTokenRequest(c, a)
	})

	// Other API endpoints that require a token
	protectedApiGroup := apiGroup.Group("", middleware.RequireToken(a))
	protectedApiGroup.GET("/ping", func(c echo.Context) error {
		return handlers.GetInfos(c, a)
	})
	protectedApiGroup.POST("/deploy", func(c echo.Context) error {
		return handlers.PostDeploy(c, a)
	})
	protectedApiGroup.POST("/push", func(c echo.Context) error {
		return handlers.PostPush(c, a)
	})
}

// bindStaticRoute bind static path
func bindStaticRoute(e *echo.Echo, a *server.App, path string) {
	e.GET(path, func(c echo.Context) error {
		return handlers.StaticRoute(c, a)
	}, echomid.Gzip())
}

// bindLoginRoute binds all login routes
func bindLoginRoute(e *echo.Echo, a *server.App, adminPath string) {
	e.GET(adminPath+"/login", func(c echo.Context) error {
		return handlers.RenderLoginPage(c, a)
	})
	e.GET(adminPath+"/login/oauth/github", func(c echo.Context) error {
		return handlers.StartOAuthGithub(c, a)
	})
	e.GET(adminPath+"/login/oauth/callback", func(c echo.Context) error {
		return handlers.OAuthCallback(c, a)
	})
	e.GET(adminPath+"/logout", func(c echo.Context) error {
		return handlers.Logout(c, a)
	})
}

// bindAdminRoute binds all admin routes
func bindAdminRoute(e *echo.Echo, a *server.App, adminPath string) {
	adminGroup := e.Group(adminPath)
	// Since login is behind the admin path, we cannot use group middleware
	adminGroup.GET("", func(c echo.Context) error {
		return c.Redirect(302, adminPath+"/manager")
	}, middleware.RequireLogin(a))

	adminGroup.GET("/manager", func(c echo.Context) error {
		return handlers.AdminManagerRoute(c, a)
	}, middleware.RequireLogin(a))
	adminGroup.GET("/cc/:ID", func(c echo.Context) error {
		return handlers.CreateContainerFullGET(c, a)
	}, middleware.RequireLogin(a))
}

// bindHTMXEndpoints binds all HTMX endpoints
func bindHTMXEndpoints(e *echo.Echo, a *server.App) {
	// Create a  group for /htmx endpoints
	htmxGroup := e.Group("/htmx")

	// Apply middleware to the group, so all endpoints are protected
	htmxGroup.Use(middleware.RequireLogin(a))

	// List all images component
	htmxGroup.GET("/image-manager", func(c echo.Context) error {
		return handlers.ImageManagerComponent(c, a)
	})
	// Delete an image
	htmxGroup.DELETE("/image-manager/delete/:ID", func(c echo.Context) error {
		return handlers.ImageManagerDelete(c, a)
	})

	// List all containers
	htmxGroup.GET("/container-manager", func(c echo.Context) error {
		return handlers.ContainerManagerComponent(c, a)
	})
	// Stop a container
	htmxGroup.POST("/container-manager/stop/:ID", func(c echo.Context) error {
		return handlers.ContainerManagerStop(c, a)
	})
	// Delete a container
	htmxGroup.DELETE("/container-manager/delete/:ID", func(c echo.Context) error {
		return handlers.ContainerManagerDelete(c, a)
	})
	// Start a container
	htmxGroup.POST("/container-manager/start/:ID", func(c echo.Context) error {
		return handlers.ContainerManagerStart(c, a)
	})
	// Edit a container view
	htmxGroup.GET("/container-manager/edit/:ID", func(c echo.Context) error {
		return handlers.ContainerManagerEditGET(c, a)
	})
	// Edit a container action
	htmxGroup.POST("/container-manager/edit/:ID", func(c echo.Context) error {
		return handlers.ContainerManagerEditPOST(c, a)
	})

	// Display upload-image component
	htmxGroup.GET("/upload-image", func(c echo.Context) error {
		return handlers.UploadImageGETHandler(c, a)
	})
	// Upload image
	htmxGroup.POST("/upload-image", func(c echo.Context) error {
		return handlers.UploadImagePOSTHandler(c, a)
	})
	// Display create-container component
	htmxGroup.GET("/create-container/:ID", func(c echo.Context) error {
		return handlers.CreateContainerGET(c, a)
	})
	// Create container
	htmxGroup.POST("/create-container/:ID", func(c echo.Context) error {
		return handlers.CreateContainerPOST(c, a)
	})
}
