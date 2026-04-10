package preview

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
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
}

// TestResolveBaseRoute_AllPreviewDomains tests that when the only non-preview
// route has already been removed (e.g. user deleted base), preview domains
// without a matching base are NOT treated as previews — they're returned as-is.
// This is correct: without a base route in the set, a domain containing the
// separator is just a regular domain name.
func TestResolveBaseRoute_AllPreviewDomains(t *testing.T) {
	// Without the base route "app.example.com" in the set, these domains
	// are not recognized as previews and the first one is returned.
	previewRoutes := []domain.Route{
		{Domain: "app-preview-abc.example.com", Image: "myapp"},
		{Domain: "app-preview-def.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "-preview-",
	}

	baseRoute := resolveBaseRoute(previewRoutes, previewConfig)
	assert.Equal(t, "app-preview-abc.example.com", baseRoute,
		"without base route in set, separator-containing domains are not treated as previews")

	// When the base route IS in the set, preview domains are properly skipped
	routesWithBase := []domain.Route{
		{Domain: "app-preview-abc.example.com", Image: "myapp"},
		{Domain: "app-preview-def.example.com", Image: "myapp"},
		{Domain: "app.example.com", Image: "myapp"},
	}

	baseRoute = resolveBaseRoute(routesWithBase, previewConfig)
	assert.Equal(t, "app.example.com", baseRoute,
		"with base route in set, preview domains are skipped and base is returned")
}

// TestResolveBaseRoute_EmptySeparator tests that when separator is empty,
// no route is treated as a preview route and the first trusted route is returned.
func TestResolveBaseRoute_EmptySeparator(t *testing.T) {
	trustedRoutes := []domain.Route{
		{Domain: "app-preview-abc.example.com", Image: "myapp"},
		{Domain: "trusted.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "",
	}

	baseRoute := resolveBaseRoute(trustedRoutes, previewConfig)
	assert.Equal(t, "app-preview-abc.example.com", baseRoute,
		"with empty separator, no route should be treated as preview domain")
}

// TestResolveBaseRoute_SkipsPreviewDomains tests that when the first route
// is a preview domain (with its base present) but later routes are normal,
// the first normal route is returned.
func TestResolveBaseRoute_SkipsPreviewDomains(t *testing.T) {
	routes := []domain.Route{
		{Domain: "app-preview-abc.example.com", Image: "myapp"},
		{Domain: "app.example.com", Image: "myapp"}, // base route must exist for preview detection
		{Domain: "trusted.example.com", Image: "myapp"},
		{Domain: "backup.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "-preview-",
	}

	baseRoute := resolveBaseRoute(routes, previewConfig)
	assert.Equal(t, "app.example.com", baseRoute,
		"should skip preview domains and return first non-preview route")
}

// TestResolveBaseRoute_FalsePositiveAvoidance tests that a domain containing
// the separator in its first label (e.g. "my--app.example.com") is NOT treated
// as a preview domain when no base route "my.example.com" exists.
func TestResolveBaseRoute_FalsePositiveAvoidance(t *testing.T) {
	routes := []domain.Route{
		{Domain: "my--app.example.com", Image: "myapp"},
		{Domain: "other.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "--",
	}

	baseRoute := resolveBaseRoute(routes, previewConfig)
	assert.Equal(t, "my--app.example.com", baseRoute,
		"my--app.example.com should NOT be treated as preview since no base route 'my.example.com' exists")
}

// TestResolveBaseRoute_ActualPreviewDetected tests that an actual generated
// preview domain IS correctly detected when its base route exists in the set.
func TestResolveBaseRoute_ActualPreviewDetected(t *testing.T) {
	routes := []domain.Route{
		{Domain: "myapp--login.example.com", Image: "myapp"}, // preview of myapp.example.com
		{Domain: "myapp.example.com", Image: "myapp"},        // base route
	}

	previewConfig := domain.PreviewConfig{
		Separator: "--",
	}

	baseRoute := resolveBaseRoute(routes, previewConfig)
	assert.Equal(t, "myapp.example.com", baseRoute,
		"should skip the preview domain and return the base route")
}

// TestResolveBaseRoute_DeterministicWithMultipleRoutes tests that base-route
// selection is deterministic when multiple non-preview routes exist.
func TestResolveBaseRoute_DeterministicWithMultipleRoutes(t *testing.T) {
	// Multiple runs should always return the same route
	routes := []domain.Route{
		{Domain: "myapp--feat.example.com", Image: "myapp"},
		{Domain: "myapp.example.com", Image: "myapp"},
		{Domain: "alias.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "--",
	}

	// Run multiple times to verify determinism
	first := resolveBaseRoute(routes, previewConfig)
	for i := 0; i < 10; i++ {
		result := resolveBaseRoute(routes, previewConfig)
		assert.Equal(t, first, result, "resolveBaseRoute should be deterministic")
	}
	assert.Equal(t, "myapp.example.com", first,
		"resolveBaseRoute should return the first non-preview route in slice order")
	// The preview route should be skipped.
	assert.NotEqual(t, "myapp--feat.example.com", first,
		"preview domain should be skipped")
}
