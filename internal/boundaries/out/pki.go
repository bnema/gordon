package out

import (
	"crypto/tls"
	"time"
)

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
}
