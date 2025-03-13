package proxy

import (
	"os"
	"strings"
	"sync"
	"time"

	"crypto/tls"
	"net/http"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/interfaces"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/acme/autocert"
)

// - core.go - Core proxy structure and constructor
// - certificates.go - Certificate management functions
// - routes.go - Route management functions
// - middleware.go - HTTP middleware functions
// - blacklist.go - IP blacklisting functionality
// - server.go - Server startup and shutdown
// - utils.go - Utility functions

// Proxy represents the reverse proxy server
type Proxy struct {
	config        common.ReverseProxyConfig
	app           interfaces.AppInterface
	httpsServer   *echo.Echo
	httpServer    *echo.Echo
	routes        map[string]*ProxyRouteInfo
	mu            sync.RWMutex
	certManager   *autocert.Manager
	fallbackCert  *tls.Certificate // Fallback self-signed certificate
	serverStarted bool

	// Flag to track specific domain certificate operations
	processingSpecificDomain bool

	// Store Gordon's own container ID at startup for reliable identification
	gordonContainerID string

	reverseProxyClient        *http.Client
	processingGlobalCert      bool
	dbMaxRetries              int
	dbRetryBaseDelay          time.Duration
	acmeManager               *autocert.Manager
	acmePerDomainRefreshMutex sync.Mutex
	acmeDirCache              autocert.DirCache
	shutdown                  chan struct{} // Channel for signaling shutdown to background goroutines

	// Track recently created containers to prevent recreation logic
	recentContainers        map[string]time.Time
	recentContainersMu      sync.RWMutex
	containerCooldownPeriod time.Duration

	// SQL queries
	queries *queries.ProxyQueries
}

// NewProxy creates a new instance of the reverse proxy
func NewProxy(app interfaces.AppInterface) (*Proxy, error) {
	logger.Debug("Initializing reverse proxy")

	// Log important configuration information
	logger.Debug("All internal connections to containers will use HTTP protocol regardless of external protocol")

	// Set up the echo server for HTTPS traffic
	httpsServer := echo.New()
	httpsServer.HideBanner = true

	// Set up the echo server for HTTP traffic (redirects to HTTPS)
	httpServer := echo.New()
	httpServer.HideBanner = true

	// Set up the config
	config := app.GetConfig().ReverseProxy

	// Detect and store our container ID (if we're running in a container)
	var ourContainerID string
	if docker.IsRunningInContainer() {
		// Get our hostname which should be the container ID in Docker/Podman
		hostname := os.Getenv("HOSTNAME")
		if hostname != "" {
			containers, err := docker.ListRunningContainers()
			if err == nil {
				for _, container := range containers {
					// Check if this container is us by comparing hostname with container ID
					if strings.HasPrefix(hostname, container.ID) {
						// Store our container ID for future reference
						ourContainerID = container.ID
						containerName := strings.TrimLeft(container.Names[0], "/")
						logger.Info("Gordon identity established",
							"container_id", ourContainerID,
							"container_name", containerName)
						break
					}
				}
			}
		}
	}

	// Initialize routes map
	routes := make(map[string]*ProxyRouteInfo)

	// Initialize SQL queries
	proxyQueries := queries.NewProxyQueries()

	// Create the proxy
	p := &Proxy{
		config:                  config,
		app:                     app,
		httpsServer:             httpsServer,
		httpServer:              httpServer,
		routes:                  routes,
		serverStarted:           false,
		gordonContainerID:       ourContainerID,
		shutdown:                make(chan struct{}),
		recentContainers:        make(map[string]time.Time),
		containerCooldownPeriod: 10 * time.Second, // Default 10 second cooldown period
		queries:                 proxyQueries,
	}

	// Now add the middleware with the proxy reference
	p.setupMiddleware()

	// Set up the certificate manager
	p.setupCertManager()

	logger.Debug("Reverse proxy initialized")
	return p, nil
}

// Close cleans up resources used by the proxy
func (p *Proxy) Close() {
	// Signal all background goroutines to stop
	close(p.shutdown)

	// Close the HTTP and HTTPS servers if they exist
	if p.httpServer != nil {
		if err := p.httpServer.Close(); err != nil {
			logger.Error("Error closing HTTP server", "error", err)
		}
	}

	if p.httpsServer != nil {
		if err := p.httpsServer.Close(); err != nil {
			logger.Error("Error closing HTTPS server", "error", err)
		}
	}

	logger.Info("Proxy resources cleaned up")
}
