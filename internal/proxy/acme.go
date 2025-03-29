package proxy

import (
	"crypto"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/interfaces"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns"
	"github.com/go-acme/lego/v4/registration"
)

// CertManagerConfig holds configuration for the certificate manager
type CertManagerConfig struct {
	CertDir          string
	Email            string
	Mode             string
	SkipCertificates bool
	BehindTLSProxy   bool
	AdminDomain      string
	RootDomain       string
	HttpPort         string
}

// CertificateManager handles ACME certificate operations
type CertificateManager struct {
	config  CertManagerConfig
	client  *lego.Client
	user    *AcmeUser
	db      *sql.DB
	queries *queries.ProxyQueries
}

// AcmeUser implements the registration.User interface
type AcmeUser struct {
	Email        string                 `json:"email"`
	Registration *registration.Resource `json:"registration"`
	key          crypto.PrivateKey      `json:"-"` // Exclude key from JSON marshal
}

// GetEmail returns the user's email
func (u *AcmeUser) GetEmail() string {
	return u.Email
}

// GetRegistration returns the user's registration
func (u *AcmeUser) GetRegistration() *registration.Resource {
	return u.Registration
}

// GetPrivateKey returns the user's private key
func (u *AcmeUser) GetPrivateKey() crypto.PrivateKey {
	return u.key
}

// NewCertificateManager creates a new certificate manager
func NewCertificateManager(config CertManagerConfig, app interfaces.AppInterface, queries *queries.ProxyQueries) (*CertificateManager, error) {
	logger.Debug("Initializing certificate manager", "config", config)

	db := app.GetDB() // Get DB connection

	// --- User Loading/Creation ---
	user, err := loadAcmeUserFromDB(config.Email, db, queries)

	if err != nil {
		// Check if the error is "user not found"
		if err == sql.ErrNoRows {
			logger.Info("No existing ACME user found in DB, creating new one.", "email", config.Email)

			// Create a new key
			privateKey, keyErr := certcrypto.GeneratePrivateKey(certcrypto.RSA2048)
			if keyErr != nil {
				return nil, fmt.Errorf("failed to generate private key: %w", keyErr)
			}

			user = &AcmeUser{
				Email: config.Email,
				key:   privateKey,
				// Registration will be nil initially
			}

			// Save the new user (key only for now, registration later)
			if saveErr := saveAcmeUserToDB(user, db, queries); saveErr != nil {
				// Log the error but proceed, registration might fix it or fail later
				logger.Error("Failed to save newly generated ACME user key to DB", "error", saveErr)
				// Depending on policy, might want to return error here:
				// return nil, fmt.Errorf("failed to save initial ACME user: %w", saveErr)
			} else {
				logger.Debug("Saved ACME user details to DB", "email", user.Email)
			}
		} else {
			// Handle other DB errors during load
			return nil, fmt.Errorf("failed to load ACME user from DB: %w", err)
		}
	} else {
		logger.Debug("Loaded existing ACME user from DB", "email", user.Email)
	}
	// --- End User Loading/Creation ---

	// Create the lego config using the loaded/created user
	legoConfig := lego.NewConfig(user)
	legoConfig.Certificate.KeyType = certcrypto.RSA2048 // Match key type used

	// Set the server based on mode
	if config.Mode == "staging" {
		logger.Info("Using Let's Encrypt staging environment")
		legoConfig.CADirURL = lego.LEDirectoryStaging
	} else {
		logger.Info("Using Let's Encrypt production environment")
		legoConfig.CADirURL = lego.LEDirectoryProduction
	}

	// Create the lego client
	client, err := lego.NewClient(legoConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create lego client: %w", err)
	}

	// --- ACME User Registration ---
	if user.Registration == nil {
		logger.Info("ACME user registration not found, attempting to register", "email", user.Email)
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			// Check if it's an "already registered" error, which is fine
			// Note: Lego might handle this implicitly, but explicit check is safer.
			// The exact error type/message might vary depending on lego version and ACME server.
			// We might need to refine this error check based on observed behavior.
			if strings.Contains(err.Error(), "urn:ietf:params:acme:error:accountDoesNotExist") {
				// This might indicate a mismatch between loaded key and LE's record.
				// We might need to re-create the key and re-register, or query the account by key.
				// For now, just log and return error.
				logger.Error("ACME account registration failed: Account does not exist (key mismatch?)", "error", err)
				return nil, fmt.Errorf("failed to register ACME user (account not found): %w", err)
			} else if strings.Contains(err.Error(), "user already exists") { // Example, adjust as needed
				logger.Info("ACME user is already registered with the server.")
				// Try to resolve the account details if possible
				// reg, err = client.Registration.ResolveAccountByKey() // Requires lego client supporting this
				// if err != nil {
				//    logger.Error("Failed to resolve existing ACME account by key", "error", err)
				//     return nil, fmt.Errorf("failed to resolve existing ACME account: %w", err)
				// }
				// For now, let's assume if registration failed with "exists", we might need manual intervention
				// or a more sophisticated recovery mechanism. Returning error for safety.
				// If Obtain works despite this, we can reconsider.
				logger.Error("ACME user registration failed, but server indicates user exists. Manual check might be needed.", "error", err)
				return nil, fmt.Errorf("failed to register ACME user (already exists conflict): %w", err)
			} else {
				// Handle other registration errors
				logger.Error("ACME user registration failed", "error", err)
				return nil, fmt.Errorf("failed to register ACME user: %w", err)
			}
		} else {
			logger.Info("ACME user registered successfully", "uri", reg.URI)
			user.Registration = reg // Store the registration resource

			// Save the user again, now with registration info
			if saveErr := saveAcmeUserToDB(user, db, queries); saveErr != nil {
				logger.Error("Failed to save ACME user registration info to DB", "error", saveErr)
				// Continue, as registration itself succeeded, but log the failure
			} else {
				logger.Debug("Saved ACME user registration info to DB")
			}
		}
	} else {
		logger.Debug("Existing ACME user registration loaded from DB", "uri", user.Registration.URI)
		// Optional: Could add a check here to ensure the registration is still valid
	}
	// --- End ACME User Registration ---

	// Create the certificate manager struct
	cm := &CertificateManager{
		config:  config,
		user:    user,
		client:  client,
		db:      db,
		queries: queries,
	}

	return cm, nil
}

