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
}

// TestResolveBaseRoute_AllPreviewDomains tests that when all candidate routes
// look like preview domains, resolveBaseRoute returns empty.
func TestResolveBaseRoute_AllPreviewDomains(t *testing.T) {
	previewRoutes := []domain.Route{
		{Domain: "app-preview-abc.example.com", Image: "myapp"},
		{Domain: "app-preview-def.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "-preview-",
	}

	baseRoute := resolveBaseRoute(previewRoutes, previewConfig)
	assert.Empty(t, baseRoute, "when all routes look like preview domains, should return empty")
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
// looks like a preview domain but later routes are normal, the first normal
// route is returned.
func TestResolveBaseRoute_SkipsPreviewDomains(t *testing.T) {
	routes := []domain.Route{
		{Domain: "app-preview-abc.example.com", Image: "myapp"},
		{Domain: "trusted.example.com", Image: "myapp"},
		{Domain: "backup.example.com", Image: "myapp"},
	}

	previewConfig := domain.PreviewConfig{
		Separator: "-preview-",
	}

	baseRoute := resolveBaseRoute(routes, previewConfig)
	assert.Equal(t, "trusted.example.com", baseRoute,
		"should skip preview domains and return first normal route")
}
