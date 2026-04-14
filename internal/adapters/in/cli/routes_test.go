package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{
			name:     "100 informational returns error",
			health:   &remote.RouteHealth{HTTPStatus: 100},
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

func TestResolveRoutesExplicitRemote_AllowsAdHocURLWhenSavedRemotesConfigUnreadable(t *testing.T) {
	originalRemoteFlag := remoteFlag
	originalTokenFlag := tokenFlag
	originalInsecureTLSFlag := insecureTLSFlag
	t.Cleanup(func() {
		remoteFlag = originalRemoteFlag
		tokenFlag = originalTokenFlag
		insecureTLSFlag = originalInsecureTLSFlag
	})

	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "https://ad-hoc.example.com")

	configPath := filepath.Join(configHome, "gordon", "remotes.toml")
	require.NoError(t, os.MkdirAll(configPath, 0o755))

	resolved, ok := resolveRoutesExplicitRemote()
	require.True(t, ok)
	require.NotNil(t, resolved)
	assert.Equal(t, "https://ad-hoc.example.com", resolved.URL)
	assert.Empty(t, resolved.Name)
}

func TestRouteStatusTitle_PreservesProbeFailureError(t *testing.T) {
	item := routeStatusItem{
		Domain:          "app.example.com",
		Image:           "app:latest",
		ContainerStatus: "running",
		HTTPStatus:      0,
		HealthError:     "connection refused",
	}

	expected := components.StatusIcon(styles.IconHTTPStatus, components.StatusError) + " " +
		components.StatusIcon(styles.IconContainerStatus, components.ParseStatus("running")) +
		" app.example.com"

	assert.Equal(t, expected, routeStatusTitle(item))
}

func TestCollectRoutesListSections_DefaultModeIncludesLocalThenSortedRemotes(t *testing.T) {
	testsDeps := routesListDeps{
		explicitRemote: func() (*remote.ResolvedRemote, bool) {
			return nil, false
		},
		loadLocal: func(context.Context, string) (routeListSection, error) {
			return routeListSection{
				Kind: "local",
				Name: "local",
				Routes: []routeListItem{{
					Domain: "app.local",
					Image:  "myapp:latest",
				}},
			}, nil
		},
		listRemotes: func() (map[string]remote.RemoteEntry, string, error) {
			return map[string]remote.RemoteEntry{
				"igor":        {URL: "https://gordon.supri.xyz"},
				"hetzner-vps": {URL: "https://reg.bnema.dev"},
			}, "igor", nil
		},
		loadRemote: func(_ context.Context, name string, entry remote.RemoteEntry) (routeListSection, error) {
			if name == "hetzner-vps" {
				return routeListSection{
					Kind:  "remote",
					Name:  name,
					URL:   entry.URL,
					Error: "remote unavailable",
				}, nil
			}
			return routeListSection{
				Kind: "remote",
				Name: name,
				URL:  entry.URL,
				Routes: []routeListItem{{
					Domain: "grafana.supri.xyz",
					Image:  "grafana",
				}},
			}, nil
		},
	}

	sections, err := collectRoutesListSections(context.Background(), "", testsDeps)

	require.NoError(t, err)
	require.Len(t, sections, 3)
	assert.Equal(t, "local", sections[0].Name)
	assert.Equal(t, "hetzner-vps", sections[1].Name)
	assert.Equal(t, "igor", sections[2].Name)
	assert.Equal(t, "remote unavailable", sections[1].Error)
	assert.Equal(t, "grafana.supri.xyz", sections[2].Routes[0].Domain)
}

func TestCollectRoutesListSections_ExplicitRemoteSkipsAggregate(t *testing.T) {
	testsDeps := routesListDeps{
		explicitRemote: func() (*remote.ResolvedRemote, bool) {
			return &remote.ResolvedRemote{
				Name: "igor",
				URL:  "https://gordon.supri.xyz",
			}, true
		},
		loadLocal: func(context.Context, string) (routeListSection, error) {
			t.Fatal("loadLocal should not be called when an explicit remote is selected")
			return routeListSection{}, nil
		},
		listRemotes: func() (map[string]remote.RemoteEntry, string, error) {
			t.Fatal("listRemotes should not be called when an explicit remote is selected")
			return nil, "", nil
		},
		loadRemote: func(_ context.Context, name string, entry remote.RemoteEntry) (routeListSection, error) {
			return routeListSection{
				Kind: "remote",
				Name: name,
				URL:  entry.URL,
				Routes: []routeListItem{{
					Domain: "test.supri.xyz",
					Image:  "hello-test",
				}},
			}, nil
		},
	}

	sections, err := collectRoutesListSections(context.Background(), "", testsDeps)

	require.NoError(t, err)
	require.Len(t, sections, 1)
	assert.Equal(t, "igor", sections[0].Name)
	assert.Equal(t, "test.supri.xyz", sections[0].Routes[0].Domain)
}

func TestRenderRoutesStatusSections_IncludesSectionHeadingsAndErrors(t *testing.T) {
	sections := []routeStatusSection{
		{
			Kind: "local",
			Name: "local",
			Routes: []routeStatusItem{{
				Domain:          "app.local",
				Image:           "myapp:latest",
				ContainerStatus: "running",
			}},
		},
		{
			Kind:  "remote",
			Name:  "igor",
			URL:   "https://gordon.supri.xyz",
			Error: "dial tcp timeout",
		},
	}

	var out bytes.Buffer
	err := renderRoutesStatusSections(&out, sections)

	require.NoError(t, err)
	rendered := stripANSI(out.String())
	lines := strings.Split(rendered, "\n")

	lineIndex := func(substr string) int {
		for i, line := range lines {
			if strings.Contains(line, substr) {
				return i
			}
		}
		return -1
	}

	titleIdx := lineIndex("Route Status")
	require.NotEqual(t, -1, titleIdx)

	localIdx := lineIndex("Local")
	require.NotEqual(t, -1, localIdx)

	routeIdx := lineIndex("app.local")
	require.NotEqual(t, -1, routeIdx)

	remoteIdx := lineIndex("Remote: igor")
	require.NotEqual(t, -1, remoteIdx)

	errorIdx := lineIndex("dial tcp timeout")
	require.NotEqual(t, -1, errorIdx)

	assert.Less(t, titleIdx, localIdx)
	assert.Less(t, localIdx, routeIdx)
	assert.Less(t, routeIdx, remoteIdx)
	assert.Less(t, remoteIdx, errorIdx)
}

func TestBuildRouteStatusTree_DeterministicAcrossInputOrder(t *testing.T) {
	routes := []routeStatusItem{
		{Domain: "zulu.example", Image: "zulu:v1", Network: "gordon-zulu", ContainerStatus: "running"},
		{Domain: "alpha.example", Image: "alpha:v1", Network: "gordon-alpha", ContainerStatus: "running"},
		{Domain: "mike.example", Image: "solo:v1", ContainerStatus: "running"},
	}

	var outA bytes.Buffer
	_, _ = outA.WriteString(stripANSI(buildRouteStatusTree(routes).Render()))

	var outB bytes.Buffer
	reordered := []routeStatusItem{routes[2], routes[0], routes[1]}
	_, _ = outB.WriteString(stripANSI(buildRouteStatusTree(reordered).Render()))

	assert.Equal(t, outA.String(), outB.String())
}
