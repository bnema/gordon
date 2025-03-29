package proxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"crypto/tls"
	"database/sql"
	"net/http"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/interfaces"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
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
	routes        map[string]*ProxyRouteInfo
	mu            sync.RWMutex
	serverStarted bool
	ctx           context.Context // Context for storing shared data

	// Flag to track specific domain certificate operations (May need review if CertManager handles this)
	processingSpecificDomain bool

	// Store Gordon's own container ID at startup for reliable identification
	gordonContainerID string

	reverseProxyClient *http.Client
	dbMaxRetries       int
	dbRetryBaseDelay   time.Duration
	shutdown           chan struct{} // Channel for signaling shutdown to background goroutines

	// Track recently created containers to prevent recreation logic
	recentContainers        map[string]time.Time
	recentContainersMu      sync.RWMutex
	containerCooldownPeriod time.Duration

	// Certificate Management
	certificateManager *CertificateManager // New refactored manager

	// SQL Queries
	Queries *queries.ProxyQueries
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
			ctx:           context.Background(),
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
			ctx:           context.Background(),
		}, nil
	}

	// Set up the echo server for HTTPS traffic
	httpsServer := echo.New()
	httpsServer.HideBanner = true

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
		config:      config,
		app:         app,
		httpsServer: httpsServer,
		// httpServer:              httpServer, // Removed assignment
		routes:                  make(map[string]*ProxyRouteInfo),
		reverseProxyClient:      reverseProxyClient,
		dbMaxRetries:            dbMaxRetries,
		dbRetryBaseDelay:        dbRetryBaseDelay,
		recentContainers:        recentContainers,
		containerCooldownPeriod: containerCooldownPeriod,
		shutdown:                make(chan struct{}),
		Queries:                 proxyQueries,
		ctx:                     context.Background(),
	}

	// --- Certificate Manager Setup ---
	// Determine cert directory
	certDir := config.CertDir
	if certDir == "" {
		certDir = app.GetConfig().General.StorageDir + "/certs"
	}

	// Initial upstream proxy detection (before creating CertManager)
	initialBehindTLSProxy := detectInitialUpstreamProxy(&config)

	// Create Certificate Manager Config
	certManagerConfig := CertManagerConfig{
		CertDir:          certDir,
		Email:            config.Email,
		Mode:             config.LetsEncryptMode, // staging or production
		SkipCertificates: config.SkipCertificates,
		BehindTLSProxy:   initialBehindTLSProxy, // Pass initial detection result
		AdminDomain:      app.GetConfig().Http.FullDomain(),
		RootDomain:       app.GetConfig().Http.Domain,
		// RouteValidator: p.isHostInRoutes, // TODO: Pass a function if needed by hostPolicy
	}

	// Create the Certificate Manager
	certMgr, err := NewCertificateManager(certManagerConfig, app, p.Queries)
	if err != nil {
		logger.Error("Failed to initialize Certificate Manager", "error", err)
		// Return an error to prevent starting a broken proxy
		return nil, fmt.Errorf("failed to initialize certificate manager: %w", err)
	} else {
		logger.Debug("Certificate manager successfully initialized")
		p.certificateManager = certMgr // Store the new manager
	}

	// Ensure admin domain has proper ACME configuration
	if err := p.ensureAdminDomainConfig(); err != nil {
		logger.Error("Failed to ensure admin domain configuration", "error", err)
		return nil, fmt.Errorf("failed to ensure admin domain configuration: %w", err)
	}

	// --- End Certificate Manager Setup ---

	// Detect our container ID for later use
	p.detectGordonContainer()

	return p, nil
}

// Close cleans up resources used by the proxy
func (p *Proxy) Close() {
	// Signal all background goroutines to stop
	close(p.shutdown)

	if p.httpsServer != nil {
		if err := p.httpsServer.Close(); err != nil {
			logger.Error("Error closing HTTPS server", "error", err)
		}
	}

	logger.Info("Proxy resources cleaned up")
}

// detectInitialUpstreamProxy checks environment variables for signs of an upstream proxy
// This is used during initialization before the CertificateManager is created.
func detectInitialUpstreamProxy(config *common.ReverseProxyConfig) bool {
	// Check if detection is explicitly disabled
	if !config.DetectUpstreamProxy {
		logger.Debug("Upstream proxy detection disabled by config")
		return false
	}

	// Check for common environment variables set by upstream proxies
	if os.Getenv("HTTPS") == "on" ||
		os.Getenv("HTTP_X_FORWARDED_PROTO") == "https" ||
		os.Getenv("HTTP_X_FORWARDED_SSL") == "on" {
		logger.Info("Initial upstream TLS-terminating proxy detected via environment variables",
			"detection_method", "environment_variables",
			"https", os.Getenv("HTTPS"),
			"x_forwarded_proto", os.Getenv("HTTP_X_FORWARDED_PROTO"),
			"x_forwarded_ssl", os.Getenv("HTTP_X_FORWARDED_SSL"))
		return true
	}

	// Check Cloudflare headers
	if cfVisitor := os.Getenv("HTTP_CF_VISITOR"); cfVisitor != "" {
		if strings.Contains(cfVisitor, "\"scheme\":\"https\"") {
			logger.Info("Initial Cloudflare proxy detected via environment variables",
				"detection_method", "cloudflare_headers",
				"cf_visitor", cfVisitor)
			return true
		}
	}

	logger.Debug("No initial upstream proxy detected via environment variables")
	return false
}