// loadAcmeUserFromDB loads the user's private key and registration info from the DB
func loadAcmeUserFromDB(email string, db *sql.DB, queries *queries.ProxyQueries) (*AcmeUser, error) {
	var privateKeyPEM string
	var registrationJSON sql.NullString

	err := db.QueryRow(queries.GetAcmeAccountByEmail, email).Scan(&privateKeyPEM, &registrationJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			logger.Debug("ACME user not found in DB", "email", email)
			return nil, sql.ErrNoRows // Return specific error
		}
		return nil, fmt.Errorf("failed to query ACME user: %w", err)
	}

	// Add detailed logging for the loaded registration info
	logger.Debug("Raw registration_info from DB", "email", email, "Valid", registrationJSON.Valid, "String", registrationJSON.String)

	// Parse the private key
	privateKey, err := certcrypto.ParsePEMPrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key from DB: %w", err)
	}

	user := &AcmeUser{
		Email: email,
		key:   privateKey,
	}

	// Load the registration info if it exists
	if registrationJSON.Valid && registrationJSON.String != "" {
		// Need to unmarshal into a registration.Resource pointer
		reg := &registration.Resource{}
		err = json.Unmarshal([]byte(registrationJSON.String), reg)
		if err != nil {
			// Log the error but potentially continue without registration? Or return error?
			// Returning error is safer.
			logger.Error("Failed to unmarshal ACME registration info from DB", "email", email, "error", err)
			return nil, fmt.Errorf("failed to unmarshal registration info: %w", err)
		}
		user.Registration = reg // Assign the pointer
		logger.Debug("Loaded ACME user registration info from DB", "email", email, "uri", user.Registration.URI)
	} else {
		logger.Debug("ACME user registration info not found or null in DB", "email", email)
	}

	return user, nil
}

