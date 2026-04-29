// Package in defines input ports (interfaces) for use cases.
// These interfaces define the contract between driving adapters (HTTP, CLI)
// and the business logic (use cases).
package in

import (
	"context"
	"crypto/tls"

	"github.com/bnema/gordon/internal/domain"
)

// PublicTLSService defines the contract for public TLS / ACME certificate management.
type PublicTLSService interface {
	// GetCertificateForHost returns a TLS certificate for the given SNI host.
	GetCertificateForHost(host string) (*tls.Certificate, error)

	// GetHTTP01Challenge returns the key authorization for an HTTP-01 challenge token.
	GetHTTP01Challenge(ctx context.Context, token string) (keyAuth string, ok bool)

	// Status returns the current public TLS status.
	Status(ctx context.Context) domain.PublicTLSStatus

	// Reconcile ensures the desired certificate state matches the actual state.
	Reconcile(ctx context.Context) error

	// Stop gracefully shuts down the public TLS service.
	Stop(ctx context.Context) error
}
