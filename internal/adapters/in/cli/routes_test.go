package cli

import (
	"testing"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/stretchr/testify/assert"
)

func TestTruncateImage(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		maxLen   int
		expected string
	}{
		// Basic cases - no truncation needed
		{
			name:     "short image fits",
			image:    "nginx:latest",
			maxLen:   20,
			expected: "nginx:latest",
		},
		{
			name:     "exact fit",
			image:    "nginx:latest",
			maxLen:   12,
			expected: "nginx:latest",
		},

		// Regular tag truncation
		{
			name:     "truncate long tag with ellipsis",
			image:    "registry.test.com/test:v1234567890",
			maxLen:   30,
			expected: "registry.test.com/test:v123...",
		},
		{
			name:     "truncate very long image",
			image:    "registry.example.com/organization/project/image:v1.2.3-beta.4",
			maxLen:   35,
			expected: "registry.example.com/organizatio...",
		},

		// Digest truncation
		{
			name:     "digest shortened to 12 chars",
			image:    "myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			maxLen:   50,
			expected: "myapp@sha256:a3ed95caeb02",
		},
		{
			name:     "digest truncated with ellipsis when too long",
			image:    "registry.example.com/org/app@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			maxLen:   35,
			expected: "registry.example.com/org/app@sha...",
		},
		{
			name:     "short digest fits",
			image:    "app@sha256:abc123",
			maxLen:   30,
			expected: "app@sha256:abc123",
		},

		// Edge cases
		{
			name:     "maxLen zero returns empty",
			image:    "nginx:latest",
			maxLen:   0,
			expected: "",
		},
		{
			name:     "maxLen negative returns empty",
			image:    "nginx:latest",
			maxLen:   -1,
			expected: "",
		},
		{
			name:     "maxLen 3 or less - no ellipsis",
			image:    "nginx:latest",
			maxLen:   3,
			expected: "ngi",
		},
		{
			name:     "maxLen 4 - truncate with ellipsis",
			image:    "nginx:latest",
			maxLen:   4,
			expected: "n...",
		},
		{
			name:     "empty image",
			image:    "",
			maxLen:   10,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateImage(tt.image, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTTPHealthToStatus(t *testing.T) {
	tests := []struct {
		name     string
		health   *remote.RouteHealth
		expected components.Status
	}{
		{
			name:     "nil health returns unknown",
			health:   nil,
			expected: components.StatusUnknown,
		},
		{
			name:     "zero status no error returns unknown",
			health:   &remote.RouteHealth{HTTPStatus: 0},
			expected: components.StatusUnknown,
		},
		{
			name:     "zero status with error returns error",
			health:   &remote.RouteHealth{HTTPStatus: 0, Error: "connection refused"},
			expected: components.StatusError,
		},
		{
			name:     "200 returns success",
			health:   &remote.RouteHealth{HTTPStatus: 200},
			expected: components.StatusSuccess,
		},
		{
			name:     "301 returns success",
			health:   &remote.RouteHealth{HTTPStatus: 301},
			expected: components.StatusSuccess,
		},
		{
			name:     "500 returns error",
			health:   &remote.RouteHealth{HTTPStatus: 500},
			expected: components.StatusError,
		},
		{
			name:     "404 returns error",
			health:   &remote.RouteHealth{HTTPStatus: 404},
			expected: components.StatusError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := httpHealthToStatus(tt.health)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGroupRoutesByNetwork(t *testing.T) {
	routes := []remote.RouteInfo{
		{Domain: "alpha.dev", Network: "gordon-alpha-dev"},
		{Domain: "beta.dev", Network: "gordon-shared"},
		{Domain: "gamma.dev", Network: "gordon-shared"},
		{Domain: "delta.dev", Network: "gordon-delta-dev"},
	}

	groups, solo := groupRoutesByNetwork(routes)

	assert.Len(t, solo, 2)
	assert.Equal(t, "alpha.dev", solo[0].Domain)
	assert.Equal(t, "delta.dev", solo[1].Domain)

	assert.Len(t, groups, 1)
	assert.Equal(t, "shared", groups[0].name)
	assert.Len(t, groups[0].routes, 2)
}

func TestStripNetworkPrefix(t *testing.T) {
	tests := []struct {
		network  string
		expected string
	}{
		{"gordon-shared-services", "shared-services"},
		{"gordon-my-app-dev", "my-app-dev"},
		{"custom-network", "custom-network"},
		{"gordon-", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			assert.Equal(t, tt.expected, stripNetworkPrefix(tt.network))
		})
	}
}
