package httpserve

import (
	"github.com/bnema/gordon/internal/httpserve/handler"
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
		return handler.DeviceCodeRequest(c, a)
	})
	apiGroup.POST("/device/token", func(c echo.Context) error {
		return handler.DeviceTokenRequest(c, a)
	})

	// Other API endpoints that require a token
	protectedApiGroup := apiGroup.Group("", middleware.RequireToken(a))
	protectedApiGroup.GET("/ping", func(c echo.Context) error {
		return handler.GetInfos(c, a)
	})
	protectedApiGroup.POST("/deploy", func(c echo.Context) error {
		return handler.PostDeploy(c, a)
	})
	protectedApiGroup.POST("/push", func(c echo.Context) error {
		return handler.PostPush(c, a)
	})
}

// bindStaticRoute bind static path
func bindStaticRoute(e *echo.Echo, a *server.App, path string) {
	e.GET(path, func(c echo.Context) error {
		return handler.StaticRoute(c, a)
	}, echomid.Gzip())
}

// bindLoginRoute binds all login routes
func bindLoginRoute(e *echo.Echo, a *server.App, adminPath string) {
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

// bindAdminRoute binds all admin routes
func bindAdminRoute(e *echo.Echo, a *server.App, adminPath string) {
	adminGroup := e.Group(adminPath)
	// Since login is behind the admin path, we cannot use group middleware
	adminGroup.GET("", func(c echo.Context) error {
		return c.Redirect(302, adminPath+"/manager")
	}, middleware.RequireLogin(a))

	adminGroup.GET("/manager", func(c echo.Context) error {
		return handler.AdminManagerRoute(c, a)
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
		return handler.ImageManagerComponent(c, a)
	})
	// Delete an image
	htmxGroup.DELETE("/image-manager/delete/:ID", func(c echo.Context) error {
		return handler.ImageManagerDelete(c, a)
	})

	// List all containers
	htmxGroup.GET("/container-manager", func(c echo.Context) error {
		return handler.ContainerManagerComponent(c, a)
	})
	// Stop a container
	htmxGroup.POST("/container-manager/stop/:ID", func(c echo.Context) error {
		return handler.ContainerManagerStop(c, a)
	})
	// Delete a container
	htmxGroup.DELETE("/container-manager/delete/:ID", func(c echo.Context) error {
		return handler.ContainerManagerDelete(c, a)
	})
	// Start a container
	htmxGroup.POST("/container-manager/start/:ID", func(c echo.Context) error {
		return handler.ContainerManagerStart(c, a)
	})
	// Edit a container view
	htmxGroup.GET("/container-manager/edit/:ID", func(c echo.Context) error {
		return handler.ContainerManagerEditGET(c, a)
	})
	// Edit a container action
	htmxGroup.POST("/container-manager/edit/:ID", func(c echo.Context) error {
		return handler.ContainerManagerEditPOST(c, a)
	})

	// Display upload-image component
	htmxGroup.GET("/upload-image", func(c echo.Context) error {
		return handler.UploadImageGETHandler(c, a)
	})
	// Upload image
	htmxGroup.POST("/upload-image", func(c echo.Context) error {
		return handler.UploadImagePOSTHandler(c, a)
	})
	// Display create-container component
	htmxGroup.GET("/create-container/:ID", func(c echo.Context) error {
		return handler.CreateContainerGET(c, a)
	})
	// Create container
	htmxGroup.POST("/create-container/:ID", func(c echo.Context) error {
		return handler.CreateContainerPOST(c, a)
	})
}
