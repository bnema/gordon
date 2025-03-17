package httpserve

import (
	"github.com/bnema/gordon/internal/httpserve/handlers"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/server"
	"github.com/charmbracelet/log"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo/v4"
	echomid "github.com/labstack/echo/v4/middleware"
)

// RegisterRoutes registers all routes and middlewares
func RegisterRoutes(e *echo.Echo, a *server.App) *echo.Echo {
	// Add default Echo logger middleware
	e.Use(echomid.Logger())
	e.Use(echomid.Recover())
	// Initiate the session middleware
	e.Use(middleware.InitSessionMiddleware(a))

	// Language detection
	e.Use(middleware.LanguageDetection)

	AdminPath := a.Config.Admin.Path
	bindLoginRoute(e, a, AdminPath)
	bindAPIEndpoints(e, a)
	bindLogLevelEndpoint(e, a)

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

	// Config endpoint for SvelteKit frontend (not protected)
	apiGroup.GET("/config", func(c echo.Context) error {
		return handlers.GetConfigAPI(c, a)
	})

	// Device flow endpoints (without RequireToken middleware)
	apiGroup.POST("/device/code", func(c echo.Context) error {
		return handlers.DeviceCodeRequest(c, a)
	})
	apiGroup.POST("/device/token", func(c echo.Context) error {
		return handlers.DeviceTokenRequest(c, a)
	})

	// Auth endpoint for SvelteKit frontend (not protected)
	apiGroup.POST("/auth/login", func(c echo.Context) error {
		return handlers.LoginAPI(c, a)
	})

	// Check if database is empty (used for first-time setup)
	apiGroup.GET("/admin/auth/check-db-empty", func(c echo.Context) error {
		return handlers.CheckDBEmpty(c, a)
	})

	// Other API endpoints that require a token
	protectedApiGroup := apiGroup.Group("", middleware.RequireToken(a))
	protectedApiGroup.GET("/ping", func(c echo.Context) error {
		return handlers.GetInfos(c, a)
	})
	protectedApiGroup.POST("/deploy", func(c echo.Context) error {
		return handlers.PostDeploy(c, a)
	})
	protectedApiGroup.POST("/deploy/chunked", func(c echo.Context) error {
		return handlers.PostDeployChunked(c, a)
	})
	protectedApiGroup.GET("/deploy/check-conflict", func(c echo.Context) error {
		return handlers.CheckDeployConflict(c, a)
	})
	// Regular push endpoint for small images
	protectedApiGroup.POST("/push", func(c echo.Context) error {
		return handlers.PostPush(c, a)
	})

	// Chunked push endpoint for large images
	protectedApiGroup.POST("/push/chunked", func(c echo.Context) error {
		return handlers.PostPushChunked(c, a)
	})
	protectedApiGroup.POST("/stop", func(c echo.Context) error {
		return handlers.PostContainerStop(c, a)
	})
	protectedApiGroup.POST("/remove", func(c echo.Context) error {
		return handlers.PostContainerRemove(c, a)
	})

	// Admin API endpoints for SvelteKit frontend (protected with session-based authentication)
	adminApiGroup := apiGroup.Group("/admin", middleware.RequireLogin(a))

	// Authentication endpoints
	adminApiGroup.POST("/auth/session", func(c echo.Context) error {
		return handlers.CreateSessionAPI(c, a)
	})
	adminApiGroup.POST("/auth/session/validate", func(c echo.Context) error {
		return handlers.ValidateSessionAPI(c, a)
	})
	adminApiGroup.POST("/auth/session/invalidate", func(c echo.Context) error {
		return handlers.InvalidateSessionAPI(c, a)
	})

	// Container endpoints
	adminApiGroup.GET("/containers", func(c echo.Context) error {
		return handlers.ContainerManagerAPI(c, a)
	})
	adminApiGroup.GET("/containers/:ID", func(c echo.Context) error {
		return handlers.ContainerInfoAPI(c, a)
	})
	adminApiGroup.POST("/containers/:ID/stop", func(c echo.Context) error {
		return handlers.ContainerStopAPI(c, a)
	})
	adminApiGroup.POST("/containers/:ID/start", func(c echo.Context) error {
		return handlers.ContainerStartAPI(c, a)
	})
	adminApiGroup.DELETE("/containers/:ID", func(c echo.Context) error {
		return handlers.ContainerDeleteAPI(c, a)
	})
	adminApiGroup.GET("/containers/:ID/edit", func(c echo.Context) error {
		return handlers.ContainerEditGetAPI(c, a)
	})
	adminApiGroup.PUT("/containers/:ID", func(c echo.Context) error {
		return handlers.ContainerEditPostAPI(c, a)
	})

	// Image endpoints
	adminApiGroup.GET("/images", func(c echo.Context) error {
		return handlers.ImageManagerAPI(c, a)
	})
	adminApiGroup.DELETE("/images/:ID", func(c echo.Context) error {
		return handlers.ImageDeleteAPI(c, a)
	})
	adminApiGroup.POST("/images/upload", func(c echo.Context) error {
		return handlers.UploadImageAPI(c, a)
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
	// Serve the SvelteKit login page - handle both with and without trailing slash
	e.GET(adminPath+"/login", func(c echo.Context) error {
		return handlers.StaticRoute(c, a)
	})

	// Add explicit handler for trailing slash to prevent 301 redirects
	e.GET(adminPath+"/login/", func(c echo.Context) error {
		return handlers.StaticRoute(c, a)
	})

	// Keep the API endpoints for login functionality
	e.POST(adminPath+"/login/submit-token", func(c echo.Context) error {
		return handlers.HandleTokenSubmission(c, a)
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

	// Add a ping endpoint for health checks and connection testing
	// This endpoint doesn't require authentication since it's used by the proxy for connection testing
	adminGroup.GET("/ping", func(c echo.Context) error {
		log.Debug("Ping endpoint hit from:", "ip", c.RealIP())
		return c.JSON(200, map[string]string{
			"status":  "ok",
			"message": "Gordon admin server is running",
		})
	})

	// For all other admin routes, serve the SvelteKit app
	// This will let the SvelteKit router handle the routing
	adminGroup.GET("/*", func(c echo.Context) error {
		return handlers.StaticRoute(c, a)
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

// Add a new endpoint to get the current log level
func bindLogLevelEndpoint(e *echo.Echo, a *server.App) {
	e.GET("/api/config/log-level", func(c echo.Context) error {
		return c.JSON(200, map[string]string{
			"level": a.Config.General.LogLevel,
		})
	})
}
