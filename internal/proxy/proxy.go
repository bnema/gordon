package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/interfaces"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/time/rate"
)

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
}

// ProxyRouteInfo contains the information needed to route traffic to a container
type ProxyRouteInfo struct {
	Domain        string
	ContainerIP   string
	ContainerPort string
	ContainerID   string
	Protocol      string
	Path          string
	Active        bool
}

// Create blacklistedIPs set to track IPs for middleware skipping
type requestContext struct {
	blacklisted bool
}

// NewProxy creates a new reverse proxy
func NewProxy(app interfaces.AppInterface) (*Proxy, error) {
	// Get the configuration from the app interface
	// This will use the already loaded global configuration
	config := app.GetConfig().ReverseProxy

	// Create the routes map
	routes := make(map[string]*ProxyRouteInfo)

	// Initialize the blacklist
	// Try both .yml and .yaml extensions, preferring .yml
	var blacklist *BlacklistConfig
	var err error

	storageDir := app.GetConfig().General.StorageDir
	// Prefer .yml over .yaml for consistency with config.yml
	blacklistPath := storageDir + "/blacklist.yml"
	blacklistLegacy := storageDir + "/blacklist.yaml"

	// Check if legacy blacklist.yaml exists but not .yml
	if _, statErrLegacy := os.Stat(blacklistLegacy); statErrLegacy == nil {
		if _, statErrYml := os.Stat(blacklistPath); statErrYml != nil && os.IsNotExist(statErrYml) {
			log.Info("Found legacy blacklist.yaml file, using it", "path", blacklistLegacy)
			blacklist, err = NewBlacklist(blacklistLegacy)
		} else {
			// Both exist or only .yml exists, prefer .yml
			log.Debug("Using blacklist.yml file", "path", blacklistPath)
			blacklist, err = NewBlacklist(blacklistPath)
		}
	} else {
		// No legacy file, use .yml
		log.Debug("Using blacklist.yml file", "path", blacklistPath)
		blacklist, err = NewBlacklist(blacklistPath)
	}

	if err != nil {
		log.Error("Failed to initialize blacklist", "error", err)
		// Continue anyway with nil blacklist
	}

	// Create the HTTPS echo instance
	httpsServer := echo.New()
	httpsServer.HideBanner = true
	httpsServer.Use(middleware.Recover())

	// Create the HTTP echo instance (for redirects to HTTPS)
	httpServer := echo.New()
	httpServer.HideBanner = true
	httpServer.Use(middleware.Recover())

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

// setupMiddleware configures middleware for both HTTP and HTTPS servers
func (p *Proxy) setupMiddleware() {
	// Create blacklist middleware for HTTPS
	p.httpsServer.Use(p.createBlacklistMiddleware())

	// Add rate limiter middleware to prevent spam attacks
	p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(10),  // 10 requests per second
				Burst:     30,              // Burst of 30 requests
				ExpiresIn: 3 * time.Minute, // Store expiration
			},
		),
		DenyHandler: func(context echo.Context, identifier string, err error) error {
			log.Warn("Rate limit exceeded",
				"ip", identifier,
				"path", context.Request().URL.Path,
				"method", context.Request().Method)
			return context.String(http.StatusTooManyRequests, "Too many requests")
		},
	}))

	// Add default Echo logger middleware
	p.httpsServer.Use(middleware.Logger())

	// Create blacklist middleware for HTTP
	p.httpServer.Use(p.createBlacklistMiddleware())

	// Add rate limiter middleware to prevent spam attacks for HTTP server
	p.httpServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(10),  // 10 requests per second
				Burst:     30,              // Burst of 30 requests
				ExpiresIn: 3 * time.Minute, // Store expiration
			},
		),
		DenyHandler: func(context echo.Context, identifier string, err error) error {
			log.Warn("Rate limit exceeded",
				"ip", identifier,
				"path", context.Request().URL.Path,
				"method", context.Request().Method)
			return context.String(http.StatusTooManyRequests, "Too many requests")
		},
	}))

	// Add default Echo logger middleware for HTTP server
	p.httpServer.Use(middleware.Logger())
}

// createBlacklistMiddleware returns a middleware function that checks IPs against the blacklist
func (p *Proxy) createBlacklistMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Get client IP
			clientIP := c.RealIP()

			// Get host and strip port if present
			host := c.Request().Host
			if strings.Contains(host, ":") {
				host = strings.Split(host, ":")[0]
			}

			// Check if the host is an IP address
			if net.ParseIP(host) != nil {
				// This is an IP address being used as a hostname - silently reject
				// This prevents log spam without adding IPs to the blacklist
				// Set context for other middleware
				c.Set("reqContext", &requestContext{blacklisted: true})
				return c.String(http.StatusForbidden, "Forbidden")
			}

			// Debug log for all requests to check if the IP is being properly recognized
			log.Debug("Received request", "ip", clientIP, "path", c.Request().URL.Path)

			// Check if IP is blacklisted (if blacklist exists)
			if p.blacklist != nil && p.blacklist.IsBlocked(clientIP) {
				// Set context for other middleware
				c.Set("reqContext", &requestContext{blacklisted: true})

				// Rate-limited logging of blocked IPs
				p.logBlockedIP(clientIP, c.Request().URL.Path, c.Request().UserAgent())

				// Return forbidden without calling next handlers at all
				return c.String(http.StatusForbidden, "Forbidden")
			}

			// Store the context in the request context for non-blacklisted IPs
			c.Set("reqContext", &requestContext{blacklisted: false})
			return next(c)
		}
	}
}

