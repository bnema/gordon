package out

import (
	"crypto/tls"
	"crypto/x509"
	"time"
)

// AppRoutes provides ACTIVE-derived host lookup for domain validation.
// Implemented by the apptraffic host index via AppHostSource plus the
// installation external routes. Replaces the retired RouteChecker
// (config-file routes are gone with the declarative-apps cutover).
type AppRoutes interface {
	AppHostSource
	// GetExternalRoutes returns installation external routes.
	GetExternalRoutes() map[string]string
}

// CertificateAuthority provides internal PKI operations.
// The adapter handles only cryptography — allowlist checks and caching
// belong in the use case layer.
type CertificateAuthority interface {
	// RootCertificate returns the root CA certificate in PEM format.
	RootCertificate() []byte

	// RootCertificateDER returns the root CA certificate in DER format
	// (for embedding in iOS .mobileconfig profiles).
	RootCertificateDER() []byte

	// IssueCertificate generates a leaf certificate for the given domain,
	// signed by the current intermediate CA. The caller must validate the
	// domain against the route table before calling this.
	IssueCertificate(domain string) (*tls.Certificate, error)

	// IntermediateExpiresAt returns the intermediate CA certificate expiry.
	IntermediateExpiresAt() time.Time

	// RenewIntermediate regenerates the intermediate CA certificate,
	// signed by the root CA. Existing leaf certs remain valid.
	RenewIntermediate() error

	// RootFingerprint returns the SHA-256 fingerprint of the root CA cert
	// formatted as colon-separated hex (e.g. "AB:CD:EF:...").
	RootFingerprint() string

	// RootCommonName returns the CN of the root CA certificate.
	RootCommonName() string

	// LeafLifetime returns the configured leaf certificate lifetime.
	LeafLifetime() time.Duration

	// IntermediateLifetime returns the configured intermediate CA lifetime.
	IntermediateLifetime() time.Duration

	// InstallRoot installs the root CA certificate into the system trust store.
	InstallRoot(cert *x509.Certificate) error

	// UninstallRoot removes the root CA certificate from the system trust store.
	UninstallRoot(cert *x509.Certificate) error
}
