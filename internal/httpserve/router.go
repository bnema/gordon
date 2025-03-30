package httpserve

import (
	"io/fs"
	"net/http"
	"os"

	"github.com/bnema/gordon/internal/httpserve/handlers"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/webui" // Import the webui package
	log "github.com/bnema/gordon/pkg/logger"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	echomid "github.com/labstack/echo/v4/middleware"
)

// RegisterRoutes registers all routes and middlewares
func RegisterRoutes(e *echo.Echo, a *server.App) *echo.Echo {
	e.Use(echomid.Recover())

	// Determine if running in live UI mode
	useOS := os.Getenv("GORDON_LIVE_UI") == "true"
	staticFileSystem := getStaticFileSystem(useOS)
	assetHandler := http.FileServer(staticFileSystem)

	// Serve static files using the asset handler
	// We wrap the handler to ensure it works correctly with Echo
	// We also need to strip the prefix if accessing assets directly (e.g. /assets/css/...)
	// The root path "/" will serve index.html or equivalent by default if present.
	e.GET("/*", echo.WrapHandler(assetHandler))

	// Log admin status
	log.Info("Admin WebUI status",
		"enabled", a.Config.Admin.IsAdminWebUIEnabled(),
		"path", a.Config.Admin.Path)

	// Initiate the session middleware
	sessionMiddleware := middleware.InitSessionMiddleware(a)
	// Apply session middleware *after* the static file handler setup
	// It will only run for routes not handled by the static file server
	e.Use(sessionMiddleware)

	// Language detection
	e.Use(middleware.LanguageDetection)

	AdminPath := a.Config.Admin.Path
	log.Debug("Binding login routes with AdminPath", "path", AdminPath)
	bindLoginRoute(e, a, AdminPath)

	log.Debug("Binding API endpoints")
	bindAPIEndpoints(e, a)

	// SetCommonDataMiddleware will pass data to the renderer
	e.Use(middleware.SetCommonDataMiddleware(a))

	// Error handler middleware
	e.Use(middleware.ErrorHandler)

	// Use middlewares
	e.Use(middleware.SecureRoutes(a))

	// Color scheme detection for dark/light mode
	e.Use(middleware.ColorSchemeDetection)

	// Set custom error handler
	e.HTTPErrorHandler = middleware.CustomHTTPErrorHandler(a)

	log.Debug("Binding admin routes with AdminPath", "path", AdminPath)
	bindAdminRoute(e, a, AdminPath)

	// Only bind HTMX endpoints if Admin WebUI is enabled
	if a.Config.Admin.IsAdminWebUIEnabled() {
		log.Info("Admin WebUI is enabled, binding HTMX endpoints")
		bindHTMXEndpoints(e, a)
	} else {
		log.Info("Admin WebUI is disabled, skipping HTMX endpoints")
	}

	// Protect the root path with a 403
	e.GET("/", func(c echo.Context) error {
		return handlers.RenderForbiddenPage(c, a)
	})

	log.Info("All routes registered successfully")
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
}

