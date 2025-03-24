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

// Certificate naming patterns
const (
	SelfSignedCertPattern     = "%s_self-signed"
	LetsEncryptStagingPattern = "%s_letsencrypt-staging"
	LetsEncryptProdPattern    = "%s_letsencrypt-production"
)

// detectUpstreamProxy checks for signs of an upstream TLS-terminating proxy
func (p *Proxy) detectUpstreamProxy() bool {
	// Check if detection is explicitly disabled
	if !p.config.DetectUpstreamProxy {
		return false
	}

	// First check our cached detection status - if we already detected a proxy previously
	p.upstreamProxyMu.RLock()
	alreadyDetected := p.upstreamProxyDetected
	p.upstreamProxyMu.RUnlock()

	if alreadyDetected {
		return true
	}

	// Check for known HTTP headers that indicate a TLS termination proxy
	// This is a best-effort approach since we're analyzing HTTP headers from
	// previous requests stored in our context (if available)

	// Look for common proxy headers in our recent requests
	// This is a static check at startup; ideally we would check incoming
	// requests dynamically, but for this implementation we're using
	// known proxy environment indicators

	// Most common proxy headers: X-Forwarded-Proto, X-Forwarded-SSL, X-Forwarded-For
	// Environment variables: HTTPS=on, HTTP_X_FORWARDED_PROTO=https

	// Check for common environment variables set by upstream proxies
	if os.Getenv("HTTPS") == "on" ||
		os.Getenv("HTTP_X_FORWARDED_PROTO") == "https" ||
		os.Getenv("HTTP_X_FORWARDED_SSL") == "on" {
		logger.Info("Upstream TLS-terminating proxy detected via environment variables",
			"detection_method", "environment_variables",
			"https", os.Getenv("HTTPS"),
			"x_forwarded_proto", os.Getenv("HTTP_X_FORWARDED_PROTO"),
			"x_forwarded_ssl", os.Getenv("HTTP_X_FORWARDED_SSL"))

		// Cache this result
		p.upstreamProxyMu.Lock()
		p.upstreamProxyDetected = true
		p.upstreamProxyMu.Unlock()

		return true
	}

	// Check if Cloudflare headers are present in environment
	// Cloudflare sets environment variables like HTTP_CF_VISITOR={"scheme":"https"}
	if cfVisitor := os.Getenv("HTTP_CF_VISITOR"); cfVisitor != "" {
		if strings.Contains(cfVisitor, "\"scheme\":\"https\"") {
			logger.Info("Cloudflare proxy detected via environment variables",
				"detection_method", "cloudflare_headers",
				"cf_visitor", cfVisitor)

			// Cache this result
			p.upstreamProxyMu.Lock()
			p.upstreamProxyDetected = true
			p.upstreamProxyMu.Unlock()

			return true
		}
	}

	// Not detected, but log if explicit configuration is set
	if p.config.SkipCertificates {
		logger.Info("No upstream proxy detected automatically, but certificate acquisition will be skipped due to configuration",
			"detection_result", "false",
			"skip_certificates", "true (forced via config)")
	}

	return false
}

// setupCertManager configures the autocert manager for Let's Encrypt
func (p *Proxy) setupCertManager() {
	// First check for upstream TLS-terminating proxies if detection is enabled
	isProxied := p.detectUpstreamProxy()

	// Get admin domain and root domain
	adminDomain := p.app.GetConfig().Http.FullDomain()
	rootDomain := p.app.GetConfig().Http.Domain

	// Generate a fallback certificate for admin domain regardless of proxy status
	var fallbackDomains []string
	fallbackDomains = append(fallbackDomains, adminDomain)

	// Include root domain in fallback certificate if it's different from admin domain
	if rootDomain != "" && rootDomain != adminDomain {
		fallbackDomains = append(fallbackDomains, rootDomain)
	}

	// Generate the fallback certificate
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

	// Skip certificate acquisition if we're behind a TLS-terminating proxy
	// and the skip certificates option is enabled
	if isProxied && p.config.SkipCertificates {
		logger.Info("Upstream TLS-terminating proxy detected, skipping Let's Encrypt certificate acquisition",
			"upstream_proxy", "detected",
			"certificate_acquisition", "disabled")

		// We will still initialize the certificate manager but will use it in a more limited way
		logger.Info("Initializing minimal Let's Encrypt certificate manager for fallback purposes")
	}

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

	p.certManager = certManager
	logger.Debug("Certificate manager setup completed",
		"directory", dir,
		"mode", p.config.LetsEncryptMode)

	// Preload certificates from disk into cache to avoid unnecessary Let's Encrypt requests
	p.preloadCertificates()

	// Only request admin certificate during server startup, not during specific domain processing
	if !p.processingSpecificDomain {
		// Request the certificate for the admin domain
		go p.requestAdminCertificate()
	}
}

// checkCertificateInCache checks if a valid certificate for the given domain exists in the cache
// Returns true if a valid certificate exists, false otherwise
func (p *Proxy) checkCertificateInCache(domain string) (bool, string) {
	if p.certManager == nil || p.certManager.Cache == nil {
		return false, ""
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
		return false, ""
	}

	// Validate the certificate data
	validCert, expiresInDays, certType, _ := isCertificateValid(certData, domain)
	if !validCert {
		return false, certType
	}

	logger.Info("Valid certificate found in cache",
		"domain", domain,
		"expires_in", expiresInDays,
		"days",
		"type", certType)

	// Check if certificate matches current mode
	if p.config.LetsEncryptMode == "production" && certType == "staging" {
		logger.Info("Found staging certificate but production mode is enabled",
			"domain", domain,
			"action", "will request new production certificate")
		return false, certType
	} else if p.config.LetsEncryptMode == "staging" && certType == "production" {
		// We're in staging mode, but we already have a production certificate
		// That's fine - keep using the production certificate
		logger.Info("Found production certificate while in staging mode",
			"domain", domain,
			"action", "using existing production certificate")
	}

	return true, certType
}

// getCertificateFileName returns the appropriate filename based on certificate type
func getCertificateFileName(domain string, certType string) string {
	switch certType {
	case "self-signed":
		return fmt.Sprintf(SelfSignedCertPattern, domain)
	case "staging":
		return fmt.Sprintf(LetsEncryptStagingPattern, domain)
	case "production":
		return fmt.Sprintf(LetsEncryptProdPattern, domain)
	default:
		// Fallback to just the domain name for backward compatibility
		return domain
	}
}

