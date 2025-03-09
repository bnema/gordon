package proxy

import (
	"sync"
	"time"

	"crypto/tls"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/interfaces"
	"github.com/charmbracelet/log"
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
}

// NewProxy creates a new instance of the reverse proxy
func NewProxy(app interfaces.AppInterface) (*Proxy, error) {
	log.Debug("Initializing reverse proxy")

	// Set up the echo server for HTTPS traffic
	httpsServer := echo.New()
	httpsServer.HideBanner = true

	// Set up the echo server for HTTP traffic (redirects to HTTPS)
	httpServer := echo.New()
	httpServer.HideBanner = true

	// Set up the config
	config := app.GetConfig().ReverseProxy

	// Initialize the blacklist
	blacklistPath := app.GetConfig().General.StorageDir + "/blacklist.json"
	blacklist, err := NewBlacklist(blacklistPath)
	if err != nil {
		log.Warn("Failed to initialize blacklist, continuing without it", "error", err)
	}

	// Initialize routes map
	routes := make(map[string]*ProxyRouteInfo)

	// Create the proxy
	p := &Proxy{
		config:           config,
		app:              app,
		httpsServer:      httpsServer,
		httpServer:       httpServer,
		routes:           routes,
		serverStarted:    false,
		blacklist:        blacklist,
		blockedIPCounter: make(map[string]int),
		lastBlockedLog:   time.Time{}, // Zero time
		recentlyBlocked:  make(map[string]time.Time),
	}

	// Now add the middleware with the proxy reference
	p.setupMiddleware()

	// Set up the certificate manager
	p.setupCertManager()

	log.Debug("Reverse proxy initialized")
	return p, nil
}
