package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/logger"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// setupCertManager configures the autocert manager for Let's Encrypt
func (p *Proxy) setupCertManager() {
	// Create a cache directory if it doesn't exist
	dir := p.config.CertDir
	if dir == "" {
		dir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.Warn("Failed to create certificate cache directory",
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
		logger.Debug("Using Let's Encrypt staging environment",
			"url", "https://acme-staging-v02.api.letsencrypt.org/directory")
	} else {
		// Explicitly set production URL when not in staging mode
		certManager.Client = &acme.Client{
			DirectoryURL: acme.LetsEncryptURL, // "https://acme-v02.api.letsencrypt.org/directory"
		}
		logger.Debug("Using Let's Encrypt production environment",
			"url", acme.LetsEncryptURL)
	}

	// Set HostPolicy to allow the admin domain
	adminDomain := p.app.GetConfig().Http.FullDomain()
	rootDomain := p.app.GetConfig().Http.Domain

	// Use a stricter hostpolicy that only allows domains in our routes
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

		// Log the attempt and reject it
		logger.Warn("Rejecting certificate request for unknown host",
			"host", host,
			"adminDomain", adminDomain,
			"allowed", "no")

		// Return an error for domains not in our routes
		return fmt.Errorf("host %q not configured in routes", host)
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
		logger.Warn("Failed to generate fallback certificate", "error", err)
	} else {
		logger.Info("Generated fallback self-signed certificate for admin domain",
			"domain", adminDomain,
			"valid_until", time.Now().Add(24*time.Hour).Format("2006-01-02 15:04:05"))
		// Store the fallback certificate
		p.fallbackCert = fallbackCert
	}

	p.certManager = certManager
	logger.Debug("Certificate manager setup completed",
		"directory", dir,
		"mode", p.config.LetsEncryptMode)

	// Only request admin certificate during server startup, not during specific domain processing
	if !p.processingSpecificDomain {
		// Request the certificate for the admin domain
		go p.requestAdminCertificate()
	}
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
		logger.Debug("Certificate not found in cache",
			"domain", domain,
			"error", err)
		return false
	}

	// Validate the certificate data
	validCert, expiresInDays := isCertificateValid(certData, domain)
	if !validCert {
		return false
	}

	logger.Info("Valid certificate found in cache",
		"domain", domain,
		"expires_in", expiresInDays,
		"days")
	return true
}

// isCertificateValid checks if a certificate PEM data is valid and not expired
// Returns whether it's valid and how many days until expiration
func isCertificateValid(certData []byte, domain string) (bool, float64) {
	// Parse the certificate to check its validity
	block, _ := pem.Decode(certData)
	if block == nil || block.Type != "CERTIFICATE" {
		logger.Warn("Invalid certificate data",
			"domain", domain)
		return false, 0
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		logger.Warn("Failed to parse certificate",
			"domain", domain,
			"error", err)
		return false, 0
	}

	// Check if the certificate is still valid
	now := time.Now()
	if now.After(cert.NotAfter) || now.Before(cert.NotBefore) {
		logger.Info("Certificate has expired or is not yet valid",
			"domain", domain,
			"not_before", cert.NotBefore,
			"not_after", cert.NotAfter)
		return false, 0
	}

	// Calculate days until expiration
	expiresInDays := cert.NotAfter.Sub(now).Hours() / 24

	// Check if the certificate is about to expire (within 30 days)
	if expiresInDays < 30 {
		logger.Info("Certificate is valid but will expire soon",
			"domain", domain,
			"expires_in", expiresInDays,
			"days")
		// Return false to trigger renewal if it's about to expire
		return false, expiresInDays
	}

	return true, expiresInDays
}