// getCertificateType determines the type of certificate by examining its contents
func getCertificateType(cert *x509.Certificate) string {
	// Check issuer to determine the certificate type
	issuer := cert.Issuer.CommonName

	// Check for self-signed certificate
	if cert.Issuer.CommonName == cert.Subject.CommonName {
		return "self-signed"
	}

	// Check for Let's Encrypt staging certificate
	if strings.Contains(issuer, "STAGING") ||
		strings.Contains(issuer, "Fake LE") ||
		strings.Contains(issuer, "Counterfeit") ||
		strings.Contains(issuer, "False Fennel") {
		return "staging"
	}

	// Check for Let's Encrypt production certificate
	if strings.Contains(issuer, "Let's Encrypt") ||
		strings.Contains(issuer, "R3") ||
		strings.Contains(issuer, "E1") {
		return "production"
	}

	// Unknown issuer
	return "unknown"
}

// isCertificateValid checks if a certificate PEM data is valid and not expired
// Returns whether it's valid and how many days until expiration, and the certificate type
func isCertificateValid(certData []byte, domain string) (bool, float64, string, *x509.Certificate) {
	// Parse the certificate to check its validity
	block, rest := pem.Decode(certData)
	if block == nil || block.Type != "CERTIFICATE" {
		logger.Warn("Invalid certificate data - not a valid PEM CERTIFICATE block",
			"domain", domain,
			"dataLength", len(certData))

		// Debug: output first few bytes to help diagnose format issues
		if len(certData) > 0 {
			previewLen := 50
			if len(certData) < previewLen {
				previewLen = len(certData)
			}
			preview := string(certData[:previewLen])
			logger.Debug("Certificate data preview",
				"domain", domain,
				"preview", preview+"...")
		}
		return false, 0, "unknown", nil
	}

	// Check if there are additional certificates in the chain
	var certChain []*x509.Certificate
	currentRest := rest

	for len(currentRest) > 0 {
		var nextBlock *pem.Block
		nextBlock, currentRest = pem.Decode(currentRest)
		if nextBlock == nil || nextBlock.Type != "CERTIFICATE" {
			break
		}
		cert, err := x509.ParseCertificate(nextBlock.Bytes)
		if err != nil {
			logger.Debug("Failed to parse certificate in chain",
				"domain", domain,
				"error", err)
			continue
		}
		certChain = append(certChain, cert)
	}

	if len(certChain) > 0 {
		logger.Debug("Found certificate chain with additional certificates",
			"domain", domain,
			"chain_length", len(certChain))
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		logger.Warn("Failed to parse certificate",
			"domain", domain,
			"error", err)
		return false, 0, "unknown", nil
	}

	// Verify the certificate includes the domain we're checking for
	var domainFound bool
	if len(cert.DNSNames) > 0 {
		logger.Debug("Certificate DNS names",
			"domain", domain,
			"certificate_domains", cert.DNSNames)

		for _, dnsName := range cert.DNSNames {
			if dnsName == domain {
				domainFound = true
				break
			}
		}
	}

	if !domainFound && cert.Subject.CommonName != domain {
		logger.Warn("Certificate doesn't match the required domain",
			"domain", domain,
			"cert_common_name", cert.Subject.CommonName,
			"cert_dns_names", cert.DNSNames)
		return false, 0, "unknown", cert
	}

	// Determine the certificate type
	certType := getCertificateType(cert)
	logger.Debug("Determined certificate type",
		"domain", domain,
		"type", certType,
		"issuer", cert.Issuer.CommonName)

	// Check if the certificate is still valid
	now := time.Now()
	if now.After(cert.NotAfter) || now.Before(cert.NotBefore) {
		logger.Info("Certificate has expired or is not yet valid",
			"domain", domain,
			"not_before", cert.NotBefore,
			"not_after", cert.NotAfter,
			"type", certType)
		return false, 0, certType, cert
	}

	// Calculate days until expiration
	expiresInDays := cert.NotAfter.Sub(now).Hours() / 24

	// Check if the certificate is about to expire (within 30 days)
	if expiresInDays < 30 {
		logger.Info("Certificate is valid but will expire soon",
			"domain", domain,
			"expires_in", expiresInDays,
			"days",
			"type", certType)
		// Return false to trigger renewal if it's about to expire
		return false, expiresInDays, certType, cert
	}

	return true, expiresInDays, certType, cert
}