// bindLoginRoute binds all login routes
func bindLoginRoute(e *echo.Echo, a *server.App, adminPath string) {
	// Skip binding admin login routes if admin webUI is disabled
	if !a.Config.Admin.IsAdminWebUIEnabled() {
		log.Debug("Admin WebUI is disabled, skipping admin login routes")
		return
	}

	e.GET(adminPath+"/login", func(c echo.Context) error {
		// Check if already logged in based on cookie
		sess, _ := session.Get("session", c) // Ignore error for initial check
		if sess != nil && sess.Values != nil {
			if authenticated, ok := sess.Values["authenticated"].(bool); ok && authenticated {
				// Cookie claims authenticated. NOW, validate against the DB too.
				err := handlers.ValidateSessionAndUser(c, a) // Use the same validation function as the middleware
				if err == nil {
					// Session is valid in BOTH cookie AND DB. Proceed to admin panel.
					log.Debug("Login page check: Valid session found (cookie & DB), redirecting to admin panel")
					return c.Redirect(http.StatusFound, a.Config.Admin.Path)
				}
				// If DB validation failed, log it and fall through to render login page
				log.Warn("Login page check: Cookie session present but DB validation failed", "error", err)
				// NOTE: Consider invalidating the inconsistent cookie here?
				// handlers.InvalidateSessionCookie(c, sess) // Optional: Force cookie cleanup
			}
		}
		// If no session cookie, not authenticated in cookie, or DB validation failed: render login page.
		log.Debug("Login page check: No valid session found or DB validation failed, rendering login page")
		// Always use the Templ-based login page renderer
		return handlers.RenderTemplLoginPage(c, a)
	})
	// Removed POST /login/submit-token route
	e.GET(adminPath+"/login/oauth/github", func(c echo.Context) error {
		return handlers.StartOAuthGithub(c, a)
	})
	e.GET("/callback", func(c echo.Context) error {
		log.Debug("OAuth callback endpoint hit", "path", c.Path(), "query", c.QueryString())
		return handlers.OAuthCallback(c, a)
	})
	e.GET(adminPath+"/logout", func(c echo.Context) error {
		return handlers.Logout(c, a)
	})
}

// bindAdminRoute binds all admin routes
func bindAdminRoute(e *echo.Echo, a *server.App, adminPath string) {
	// Skip binding admin routes if admin webUI is disabled
	if !a.Config.Admin.IsAdminWebUIEnabled() {
		log.Debug("Admin WebUI is disabled, skipping admin routes")
		return
	}

	adminGroup := e.Group(adminPath)
	// Since login is behind the admin path, we cannot use group middleware
	adminGroup.GET("", func(c echo.Context) error {
		return c.Redirect(302, adminPath+"/manager")
	}, middleware.RequireLogin(a))

	adminGroup.GET("/manager", func(c echo.Context) error {
		return handlers.RenderTemplManagerPage(c, a)
	}, middleware.RequireLogin(a))
	adminGroup.GET("/cc/:ID", func(c echo.Context) error {
		return handlers.CreateContainerFullGET(c, a)
	}, middleware.RequireLogin(a))

	// Add a ping endpoint for health checks and connection testing
	// This endpoint doesn't require authentication since it's used by the proxy for connection testing
	adminGroup.GET("/ping", func(c echo.Context) error {
		log.Debug("Ping endpoint hit from:", "ip", c.RealIP())
		return c.JSON(200, map[string]string{
			"status":  "ok",
			"message": "Gordon admin server is running",
		})
	})
}

// bindHTMXEndpoints binds all HTMX endpoints
func bindHTMXEndpoints(e *echo.Echo, a *server.App) {
	// Skip if AdminWebUI is disabled
	if !a.Config.Admin.IsAdminWebUIEnabled() {
		log.Debug("Admin WebUI is disabled, skipping HTMX endpoints")
		return
	}

	// Create a group for /htmx endpoints
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
	// Purge unused images
	htmxGroup.POST("/image-manager/prune", func(c echo.Context) error {
		return handlers.ImageManagerPrune(c, a)
	})

	// List all containers
	htmxGroup.GET("/container-manager", func(c echo.Context) error {
		return handlers.RenderTemplContainerList(c, a)
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
	// Handle create-container form submission
	htmxGroup.POST("/create-container/:ID", func(c echo.Context) error {
		return handlers.CreateContainerPOST(c, a)
	})
}

// getStaticFileSystem returns the appropriate http.FileSystem based on the GORDON_LIVE_UI env var.
func getStaticFileSystem(useOS bool) http.FileSystem {
	if useOS {
		log.Info("Serving static files from OS filesystem (live mode)")
		return http.FS(os.DirFS("internal/webui/public"))
	}

	log.Info("Serving static files from embedded filesystem")
	// Use the PublicFS from the webui package
	fsys, err := fs.Sub(webui.PublicFS, "public")
	if err != nil {
		panic(err) // Should not happen with embedded files
	}

	return http.FS(fsys)
}
