package proxy

import (
	"net"
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

	// Upstream proxy detection
	upstreamProxyDetected bool
	upstreamProxyMu       sync.RWMutex

	// SQL queries
	queries *queries.ProxyQueries
}

// NewProxy creates a new instance of the reverse proxy
func NewProxy(app interfaces.AppInterface) (*Proxy, error) {
	logger.Debug("Initializing reverse proxy")

	// Set up the config
	config := app.GetConfig().ReverseProxy

	// Check if proxy is disabled through configuration
	if !config.Enabled {
		logger.Info("Reverse proxy is disabled via configuration")
		// Return a non-active proxy instance
		return &Proxy{
			config:        config,
			app:           app,
			serverStarted: false,
			shutdown:      make(chan struct{}),
		}, nil
	}

	// Check if running inside a container
	isContainer := docker.IsRunningInContainer()
	logger.Debug("Container environment check", "is_running_in_container", isContainer)

	if !isContainer {
		logger.Info("Gordon is not running in a container, proxy will be disabled automatically")
		// Override the enabled setting to false
		config.Enabled = false
		return &Proxy{
			config:        config,
			app:           app,
			serverStarted: false,
			shutdown:      make(chan struct{}),
		}, nil
	}

	// Set up the echo server for HTTPS traffic
	httpsServer := echo.New()
	httpsServer.HideBanner = true

	// Set up the echo server for HTTP traffic (redirects to HTTPS)
	httpServer := echo.New()
	httpServer.HideBanner = true

	// Initialize container storage and cooldown settings
	recentContainers := make(map[string]time.Time)
	containerCooldownPeriod := time.Duration(10) * time.Second
	// Use a fixed cooldown period of 10 seconds (currently no config option for this)

	// Set up DB retry configuration
	dbMaxRetries := 3
	dbRetryBaseDelay := time.Duration(500) * time.Millisecond

	// Initialize the reverseProxyClient with reasonable timeouts
	reverseProxyClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			// Add more detailed timeout settings
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 5 * time.Second,
			// Add a custom dialer with explicit timeouts
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}

	// Initialize the proxy queries
	proxyQueries := queries.NewProxyQueries()

	// Create the proxy instance
	p := &Proxy{
		config:                  config,
		app:                     app,
		httpsServer:             httpsServer,
		httpServer:              httpServer,
		routes:                  make(map[string]*ProxyRouteInfo),
		reverseProxyClient:      reverseProxyClient,
		dbMaxRetries:            dbMaxRetries,
		dbRetryBaseDelay:        dbRetryBaseDelay,
		recentContainers:        recentContainers,
		containerCooldownPeriod: containerCooldownPeriod,
		shutdown:                make(chan struct{}),
		queries:                 proxyQueries,
	}

	// Setup the certificate manager for HTTPS
	p.setupCertManager()

	// Verify the certificate manager was properly initialized
	if p.certManager == nil {
		logger.Error("Certificate manager initialization failed",
			"solution", "Check certificate directory permissions and configuration")
	} else {
		logger.Debug("Certificate manager successfully initialized")
	}

	// Detect our container ID for later use
	p.detectGordonContainer()

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