// checkCertificateInFilesystem checks if valid certificate files exist directly in the certs directory
// This is useful when the database record might be missing but valid certificate files exist
func (p *Proxy) checkCertificateInFilesystem(domain string) bool {
	if p.certManager == nil {
		return false
	}

	// Get the certificate directory path
	certDir := p.config.CertDir
	if certDir == "" {
		certDir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// Check for the certificate file and its corresponding private key
	certFile := filepath.Join(certDir, domain)
	keyFile := filepath.Join(certDir, domain+"+rsa")

	// Check if both files exist
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		logger.Debug("Certificate file not found in filesystem",
			"domain", domain,
			"file", certFile)
		return false
	}

	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		logger.Debug("Certificate key file not found in filesystem",
			"domain", domain,
			"file", keyFile)
		return false
	}

	// If we found both files, validate the certificate
	certData, err := os.ReadFile(certFile)
	if err != nil {
		logger.Warn("Failed to read certificate file",
			"domain", domain,
			"file", certFile,
			"error", err)
		return false
	}

	// Validate the certificate data
	validCert, expiresInDays := isCertificateValid(certData, domain)
	if !validCert {
		return false
	}

	logger.Info("Valid certificate found in filesystem",
		"domain", domain,
		"expires_in", expiresInDays,
		"days",
		"not_in_cache", true)

	// Log some debug information about the certificate files
	certFileInfo, _ := os.Stat(certFile)
	keyFileInfo, _ := os.Stat(keyFile)
	logger.Debug("Certificate file details",
		"domain", domain,
		"cert_file", certFile,
		"cert_size", certFileInfo.Size(),
		"cert_modified", certFileInfo.ModTime(),
		"key_file", keyFile,
		"key_size", keyFileInfo.Size(),
		"key_modified", keyFileInfo.ModTime())

	return true
}

// testCertificateWithTLSConnection attempts to establish a TLS connection to verify the certificate
// This is an optional verification step for certificates we find on the filesystem
func testCertificateWithTLSConnection(domain string, port string) bool {
	if port == "" {
		port = "443" // Default HTTPS port
	}

	// Create a connection with a short timeout
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
	}

	// Connect to the server
	logger.Debug("Testing certificate by establishing TLS connection",
		"domain", domain,
		"address", domain+":"+port)

	conn, err := tls.DialWithDialer(dialer, "tcp", domain+":"+port, &tls.Config{
		// Skip verification since we're testing if the connection works
		// not validating the cert chain (which might use custom CA)
		InsecureSkipVerify: true,
		ServerName:         domain,
	})

	if err != nil {
		logger.Debug("TLS connection test failed",
			"domain", domain,
			"error", err)
		return false
	}

	// Close the connection when done
	defer conn.Close()

	// Verify the connection state
	state := conn.ConnectionState()

	// Check if the certificate is valid
	for _, cert := range state.PeerCertificates {
		// Verify the certificate is valid for the domain
		if err := cert.VerifyHostname(domain); err != nil {
			logger.Debug("Certificate hostname verification failed",
				"domain", domain,
				"cert_domains", cert.DNSNames,
				"error", err)
			continue
		}

		// Check certificate expiration
		now := time.Now()
		if now.After(cert.NotAfter) || now.Before(cert.NotBefore) {
			logger.Debug("Certificate from TLS connection is not valid",
				"domain", domain,
				"not_before", cert.NotBefore,
				"not_after", cert.NotAfter)
			continue
		}

		// We found a valid certificate
		expiresInDays := cert.NotAfter.Sub(now).Hours() / 24
		logger.Info("Successfully verified certificate via TLS connection",
			"domain", domain,
			"expires_in", expiresInDays,
			"days",
			"issuer", cert.Issuer.CommonName)
		return true
	}

	logger.Debug("No valid certificate found from TLS connection",
		"domain", domain)
	return false
}