// setupCertManager configures the autocert manager for Let's Encrypt
func (p *Proxy) setupCertManager() {
	// Create a cache directory if it doesn't exist
	dir := p.config.CertDir
	if dir == "" {
		dir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Warn("Failed to create certificate cache directory",
			"dir", dir,
			"error", err)
	}

	// Set up the certificate manager
	certManager := &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(dir),
	}

	// Configure the email if provided
	if p.config.Email != "" {
		certManager.Email = p.config.Email
	}

	// Configure the Let's Encrypt client
	if p.config.LetsEncryptMode == "staging" {
		certManager.Client = &acme.Client{
			DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory",
		}
		log.Debug("Using Let's Encrypt staging environment",
			"url", "https://acme-staging-v02.api.letsencrypt.org/directory")
	} else {
		// Explicitly set production URL when not in staging mode
		certManager.Client = &acme.Client{
			DirectoryURL: acme.LetsEncryptURL, // "https://acme-v02.api.letsencrypt.org/directory"
		}
		log.Debug("Using Let's Encrypt production environment",
			"url", acme.LetsEncryptURL)
	}

	// Set HostPolicy to allow the admin domain
	adminDomain := p.app.GetConfig().Http.FullDomain()
	rootDomain := p.app.GetConfig().Http.Domain

	// Use a more permissive hostpolicy that logs unknown domains rather than rejecting them
	// This helps debug certificate acquisition issues
	certManager.HostPolicy = func(_ context.Context, host string) error {
		// Always allow the admin domain and root domain
		if host == adminDomain || host == rootDomain {
			return nil
		}

		// For other domains, check if they are in our routes
		p.mu.RLock()
		defer p.mu.RUnlock()

		if _, ok := p.routes[host]; ok {
			return nil
		}

		// Log the attempt but still allow it - this helps debug
		// certificate acquisition issues during development
		log.Warn("Unknown host in certificate request",
			"host", host,
			"adminDomain", adminDomain,
			"allowed", "yes")

		// Allow unknown domains temporarily to help diagnose issues
		// Change this to return an error in production for stricter security
		return nil
	}

	// Generate a fallback self-signed certificate for the admin domain
	var fallbackDomains []string
	fallbackDomains = append(fallbackDomains, adminDomain)

	// Include root domain in fallback certificate if it's different from admin domain
	if rootDomain != "" && rootDomain != adminDomain {
		fallbackDomains = append(fallbackDomains, rootDomain)
	}

	fallbackCert, err := generateFallbackCertificates(fallbackDomains)
	if err != nil {
		log.Warn("Failed to generate fallback certificate", "error", err)
	} else {
		log.Info("Generated fallback self-signed certificate for admin domain",
			"domain", adminDomain,
			"valid_until", time.Now().Add(24*time.Hour).Format("2006-01-02 15:04:05"))
		// Store the fallback certificate
		p.fallbackCert = fallbackCert
	}

	p.certManager = certManager
	log.Debug("Certificate manager setup completed",
		"directory", dir,
		"mode", p.config.LetsEncryptMode)

	// Request the certificate for the admin domain
	go p.requestAdminCertificate()
}

// checkCertificateInCache checks if a valid certificate for the given domain exists in the cache
// Returns true if a valid certificate exists, false otherwise
func (p *Proxy) checkCertificateInCache(domain string) bool {
	if p.certManager == nil || p.certManager.Cache == nil {
		return false
	}

	// The cache key format used by autocert is "cert-" + domain
	cacheKey := "cert-" + domain

	// Create a context with a short timeout for the cache check
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to get the certificate from the cache
	certData, err := p.certManager.Cache.Get(ctx, cacheKey)
	if err != nil {
		// ErrCacheMiss or other error means the certificate is not in the cache
		log.Debug("Certificate not found in cache",
			"domain", domain,
			"error", err)
		return false
	}

	// Parse the certificate to check its validity
	block, _ := pem.Decode(certData)
	if block == nil || block.Type != "CERTIFICATE" {
		log.Warn("Invalid certificate data in cache",
			"domain", domain)
		return false
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		log.Warn("Failed to parse certificate from cache",
			"domain", domain,
			"error", err)
		return false
	}

	// Check if the certificate is still valid
	now := time.Now()
	if now.After(cert.NotAfter) || now.Before(cert.NotBefore) {
		log.Info("Certificate in cache has expired or is not yet valid",
			"domain", domain,
			"not_before", cert.NotBefore,
			"not_after", cert.NotAfter)
		return false
	}

	// Check if the certificate is about to expire (within 30 days)
	if now.Add(30 * 24 * time.Hour).After(cert.NotAfter) {
		log.Info("Certificate in cache is valid but will expire soon",
			"domain", domain,
			"expires_in", cert.NotAfter.Sub(now).Hours()/24,
			"days")
		// Return false to trigger renewal if it's about to expire
		return false
	}

	log.Info("Valid certificate found in cache",
		"domain", domain,
		"expires_in", cert.NotAfter.Sub(now).Hours()/24,
		"days")
	return true
}

