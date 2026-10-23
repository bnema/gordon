package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// WorkloadIdentityService verifies workload identity tokens (OIDC JWTs issued
// to CI jobs) against the configured trusted issuers.
type WorkloadIdentityService interface {
	// Verify returns the principal of a valid token. It returns
	// domain.ErrInvalidToken for invalid or untrusted tokens and
	// domain.ErrIdentityProviderUnavailable when the issuer keys cannot be
	// obtained (HTTP adapters map these to 401 and 503).
	Verify(ctx context.Context, rawJWT string) (domain.WorkloadPrincipal, error)

	// SetIssuers atomically replaces the trusted issuers. It never blocks on
	// the network; issuer keys are fetched in the background.
	SetIssuers(issuers []domain.TrustedIssuer)
}
