package cli

import (
	"testing"

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

func TestTruncateNetwork(t *testing.T) {
	tests := []struct {
		name     string
		network  string
		maxLen   int
		expected string
	}{
		// Basic cases
		{
			name:     "short network fits",
			network:  "gordon-net",
			maxLen:   20,
			expected: "gordon-net",
		},
		{
			name:     "exact fit",
			network:  "gordon-net",
			maxLen:   10,
			expected: "gordon-net",
		},

		// Truncation
		{
			name:     "truncate long network with ellipsis",
			network:  "gordon-my-very-long-network-name",
			maxLen:   20,
			expected: "gordon-my-very-lo...",
		},

		// Special values
		{
			name:     "empty returns dash",
			network:  "",
			maxLen:   10,
			expected: "-",
		},
		{
			name:     "dash returns dash",
			network:  "-",
			maxLen:   10,
			expected: "-",
		},

		// Edge cases
		{
			name:     "maxLen zero returns empty",
			network:  "gordon-net",
			maxLen:   0,
			expected: "",
		},
		{
			name:     "maxLen negative returns empty",
			network:  "gordon-net",
			maxLen:   -1,
			expected: "",
		},
		{
			name:     "maxLen 3 or less - no ellipsis",
			network:  "gordon-net",
			maxLen:   3,
			expected: "gor",
		},
		{
			name:     "maxLen 4 - truncate with ellipsis",
			network:  "gordon-net",
			maxLen:   4,
			expected: "g...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateNetwork(tt.network, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}