// requestAdminCertificate preemptively requests a Let's Encrypt certificate
// for the Gordon admin interface
func (p *Proxy) requestAdminCertificate() {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	if adminDomain == "" {
		return
	}

	// Only proceed if HTTPS is enabled
	if !p.app.GetConfig().Http.Https {
		log.Debug("HTTPS is disabled, skipping admin certificate request")
		return
	}

	// Extract hostname from admin domain, resolving it to see if it's publicly accessible
	host := adminDomain
	ips, err := net.LookupIP(host)
	if err != nil {
		log.Error("Could not resolve admin domain, Let's Encrypt will likely fail",
			"domain", adminDomain,
			"error", err.Error(),
			"solution", "Check DNS settings and ensure domain points to this server")
		return
	}

	var ipStrings []string
	for _, ip := range ips {
		ipStrings = append(ipStrings, ip.String())
	}

	log.Info("Successfully resolved admin domain",
		"domain", adminDomain,
		"ips", ipStrings)

	// Check if we already have a valid certificate in the cache
	if p.checkCertificateInCache(adminDomain) {
		log.Info("Using existing certificate from cache",
			"domain", adminDomain,
			"action", "skipping Let's Encrypt request to avoid rate limits")
		return
	}

	// Check if environment is production
	mode := "staging"
	email := p.config.Email

	if !p.app.IsDevEnvironment() && email != "" {
		mode = "production"
	}

	// Log the certificate request intent
	log.Info("Initiating Let's Encrypt certificate request for admin domain",
		"domain", adminDomain,
		"email", email,
		"mode", mode)

	log.Info("⏳ Waiting for Let's Encrypt to validate domain ownership",
		"domain", adminDomain,
		"validation_method", "HTTP-01 challenge",
		"requirements", "Domain must be publicly accessible on port 80",
		"timeout", "1 minute")

	// Create a context with timeout for the initial certificate request
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Try to obtain the certificate with timeout
	var certResult *tls.Certificate
	var certErr error

	// Use a channel to handle the timeout
	certChan := make(chan struct {
		cert *tls.Certificate
		err  error
	})

	go func() {
		c, e := p.certManager.GetCertificate(&tls.ClientHelloInfo{
			ServerName: adminDomain,
		})
		certChan <- struct {
			cert *tls.Certificate
			err  error
		}{c, e}
	}()

	// Wait for either the certificate request to complete or the timeout
	select {
	case result := <-certChan:
		certResult = result.cert
		certErr = result.err
	case <-ctx.Done():
		certErr = fmt.Errorf("certificate request timed out after 2 minutes: %w", ctx.Err())
		log.Error("Let's Encrypt certificate request timed out",
			"domain", adminDomain,
			"timeout", "2 minutes",
			"error", certErr)
	}

	if certErr != nil {
		// Check for rate limit errors
		if strings.Contains(strings.ToLower(certErr.Error()), "ratelimited") {
			// Extract the retry-after time if available in the error message
			retryAfterStr := ""
			re := regexp.MustCompile(`retry after ([^:]+)`)
			matches := re.FindStringSubmatch(certErr.Error())
			if len(matches) > 1 {
				retryAfterStr = matches[1]
			}

			log.Error("Let's Encrypt rate limit reached",
				"domain", adminDomain,
				"error", certErr,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// Don't retry on rate limits
			log.Info("Skipping certificate request retries due to rate limiting",
				"domain", adminDomain)
		} else {
			log.Error("Failed to obtain Let's Encrypt certificate",
				"domain", adminDomain,
				"error", certErr)

			log.Info("⏳ Retrying certificate request with exponential backoff",
				"domain", adminDomain,
				"timeout", "2 minutes")
			// Implement retry logic with backoff
			go p.retryCertificateRequest(adminDomain, 3, 10*time.Second)
		}
	} else if certResult != nil {
		log.Info("Successfully obtained Let's Encrypt certificate",
			"domain", adminDomain)
	} else {
		log.Error("Unexpected error: received nil certificate but no error",
			"domain", adminDomain,
			"timeout", "2 minutes")
		// Implement retry logic with backoff
		go p.retryCertificateRequest(adminDomain, 3, 10*time.Second)
	}
}

// requestDomainCertificate proactively requests a Let's Encrypt certificate
// for a container domain
func (p *Proxy) requestDomainCertificate(domain string) {
	if domain == "" {
		log.Warn("Empty domain provided to requestDomainCertificate")
		return
	}

	// Extract hostname from domain, resolving it to see if it's publicly accessible
	ips, err := net.LookupIP(domain)
	if err != nil {
		log.Error("Could not resolve domain, Let's Encrypt will likely fail",
			"domain", domain,
			"error", err.Error(),
			"solution", "Check DNS settings and ensure domain points to this server")
		return
	}

	var ipStrings []string
	for _, ip := range ips {
		ipStrings = append(ipStrings, ip.String())
	}

	log.Info("Successfully resolved domain for certificate request",
		"domain", domain,
		"ips", ipStrings)

	// Check if we already have a valid certificate in the cache
	if p.checkCertificateInCache(domain) {
		log.Info("Using existing certificate from cache",
			"domain", domain,
			"action", "skipping Let's Encrypt request to avoid rate limits")
		return
	}

	// Check if environment is production
	mode := "staging"
	email := p.config.Email

	if !p.app.IsDevEnvironment() && email != "" {
		mode = "production"
	}

	// Log the certificate request intent
	log.Info("Initiating Let's Encrypt certificate request for container domain",
		"domain", domain,
		"email", email,
		"mode", mode)

	log.Info("⏳ Waiting for Let's Encrypt to validate domain ownership",
		"domain", domain,
		"validation_method", "HTTP-01 challenge",
		"requirements", "Domain must be publicly accessible on port 80",
		"timeout", "1 minute")

	// Create a context with timeout for the initial certificate request
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Try to obtain the certificate with timeout
	var certResult *tls.Certificate
	var certErr error

	// Use a channel to handle the timeout
	certChan := make(chan struct {
		cert *tls.Certificate
		err  error
	})

	go func() {
		c, e := p.certManager.GetCertificate(&tls.ClientHelloInfo{
			ServerName: domain,
		})
		certChan <- struct {
			cert *tls.Certificate
			err  error
		}{c, e}
	}()

	// Wait for either the certificate request to complete or the timeout
	select {
	case result := <-certChan:
		certResult = result.cert
		certErr = result.err
	case <-ctx.Done():
		certErr = fmt.Errorf("certificate request timed out after 1 minute: %w", ctx.Err())
		log.Error("Let's Encrypt certificate request timed out",
			"domain", domain,
			"timeout", "1 minute",
			"error", certErr)
	}

	if certErr != nil {
		// Check for rate limit errors
		if strings.Contains(strings.ToLower(certErr.Error()), "ratelimited") {
			// Extract the retry-after time if available in the error message
			retryAfterStr := ""
			re := regexp.MustCompile(`retry after ([^:]+)`)
			matches := re.FindStringSubmatch(certErr.Error())
			if len(matches) > 1 {
				retryAfterStr = matches[1]
			}

			log.Error("Let's Encrypt rate limit reached",
				"domain", domain,
				"error", certErr,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// Don't retry on rate limits
			log.Info("Skipping certificate request retries due to rate limiting",
				"domain", domain)
		} else {
			log.Error("Failed to obtain Let's Encrypt certificate",
				"domain", domain,
				"error", certErr)

			log.Info("⏳ Retrying certificate request with exponential backoff",
				"domain", domain,
				"timeout", "2 minutes")
			// Implement retry logic with backoff
			go p.retryCertificateRequest(domain, 3, 10*time.Second)
		}
	} else if certResult != nil {
		log.Info("Successfully obtained Let's Encrypt certificate",
			"domain", domain)
	} else {
		log.Error("Unexpected error: received nil certificate but no error",
			"domain", domain,
			"timeout", "1 minute")
		// Implement retry logic with backoff
		go p.retryCertificateRequest(domain, 3, 10*time.Second)
	}
}

// retryCertificateRequest attempts to request a certificate with exponential backoff
func (p *Proxy) retryCertificateRequest(domain string, maxRetries int, initialBackoff time.Duration) {
	backoff := initialBackoff

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Wait for backoff period
		log.Info("Retrying Let's Encrypt certificate request",
			"domain", domain,
			"attempt", attempt,
			"max_retries", maxRetries,
			"wait_time", backoff)
		time.Sleep(backoff)

		// Before each retry, check if the HTTP challenge endpoint is accessible
		if attempt > 1 {
			// Try connecting to our own HTTP server on port 80
			// to verify its availability for Let's Encrypt validation
			client := &http.Client{
				Timeout: 5 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					// Don't follow redirects for this test
					return http.ErrUseLastResponse
				},
			}

			// Test the ACME challenge path with a fake token
			testURL := fmt.Sprintf("http://%s/.well-known/acme-challenge/test-token", domain)
			resp, err := client.Get(testURL)

			if err != nil {
				log.Warn("HTTP challenge endpoint might not be accessible",
					"domain", domain,
					"url", testURL,
					"error", err,
					"solution", "Ensure port 80 is accessible and not blocked by firewall")
			} else {
				resp.Body.Close()
				log.Info("HTTP challenge endpoint is accessible",
					"domain", domain,
					"url", testURL,
					"status", resp.StatusCode)
			}
		}

		// Create context with timeout for this attempt
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)

		// Request certificate
		err := func(ctx context.Context) error {
			defer cancel()
			cert, certErr := p.certManager.GetCertificate(&tls.ClientHelloInfo{
				ServerName: domain,
			})
			if cert == nil && certErr == nil {
				return fmt.Errorf("certificate is nil but no error returned")
			}
			return certErr
		}(ctx)

		// Check result
		if err == nil {
			log.Info("Successfully obtained Let's Encrypt certificate on retry",
				"domain", domain,
				"attempt", attempt)
			return
		}

		log.Error("Let's Encrypt certificate request retry failed",
			"domain", domain,
			"attempt", attempt,
			"error", err)

		// Provide more detailed diagnostics based on the error
		if strings.Contains(strings.ToLower(err.Error()), "connection refused") ||
			strings.Contains(strings.ToLower(err.Error()), "timeout") {
			log.Error("Let's Encrypt connection failed - this typically indicates:",
				"issue_1", "Port 80 is not accessible from the internet",
				"issue_2", "Firewall is blocking inbound connections",
				"issue_3", "DNS records not properly propagated",
				"solution", "Check firewall settings and DNS configuration")
		} else if strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
			log.Error("Let's Encrypt authorization failed - this typically indicates:",
				"issue", "Domain ownership validation failed",
				"solution", "Ensure the server is publicly accessible on port 80")
		} else if strings.Contains(strings.ToLower(err.Error()), "ratelimited") {
			// Extract the retry-after date if present
			retryAfterStr := "see error message for details"
			if strings.Contains(err.Error(), "retry after") {
				parts := strings.Split(err.Error(), "retry after")
				if len(parts) > 1 {
					retryAfterStr = strings.TrimSpace(parts[1])
					if idx := strings.Index(retryAfterStr, ":"); idx > 0 {
						retryAfterStr = retryAfterStr[:idx]
					}
				}
			}

			log.Error("Let's Encrypt rate limit reached - cannot issue more certificates for this domain yet",
				"domain", domain,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// No point in retrying on rate limit errors
			return
		}

		// Increase backoff for next attempt (exponential backoff)
		backoff *= 2
	}

	log.Error("All Let's Encrypt certificate request retries failed",
		"domain", domain,
		"max_retries", maxRetries,
		"fallback", "Using self-signed certificate")

	// At this point, all retries have failed, so we'll rely on the fallback self-signed certificate
	// But let's log this prominently for debugging
	log.Error("⚠️ HTTPS is using a self-signed certificate which browsers will warn about",
		"domain", domain,
		"reason", "Let's Encrypt certificate issuance failed",
		"solution", "Check network settings and Let's Encrypt status")

	// Suggest checking the common issues
	log.Error("Common Let's Encrypt issues to check:",
		"check_1", "Ensure ports 80 and 443 are open on your firewall",
		"check_2", "Verify DNS records point to your server IP",
		"check_3", "Make sure no other services are running on ports 80/443",
		"check_4", "Check if Let's Encrypt service is having issues (https://letsencrypt.status.io/)")
}