// saveAcmeUserToDB saves the user's private key and registration info to the DB
func saveAcmeUserToDB(user *AcmeUser, db *sql.DB, queries *queries.ProxyQueries) error {
	if user.key == nil {
		return fmt.Errorf("cannot save user without a private key")
	}

	// Encode private key to PEM
	privateKeyPEM := certcrypto.PEMEncode(user.key)

	// Marshal registration info to JSON (handle nil)
	var registrationJSON sql.NullString
	if user.Registration != nil {
		regBytes, err := json.Marshal(user.Registration)
		if err != nil {
			return fmt.Errorf("failed to marshal registration info: %w", err)
		}
		registrationJSON = sql.NullString{String: string(regBytes), Valid: true}
	}

	now := time.Now().Format(time.RFC3339)

	// Use InsertOrReplace query
	_, err := db.Exec(queries.InsertOrReplaceAcmeAccount,
		user.Email,
		string(privateKeyPEM),
		registrationJSON, // Pass the NullString directly
		now,              // created_at (will be updated on replace)
		now,              // updated_at
	)

	if err != nil {
		return fmt.Errorf("failed to save ACME user to DB: %w", err)
	}

	logger.Debug("Saved ACME user details to DB", "email", user.Email)
	return nil
}

// SetupChallengeProvider sets up the appropriate challenge provider for a domain
func (cm *CertificateManager) SetupChallengeProvider(domainName string, proxy *Proxy) error {
	// Get domain configuration from database
	var domain struct {
		AcmeEnabled           bool
		AcmeChallengeType     sql.NullString
		AcmeDnsProvider       sql.NullString
		AcmeDnsCredentialsRef sql.NullString
	}

	err := proxy.app.GetDB().QueryRow(`
		SELECT acme_enabled, acme_challenge_type, acme_dns_provider, acme_dns_credentials_ref
		FROM domain WHERE name = ?
	`, domainName).Scan(&domain.AcmeEnabled, &domain.AcmeChallengeType, &domain.AcmeDnsProvider, &domain.AcmeDnsCredentialsRef)

	if err != nil {
		return fmt.Errorf("failed to get domain configuration: %w", err)
	}

	// Determine challenge type
	challengeType := domain.AcmeChallengeType.String
	if !domain.AcmeChallengeType.Valid {
		challengeType = proxy.config.DefaultChallengeType
	}

	// Set up the appropriate challenge provider
	switch challengeType {
	case "http-01":
		// Use the configured HTTP challenge port
		port := proxy.config.DefaultHttpChallengePort
		if port == "" {
			port = "80"
		}
		provider := http01.NewProviderServer("", port)
		if err := cm.client.Challenge.SetHTTP01Provider(provider); err != nil {
			return fmt.Errorf("failed to set HTTP-01 provider: %w", err)
		}

	case "dns-01":
		if domain.AcmeDnsProvider.String == "" {
			return fmt.Errorf("DNS provider not configured for domain %s", domainName)
		}

		// Get DNS provider credentials from environment
		creds := cm.getDNSProviderCredentials(domain.AcmeDnsCredentialsRef.String)
		if creds == nil {
			return fmt.Errorf("DNS provider credentials not found for domain %s", domainName)
		}

		// Create and configure the DNS provider
		provider, err := dns.NewDNSChallengeProviderByName(domain.AcmeDnsProvider.String)
		if err != nil {
			return fmt.Errorf("failed to create DNS provider: %w", err)
		}

		// Configure the provider with credentials
		if err := provider.Present(domainName, "", ""); err != nil {
			return fmt.Errorf("failed to configure DNS provider: %w", err)
		}

		// Set DNS timeouts
		if dnsProvider, ok := provider.(interface {
			SetPropagationTimeout(time.Duration)
			SetPollingInterval(time.Duration)
		}); ok {
			dnsProvider.SetPropagationTimeout(
				time.Duration(proxy.config.DefaultDnsChallengePropagationTimeout) * time.Second,
			)
			dnsProvider.SetPollingInterval(
				time.Duration(proxy.config.DefaultDnsChallengePollingInterval) * time.Second,
			)
		}

		if err := cm.client.Challenge.SetDNS01Provider(provider); err != nil {
			return fmt.Errorf("failed to set DNS-01 provider: %w", err)
		}

	default:
		return fmt.Errorf("unsupported challenge type: %s", challengeType)
	}

	return nil
}

// GetKeyAuthorization returns the key authorization for a token
func (cm *CertificateManager) GetKeyAuthorization(token string) (string, error) {
	// Create a new HTTP-01 provider server
	provider := http01.NewProviderServer("", cm.config.HttpPort)

	// Present the challenge
	err := provider.Present("", token, "")
	if err != nil {
		return "", fmt.Errorf("failed to present challenge: %w", err)
	}

	// Get the key authorization from the provider
	path := http01.ChallengePath(token)
	return path, nil
}

