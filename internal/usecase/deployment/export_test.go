package deployment

import "github.com/bnema/gordon/internal/domain"

// FixedImagePolicy returns a provider that always yields policy, for tests
// that need no reload.
func FixedImagePolicy(policy domain.ImageSourcePolicy) func() domain.ImageSourcePolicy {
	return func() domain.ImageSourcePolicy { return policy }
}