// checkCertificateExists is a helper function that checks if a valid certificate exists
// either in the cache or the filesystem. Returns true if a valid certificate is found.
func (p *Proxy) checkCertificateExists(domain string) bool {
	// First check in the cache
	if p.checkCertificateInCache(domain) {
		logger.Debug("Certificate exists in cache", "domain", domain)
		return true
	}

	// If not in cache, check the filesystem
	if p.checkCertificateInFilesystem(domain) {
		logger.Debug("Certificate exists in filesystem but not in cache", "domain", domain)
		return true
	}

	// No valid certificate found
	logger.Debug("No valid certificate found for domain", "domain", domain)
	return false
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
		logger.Debug("HTTPS is disabled, skipping admin certificate request")
		return
	}

	// Extract hostname from admin domain, resolving it to see if it's publicly accessible
	host := adminDomain
	ips, err := net.LookupIP(host)
	if err != nil {
		logger.Error("Could not resolve admin domain, Let's Encrypt will likely fail",
			"domain", adminDomain,
			"error", err.Error(),
			"solution", "Check DNS settings and ensure domain points to this server")
		return
	}

	var ipStrings []string
	for _, ip := range ips {
		ipStrings = append(ipStrings, ip.String())
	}

	logger.Info("Successfully resolved admin domain",
		"domain", adminDomain,
		"ips", ipStrings)

	// Check if we already have a valid certificate (cache or filesystem)
	if p.checkCertificateExists(adminDomain) {
		logger.Info("Using existing certificate",
			"domain", adminDomain,
			"action", "skipping Let's Encrypt request to avoid rate limits")

		// If a valid certificate was found in the filesystem but not in the cache,
		// try to verify it with a TLS connection (non-blocking)
		go func() {
			// Only attempt TLS verification if certificate was found in filesystem
			// and not in cache (to avoid unnecessary check for cached certificates)
			if !p.checkCertificateInCache(adminDomain) && p.checkCertificateInFilesystem(adminDomain) {
				if testCertificateWithTLSConnection(adminDomain, p.config.Port) {
					logger.Debug("Certificate verification succeeded via TLS connection",
						"domain", adminDomain,
						"note", "Certificate is trusted and working properly")
				} else {
					logger.Warn("Certificate verification via TLS connection failed",
						"domain", adminDomain,
						"note", "Certificate exists but might not be trusted or properly configured")
				}
			}
		}()

		return
	}

	// Check if environment is production
	mode := "staging"
	email := p.config.Email

	if !p.app.IsDevEnvironment() && email != "" {
		mode = "production"
	}

	// Log the certificate request intent
	logger.Info("Initiating Let's Encrypt certificate request for admin domain",
		"domain", adminDomain,
		"email", email,
		"mode", mode)

	logger.Info("⏳ Waiting for Let's Encrypt to validate domain ownership",
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
		logger.Error("Let's Encrypt certificate request timed out",
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

			logger.Error("Let's Encrypt rate limit reached",
				"domain", adminDomain,
				"error", certErr,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// Don't retry on rate limits
			logger.Info("Skipping certificate request retries due to rate limiting",
				"domain", adminDomain)
		} else {
			logger.Error("Failed to obtain Let's Encrypt certificate",
				"domain", adminDomain,
				"error", certErr)

			logger.Info("⏳ Retrying certificate request with exponential backoff",
				"domain", adminDomain,
				"timeout", "2 minutes")
			// Implement retry logic with backoff
			go p.retryCertificateRequest(adminDomain, 3, 10*time.Second)
		}
	} else if certResult != nil {
		logger.Info("Successfully obtained Let's Encrypt certificate",
			"domain", adminDomain)
	} else {
		logger.Error("Unexpected error: received nil certificate but no error",
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
		logger.Warn("Empty domain provided to requestDomainCertificate")
		return
	}

	// Extract hostname from domain, resolving it to see if it's publicly accessible
	ips, err := net.LookupIP(domain)
	if err != nil {
		logger.Error("Could not resolve domain, Let's Encrypt will likely fail",
			"domain", domain,
			"error", err.Error(),
			"solution", "Check DNS settings and ensure domain points to this server")
		return
	}

	var ipStrings []string
	for _, ip := range ips {
		ipStrings = append(ipStrings, ip.String())
	}

	logger.Info("Successfully resolved domain for certificate request",
		"domain", domain,
		"ips", ipStrings)

	// Check if we already have a valid certificate (cache or filesystem)
	if p.checkCertificateExists(domain) {
		logger.Info("Using existing certificate",
			"domain", domain,
			"action", "skipping Let's Encrypt request to avoid rate limits")

		// If a valid certificate was found in the filesystem but not in the cache,
		// try to verify it with a TLS connection (non-blocking)
		go func() {
			// Only attempt TLS verification if certificate was found in filesystem
			// and not in cache (to avoid unnecessary check for cached certificates)
			if !p.checkCertificateInCache(domain) && p.checkCertificateInFilesystem(domain) {
				if testCertificateWithTLSConnection(domain, p.config.Port) {
					logger.Debug("Certificate verification succeeded via TLS connection",
						"domain", domain,
						"note", "Certificate is trusted and working properly")
				} else {
					logger.Warn("Certificate verification via TLS connection failed",
						"domain", domain,
						"note", "Certificate exists but might not be trusted or properly configured")
				}
			}
		}()

		return
	}

	// Check if environment is production
	mode := "staging"
	email := p.config.Email

	if !p.app.IsDevEnvironment() && email != "" {
		mode = "production"
	}

	// Log the certificate request intent
	logger.Info("Initiating Let's Encrypt certificate request for container domain",
		"domain", domain,
		"email", email,
		"mode", mode)

	logger.Info("⏳ Waiting for Let's Encrypt to validate domain ownership",
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
		logger.Error("Let's Encrypt certificate request timed out",
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

			logger.Error("Let's Encrypt rate limit reached",
				"domain", domain,
				"error", certErr,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// Don't retry on rate limits
			logger.Info("Skipping certificate request retries due to rate limiting",
				"domain", domain)
		} else {
			logger.Error("Failed to obtain Let's Encrypt certificate",
				"domain", domain,
				"error", certErr)

			logger.Info("⏳ Retrying certificate request with exponential backoff",
				"domain", domain,
				"timeout", "2 minutes")
			// Implement retry logic with backoff
			go p.retryCertificateRequest(domain, 3, 10*time.Second)
		}
	} else if certResult != nil {
		logger.Info("Successfully obtained Let's Encrypt certificate",
			"domain", domain)
	} else {
		logger.Error("Unexpected error: received nil certificate but no error",
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
		logger.Info("Retrying Let's Encrypt certificate request",
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
				logger.Warn("HTTP challenge endpoint might not be accessible",
					"domain", domain,
					"url", testURL,
					"error", err,
					"solution", "Ensure port 80 is accessible and not blocked by firewall")
			} else {
				resp.Body.Close()
				logger.Info("HTTP challenge endpoint is accessible",
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
			logger.Info("Successfully obtained Let's Encrypt certificate on retry",
				"domain", domain,
				"attempt", attempt)
			return
		}

		logger.Error("Let's Encrypt certificate request retry failed",
			"domain", domain,
			"attempt", attempt,
			"error", err)

		// Provide more detailed diagnostics based on the error
		if strings.Contains(strings.ToLower(err.Error()), "connection refused") ||
			strings.Contains(strings.ToLower(err.Error()), "timeout") {
			logger.Error("Let's Encrypt connection failed - this typically indicates:",
				"issue_1", "Port 80 is not accessible from the internet",
				"issue_2", "Firewall is blocking inbound connections",
				"issue_3", "DNS records not properly propagated",
				"solution", "Check firewall settings and DNS configuration")
		} else if strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
			logger.Error("Let's Encrypt authorization failed - this typically indicates:",
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

			logger.Error("Let's Encrypt rate limit reached - cannot issue more certificates for this domain yet",
				"domain", domain,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// No point in retrying on rate limit errors
			return
		}

		// Increase backoff for next attempt (exponential backoff)
		backoff *= 2
	}

	logger.Error("All Let's Encrypt certificate request retries failed",
		"domain", domain,
		"max_retries", maxRetries,
		"fallback", "Using self-signed certificate")

	// At this point, all retries have failed, so we'll rely on the fallback self-signed certificate
	// But let's log this prominently for debugging
	logger.Error("⚠️ HTTPS is using a self-signed certificate which browsers will warn about",
		"domain", domain,
		"reason", "Let's Encrypt certificate issuance failed",
		"solution", "Check network settings and Let's Encrypt status")

	// Suggest checking the common issues
	logger.Error("Common Let's Encrypt issues to check:",
		"check_1", "Ensure ports 80 and 443 are open on your firewall",
		"check_2", "Verify DNS records point to your server IP",
		"check_3", "Make sure no other services are running on ports 80/443",
		"check_4", "Check if Let's Encrypt service is having issues (https://letsencrypt.status.io/)")
}

// generateFallbackCertificates generates a self-signed fallback certificate
func generateFallbackCertificates(domains []string) (*tls.Certificate, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("no domains provided for fallback certificate")
	}

	// Create a new private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create a template for the certificate
	notBefore := time.Now()
	notAfter := notBefore.Add(24 * time.Hour) // Valid for 24 hours

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Gordon Self-Signed Certificate"},
			CommonName:   domains[0], // Use the first domain as the common name
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Add all domains as SANs
		DNSNames: domains,
	}

	// Add IP addresses to the certificate if domains contain IP addresses
	for _, domain := range domains {
		ip := net.ParseIP(domain)
		if ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		}
	}

	// Self-sign the certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Encode certificate to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if certPEM == nil {
		return nil, fmt.Errorf("failed to encode certificate to PEM")
	}

	// Encode private key to PEM
	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}

	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyBytes})
	if privateKeyPEM == nil {
		return nil, fmt.Errorf("failed to encode private key to PEM")
	}

	// Load the certificate
	cert, err := tls.X509KeyPair(certPEM, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificate: %w", err)
	}

	return &cert, nil
}
