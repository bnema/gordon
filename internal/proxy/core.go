package proxy

import (
	"os"
	"strings"
	"sync"
	"time"

	"crypto/tls"

	"github.com/bnema/gordon/internal/common"
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
	blacklist     *BlacklistConfig

	// Fields for throttling blacklist logs
	lastBlockedLog   time.Time
	blockedIPCounter map[string]int
	blockedIPCountMu sync.Mutex

	// Firewall-like memory of recently blocked IPs for quick rejection
	recentlyBlocked   map[string]time.Time
	recentlyBlockedMu sync.RWMutex

	// Flag to track specific domain certificate operations
	processingSpecificDomain bool

	// Store Gordon's own container ID at startup for reliable identification
	gordonContainerID string
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

	// Initialize the blacklist
	blacklistPath := app.GetConfig().General.StorageDir + "/blacklist.json"
	blacklist, err := NewBlacklist(blacklistPath)
	if err != nil {
		logger.Warn("Failed to initialize blacklist, continuing without it", "error", err)
	}

	// Initialize routes map
	routes := make(map[string]*ProxyRouteInfo)

	// Create the proxy
	p := &Proxy{
		config:            config,
		app:               app,
		httpsServer:       httpsServer,
		httpServer:        httpServer,
		routes:            routes,
		serverStarted:     false,
		blacklist:         blacklist,
		blockedIPCounter:  make(map[string]int),
		lastBlockedLog:    time.Time{}, // Zero time
		recentlyBlocked:   make(map[string]time.Time),
		gordonContainerID: ourContainerID,
	}

	// Now add the middleware with the proxy reference
	p.setupMiddleware()

	// Set up the certificate manager
	p.setupCertManager()

	logger.Debug("Reverse proxy initialized")
	return p, nil
}
