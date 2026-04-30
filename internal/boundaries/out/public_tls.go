// Package out defines output ports (interfaces) for infrastructure.
// These interfaces define the contract between use cases and driven adapters.
package out

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// CertificateOrder represents a request to obtain or renew a certificate.
type CertificateOrder struct {
	ID        string
	Names     []string
	Challenge domain.ACMEChallengeMode
}

// StoredCertificate represents a persisted certificate with its metadata.
type StoredCertificate struct {
	ID            string
	Names         []string
	Challenge     domain.ACMEChallengeMode
	Certificate   tls.Certificate
	CertPEM       []byte
	ChainPEM      []byte
	FullchainPEM  []byte
	PrivateKeyPEM []byte
	NotAfter      time.Time
	LastError     string
}

// ACMEAccount represents a stored ACME account registration.
type ACMEAccount struct {
	Email           string
	PrivateKeyPEM   []byte
	RegistrationURI string
	BodyJSON        []byte
}

// SecretValue holds a resolved secret value and its source.
type SecretValue struct {
	Value  string
	Source domain.ACMETokenSource
}

// CloudflareZone represents a Cloudflare DNS zone.
type CloudflareZone struct {
	ID   string
	Name string
}

// HTTPChallengeSink defines the contract for storing HTTP-01 challenge tokens
// while an ACME issuer completes domain validation.
type HTTPChallengeSink interface {
	// Present stores the key authorization for a challenge token.
	Present(token, keyAuth string) error

	// CleanUp removes the key authorization for a challenge token.
	CleanUp(token string) error
}

// PublicCertificateIssuer defines the contract for obtaining and renewing
// TLS certificates via ACME.
type PublicCertificateIssuer interface {
	// Obtain requests a new certificate for the given order.
	Obtain(ctx context.Context, order CertificateOrder) (*StoredCertificate, error)

	// Renew renews an existing certificate.
	Renew(ctx context.Context, cert StoredCertificate) (*StoredCertificate, error)
}

// CertificateStore defines the contract for persisting ACME accounts and certificates.
type CertificateStore interface {
	// LoadAccount retrieves an ACME account.
	LoadAccount(ctx context.Context) (*ACMEAccount, error)

	// SaveAccount persists an ACME account.
	SaveAccount(ctx context.Context, account ACMEAccount) error

	// LoadAll returns all stored certificates.
	LoadAll(ctx context.Context) ([]StoredCertificate, error)

	// Save persists a certificate.
	Save(ctx context.Context, cert StoredCertificate) error

	// Lock acquires a lock to prevent concurrent certificate operations.
	Lock(ctx context.Context) (unlock func() error, err error)
}

// SecretResolver defines the contract for resolving Cloudflare API tokens.
type SecretResolver interface {
	// ResolveCloudflareToken retrieves the Cloudflare API token from the configured source.
	ResolveCloudflareToken(ctx context.Context) (SecretValue, error)
}

// CloudflareZoneResolver defines the contract for resolving DNS zones.
type CloudflareZoneResolver interface {
	// FindZone finds the Cloudflare zone for the given domain.
	FindZone(ctx context.Context, domain string) (CloudflareZone, error)
}