// checkCertificateInFilesystem checks if valid certificate files exist directly in the certs directory
// This is useful when the database record might be missing but valid certificate files exist
func (p *Proxy) checkCertificateInFilesystem(domain string) (bool, string) {
	if p.certManager == nil {
		return false, ""
	}

	// Get the certificate directory path
	certDir := p.config.CertDir
	if certDir == "" {
		certDir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// First check for known certificate patterns
	certFiles := []string{
		// Standard autocert naming
		domain,
		// Our naming patterns
		getCertificateFileName(domain, "self-signed"),
		getCertificateFileName(domain, "staging"),
		getCertificateFileName(domain, "production"),
	}

	// Check each possible certificate file using known patterns
	for _, baseName := range certFiles {
		certFile := filepath.Join(certDir, baseName)
		keyFile := filepath.Join(certDir, baseName+"+rsa")

		// Skip if either file doesn't exist
		if _, err := os.Stat(certFile); os.IsNotExist(err) {
			continue
		}

		if _, err := os.Stat(keyFile); os.IsNotExist(err) {
			continue
		}

		// If we found both files, validate the certificate
		certData, err := os.ReadFile(certFile)
		if err != nil {
			logger.Warn("Failed to read certificate file",
				"domain", domain,
				"file", certFile,
				"error", err)
			continue
		}

		// Validate the certificate data
		validCert, expiresInDays, certType, _ := isCertificateValid(certData, domain)
		if !validCert {
			continue
		}

		logger.Info("Valid certificate found in filesystem with standard naming pattern",
			"domain", domain,
			"expires_in", expiresInDays,
			"days",
			"type", certType,
			"cert_file", certFile)

		// Check if certificate matches current mode
		if p.config.LetsEncryptMode == "production" && certType == "staging" {
			logger.Info("Found staging certificate but production mode is enabled",
				"domain", domain,
				"action", "will request new production certificate")
			// Delete the staging certificate
			deleteCertificateFiles(certDir, baseName)
			return false, certType
		} else if p.config.LetsEncryptMode == "staging" && certType == "production" {
			// We're in staging mode, but we already have a production certificate
			// That's fine - keep using the production certificate
			logger.Info("Found production certificate while in staging mode",
				"domain", domain,
				"action", "using existing production certificate")
		}

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

		// Attempt to add to autocert cache if it's not already there
		if certType == "production" || (certType == "staging" && p.config.LetsEncryptMode == "staging") {
			// Add to autocert cache for future use
			p.addCertificateToCache(domain, certFile, keyFile)
		}

		return true, certType
	}

	// If no matches with standard patterns, try a glob search for any files starting with the domain name
	// This will catch files with any suffix like domain_letsencrypt-production
	// and domain_letsencrypt-staging
	logger.Debug("No standard certificate files found, searching for any matching certificates",
		"domain", domain)

	files, err := filepath.Glob(filepath.Join(certDir, domain+"*"))
	if err != nil {
		logger.Warn("Error searching for certificate files",
			"domain", domain,
			"error", err)
		return false, ""
	}

	// Extract base names without the path
	var candidates []string
	for _, file := range files {
		// Skip key files ending with +rsa since we'll process them together with certs
		if strings.HasSuffix(file, "+rsa") {
			continue
		}

		baseFile := filepath.Base(file)
		// Check if the corresponding key file exists
		keyFile := file + "+rsa"
		if _, err := os.Stat(keyFile); os.IsNotExist(err) {
			continue
		}

		// Add this candidate
		candidates = append(candidates, baseFile)
	}

	// No certificate files found
	if len(candidates) == 0 {
		logger.Debug("No certificate files found in filesystem glob search",
			"domain", domain)
		return false, ""
	}

	logger.Debug("Found certificate candidates in glob search",
		"domain", domain,
		"candidates", candidates)

	// Process each candidate (if there are multiple matches)
	for _, baseName := range candidates {
		certFile := filepath.Join(certDir, baseName)
		keyFile := filepath.Join(certDir, baseName+"+rsa")

		// Read and validate the certificate
		certData, err := os.ReadFile(certFile)
		if err != nil {
			logger.Warn("Failed to read certificate file from glob search",
				"domain", domain,
				"file", certFile,
				"error", err)
			continue
		}

		// Validate the certificate data
		validCert, expiresInDays, certType, _ := isCertificateValid(certData, domain)
		if !validCert {
			continue
		}

		logger.Info("Valid certificate found in filesystem with non-standard naming",
			"domain", domain,
			"expires_in", expiresInDays,
			"days",
			"type", certType,
			"cert_file", certFile)

		// If we're in production mode and this is a production certificate, use it
		if p.config.LetsEncryptMode == "production" && certType == "production" {
			// Attempt to add to autocert cache
			p.addCertificateToCache(domain, certFile, keyFile)
			return true, certType
		}

		// If we're in staging mode, prefer production certificates if available
		if p.config.LetsEncryptMode == "staging" {
			if certType == "production" {
				// Attempt to add to autocert cache
				p.addCertificateToCache(domain, certFile, keyFile)
				return true, certType
			} else if certType == "staging" {
				// If this is a staging certificate and we're in staging mode, use it
				// Attempt to add to autocert cache
				p.addCertificateToCache(domain, certFile, keyFile)
				return true, certType
			}
		}

		// If this is a staging certificate but we're in production mode, ignore it
		// and continue checking other candidates
		if p.config.LetsEncryptMode == "production" && certType == "staging" {
			logger.Info("Ignoring staging certificate in production mode",
				"domain", domain,
				"cert_file", certFile)
			continue
		}

		// Fallback: use the certificate if it's valid
		// Attempt to add to autocert cache
		p.addCertificateToCache(domain, certFile, keyFile)
		return true, certType
	}

	logger.Debug("No valid certificate files found in filesystem",
		"domain", domain)
	return false, ""
}

// deleteCertificateFiles removes certificate files from the filesystem
func deleteCertificateFiles(certDir string, baseName string) {
	certFile := filepath.Join(certDir, baseName)
	keyFile := filepath.Join(certDir, baseName+"+rsa")

	// Delete certificate file
	if err := os.Remove(certFile); err != nil && !os.IsNotExist(err) {
		logger.Warn("Failed to delete certificate file",
			"file", certFile,
			"error", err)
	} else if !os.IsNotExist(err) {
		logger.Info("Deleted certificate file", "file", certFile)
	}

	// Delete key file
	if err := os.Remove(keyFile); err != nil && !os.IsNotExist(err) {
		logger.Warn("Failed to delete certificate key file",
			"file", keyFile,
			"error", err)
	} else if !os.IsNotExist(err) {
		logger.Info("Deleted certificate key file", "file", keyFile)
	}
}

// addCertificateToCache adds a certificate found in the filesystem to the autocert cache
// This ensures certificates found on disk are also available for the autocert manager
func (p *Proxy) addCertificateToCache(domain string, certFile, keyFile string) {
	// Skip if we don't have a certificate manager
	if p.certManager == nil || p.certManager.Cache == nil {
		logger.Warn("Cannot add certificate to cache: certificate manager not initialized",
			"domain", domain)
		return
	}

	// Create context for cache operations
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read the certificate and key files
	certData, err := os.ReadFile(certFile)
	if err != nil {
		logger.Warn("Failed to read certificate file for cache addition",
			"domain", domain,
			"file", certFile,
			"error", err)
		return
	}

	keyData, err := os.ReadFile(keyFile)
	if err != nil {
		logger.Warn("Failed to read certificate key file for cache addition",
			"domain", domain,
			"file", keyFile,
			"error", err)
		return
	}

	// Format of cache key used by autocert is "cert-" + domain
	cacheKey := "cert-" + domain

	// Check if certificate is already in the cache
	_, getCacheErr := p.certManager.Cache.Get(ctx, cacheKey)
	if getCacheErr == nil {
		// Certificate already exists in cache, no need to add
		logger.Debug("Certificate already exists in autocert cache, skipping addition",
			"domain", domain)
		return
	}

	// Add certificate to cache
	err = p.certManager.Cache.Put(ctx, cacheKey, certData)
	if err != nil {
		logger.Warn("Failed to add certificate to autocert cache",
			"domain", domain,
			"error", err)
		return
	}

	// Add key to cache with proper key format
	keyKey := "key-" + domain
	err = p.certManager.Cache.Put(ctx, keyKey, keyData)
	if err != nil {
		logger.Warn("Failed to add certificate key to autocert cache",
			"domain", domain,
			"error", err)
		return
	}

	logger.Info("Successfully added certificate to autocert cache",
		"domain", domain,
		"from_cert_file", certFile,
		"from_key_file", keyFile)
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
// either in the cache or the filesystem. Returns true if a valid certificate is found,
// along with the certificate type.
func (p *Proxy) checkCertificateExists(domain string) (bool, string) {
	// First check in the cache
	validInCache, cacheType := p.checkCertificateInCache(domain)
	if validInCache {
		logger.Debug("Certificate exists in cache",
			"domain", domain,
			"type", cacheType)
		return true, cacheType
	}

	// If not in cache, check the filesystem
	validInFS, fsType := p.checkCertificateInFilesystem(domain)
	if validInFS {
		logger.Debug("Certificate exists in filesystem but not in cache",
			"domain", domain,
			"type", fsType)
		return true, fsType
	}

	// No valid certificate found
	logger.Debug("No valid certificate found for domain", "domain", domain)
	return false, ""
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

	// Check for upstream proxy with certificate skipping enabled
	isProxied := p.detectUpstreamProxy()
	if isProxied && p.config.SkipCertificates {
		logger.Info("Upstream TLS-terminating proxy detected, skipping admin certificate request",
			"domain", adminDomain,
			"skip_reason", "behind TLS-terminating proxy")
		return
	}

	// First, check if certificate is already in the autocert cache
	// This is the critical step to avoid unnecessary Let's Encrypt requests
	cacheKey := "cert-" + adminDomain
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if p.certManager != nil && p.certManager.Cache != nil {
		certData, err := p.certManager.Cache.Get(ctx, cacheKey)
		if err == nil && len(certData) > 0 {
			// Check if the cached certificate is valid
			isValid, expiresInDays, certType, _ := isCertificateValid(certData, adminDomain)
			if isValid {
				logger.Info("Using valid certificate from cache",
					"domain", adminDomain,
					"type", certType,
					"expires_in_days", expiresInDays)
				return
			}

			// If certificate is invalid or expiring soon, log but continue
			logger.Info("Cached certificate is invalid or expiring soon",
				"domain", adminDomain,
				"expires_in_days", expiresInDays,
				"valid", isValid)
		}
	}

	// Additional rate limit safety check
	// Check if we've recently hit a rate limit for this domain
	// This is a temporary memory-only safeguard
	rlCacheKey := adminDomain + "_ratelimit_history"
	rateLimitData, _ := p.certManager.Cache.Get(ctx, rlCacheKey)
	if len(rateLimitData) > 0 {
		logger.Warn("Rate limit history found for admin domain, skipping certificate request",
			"domain", adminDomain,
			"data", string(rateLimitData),
			"solution", "Wait for rate limit to expire")
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
	hasValidCert, certType := p.checkCertificateExists(adminDomain)

	// If we have a valid cert that matches our mode, use it
	if hasValidCert {
		// Check for mode mismatch
		if p.config.LetsEncryptMode == "production" && certType == "staging" {
			logger.Info("Found staging certificate but production mode is enabled",
				"domain", adminDomain,
				"action", "requesting new production certificate")
			// Continue to request a new certificate
		} else {
			logger.Info("Using existing certificate",
				"domain", adminDomain,
				"type", certType,
				"action", "skipping Let's Encrypt request to avoid rate limits")

			// If a valid certificate was found in the filesystem but not in the cache,
			// try to verify it with a TLS connection (non-blocking)
			go func() {
				// Only attempt TLS verification if certificate was found in filesystem
				// and not in cache (to avoid unnecessary check for cached certificates)
				validInCache, _ := p.checkCertificateInCache(adminDomain)
				validInFS, _ := p.checkCertificateInFilesystem(adminDomain)
				if !validInCache && validInFS {
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
	}

	// Get the certificate directory path for naming the output files
	certDir := p.config.CertDir
	if certDir == "" {
		certDir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// Ensure certificate directory exists
	if err := os.MkdirAll(certDir, 0755); err != nil {
		logger.Warn("Failed to create certificate directory",
			"dir", certDir,
			"error", err)
	}

	// Check if environment is production
	mode := p.config.LetsEncryptMode
	email := p.config.Email

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
	ctx, cancel = context.WithTimeout(context.Background(), 1*time.Minute)
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
				retryAfterStr = strings.TrimSpace(matches[1])
				if idx := strings.Index(retryAfterStr, ":"); idx > 0 {
					retryAfterStr = retryAfterStr[:idx]
				}
			}

			logger.Error("Let's Encrypt rate limit reached",
				"domain", adminDomain,
				"error", certErr,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// Store rate limit history in cache to prevent further requests
			rlCacheKey := adminDomain + "_ratelimit_history"
			rlCtx, rlCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rlCancel()

			// Store the error message and timestamp in the cache
			rlData := []byte(fmt.Sprintf("Rate limited at %s: %s",
				time.Now().Format(time.RFC3339), certErr.Error()))

			if p.certManager != nil && p.certManager.Cache != nil {
				// Store with a long expiration - Let's Encrypt rate limits are typically for a week
				err := p.certManager.Cache.Put(rlCtx, rlCacheKey, rlData)
				if err != nil {
					logger.Debug("Failed to store rate limit history in cache",
						"domain", adminDomain, "error", err)
				} else {
					logger.Info("Stored rate limit history in cache to prevent further requests",
						"domain", adminDomain, "retry_after", retryAfterStr)
				}
			}

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
		// Rename the certificate files based on the mode
		certType := "production"
		if p.config.LetsEncryptMode == "staging" {
			certType = "staging"
		}

		// Try to rename the certificate files
		oldCertPath := filepath.Join(certDir, adminDomain)
		oldKeyPath := filepath.Join(certDir, adminDomain+"+rsa")

		newName := getCertificateFileName(adminDomain, certType)
		newCertPath := filepath.Join(certDir, newName)
		newKeyPath := filepath.Join(certDir, newName+"+rsa")

		// Debug log certificate file paths
		logger.Debug("Certificate file paths",
			"domain", adminDomain,
			"old_cert", oldCertPath,
			"new_cert", newCertPath,
			"old_key", oldKeyPath,
			"new_key", newKeyPath)

		// Check if files exist first
		oldCertExists := false
		if _, err := os.Stat(oldCertPath); err == nil {
			oldCertExists = true
			logger.Debug("Old certificate file exists", "path", oldCertPath)
		} else {
			logger.Debug("Old certificate file not found", "path", oldCertPath, "error", err)
		}

		// Read the certificate files directly to verify they contain valid data
		if oldCertExists {
			certBytes, err := os.ReadFile(oldCertPath)
			if err != nil {
				logger.Error("Failed to read certificate file",
					"path", oldCertPath,
					"error", err)
			} else {
				logger.Debug("Successfully read certificate file",
					"path", oldCertPath,
					"size", len(certBytes))

				// Verify it's a valid PEM format
				block, _ := pem.Decode(certBytes)
				if block == nil {
					logger.Error("Certificate file does not contain valid PEM data",
						"path", oldCertPath)
				} else {
					logger.Debug("Certificate file contains valid PEM data",
						"path", oldCertPath,
						"type", block.Type)
				}
			}

			// Check permissions on cert and key files
			certFileInfo, err := os.Stat(oldCertPath)
			if err == nil {
				mode := certFileInfo.Mode()
				logger.Debug("Certificate file permissions",
					"path", oldCertPath,
					"mode", mode.String())

				// Ensure cert file is readable by all
				if mode.Perm()&0444 != 0444 {
					logger.Warn("Certificate file has restrictive permissions, fixing",
						"path", oldCertPath,
						"current_mode", mode.String())
					if err := os.Chmod(oldCertPath, 0644); err != nil {
						logger.Error("Failed to fix certificate file permissions",
							"path", oldCertPath,
							"error", err)
					}
				}
			}

			// Check key file permissions
			if _, err := os.Stat(oldKeyPath); err == nil {
				keyFileInfo, err := os.Stat(oldKeyPath)
				if err == nil {
					mode := keyFileInfo.Mode()
					logger.Debug("Key file permissions",
						"path", oldKeyPath,
						"mode", mode.String())

					// Ensure key file is only readable by owner
					if mode.Perm()&0400 != 0400 || mode.Perm()&0077 != 0 {
						logger.Warn("Key file has incorrect permissions, fixing",
							"path", oldKeyPath,
							"current_mode", mode.String())
						if err := os.Chmod(oldKeyPath, 0600); err != nil {
							logger.Error("Failed to fix key file permissions",
								"path", oldKeyPath,
								"error", err)
						}
					}
				}
			}
		}

		// Only rename if the files exist and the new names are different
		if oldCertPath != newCertPath {
			// Check if old cert exists
			if oldCertExists {
				// Rename cert
				if err := os.Rename(oldCertPath, newCertPath); err != nil {
					logger.Warn("Failed to rename certificate file",
						"domain", adminDomain,
						"from", oldCertPath,
						"to", newCertPath,
						"error", err)
				} else {
					logger.Info("Renamed certificate file with appropriate type",
						"domain", adminDomain,
						"type", certType,
						"from", oldCertPath,
						"to", newCertPath)
				}

				// Rename key
				if _, err := os.Stat(oldKeyPath); err == nil {
					if err := os.Rename(oldKeyPath, newKeyPath); err != nil {
						logger.Warn("Failed to rename certificate key file",
							"domain", adminDomain,
							"from", oldKeyPath,
							"to", newKeyPath,
							"error", err)
					} else {
						logger.Info("Renamed certificate key file with appropriate type",
							"domain", adminDomain,
							"type", certType,
							"from", oldKeyPath,
							"to", newKeyPath)
					}
				} else {
					logger.Warn("Old key file not found", "path", oldKeyPath, "error", err)
				}
			}
		}

		logger.Info("Successfully obtained Let's Encrypt certificate",
			"domain", adminDomain,
			"type", certType)

		// Verify the certificate files are accessible in the cache
		cacheKey := "cert-" + adminDomain
		cacheCtx, cacheCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cacheCancel()

		certData, cacheErr := p.certManager.Cache.Get(cacheCtx, cacheKey)
		if cacheErr != nil {
			logger.Warn("Certificate not found in cache after successful obtainment",
				"domain", adminDomain,
				"error", cacheErr,
				"solution", "Check certificate permissions and file paths")

			// If certificate isn't in autocert cache but exists in filesystem, try to manually add it
			newCertExists := false
			if _, err := os.Stat(newCertPath); err == nil {
				newCertExists = true
			}

			if oldCertExists || newCertExists {
				logger.Info("Attempting to manually add certificate to cache from filesystem",
					"domain", adminDomain)

				sourceCertPath := newCertPath
				sourceKeyPath := newKeyPath

				// If the file wasn't renamed, use the old paths
				if _, err := os.Stat(sourceCertPath); err != nil {
					sourceCertPath = oldCertPath
					sourceKeyPath = oldKeyPath
				}

				// Read the certificate and key files
				certBytes, err := os.ReadFile(sourceCertPath)
				if err != nil {
					logger.Error("Failed to read certificate file for manual cache addition",
						"path", sourceCertPath,
						"error", err)
				} else {
					keyBytes, err := os.ReadFile(sourceKeyPath)
					if err != nil {
						logger.Error("Failed to read key file for manual cache addition",
							"path", sourceKeyPath,
							"error", err)
					} else {
						// Add to autocert cache
						if err := p.certManager.Cache.Put(cacheCtx, cacheKey, certBytes); err != nil {
							logger.Error("Failed to manually add certificate to cache",
								"domain", adminDomain,
								"error", err)
						} else {
							keyKey := "key-" + adminDomain
							if err := p.certManager.Cache.Put(cacheCtx, keyKey, keyBytes); err != nil {
								logger.Error("Failed to manually add key to cache",
									"domain", adminDomain,
									"error", err)
							} else {
								logger.Info("Successfully manually added certificate to cache",
									"domain", adminDomain)
							}
						}
					}
				}
			}
		} else {
			logger.Debug("Certificate successfully stored in cache",
				"domain", adminDomain,
				"cert_size", len(certData))
		}
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

	// Check for upstream proxy with certificate skipping enabled
	isProxied := p.detectUpstreamProxy()
	if isProxied && p.config.SkipCertificates {
		logger.Info("Upstream TLS-terminating proxy detected, skipping domain certificate request",
			"domain", domain,
			"skip_reason", "behind TLS-terminating proxy")
		return
	}

	// First, check if certificate is already in the autocert cache
	// This is the critical step to avoid unnecessary Let's Encrypt requests
	cacheKey := "cert-" + domain
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if p.certManager != nil && p.certManager.Cache != nil {
		certData, err := p.certManager.Cache.Get(ctx, cacheKey)
		if err == nil && len(certData) > 0 {
			// Check if the cached certificate is valid
			isValid, expiresInDays, certType, _ := isCertificateValid(certData, domain)
			if isValid {
				logger.Info("Using valid certificate from cache",
					"domain", domain,
					"type", certType,
					"expires_in_days", expiresInDays)
				return
			}

			// If certificate is invalid or expiring soon, log but continue
			logger.Info("Cached certificate is invalid or expiring soon",
				"domain", domain,
				"expires_in_days", expiresInDays,
				"valid", isValid)
		}
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
	hasValidCert, certType := p.checkCertificateExists(domain)

	// If we have a valid cert that matches our mode, use it
	if hasValidCert {
		// Check for mode mismatch
		if p.config.LetsEncryptMode == "production" && certType == "staging" {
			logger.Info("Found staging certificate but production mode is enabled",
				"domain", domain,
				"action", "requesting new production certificate")
			// Continue to request a new certificate
		} else {
			logger.Info("Using existing certificate",
				"domain", domain,
				"type", certType,
				"action", "skipping Let's Encrypt request to avoid rate limits")

			// If a valid certificate was found in the filesystem but not in the cache,
			// try to verify it with a TLS connection (non-blocking)
			go func() {
				// Only attempt TLS verification if certificate was found in filesystem
				// and not in cache (to avoid unnecessary check for cached certificates)
				validInCache, _ := p.checkCertificateInCache(domain)
				validInFS, _ := p.checkCertificateInFilesystem(domain)
				if !validInCache && validInFS {
					// Try to load the certificate into the cache if it's not there already
					// This helps prevent future Let's Encrypt requests
					hasOnDisk, certType := p.checkCertificateInFilesystem(domain)
					if hasOnDisk {
						logger.Info("Loading filesystem certificate into cache",
							"domain", domain,
							"type", certType)

						// Get the certificate directory path for finding files
						certDir := p.config.CertDir
						if certDir == "" {
							certDir = p.app.GetConfig().General.StorageDir + "/certs"
						}

						// Look for production certificate first, then staging
						certFile := getCertificateFileName(domain, "production")
						certPath := filepath.Join(certDir, certFile)
						keyPath := filepath.Join(certDir, certFile+"+rsa")

						// If production cert doesn't exist, try staging
						if _, err := os.Stat(certPath); os.IsNotExist(err) {
							certFile = getCertificateFileName(domain, "staging")
							certPath = filepath.Join(certDir, certFile)
							keyPath = filepath.Join(certDir, certFile+"+rsa")
						}

						// If no specific cert found, try the domain name directly
						if _, err := os.Stat(certPath); os.IsNotExist(err) {
							certPath = filepath.Join(certDir, domain)
							keyPath = filepath.Join(certDir, domain+"+rsa")
						}

						// Add to cache if files exist
						if _, err := os.Stat(certPath); err == nil {
							if _, err := os.Stat(keyPath); err == nil {
								p.addCertificateToCache(domain, certPath, keyPath)
							}
						}
					}

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
	}

	// Additional rate limit safety check
	// Check if we've recently hit a rate limit for this domain
	// This is a temporary memory-only safeguard
	cacheKey = domain + "_ratelimit_history"
	rateLimitData, _ := p.certManager.Cache.Get(ctx, cacheKey)
	if len(rateLimitData) > 0 {
		logger.Warn("Rate limit history found for this domain, skipping certificate request",
			"domain", domain,
			"solution", "Wait for rate limit to expire")
		return
	}

	// Get the certificate directory path for naming the output files
	certDir := p.config.CertDir
	if certDir == "" {
		certDir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// Ensure certificate directory exists
	if err := os.MkdirAll(certDir, 0755); err != nil {
		logger.Warn("Failed to create certificate directory",
			"dir", certDir,
			"error", err)
	}

	// Use the configured mode
	mode := p.config.LetsEncryptMode
	email := p.config.Email

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
	ctx, cancel = context.WithTimeout(context.Background(), 1*time.Minute)
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
				retryAfterStr = strings.TrimSpace(matches[1])
				if idx := strings.Index(retryAfterStr, ":"); idx > 0 {
					retryAfterStr = retryAfterStr[:idx]
				}
			}

			logger.Error("Let's Encrypt rate limit reached",
				"domain", domain,
				"error", certErr,
				"retry_after", retryAfterStr,
				"solution", "Wait until the rate limit expires, then restart the server")

			// Store rate limit history in cache to prevent further requests
			rlCacheKey := domain + "_ratelimit_history"
			rlCtx, rlCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rlCancel()

			// Store the error message and timestamp in the cache
			rlData := []byte(fmt.Sprintf("Rate limited at %s: %s",
				time.Now().Format(time.RFC3339), certErr.Error()))

			if p.certManager != nil && p.certManager.Cache != nil {
				// Store with a long expiration - Let's Encrypt rate limits are typically for a week
				err := p.certManager.Cache.Put(rlCtx, rlCacheKey, rlData)
				if err != nil {
					logger.Debug("Failed to store rate limit history in cache",
						"domain", domain, "error", err)
				} else {
					logger.Info("Stored rate limit history in cache to prevent further requests",
						"domain", domain, "retry_after", retryAfterStr)
				}
			}

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
		// Rename the certificate files based on the mode
		certType := "production"
		if p.config.LetsEncryptMode == "staging" {
			certType = "staging"
		}

		// Try to rename the certificate files
		oldCertPath := filepath.Join(certDir, domain)
		oldKeyPath := filepath.Join(certDir, domain+"+rsa")

		newName := getCertificateFileName(domain, certType)
		newCertPath := filepath.Join(certDir, newName)
		newKeyPath := filepath.Join(certDir, newName+"+rsa")

		// Only rename if the files exist and the new names are different
		if oldCertPath != newCertPath {
			// Check if old cert exists
			if _, err := os.Stat(oldCertPath); err == nil {
				// Rename cert
				if err := os.Rename(oldCertPath, newCertPath); err != nil {
					logger.Warn("Failed to rename certificate file",
						"domain", domain,
						"from", oldCertPath,
						"to", newCertPath,
						"error", err)
				} else {
					logger.Info("Renamed certificate file with appropriate type",
						"domain", domain,
						"type", certType,
						"from", oldCertPath,
						"to", newCertPath)
				}

				// Rename key
				if _, err := os.Stat(oldKeyPath); err == nil {
					if err := os.Rename(oldKeyPath, newKeyPath); err != nil {
						logger.Warn("Failed to rename certificate key file",
							"domain", domain,
							"from", oldKeyPath,
							"to", newKeyPath,
							"error", err)
					} else {
						logger.Info("Renamed certificate key file with appropriate type",
							"domain", domain,
							"type", certType,
							"from", oldKeyPath,
							"to", newKeyPath)
					}
				} else {
					logger.Warn("Old key file not found", "path", oldKeyPath, "error", err)
				}
			}
		}

		logger.Info("Successfully obtained Let's Encrypt certificate",
			"domain", domain,
			"type", certType)
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

	// Get the certificate directory for renaming
	certDir := p.config.CertDir
	if certDir == "" {
		certDir = p.app.GetConfig().General.StorageDir + "/certs"
	}

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
			// Determine certificate type based on mode
			certType := "production"
			if p.config.LetsEncryptMode == "staging" {
				certType = "staging"
			}

			// Rename the certificate files to include the type
			oldCertPath := filepath.Join(certDir, domain)
			oldKeyPath := filepath.Join(certDir, domain+"+rsa")

			newName := getCertificateFileName(domain, certType)
			newCertPath := filepath.Join(certDir, newName)
			newKeyPath := filepath.Join(certDir, newName+"+rsa")

			// Only rename if the files exist and the new names are different
			if oldCertPath != newCertPath {
				// Check if old cert exists
				if _, err := os.Stat(oldCertPath); err == nil {
					// Rename cert
					if err := os.Rename(oldCertPath, newCertPath); err != nil {
						logger.Warn("Failed to rename certificate file",
							"domain", domain,
							"from", oldCertPath,
							"to", newCertPath,
							"error", err)
					} else {
						logger.Info("Renamed certificate file with appropriate type",
							"domain", domain,
							"type", certType,
							"from", oldCertPath,
							"to", newCertPath)
					}

					// Rename key
					if _, err := os.Stat(oldKeyPath); err == nil {
						if err := os.Rename(oldKeyPath, newKeyPath); err != nil {
							logger.Warn("Failed to rename certificate key file",
								"domain", domain,
								"from", oldKeyPath,
								"to", newKeyPath,
								"error", err)
						} else {
							logger.Info("Renamed certificate key file with appropriate type",
								"domain", domain,
								"type", certType,
								"from", oldKeyPath,
								"to", newKeyPath)
						}
					} else {
						logger.Warn("Old key file not found", "path", oldKeyPath, "error", err)
					}
				}
			}

			logger.Info("Successfully obtained Let's Encrypt certificate on retry",
				"domain", domain,
				"attempt", attempt,
				"type", certType)
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

	// Generate a self-signed certificate as a fallback
	logger.Error("All Let's Encrypt certificate request retries failed",
		"domain", domain,
		"max_retries", maxRetries,
		"fallback", "Generating self-signed certificate")

	// Generate a self-signed certificate for this domain
	fallbackCert, err := generateFallbackCertificates([]string{domain})
	if err != nil {
		logger.Error("Failed to generate self-signed fallback certificate",
			"domain", domain,
			"error", err)
	} else {
		// Save the self-signed certificate to disk with appropriate naming
		selfSignedName := getCertificateFileName(domain, "self-signed")
		certPath := filepath.Join(certDir, selfSignedName)
		keyPath := filepath.Join(certDir, selfSignedName+"+rsa")

		// Save the certificate and key
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fallbackCert.Certificate[0]})
		keyBytes, err := x509.MarshalPKCS8PrivateKey(fallbackCert.PrivateKey)
		if err == nil {
			keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

			// Write files
			if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
				logger.Error("Failed to write self-signed certificate to disk",
					"domain", domain,
					"path", certPath,
					"error", err)
			}

			if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
				logger.Error("Failed to write self-signed key to disk",
					"domain", domain,
					"path", keyPath,
					"error", err)
			}

			logger.Info("Generated and saved self-signed fallback certificate",
				"domain", domain,
				"cert_path", certPath,
				"key_path", keyPath,
				"valid_until", time.Now().Add(24*time.Hour).Format("2006-01-02 15:04:05"))
		}
	}

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

// preloadCertificates scans the certificate directory and loads all valid certificates
// into the autocert cache to prevent unnecessary requests to Let's Encrypt
func (p *Proxy) preloadCertificates() {
	if p.certManager == nil || p.certManager.Cache == nil {
		logger.Warn("Cannot preload certificates: certificate manager or cache not initialized")
		return
	}

	// Get the certificate directory path
	certDir := p.config.CertDir
	if certDir == "" {
		certDir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	logger.Info("Preloading certificates from directory", "dir", certDir)

	// Ensure directory exists
	if _, err := os.Stat(certDir); os.IsNotExist(err) {
		logger.Warn("Certificate directory does not exist, creating it", "dir", certDir)
		if err := os.MkdirAll(certDir, 0755); err != nil {
			logger.Error("Failed to create certificate directory", "dir", certDir, "error", err)
			return
		}
	}

	// List all files in the certificate directory
	files, err := os.ReadDir(certDir)
	if err != nil {
		logger.Error("Failed to read certificate directory", "dir", certDir, "error", err)
		return
	}

	// Log all files in the directory to help troubleshoot
	var fileNames []string
	for _, file := range files {
		fileNames = append(fileNames, file.Name())

		// Check file permissions and fix if too restrictive
		filePath := filepath.Join(certDir, file.Name())
		info, err := os.Stat(filePath)
		if err != nil {
			logger.Warn("Could not stat file", "path", filePath, "error", err)
			continue
		}

		mode := info.Mode()
		if strings.HasSuffix(file.Name(), "+rsa") {
			// Key files should be 0600 (owner read/write only)
			if mode.Perm() != 0600 {
				logger.Info("Fixing key file permissions", "file", file.Name(), "current_mode", mode.String(), "target_mode", "0600")
				if err := os.Chmod(filePath, 0600); err != nil {
					logger.Warn("Failed to fix key file permissions", "file", file.Name(), "error", err)
				}
			}
		} else {
			// Certificate files should be 0644 (readable by all)
			if mode.Perm() != 0644 {
				logger.Info("Fixing certificate file permissions", "file", file.Name(), "current_mode", mode.String(), "target_mode", "0644")
				if err := os.Chmod(filePath, 0644); err != nil {
					logger.Warn("Failed to fix certificate file permissions", "file", file.Name(), "error", err)
				}
			}
		}
	}

	logger.Debug("Files in certificate directory", "dir", certDir, "files", fileNames)

	// Track the number of certificates loaded
	loadedCount := 0

	// Create a context for cache operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find and process certificate files (excluding key files with +rsa suffix)
	// Group them by domain for easier processing
	domainCerts := make(map[string][]string)

	for _, file := range files {
		name := file.Name()

		// Skip account keys and +rsa key files as we'll handle these separately
		if strings.HasSuffix(name, "+rsa") || strings.Contains(name, "acme_account") {
			continue
		}

		// Get associated key file name (file+rsa)
		keyName := name + "+rsa"
		keyPath := filepath.Join(certDir, keyName)

		// Skip certificates without key files
		if _, err := os.Stat(keyPath); os.IsNotExist(err) {
			logger.Debug("Skipping certificate without key file", "cert", name, "key", keyName)
			continue
		}

		// Try to determine the domain from the filename
		domain := name

		// Handle special patterns like domain_letsencrypt-production
		for _, pattern := range []string{"_letsencrypt-production", "_letsencrypt-staging", "_self-signed"} {
			if strings.HasSuffix(domain, pattern) {
				domain = strings.TrimSuffix(domain, pattern)
				break
			}
		}

		// Add to domain certificates map
		domainCerts[domain] = append(domainCerts[domain], name)
	}

	// Process certificates for each domain
	for domain, certFiles := range domainCerts {
		logger.Debug("Found certificate files for domain", "domain", domain, "files", certFiles)

		// Prioritize production certificates if multiple exist
		var bestCert string
		bestCertType := ""

		for _, certFile := range certFiles {
			certPath := filepath.Join(certDir, certFile)

			// Read certificate to check its validity
			certData, err := os.ReadFile(certPath)
			if err != nil {
				logger.Warn("Failed to read certificate file", "path", certPath, "error", err)
				continue
			}

			// Debug certificate content
			if len(certData) < 100 {
				logger.Warn("Certificate file too small, may be corrupted",
					"path", certPath, "size", len(certData))
				continue
			}

			// Validate the certificate
			valid, expiresInDays, certType, _ := isCertificateValid(certData, domain)
			if !valid {
				logger.Debug("Invalid certificate found", "domain", domain, "file", certFile)
				continue
			}

			logger.Info("Found valid certificate", "domain", domain, "type", certType,
				"expires_in_days", expiresInDays, "file", certFile)

			// Prioritize production certificates over staging ones
			if bestCertType == "" ||
				(bestCertType != "production" && certType == "production") {
				bestCert = certFile
				bestCertType = certType
			}
		}

		// If we found a valid certificate for this domain, add it to the cache
		if bestCert != "" {
			certPath := filepath.Join(certDir, bestCert)
			keyPath := filepath.Join(certDir, bestCert+"+rsa")

			// Read certificate and key data
			certData, err := os.ReadFile(certPath)
			if err != nil {
				logger.Warn("Failed to read certificate file", "path", certPath, "error", err)
				continue
			}

			keyData, err := os.ReadFile(keyPath)
			if err != nil {
				logger.Warn("Failed to read key file", "path", keyPath, "error", err)
				continue
			}

			// Add certificate to autocert cache
			cacheKey := "cert-" + domain
			err = p.certManager.Cache.Put(ctx, cacheKey, certData)
			if err != nil {
				logger.Error("Failed to add certificate to cache", "domain", domain, "error", err)
				continue
			}

			// Add key to cache
			keyKey := "key-" + domain
			err = p.certManager.Cache.Put(ctx, keyKey, keyData)
			if err != nil {
				logger.Error("Failed to add key to cache", "domain", domain, "error", err)
				continue
			}

			logger.Info("Preloaded certificate into cache", "domain", domain, "type", bestCertType)
			loadedCount++
		} else {
			logger.Warn("No valid certificate found for domain after checking all files",
				"domain", domain,
				"files_checked", certFiles)
		}
	}

	logger.Info("Certificate preloading complete", "loaded_certificates", loadedCount)
}

// manuallyLoadCertificate is a fallback method to load certificates directly
// when autocert cache is failing. This directly builds the tls.Certificate from files.
func (p *Proxy) manuallyLoadCertificate(domain string) (*tls.Certificate, error) {
	// Get certificate directory
	certDir := p.config.CertDir
	if certDir == "" {
		certDir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// Check preferred file naming patterns
	filePatterns := []string{
		// Try production certificate first
		fmt.Sprintf(LetsEncryptProdPattern, domain),
		// Then staging certificate
		fmt.Sprintf(LetsEncryptStagingPattern, domain),
		// Then self-signed
		fmt.Sprintf(SelfSignedCertPattern, domain),
		// Finally plain domain name
		domain,
	}

	for _, pattern := range filePatterns {
		certPath := filepath.Join(certDir, pattern)
		keyPath := filepath.Join(certDir, pattern+"+rsa")

		// Check if both files exist
		if _, err := os.Stat(certPath); os.IsNotExist(err) {
			continue
		}
		if _, err := os.Stat(keyPath); os.IsNotExist(err) {
			continue
		}

		// Try to load the certificate pair
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			logger.Warn("Failed to load certificate key pair",
				"domain", domain,
				"certPath", certPath,
				"keyPath", keyPath,
				"error", err)

			// Additional diagnostics
			if data, readErr := os.ReadFile(certPath); readErr == nil {
				logger.Debug("Certificate file content preview",
					"path", certPath,
					"size", len(data),
					"preview", string(data[:min(100, len(data))]))

				// Try to parse the certificate - this may help diagnose format issues
				block, _ := pem.Decode(data)
				if block == nil {
					logger.Error("Certificate file does not contain valid PEM data",
						"path", certPath)
				} else {
					logger.Debug("Certificate PEM block info",
						"type", block.Type,
						"headers", block.Headers)
				}
			}

			continue
		}

		// Success!
		logger.Info("Manually loaded certificate from filesystem",
			"domain", domain,
			"certPath", certPath)
		return &cert, nil
	}

	// Also try with glob pattern
	filePaths, err := filepath.Glob(filepath.Join(certDir, domain+"*"))
	if err != nil {
		return nil, fmt.Errorf("glob error: %w", err)
	}

	for _, certPath := range filePaths {
		// Skip if it's an RSA key
		if strings.HasSuffix(certPath, "+rsa") {
			continue
		}

		// Check if key exists
		keyPath := certPath + "+rsa"
		if _, err := os.Stat(keyPath); os.IsNotExist(err) {
			continue
		}

		// Try to load
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			logger.Debug("Failed to load certificate from glob match",
				"domain", domain,
				"certPath", certPath,
				"error", err)
			continue
		}

		logger.Info("Manually loaded certificate from glob search",
			"domain", domain,
			"certPath", certPath)
		return &cert, nil
	}

	return nil, fmt.Errorf("no valid certificate found for domain %s", domain)
}

// min returns the smaller of x or y.
func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}