// Start loads the routes from the database and starts the proxy server
func (p *Proxy) Start() error {
	// Check if there might be port conflicts with the main server
	mainServerPort := p.app.GetConfig().Http.Port

	// Check HTTP port conflict
	if p.config.HttpPort == mainServerPort {
		log.Warn("HTTP port for reverse proxy conflicts with main server port",
			"port", p.config.HttpPort,
			"solution", "reverse proxy HTTP server will be disabled")
		return nil
	}

	// Check HTTPS port conflict (less common, but still possible)
	if p.config.Port == mainServerPort {
		log.Warn("HTTPS port for reverse proxy conflicts with main server port",
			"port", p.config.Port,
			"solution", "reverse proxy HTTPS server will be disabled")
		return nil
	}

	// Load routes from the database
	if err := p.loadRoutes(); err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	// Add a special route for the admin domain (Gordon itself)
	adminDomain := p.app.GetConfig().Http.FullDomain()

	// In a container environment, we need to use the host container IP
	// instead of 127.0.0.1 because each container has its own localhost
	containerIP := "localhost" // Default to localhost for most reliable connectivity

	// Fall back options for container IP
	if os.Getenv("GORDON_ADMIN_HOST") != "" {
		// Allow explicit configuration via env var
		containerIP = os.Getenv("GORDON_ADMIN_HOST")
		log.Debug("Using admin host from environment variable",
			"host", containerIP)
	} else if os.Getenv("HOSTNAME") != "" {
		// Use container's own hostname as they're on the same network
		containerIP = os.Getenv("HOSTNAME")
		log.Debug("Using container hostname for admin routing",
			"hostname", containerIP)
	}

	// Don't test connections yet - the server isn't running
	// We'll add the admin route with the default host for now
	// and test connections later with TestAdminConnectionLater

	p.mu.Lock()
	// Only add if it doesn't already exist
	if _, exists := p.routes[adminDomain]; !exists {
		p.routes[adminDomain] = &ProxyRouteInfo{
			Domain:        adminDomain,
			ContainerIP:   containerIP,
			ContainerPort: p.app.GetConfig().Http.Port,
			ContainerID:   "gordon-server",
			Protocol:      "http", // Gordon server uses HTTP internally
			Path:          "/",
			Active:        true,
		}
		log.Info("Added special route for admin domain",
			"domain", adminDomain,
			"target", fmt.Sprintf("http://%s:%s", containerIP, p.app.GetConfig().Http.Port))
	}

	// Add support for the root domain, redirecting to admin subdomain
	rootDomain := p.app.GetConfig().Http.Domain
	if rootDomain != "" && rootDomain != adminDomain {
		if _, exists := p.routes[rootDomain]; !exists {
			p.routes[rootDomain] = &ProxyRouteInfo{
				Domain:        rootDomain,
				ContainerIP:   containerIP,
				ContainerPort: p.app.GetConfig().Http.Port,
				ContainerID:   "gordon-server",
				Protocol:      "http", // Gordon server uses HTTP internally
				Path:          "/",
				Active:        true,
			}
			log.Info("Added route for root domain with redirect to admin subdomain",
				"root_domain", rootDomain,
				"admin_domain", adminDomain)
		}
	}
	p.mu.Unlock()

	// Configure the proxy routes
	p.configureRoutes()

	// Configure HTTP server to handle Let's Encrypt HTTP-01 challenges
	// and redirect everything else to HTTPS
	p.httpServer.Any("/.well-known/acme-challenge/*", echo.WrapHandler(p.certManager.HTTPHandler(nil)))

	// Handle all other HTTP requests by redirecting to HTTPS
	p.httpServer.Any("/*", func(c echo.Context) error {
		// Skip handling acme challenges (already handled above)
		if strings.HasPrefix(c.Request().URL.Path, "/.well-known/acme-challenge/") {
			return nil
		}

		// No need to check for blacklisting here as it's already checked in middleware
		// and all blacklisted requests will be stopped before reaching this handler

		host := c.Request().Host
		if strings.Contains(host, ":") {
			host = strings.Split(host, ":")[0]
		}

		// Special handling for root domain - redirect to admin subdomain
		adminDomain := p.app.GetConfig().Http.FullDomain()
		rootDomain := p.app.GetConfig().Http.Domain

		if host == rootDomain && rootDomain != adminDomain {
			// Redirect to HTTPS admin subdomain
			redirectURL := fmt.Sprintf("https://%s%s", adminDomain, c.Request().RequestURI)
			log.Debug("Redirecting HTTP root domain request to HTTPS admin subdomain",
				"from", host,
				"to", adminDomain,
				"redirect_url", redirectURL)
			return c.Redirect(http.StatusPermanentRedirect, redirectURL)
		}

		// Redirect to HTTPS
		return c.Redirect(http.StatusMovedPermanently,
			fmt.Sprintf("https://%s%s", host, c.Request().RequestURI))
	})

	// Start the HTTPS server
	httpsServer := &http.Server{
		Addr:    ":" + p.config.Port,
		Handler: p.httpsServer,
		TLSConfig: &tls.Config{
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				// Enhanced logging to debug SNI issues
				adminDomain := p.app.GetConfig().Http.FullDomain()

				// If SNI is missing, use the admin domain
				if hello.ServerName == "" {
					log.Debug("TLS handshake without SNI, using default admin domain",
						"default_domain", adminDomain)
					hello.ServerName = adminDomain
				} else {
					log.Debug("TLS handshake with SNI",
						"server_name", hello.ServerName)
				}

				// Try to get the certificate from the autocert manager
				cert, err := p.certManager.GetCertificate(hello)

				// If we can't get a certificate and we have a fallback, use it for the admin domain
				if (err != nil || cert == nil) && hello.ServerName == adminDomain && p.fallbackCert != nil {
					log.Debug("Using fallback certificate for admin domain",
						"domain", adminDomain,
						"error", err)
					return p.fallbackCert, nil
				}

				return cert, err
			},
			MinVersion: tls.VersionTLS12,
			// Add server name to use when client doesn't send SNI
			ServerName: p.app.GetConfig().Http.FullDomain(),
			// Add support for TLS-ALPN-01 challenges
			NextProtos: []string{acme.ALPNProto},
		},
		// Add timeouts to prevent hanging connections
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start the HTTP server (for redirects and Let's Encrypt challenges)
	httpServer := &http.Server{
		Addr:         ":" + p.config.HttpPort,
		Handler:      p.httpServer,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	p.serverStarted = true

	// Start the HTTPS server
	go func() {
		log.Info("Starting HTTPS reverse proxy server", "port", p.config.Port)
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Error("Failed to start HTTPS reverse proxy server", "err", err)
		}
	}()

	// Start the HTTP server (for redirects and Let's Encrypt challenges)
	go func() {
		log.Info("Starting HTTP reverse proxy server (for redirects and Let's Encrypt challenges)", "port", p.config.HttpPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("Failed to start HTTP reverse proxy server", "err", err)
		}
	}()

	return nil
}

// testAdminConnection attempts to find a working connection to the Gordon admin
// by testing different hostnames and IPs
func (p *Proxy) testAdminConnection(defaultHost string, port string) string {
	log.Info("Testing connections to Gordon admin server")

	// Create a client with a short timeout to quickly test connections
	client := &http.Client{
		Timeout: 2 * time.Second,
	}

	// List of potential hosts to try, in order of preference
	hostsToTry := []string{
		"localhost",
		"127.0.0.1",                // Always try localhost first
		defaultHost,                // The current host value
		"host.docker.internal",     // Docker Desktop default
		"host.containers.internal", // Modern Docker/Podman default
	}

	// Add hostname if available
	if hostname := os.Getenv("HOSTNAME"); hostname != "" {
		hostsToTry = append(hostsToTry, hostname)
	}

	// If GORDON_ADMIN_HOST is defined, make it highest priority after localhost
	if adminHost := os.Getenv("GORDON_ADMIN_HOST"); adminHost != "" && adminHost != defaultHost {
		// Add after localhost
		hostsToTry = append(hostsToTry[:1], append([]string{adminHost}, hostsToTry[1:]...)...)
	}

	// Deduplicate
	seen := make(map[string]bool)
	var uniqueHosts []string
	for _, host := range hostsToTry {
		if !seen[host] && host != "" {
			seen[host] = true
			uniqueHosts = append(uniqueHosts, host)
		}
	}
	hostsToTry = uniqueHosts

	log.Debug("Will test the following hosts for admin connections",
		"hosts", strings.Join(hostsToTry, ", "))

	// Test each host
	for _, host := range hostsToTry {
		testPath := fmt.Sprintf("http://%s:%s%s/ping", host, port, p.app.GetConfig().Admin.Path)
		log.Info("Testing connection to Gordon admin", "host", host, "url", testPath)

		startTime := time.Now()
		resp, err := client.Get(testPath)
		duration := time.Since(startTime)

		if err == nil {
			statusCode := resp.StatusCode
			resp.Body.Close()

			log.Info("Successfully connected to Gordon admin",
				"host", host,
				"status", statusCode,
				"duration", duration)

			// Consider any connection successful, even non-2xx responses
			// This is important since the route might be valid even if the specific endpoint
			// returns a different status (e.g. 404 if /ping doesn't exist)
			return host
		} else {
			log.Debug("Failed to connect to Gordon admin",
				"host", host,
				"error", err.Error(),
				"duration", duration)
		}
	}

	// If no connections worked, default to the original
	log.Warn("Could not establish connection to Gordon admin on any tested host",
		"fallback", defaultHost)
	return defaultHost
}

// Stop stops the proxy server
func (p *Proxy) Stop() error {
	if !p.serverStarted {
		return nil
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(p.config.GracePeriod)*time.Second,
	)
	defer cancel()

	// Shutdown the servers
	log.Info("Stopping reverse proxy servers")

	// Stop HTTPS server
	if err := p.httpsServer.Shutdown(ctx); err != nil {
		log.Error("Error stopping HTTPS server", "err", err)
	}

	// Stop HTTP server
	if err := p.httpServer.Shutdown(ctx); err != nil {
		log.Error("Error stopping HTTP server", "err", err)
	}

	return nil
}

// loadRoutes loads the routes from the database
func (p *Proxy) loadRoutes() error {
	// Lock the routes map
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Debug("Loading proxy routes from database")

	// Query the database for active proxy routes
	rows, err := p.app.GetDB().Query(`
		SELECT pr.id, d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
		FROM proxy_route pr
		JOIN domain d ON pr.domain_id = d.id
		WHERE pr.active = 1
	`)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Info("No active proxy routes found")
			return nil
		}
		return fmt.Errorf("failed to query database: %w", err)
	}
	defer rows.Close()

	// Clear the routes map
	p.routes = make(map[string]*ProxyRouteInfo)

	// Populate the routes map
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Add the route to the map
		p.routes[domain] = &ProxyRouteInfo{
			Domain:        domain,
			ContainerID:   containerID,
			ContainerIP:   containerIP,
			ContainerPort: containerPort,
			Protocol:      protocol,
			Path:          path,
			Active:        active,
		}

		log.Debug("Loaded proxy route",
			"domain", domain,
			"containerIP", containerIP,
			"containerPort", containerPort,
		)
	}

	log.Info("Loaded proxy routes", "count", len(p.routes))
	return nil
}

