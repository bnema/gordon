package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// OIDCProvider performs issuer-scoped discovery and key retrieval. Both calls
// receive the issuer policy so AllowPrivateNetwork applies to every dial,
// including a discovered JWKS endpoint on a different host.
type OIDCProvider interface {
	Discover(ctx context.Context, issuer domain.TrustedIssuer) (jwksURI string, err error)
	FetchJWKS(ctx context.Context, issuer domain.TrustedIssuer, jwksURI string) ([]byte, error)
}
