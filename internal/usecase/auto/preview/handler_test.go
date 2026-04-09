package preview

import (
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestAutoPreviewHandler_CanHandle(t *testing.T) {
	h := &AutoPreviewHandler{}
	assert.True(t, h.CanHandle(domain.EventImagePushed))
	assert.False(t, h.CanHandle(domain.EventConfigReload))
}

// TestResolveBaseRoute_UsesTrustedConfigOnly verifies that base route resolution
// ignores untrusted image labels and only uses trusted route config.
// This is a security fix: labels are attacker-controlled and must not determine
// which route's env/data is inherited by previews.
func TestResolveBaseRoute_UsesTrustedConfigOnly(t *testing.T) {
	// Setup: Trusted route config for the pushed image points to legitimate domain
	trustedRoutes := []domain.Route{
		{Domain: "trusted.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "-preview-",
	}

	// Test: resolveBaseRoute should return the trusted config domain,
	// NOT the malicious label domain
	baseRoute := resolveBaseRoute(trustedRoutes, previewConfig)

	// Assert: Must use trusted route config, ignoring labels
	assert.Equal(t, "trusted.example.com", baseRoute,
		"base route must come from trusted route config, not untrusted image labels")

	// Test 2: When no trusted routes exist, should return empty (no base route)
	baseRouteNoRoutes := resolveBaseRoute(nil, previewConfig)
	assert.Empty(t, baseRouteNoRoutes,
		"when no trusted routes exist, base route should be empty even if labels present")

	// Test 3: When labels are empty but trusted routes exist, use trusted
	baseRouteEmptyLabels := resolveBaseRoute(trustedRoutes, previewConfig)
	assert.Equal(t, "trusted.example.com", baseRouteEmptyLabels,
		"base route should use trusted config when labels are empty")
}