// ObtainCertificate obtains a new certificate for a domain
func (cm *CertificateManager) ObtainCertificate(domainName string, proxy *Proxy) (*certificate.Resource, error) {
	logger.Debug("Obtaining certificate for domain", "domain", domainName)

	// Set up the challenge provider
	if err := cm.SetupChallengeProvider(domainName, proxy); err != nil {
		return nil, fmt.Errorf("failed to set up challenge provider: %w", err)
	}

	// Request the certificate
	request := certificate.ObtainRequest{
		Domains: []string{domainName},
		Bundle:  true,
	}

	cert, err := cm.client.Certificate.Obtain(request)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain certificate: %w", err)
	}

	// Update the certificate in the database
	if err := cm.updateCertificateInDB(domainName, cert, proxy); err != nil {
		logger.Error("Failed to update certificate in database", "error", err)
	}

	return cert, nil
}

// updateCertificateInDB updates the certificate information in the database
func (cm *CertificateManager) updateCertificateInDB(domainName string, cert *certificate.Resource, proxy *Proxy) error {
	// Get the domain ID
	var domainID string
	err := proxy.app.GetDB().QueryRow("SELECT id FROM domain WHERE name = ?", domainName).Scan(&domainID)
	if err != nil {
		return fmt.Errorf("failed to get domain ID: %w", err)
	}

	// Parse the certificate to get expiry date
	x509Cert, err := certcrypto.ParsePEMCertificate(cert.Certificate)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Update or insert the certificate
	_, err = proxy.app.GetDB().Exec(`
		INSERT OR REPLACE INTO certificate (
			id, domain_id, cert_file, key_file, issued_at, expires_at, issuer, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		cert.Domain,
		domainID,
		string(cert.Certificate),
		string(cert.PrivateKey),
		time.Now().Format(time.RFC3339),
		x509Cert.NotAfter.Format(time.RFC3339),
		"Let's Encrypt",
		"valid",
	)

	if err != nil {
		return fmt.Errorf("failed to update certificate in database: %w", err)
	}

	return nil
}

// RenewCertificate renews an existing certificate
func (cm *CertificateManager) RenewCertificate(domainName string, proxy *Proxy) (*certificate.Resource, error) {
	logger.Debug("Renewing certificate for domain", "domain", domainName)

	// Set up the challenge provider
	if err := cm.SetupChallengeProvider(domainName, proxy); err != nil {
		return nil, fmt.Errorf("failed to set up challenge provider: %w", err)
	}

	// Get the existing certificate from the database
	var certFile, keyFile string
	err := proxy.app.GetDB().QueryRow(`
		SELECT cert_file, key_file FROM certificate c
		JOIN domain d ON c.domain_id = d.id
		WHERE d.name = ?
	`, domainName).Scan(&certFile, &keyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing certificate: %w", err)
	}

	// Create certificate resource for renewal
	cert := &certificate.Resource{
		Domain:      domainName,
		Certificate: []byte(certFile),
		PrivateKey:  []byte(keyFile),
	}

	// Renew the certificate
	_, err = cm.client.Certificate.Renew(*cert, true, false, "")
	if err != nil {
		return nil, fmt.Errorf("failed to renew certificate: %w", err)
	}

	// Update the certificate in the database
	if err := cm.updateCertificateInDB(domainName, cert, proxy); err != nil {
		logger.Error("Failed to update certificate in database", "error", err)
	}

	return cert, nil
}

// GetCertificateForTLS returns a certificate for TLS handshake
func (cm *CertificateManager) GetCertificateForTLS(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	logger.Debug("Getting certificate for TLS handshake", "server_name", hello.ServerName)

	// Get the certificate resource from the database
	certResource, err := cm.GetCertificate(hello.ServerName)
	if err != nil {
		// If the certificate is specifically not found in the DB,
		// return nil, nil to indicate no certificate is available for this ServerName.
		// This allows the TLS handshake to potentially use other certificates or fail gracefully.
		if err == sql.ErrNoRows {
			logger.Debug("Certificate not found in DB during TLS handshake", "server_name", hello.ServerName)
			return nil, nil // Indicate no certificate available for this name
		}
		// For other errors (DB connection issues, etc.), return the error.
		logger.Error("Error getting certificate during TLS handshake", "server_name", hello.ServerName, "error", err)
		return nil, fmt.Errorf("failed to get certificate for %s: %w", hello.ServerName, err)
	}

	// Convert the retrieved certificate resource to tls.Certificate
	tlsCert, err := tls.X509KeyPair(certResource.Certificate, certResource.PrivateKey)
	if err != nil {
		logger.Error("Failed to create key pair from retrieved certificate", "server_name", hello.ServerName, "error", err)
		return nil, fmt.Errorf("failed to parse retrieved certificate for %s: %w", hello.ServerName, err)
	}

	return &tlsCert, nil
}

// GetCertificate retrieves a certificate for a domain from the database
func (cm *CertificateManager) GetCertificate(domainName string) (*certificate.Resource, error) {
	logger.Debug("Attempting to retrieve certificate from DB", "domain", domainName)
	var certPEM, keyPEM string
	// Add dummy variables to scan the extra columns we don't need in this function
	var issuedAt, expiresAt, issuer, status sql.NullString // Use sql.NullString or appropriate types

	// Query the database using the predefined query (which now returns 6 columns)
	err := cm.db.QueryRow(cm.queries.GetCertificateByDomain, domainName).Scan(
		&certPEM, &keyPEM, // The columns we need
		&issuedAt, &expiresAt, &issuer, &status, // Dummy scans for the rest
	)

	if err != nil {
		// Check for ErrNoRows *after* attempting the scan
		if err == sql.ErrNoRows {
			logger.Warn("Certificate not found in DB for domain, attempting to obtain.", "domain", domainName)
			// Certificate not found, try obtaining a new one.
			// Note: Need the 'proxy' instance here. This function signature might need adjustment,
			// or the obtaining logic should be handled higher up (e.g., in GetCertificateForTLS or the proxy core).
			// For now, returning an error to highlight this dependency.
			// Alternatively, we could pass the proxy instance or necessary config down.
			// Let's return a specific error indicating it needs obtaining.
			return nil, fmt.Errorf("certificate not found in DB for domain %s, obtain needed", domainName)

			// --- Alternative: Obtain directly (Requires Proxy instance or refactor) ---
			// proxyInstance := ??? // How to get the proxy instance here?
			// if proxyInstance == nil {
			// 	 return nil, fmt.Errorf("cannot obtain certificate without proxy instance in GetCertificate")
			// }
			// obtainedCert, obtainErr := cm.ObtainCertificate(domainName, proxyInstance)
			// if obtainErr != nil {
			// 	 logger.Error("Failed to obtain certificate after not finding in DB", "domain", domainName, "error", obtainErr)
			// 	 return nil, fmt.Errorf("certificate not found and obtain failed for %s: %w", domainName, obtainErr)
			// }
			// logger.Info("Successfully obtained certificate after not finding in DB", "domain", domainName)
			// return obtainedCert, nil
			// --- End Alternative ---

		}
		logger.Error("Failed to query certificate from DB", "domain", domainName, "error", err)
		return nil, fmt.Errorf("database error retrieving certificate for %s: %w", domainName, err)
	}

	logger.Debug("Successfully retrieved certificate from DB", "domain", domainName)

	// Construct the certificate resource
	cert := &certificate.Resource{
		Domain:            domainName,
		CertURL:           "", // Not stored/retrieved from DB in this example
		CertStableURL:     "", // Not stored/retrieved from DB
		PrivateKey:        []byte(keyPEM),
		Certificate:       []byte(certPEM),
		IssuerCertificate: nil, // Not typically stored/needed for serving
		CSR:               nil, // Not stored
	}

	return cert, nil
}

// GetAutocertManager returns the autocert manager
func (cm *CertificateManager) GetAutocertManager() *tls.Config {
	return &tls.Config{
		GetCertificate: cm.GetCertificateForTLS,
		MinVersion:     tls.VersionTLS12,
	}
}

// getDNSProviderCredentials retrieves DNS provider credentials from environment variables
func (cm *CertificateManager) getDNSProviderCredentials(ref string) map[string]string {
	if ref == "" {
		return nil
	}

	creds := make(map[string]string)
	prefix := fmt.Sprintf("GORDON_DNS_CRED_%s_", ref)

	for _, env := range os.Environ() {
		if strings.HasPrefix(env, prefix) {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimPrefix(parts[0], prefix)
				creds[key] = parts[1]
			}
		}
	}

	if len(creds) == 0 {
		return nil
	}

	return creds
}