// requestDomainCertificate requests a certificate for a domain
func (p *Proxy) requestDomainCertificate(domainName string) error {
	logger.Debug("Requesting certificate for domain", "domain", domainName)

	// Check if we're already processing this domain
	if p.processingSpecificDomain {
		logger.Debug("Already processing a specific domain, skipping", "domain", domainName)
		return nil
	}

	// Get domain configuration
	var acmeEnabled bool
	// Use sql.NullString for potentially nullable string columns
	var acmeChallengeType, acmeDnsProvider, acmeDnsCredentialsRef sql.NullString
	err := p.app.GetDB().QueryRow(p.Queries.GetDomainAcmeConfig, domainName).Scan(
		&acmeEnabled, &acmeChallengeType, &acmeDnsProvider, &acmeDnsCredentialsRef,
	)
	if err != nil {
		// Handle case where the domain might not exist in the acme_configs table yet
		if err == sql.ErrNoRows {
			logger.Debug("No ACME configuration found for domain, assuming ACME is disabled", "domain", domainName)
			return nil // Treat as ACME disabled if no config exists
		}
		logger.Error("Failed to get domain ACME configuration", "domain", domainName, "error", err)
		return err
	}

	if !acmeEnabled {
		logger.Debug("ACME not enabled for domain", "domain", domainName)
		return nil
	}

	// Check if certificate exists and is valid
	var certFile, keyFile string
	var issuedAtStr, expiresAtStr string // Read timestamps as strings first
	var issuer, status string
	var issuedAt, expiresAt time.Time // Keep these for use after parsing

	// Scan from DB, reading timestamps as strings
	err = p.app.GetDB().QueryRow(p.Queries.GetCertificateByDomain, domainName).Scan(
		&certFile, &keyFile, &issuedAtStr, &expiresAtStr, &issuer, &status,
	)

	// Handle potential sql.ErrNoRows if the certificate doesn't exist yet
	if err != nil && err != sql.ErrNoRows {
		logger.Error("Failed to check existing certificate", "domain", domainName, "error", err)
		return err
	}

	// If a certificate was found (no error or ErrNoRows), parse the timestamps
	if err == nil {
		issuedAt, err = time.Parse(time.RFC3339, issuedAtStr)
		if err != nil {
			logger.Error("Failed to parse issued_at timestamp from DB", "domain", domainName, "value", issuedAtStr, "error", err)
			// Return the parse error as it indicates data corruption or an unexpected format
			return fmt.Errorf("failed to parse issued_at for domain %s: %w", domainName, err)
		}
		expiresAt, err = time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			logger.Error("Failed to parse expires_at timestamp from DB", "domain", domainName, "value", expiresAtStr, "error", err)
			// Return the parse error
			return fmt.Errorf("failed to parse expires_at for domain %s: %w", domainName, err)
		}
		logger.Debug("Successfully scanned and parsed certificate timestamps", "domain", domainName, "issued", issuedAt, "expires", expiresAt)
	}
	// Note: If err was sql.ErrNoRows, issuedAt/expiresAt will remain zero time.Time, which is handled correctly later.

	// If certificate exists and is valid
	if err == nil && status == "valid" {
		// Check if certificate needs renewal
		renewBefore := time.Duration(p.config.RenewBefore) * 24 * time.Hour
		if time.Now().Add(renewBefore).Before(expiresAt) {
			logger.Debug("Certificate is still valid and does not need renewal",
				"domain", domainName,
				"expires_at", expiresAt,
				"renew_before_days", p.config.RenewBefore)
			return nil // Certificate is valid, no action needed
		}
		logger.Debug("Certificate requires renewal", "domain", domainName, "expires_at", expiresAt)
	} else if err == sql.ErrNoRows {
		logger.Debug("No existing certificate found for domain", "domain", domainName)
	} else {
		logger.Debug("Existing certificate is not valid", "domain", domainName, "status", status, "scan_error", err)
	}

	// --- Proceed with obtaining/renewing certificate ---

	// Check if required ACME config fields are present now that we know ACME is enabled
	if !acmeChallengeType.Valid || acmeChallengeType.String == "" {
		err := fmt.Errorf("ACME challenge type is missing for domain %s", domainName)
		logger.Error(err.Error())
		return err
	}
	// If DNS challenge, provider and credentials ref are required
	if acmeChallengeType.String == "dns-01" {
		if !acmeDnsProvider.Valid || acmeDnsProvider.String == "" {
			err := fmt.Errorf("ACME DNS provider is missing for domain %s using dns-01 challenge", domainName)
			logger.Error(err.Error())
			return err
		}
		if !acmeDnsCredentialsRef.Valid || acmeDnsCredentialsRef.String == "" {
			err := fmt.Errorf("ACME DNS credentials reference is missing for domain %s using dns-01 challenge", domainName)
			logger.Error(err.Error())
			return err
		}
		// Log the DNS provider being used (optional, but helpful for debugging)
		logger.Debug("Using DNS provider for ACME challenge", "domain", domainName, "provider", acmeDnsProvider.String)
	}

	// Set processing flag
	p.processingSpecificDomain = true
	defer func() {
		p.processingSpecificDomain = false
	}()

	// Request new certificate using the CertificateManager
	cert, err := p.certificateManager.ObtainCertificate(domainName, p) // Pass 'p' (Proxy instance)
	if err != nil {
		logger.Error("Failed to obtain/renew certificate", "domain", domainName, "error", err)
		// Update certificate status to 'failed' in DB?
		// _, dbErr := p.app.GetDB().Exec(p.Queries.UpdateCertificateStatus, "failed", domainName)
		// if dbErr != nil {
		// 	logger.Error("Failed to update certificate status after obtain failure", "domain", domainName, "db_error", dbErr)
		// }
		return err // Return the obtain error
	}

	logger.Info("Successfully obtained certificate",
		"domain", domainName,
		"cert_url", cert.CertURL)

	return nil
}