// configureRoutes sets up the HTTP and HTTPS routes
func (p *Proxy) configureRoutes() {
	// Lock the routes map for reading
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Log all available routes for debugging
	log.Debug("Configuring proxy routes")
	for domain, route := range p.routes {
		log.Debug("Configured route",
			"domain", domain,
			"target", fmt.Sprintf("%s://%s:%s", route.Protocol, route.ContainerIP, route.ContainerPort),
			"active", route.Active)
	}

	// Add a handler for all incoming requests (HTTPS)
	p.httpsServer.Any("/*", func(c echo.Context) error {
		// No need to check for blacklisting here as it's already checked in middleware
		// and all blacklisted requests will be stopped before reaching this handler

		host := c.Request().Host

		// Strip the port from the host
		if strings.Contains(host, ":") {
			host = strings.Split(host, ":")[0]
		}

		log.Debug("Processing request",
			"host", host,
			"path", c.Request().URL.Path,
			"method", c.Request().Method)

		// Special handling for root domain - redirect to admin subdomain
		adminDomain := p.app.GetConfig().Http.FullDomain()
		rootDomain := p.app.GetConfig().Http.Domain

		if host == rootDomain && rootDomain != adminDomain {
			redirectURL := fmt.Sprintf("https://%s%s", adminDomain, c.Request().URL.Path)
			log.Debug("Redirecting root domain request to admin subdomain",
				"from", host,
				"to", adminDomain,
				"redirect_url", redirectURL)
			return c.Redirect(http.StatusPermanentRedirect, redirectURL)
		}

		// Find the route for this host
		route, ok := p.routes[host]
		if !ok {
			// Check if the host is an IP address - silently handle without logging warnings
			if net.ParseIP(host) != nil {
				// For IP-based requests, just return a 404 without logging warnings
				return c.String(http.StatusNotFound, "Domain not found")
			}

			// Create list of available domains for debugging
			availableDomains := make([]string, 0, len(p.routes))
			for d := range p.routes {
				availableDomains = append(availableDomains, d)
			}

			// Log warning for non-IP hosts that aren't configured
			log.Warn("Request with unknown host",
				"requested_host", host,
				"client_ip", c.RealIP(),
				"available_domains", strings.Join(availableDomains, ", "))
			return c.String(http.StatusNotFound, "Domain not found")
		}

		// Check if the route is active
		if !route.Active {
			log.Warn("Route is not active",
				"domain", host,
				"client_ip", c.RealIP())
			return c.String(http.StatusServiceUnavailable, "Route is not active")
		}

		// Special handling for the admin domain
		if host == adminDomain {
			log.Debug("Proxying request to admin domain",
				"domain", host,
				"target", fmt.Sprintf("%s://%s:%s", route.Protocol, route.ContainerIP, route.ContainerPort),
				"path", c.Request().URL.Path)
		}

		// Create the target URL
		targetURL := &url.URL{
			Scheme: route.Protocol,
			Host:   fmt.Sprintf("%s:%s", route.ContainerIP, route.ContainerPort),
		}

		// Create a reverse proxy
		proxy := httputil.NewSingleHostReverseProxy(targetURL)

		// Update headers to allow for SSL redirection
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", host)
			req.Header.Set("X-Forwarded-For", c.RealIP())
			req.Header.Set("X-Real-IP", c.RealIP())

			// Debug information
			log.Debug("Proxying request",
				"host", host,
				"target", targetURL.String(),
				"path", req.URL.Path,
				"clientIP", c.RealIP())
		}

		// Add error handling
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error("Proxy error",
				"host", host,
				"path", r.URL.Path,
				"error", err)
			c.String(http.StatusBadGateway, "Proxy Error: "+err.Error())
		}

		// Serve the request
		proxy.ServeHTTP(c.Response(), c.Request())
		return nil
	})
}

