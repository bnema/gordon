package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// TargetResolver is the outbound port for resolving domains to proxy targets.
// It is implemented by the CoreService gRPC client in the proxy component.
type TargetResolver interface {
	// GetTarget resolves a domain to its proxy target (container address, port, etc.)
	GetTarget(ctx context.Context, domain string) (*domain.ProxyTarget, error)

	// GetRoutes returns all registered routes from the core
	GetRoutes(ctx context.Context) ([]domain.Route, error)

	// GetExternalRoutes returns the external route mappings (domain → target URL)
	GetExternalRoutes(ctx context.Context) (map[string]string, error)
}
