package tokenstore

import (
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// NewStore creates a TokenStore based on the configured backend.
func NewStore(backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (out.TokenStore, error) {
	switch backend {
	case domain.SecretsBackendPass:
		store := NewPassStore(log)
		if !store.IsAvailable() {
			return nil, fmt.Errorf("pass is not available in the system")
		}
		return store, nil

	case domain.SecretsBackendSops:
		// SOPS implementation would go here
		// For now, return an error indicating it's not yet implemented
		return nil, fmt.Errorf("sops backend is not yet implemented")

	case domain.SecretsBackendUnsafe:
		if dataDir == "" {
			return nil, fmt.Errorf("data_dir is required for unsafe backend")
		}
		return NewUnsafeStore(dataDir, log)

	default:
		return nil, fmt.Errorf("unknown secrets backend: %s", backend)
	}
}