// Reload reloads the routes from the database and reconfigures the proxy
func (p *Proxy) Reload() error {
	// Load routes from the database
	if err := p.loadRoutes(); err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	// Reconfigure the routes
	p.configureRoutes()

	return nil
}

// AddRoute adds a new route to the database and reloads the proxy
func (p *Proxy) AddRoute(domainName, containerID, containerIP, containerPort, protocol, path string) error {
	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Extract the hostname from domainName if it contains a protocol
	hostname := domainName
	if strings.Contains(hostname, "://") {
		parsedURL, err := url.Parse(hostname)
		if err == nil && parsedURL.Host != "" {
			hostname = parsedURL.Host
		}
	}

	// Check if the domain exists
	var domainID string
	err = tx.QueryRow("SELECT id FROM domain WHERE name = ?", hostname).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Domain doesn't exist, create it
			domainID = generateUUID()
			now := time.Now().Format(time.RFC3339)
			_, err = tx.Exec(
				"INSERT INTO domain (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)",
				domainID, hostname, now, now,
			)
			if err != nil {
				return fmt.Errorf("failed to insert domain: %w", err)
			}
		} else {
			return fmt.Errorf("failed to query domain: %w", err)
		}
	}

	// Check if a route already exists for this domain
	var existingRouteID string
	err = tx.QueryRow("SELECT id FROM proxy_route WHERE domain_id = ?", domainID).Scan(&existingRouteID)
	if err == nil {
		// Route exists, update it
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec(
			`UPDATE proxy_route SET 
				container_id = ?, 
				container_ip = ?, 
				container_port = ?, 
				protocol = ?, 
				path = ?, 
				active = ?, 
				updated_at = ? 
			WHERE id = ?`,
			containerID, containerIP, containerPort, protocol, path, true, now, existingRouteID,
		)
		if err != nil {
			return fmt.Errorf("failed to update route: %w", err)
		}
	} else if err == sql.ErrNoRows {
		// Route doesn't exist, create it
		routeID := generateUUID()
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec(
			`INSERT INTO proxy_route (
				id, domain_id, container_id, container_ip, container_port, 
				protocol, path, active, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			routeID, domainID, containerID, containerIP, containerPort,
			protocol, path, true, now, now,
		)
		if err != nil {
			return fmt.Errorf("failed to insert route: %w", err)
		}
	} else {
		return fmt.Errorf("failed to query route: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Reload the routes
	if err := p.Reload(); err != nil {
		return fmt.Errorf("failed to reload routes: %w", err)
	}

	log.Info("Added proxy route",
		"domain", hostname,
		"containerIP", containerIP,
		"containerPort", containerPort,
	)

	// Request a certificate if this is an HTTPS route
	if strings.ToLower(protocol) == "https" {
		log.Info("Requesting Let's Encrypt certificate for new HTTPS route",
			"domain", hostname)
		// Run in a goroutine to avoid blocking
		go p.requestDomainCertificate(hostname)
	}

	return nil
}

// RemoveRoute removes a route from the database and reloads the proxy
func (p *Proxy) RemoveRoute(domainName string) error {
	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get the domain ID
	var domainID string
	err = tx.QueryRow("SELECT id FROM domain WHERE name = ?", domainName).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("domain not found: %s", domainName)
		}
		return fmt.Errorf("failed to query domain: %w", err)
	}

	// Delete the route
	_, err = tx.Exec("DELETE FROM proxy_route WHERE domain_id = ?", domainID)
	if err != nil {
		return fmt.Errorf("failed to delete route: %w", err)
	}

	// Delete the domain
	_, err = tx.Exec("DELETE FROM domain WHERE id = ?", domainID)
	if err != nil {
		return fmt.Errorf("failed to delete domain: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Reload the routes
	if err := p.Reload(); err != nil {
		return fmt.Errorf("failed to reload routes: %w", err)
	}

	log.Info("Removed proxy route", "domain", domainName)
	return nil
}

// generateUUID generates a UUID for use as a primary key
func generateUUID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// GetRoutes returns a copy of the routes map
func (p *Proxy) GetRoutes() map[string]*ProxyRouteInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Create a copy of the routes map
	routes := make(map[string]*ProxyRouteInfo, len(p.routes))
	for k, v := range p.routes {
		routes[k] = v
	}

	return routes
}

// Add a helper method to log blocked IPs with rate limiting
func (p *Proxy) logBlockedIP(clientIP, path, userAgent string) {
	p.blockedIPCountMu.Lock()
	defer p.blockedIPCountMu.Unlock()

	now := time.Now()

	// If this is the first block or it's been more than 5 minutes since the last summary,
	// or if this is a new IP we haven't seen recently
	if p.lastBlockedLog.IsZero() ||
		now.Sub(p.lastBlockedLog) > 5*time.Minute ||
		p.blockedIPCounter[clientIP] == 0 {

		// Log this block and update counters
		log.Info("Blocked request from blacklisted IP",
			"ip", clientIP,
			"path", path,
			"user_agent", userAgent)

		// If we're resetting due to time, clear the counters
		if p.lastBlockedLog.IsZero() || now.Sub(p.lastBlockedLog) > 5*time.Minute {
			p.blockedIPCounter = make(map[string]int)
		}

		p.blockedIPCounter[clientIP] = 1
		p.lastBlockedLog = now
		return
	}

	// Increment counter for this IP
	p.blockedIPCounter[clientIP]++

	// Only log summaries every 5 minutes by default
	if now.Sub(p.lastBlockedLog) >= 5*time.Minute {
		// Log summary of blocked requests
		totalBlocked := 0
		for _, count := range p.blockedIPCounter {
			totalBlocked += count
		}

		log.Info("Blocked IP summary",
			"unique_ips", len(p.blockedIPCounter),
			"total_requests", totalBlocked,
			"since", p.lastBlockedLog.Format(time.RFC3339))

		// Reset counters
		p.blockedIPCounter = make(map[string]int)
		p.lastBlockedLog = now
	}

	// Debug logging only if needed
	if false {
		log.Debug("Request marked as blacklisted for logging skip", "ip", clientIP)
	}
}

// generateFallbackCertificates creates a self-signed certificate for use when no certificate is available.
// This prevents initial handshake failures while waiting for Let's Encrypt certificates.
func generateFallbackCertificates(domains []string) (*tls.Certificate, error) {
	// Generate a private key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	// Create a certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Gordon Server Temporary Certificate"},
			CommonName:   domains[0],
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour), // Valid for 24 hours
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              domains,
	}

	// Create the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	// Convert to PEM format
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Convert private key to PKCS8 format
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	// Parse the certificate
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	return &cert, nil
}

// TestAdminConnectionLater tests connections to the admin server after startup
func (p *Proxy) TestAdminConnectionLater() {
	log.Debug("Starting deferred admin connection testing")

	// Wait a moment for the server to fully start
	time.Sleep(2 * time.Second)

	adminDomain := p.app.GetConfig().Http.FullDomain()

	// Get the current route
	p.mu.RLock()
	currentRoute, exists := p.routes[adminDomain]
	p.mu.RUnlock()

	if !exists {
		log.Warn("Admin route not found when testing connections")
		return
	}

	log.Debug("Testing admin connections after server startup",
		"domain", adminDomain,
		"current_ip", currentRoute.ContainerIP)

	// Try to detect the optimal Gordon admin host by testing connections
	testedIP := p.testAdminConnection(currentRoute.ContainerIP, p.app.GetConfig().Http.Port)
	if testedIP != "" && testedIP != currentRoute.ContainerIP {
		log.Info("Auto-detected working connection to Gordon admin",
			"host", testedIP,
			"original", currentRoute.ContainerIP)

		// Update the route with the working IP
		p.mu.Lock()
		if route, exists := p.routes[adminDomain]; exists {
			route.ContainerIP = testedIP
			log.Info("Updated admin route with working connection",
				"domain", adminDomain,
				"target", fmt.Sprintf("http://%s:%s", testedIP, p.app.GetConfig().Http.Port))
		}
		p.mu.Unlock()
	}

	// Now that we have a working connection, check if ports 80 and 443 are accessible
	p.checkExternalPortAccess()
}

// Helper function to check if ports 80 and 443 are accessible
func (p *Proxy) checkExternalPortAccess() {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	if adminDomain == "" {
		return
	}

	// Try to connect to our own HTTP server on port 80
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Don't follow redirects for this test
			return http.ErrUseLastResponse
		},
	}

	// Testing port 80 (HTTP)
	httpURL := fmt.Sprintf("http://%s/.well-known/acme-challenge/test-token", adminDomain)
	_, httpErr := client.Get(httpURL)

	if httpErr != nil {
		log.Warn("External HTTP port 80 might not be accessible",
			"domain", adminDomain,
			"error", httpErr.Error(),
			"solution", "Ensure port 80 is open in your firewall and not blocked by ISP")
	} else {
		log.Info("External HTTP port 80 is accessible - good for Let's Encrypt validation",
			"domain", adminDomain)
	}

	// Testing port 443 (HTTPS)
	// Use a custom transport with InsecureSkipVerify since we might have a self-signed cert
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	httpsClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	httpsURL := fmt.Sprintf("https://%s/", adminDomain)
	_, httpsErr := httpsClient.Get(httpsURL)

	if httpsErr != nil {
		log.Warn("External HTTPS port 443 might not be accessible",
			"domain", adminDomain,
			"error", httpsErr.Error(),
			"solution", "Ensure port 443 is open in your firewall and not blocked by ISP")
	} else {
		log.Info("External HTTPS port 443 is accessible",
			"domain", adminDomain)
	}
}
