package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
)

type resolveFromImageTestControlPlane struct {
	getRoute          func(context.Context, string) (*domain.Route, error)
	findRoutesByImage func(context.Context, string) ([]domain.Route, error)
}

var _ ControlPlane = (*resolveFromImageTestControlPlane)(nil)

func (c *resolveFromImageTestControlPlane) ListRoutesWithDetails(context.Context) ([]remote.RouteInfo, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetHealth(context.Context) (map[string]*remote.RouteHealth, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetRoute(ctx context.Context, domainName string) (*domain.Route, error) {
	if c.getRoute != nil {
		return c.getRoute(ctx, domainName)
	}
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) FindRoutesByImage(ctx context.Context, imageName string) ([]domain.Route, error) {
	if c.findRoutesByImage != nil {
		return c.findRoutesByImage(ctx, imageName)
	}
	return nil, nil
}

func (c *resolveFromImageTestControlPlane) AddRoute(context.Context, domain.Route) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) UpdateRoute(context.Context, domain.Route) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) RemoveRoute(context.Context, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) Bootstrap(context.Context, dto.BootstrapRequest) (*dto.BootstrapResponse, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) ListSecretsWithAttachments(context.Context, string) (*remote.SecretsListResult, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) SetSecrets(context.Context, string, map[string]string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) DeleteSecret(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) SetAttachmentSecrets(context.Context, string, string, map[string]string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) DeleteAttachmentSecret(context.Context, string, string, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetAllAttachmentsConfig(context.Context) (map[string][]string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetAttachmentsConfig(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) FindAttachmentTargetsByImage(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) AddAttachment(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) RemoveAttachment(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetAutoRouteAllowedDomains(context.Context) ([]string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) AddAutoRouteAllowedDomain(context.Context, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) RemoveAutoRouteAllowedDomain(context.Context, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetStatus(context.Context) (*remote.Status, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) Reload(context.Context) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetConfig(context.Context) (*remote.Config, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) DeployIntent(context.Context, string) error {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) Deploy(context.Context, string) (*remote.DeployResult, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) Restart(context.Context, string, bool) (*remote.RestartResult, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) ListTags(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) ListBackups(context.Context, string) ([]dto.BackupJob, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) BackupStatus(context.Context) ([]dto.BackupJob, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) RunBackup(context.Context, string, string) (*dto.BackupRunResponse, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) DetectDatabases(context.Context, string) ([]dto.DatabaseInfo, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetProcessLogs(context.Context, int) ([]string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) GetContainerLogs(context.Context, string, int) ([]string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) StreamProcessLogs(context.Context, int) (<-chan string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) StreamContainerLogs(context.Context, string, int) (<-chan string, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) ListVolumes(context.Context) ([]dto.Volume, error) {
	panic("unexpected call")
}

func (c *resolveFromImageTestControlPlane) PruneVolumes(context.Context, dto.VolumePruneRequest) (*dto.VolumePruneResponse, error) {
	panic("unexpected call")
}

func TestValidateBuildArg(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantErr bool
	}{
		{"valid simple", "FOO=bar", false},
		{"valid with underscore key", "_FOO=bar", false},
		{"valid with numbers in key", "FOO123=bar", false},
		{"valid empty value", "FOO=", false},
		{"valid complex value", "FOO=bar baz=qux", false},
		{"invalid no equals", "FOO", true},
		{"invalid starts with number", "1FOO=bar", true},
		{"invalid starts with dash", "-FOO=bar", true},
		{"invalid special chars in key", "FOO-BAR=baz", true},
		{"invalid empty string", "", true},
		{"invalid equals only", "=value", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBuildArg(tt.arg)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		name         string
		image        string
		wantRegistry string
		wantName     string
		wantTag      string
	}{
		{"full ref", "reg.example.com/myapp:v1.0.0", "reg.example.com", "myapp", "v1.0.0"},
		{"no tag", "reg.example.com/myapp", "reg.example.com", "myapp", "latest"},
		{"latest tag", "reg.example.com/myapp:latest", "reg.example.com", "myapp", "latest"},
		{"no slash", "myapp:v1.0.0", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, name, tag := parseImageRef(tt.image)
			assert.Equal(t, tt.wantRegistry, registry)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}

func TestClassifyPushArgument(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want classifiedPushArg
	}{
		{name: "tagged image latest", arg: "myapp:latest", want: classifiedPushArg{kind: pushArgKindImage, lookupImage: "myapp"}},
		{name: "tagged image semver", arg: "myapp:v1.2.3", want: classifiedPushArg{kind: pushArgKindImage, lookupImage: "myapp"}},
		{name: "registry qualified image", arg: "registry.example.com/myapp:v1.2.3", want: classifiedPushArg{kind: pushArgKindImage, lookupImage: "registry.example.com/myapp"}},
		{name: "registry qualified digest", arg: "registry.example.com/myapp@sha256:deadbeef", want: classifiedPushArg{kind: pushArgKindImage, lookupImage: "registry.example.com/myapp@sha256:deadbeef"}},
		{name: "legacy domain", arg: "app.example.com", want: classifiedPushArg{kind: pushArgKindLegacyDomain, legacyDomain: "app.example.com"}},
		{name: "bare image name", arg: "myapp", want: classifiedPushArg{kind: pushArgKindImage, lookupImage: "myapp"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPushArgument(tt.arg)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildAndPush_BuildArgs(t *testing.T) {
	// Verify buildImageArgs produces --load instead of --push
	args := buildImageArgs(context.Background(), "v1.0.0", "linux/amd64", "Dockerfile", []string{"CGO_ENABLED=0"}, "reg.example.com/app:v1.0.0", "reg.example.com/app:latest")

	assert.Contains(t, args, "--load")
	assert.NotContains(t, args, "--push")
	assert.Contains(t, args, "--platform")
	assert.Contains(t, args, "-f")
	assert.Contains(t, args, "Dockerfile")
	assert.Contains(t, args, "VERSION=v1.0.0")
}

func TestBuildImageArgsInjectsGitBuildArgs(t *testing.T) {
	args := buildImageArgs(context.Background(), "v1.2.3", "linux/amd64", "Dockerfile", nil, "registry/img:v1.2.3", "registry/img:latest")

	// Must contain explicit KEY=VALUE for all standard git build args
	argStr := strings.Join(args, " ")
	for _, key := range []string{"VERSION=v1.2.3", "GIT_TAG=v1.2.3", "GIT_SHA=", "BUILD_TIME="} {
		if !strings.Contains(argStr, key) {
			t.Errorf("expected args to contain %q, got: %s", key, argStr)
		}
	}

	// Must NOT contain bare "--build-arg VERSION" (without =value)
	for i, a := range args {
		if a == "--build-arg" && i+1 < len(args) && args[i+1] == "VERSION" {
			t.Error("found bare '--build-arg VERSION' (without =value); should be '--build-arg VERSION=v1.2.3'")
		}
	}
}

func TestBuildImageArgsUserArgsOverrideDefaults(t *testing.T) {
	userArgs := []string{"GIT_TAG=custom-override"}
	args := buildImageArgs(context.Background(), "v1.2.3", "linux/amd64", "Dockerfile", userArgs, "r/i:v1.2.3", "r/i:latest")

	// Count how many times GIT_TAG appears and track the last occurrence index.
	// Docker uses the last occurrence of a duplicate --build-arg key, so the user
	// override must come after the default injected value.
	count := 0
	lastIdx := -1
	for i, a := range args {
		if a == "--build-arg" && i+1 < len(args) && strings.HasPrefix(args[i+1], "GIT_TAG=") {
			count++
			lastIdx = i + 1
		}
	}
	if count < 2 {
		t.Errorf("expected GIT_TAG to appear twice (default + override), got %d", count)
	}
	if lastIdx < 0 || args[lastIdx] != "GIT_TAG="+userArgs[0][len("GIT_TAG="):] {
		t.Errorf("expected last GIT_TAG= arg to be the user override %q, got %q", "GIT_TAG=custom-override", args[lastIdx])
	}
}

func TestParseTagRef(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{name: "github tag ref", ref: "refs/tags/v1.2.3", want: "v1.2.3"},
		{name: "peeled tag ref", ref: "refs/tags/v1.2.3^{}", want: "v1.2.3"},
		{name: "branch ref", ref: "refs/heads/main", want: ""},
		{name: "empty", ref: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseTagRef(tt.ref))
		})
	}
}

func TestVersionFromTagRefs(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "github ref tag",
			env:  map[string]string{"GITHUB_REF": "refs/tags/v2.0.0"},
			want: "v2.0.0",
		},
		{
			name: "github ref type and name",
			env: map[string]string{
				"GITHUB_REF_TYPE": "tag",
				"GITHUB_REF_NAME": "v2.1.0",
			},
			want: "v2.1.0",
		},
		{
			name: "gitlab commit tag",
			env:  map[string]string{"CI_COMMIT_TAG": "v3.0.0"},
			want: "v3.0.0",
		},
		{
			name: "azure source branch tag ref",
			env:  map[string]string{"BUILD_SOURCEBRANCH": "refs/tags/v4.0.0"},
			want: "v4.0.0",
		},
		{
			name: "no tag refs",
			env:  map[string]string{"GITHUB_REF": "refs/heads/main"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionFromTagRefs(func(key string) string {
				return tt.env[key]
			})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildImageArgs_CustomDockerfile(t *testing.T) {
	args := buildImageArgs(context.Background(), "v1.0.0", "linux/amd64", "docker/app/Dockerfile", nil, "reg.example.com/app:v1.0.0", "reg.example.com/app:latest")

	assert.Contains(t, args, "-f")
	assert.Contains(t, args, "docker/app/Dockerfile")
}

func TestParseDockerfileLabels(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected map[string]string
	}{
		{
			name:    "simple label",
			content: `LABEL gordon.domain="myapp.example.com"`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name: "multiple labels",
			content: `LABEL gordon.domain="myapp.example.com"
LABEL gordon.proxy.port="8080"
LABEL gordon.health="/health"`,
			expected: map[string]string{
				"gordon.domain":     "myapp.example.com",
				"gordon.proxy.port": "8080",
				"gordon.health":     "/health",
			},
		},
		{
			name:    "multi-label on one line",
			content: `LABEL gordon.domain="myapp.example.com" gordon.proxy.port="8080"`,
			expected: map[string]string{
				"gordon.domain":     "myapp.example.com",
				"gordon.proxy.port": "8080",
			},
		},
		{
			name:    "unquoted value",
			content: `LABEL gordon.domain=myapp.example.com`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name:    "single-quoted value",
			content: `LABEL gordon.domain='myapp.example.com'`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name:    "non-gordon labels ignored",
			content: `LABEL maintainer="John" gordon.domain="myapp.example.com"`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name:    "comma-separated domains",
			content: `LABEL gordon.domains="app1.example.com,app2.example.com"`,
			expected: map[string]string{
				"gordon.domains": "app1.example.com,app2.example.com",
			},
		},
		{
			name:     "no labels",
			content:  `FROM alpine:latest`,
			expected: map[string]string{},
		},
		{
			name:     "comments and empty lines",
			content:  "# This is a comment\n\nLABEL gordon.domain=\"test.com\"",
			expected: map[string]string{"gordon.domain": "test.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write a temp Dockerfile
			dir := t.TempDir()
			dockerfile := filepath.Join(dir, "Dockerfile")
			err := os.WriteFile(dockerfile, []byte(tt.content), 0644)
			assert.NoError(t, err)

			labels := parseDockerfileLabels(dockerfile)
			assert.Equal(t, tt.expected, labels)
		})
	}
}

func TestParseDockerfileLabels_NonExistent(t *testing.T) {
	labels := parseDockerfileLabels("/nonexistent/Dockerfile")
	assert.Empty(t, labels)
}

func TestDetectImageName_FromDockerfileLabels(t *testing.T) {
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	err := os.WriteFile(dockerfile, []byte(`FROM alpine
LABEL gordon.domain="myapp.example.com"
`), 0644)
	assert.NoError(t, err)

	name, err := detectImageName(dockerfile)
	assert.NoError(t, err)
	assert.Equal(t, "myapp.example.com", name)
}

func TestDetectImageName_FallbackToDir(t *testing.T) {
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	err := os.WriteFile(dockerfile, []byte(`FROM alpine
`), 0644)
	assert.NoError(t, err)

	// When no gordon.domain label, falls back to cwd basename
	name, err := detectImageName(dockerfile)
	assert.NoError(t, err)
	assert.NotEmpty(t, name)
}

func TestSplitLabelPairs(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:     "single pair",
			content:  `gordon.domain="test.com"`,
			expected: []string{`gordon.domain="test.com"`},
		},
		{
			name:     "two pairs",
			content:  `gordon.domain="test.com" gordon.proxy.port="8080"`,
			expected: []string{`gordon.domain="test.com"`, `gordon.proxy.port="8080"`},
		},
		{
			name:     "unquoted",
			content:  `gordon.domain=test.com gordon.proxy.port=8080`,
			expected: []string{`gordon.domain=test.com`, `gordon.proxy.port=8080`},
		},
		{
			name:     "quoted with spaces in value",
			content:  `gordon.domain="my app.com"`,
			expected: []string{`gordon.domain="my app.com"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitLabelPairs(tt.content)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetGitVersionNoTagsReturnsFallbackAndWarns(t *testing.T) {
	ctx := context.Background()

	// Create a temp dir with a git repo that has no tags
	tmpDir := t.TempDir()
	if err := exec.Command("git", "-C", tmpDir, "init").Run(); err != nil { // #nosec G204
		t.Skipf("git init failed: %v", err)
	}
	// Need at least one commit so git describe has something to describe
	if err := exec.Command("git", "-C", tmpDir, "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "--allow-empty", "-m", "init").Run(); err != nil { // #nosec G204
		t.Skipf("git commit failed: %v", err)
	}

	// Change to the tmpDir for this test so getGitVersion uses the tag-less repo
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil { // #nosec G204
		t.Fatal(err)
	}
	defer os.Chdir(origDir) // #nosec G204

	// Redirect stderr to capture the warning
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() {
		w.Close()
		os.Stderr = origStderr
	}()

	v := getGitVersion(ctx)

	w.Close()
	os.Stderr = origStderr
	stderrOutput, _ := io.ReadAll(r)

	// Should return "" (fallback to "latest" handled by determineVersion)
	assert.Equal(t, "", v, "expected empty string fallback when no git tags exist")

	// Should have printed a warning to stderr
	assert.Contains(t, string(stderrOutput), "latest",
		"expected a warning about 'latest' fallback on stderr")
}

func TestParseLabelPair(t *testing.T) {
	tests := []struct {
		name      string
		pair      string
		wantKey   string
		wantValue string
		wantOk    bool
	}{
		{"quoted", `gordon.domain="test.com"`, "gordon.domain", "test.com", true},
		{"unquoted", `gordon.domain=test.com`, "gordon.domain", "test.com", true},
		{"single quoted", `gordon.domain='test.com'`, "gordon.domain", "test.com", true},
		{"empty value", `gordon.domain=`, "gordon.domain", "", true},
		{"no equals", `gordon.domain`, "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, ok := parseLabelPair(tt.pair)
			assert.Equal(t, tt.wantOk, ok)
			if ok {
				assert.Equal(t, tt.wantKey, key)
				assert.Equal(t, tt.wantValue, value)
			}
		})
	}
}

func TestResolveFromImage_NoRouteSuggestsBootstrap(t *testing.T) {
	cp := &resolveFromImageTestControlPlane{
		findRoutesByImage: func(context.Context, string) ([]domain.Route, error) {
			return nil, nil
		},
	}

	_, _, _, err := resolveFromImage(context.Background(), cp, "myapp", "Dockerfile")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), `no route configured for image "myapp"`)
	assert.Contains(t, err.Error(), "gordon bootstrap")
}

func TestResolveRoute_DottedBareImageUsesImageLookup(t *testing.T) {
	cp := &resolveFromImageTestControlPlane{
		findRoutesByImage: func(_ context.Context, imageName string) ([]domain.Route, error) {
			assert.Equal(t, "my.app", imageName)
			return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/my.app:latest"}}, nil
		},
		getRoute: func(context.Context, string) (*domain.Route, error) {
			t.Fatalf("unexpected GetRoute call")
			return nil, nil
		},
	}

	registry, imageName, pushDomain, err := resolveRoute(context.Background(), cp, "my.app", "", "Dockerfile")

	assert.NoError(t, err)
	assert.Equal(t, "registry.example.com", registry)
	assert.Equal(t, "my.app", imageName)
	assert.Equal(t, "app.example.com", pushDomain)
}

func TestResolveRoute_DottedBareImageNoRoutesKeepsBootstrapError(t *testing.T) {
	sawImageLookup := false
	cp := &resolveFromImageTestControlPlane{
		findRoutesByImage: func(_ context.Context, imageName string) ([]domain.Route, error) {
			sawImageLookup = true
			assert.Equal(t, "my.app", imageName)
			return nil, nil
		},
		getRoute: func(_ context.Context, domainName string) (*domain.Route, error) {
			assert.True(t, sawImageLookup, "expected image lookup before legacy domain lookup")
			assert.Equal(t, "my.app", domainName)
			return nil, domain.ErrRouteNotFound
		},
	}

	_, _, _, err := resolveRoute(context.Background(), cp, "my.app", "", "Dockerfile")

	assert.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrNoRouteForImage))
	assert.Contains(t, err.Error(), `no route configured for image "my.app"`)
	assert.Contains(t, err.Error(), "gordon bootstrap")
	assert.NotContains(t, err.Error(), "failed to get route for domain")
}

func TestNoRouteForImageErrorWrapsSentinel(t *testing.T) {
	err := noRouteForImageError("myapp")

	assert.True(t, errors.Is(err, domain.ErrNoRouteForImage))
	assert.Contains(t, err.Error(), `no route configured for image "myapp"`)
}

func TestResolveRoute_DottedBareDomainFallsBackToLegacyLookup(t *testing.T) {
	sawImageLookup := false
	cp := &resolveFromImageTestControlPlane{
		findRoutesByImage: func(_ context.Context, imageName string) ([]domain.Route, error) {
			sawImageLookup = true
			assert.Equal(t, "app.example.com", imageName)
			return nil, nil
		},
		getRoute: func(_ context.Context, domainName string) (*domain.Route, error) {
			assert.True(t, sawImageLookup, "expected image lookup before legacy domain lookup")
			assert.Equal(t, "app.example.com", domainName)
			return &domain.Route{Domain: domainName, Image: "registry.example.com/myapp:latest"}, nil
		},
	}

	registry, imageName, pushDomain, err := resolveRoute(context.Background(), cp, "app.example.com", "", "Dockerfile")

	assert.NoError(t, err)
	assert.Equal(t, "registry.example.com", registry)
	assert.Equal(t, "myapp", imageName)
	assert.Equal(t, "app.example.com", pushDomain)
}

func TestResolveRoute_TaggedImageStripsTagBeforeLookup(t *testing.T) {
	cp := &resolveFromImageTestControlPlane{
		findRoutesByImage: func(_ context.Context, imageName string) ([]domain.Route, error) {
			assert.Equal(t, "myapp", imageName)
			return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/myapp:latest"}}, nil
		},
	}

	registry, imageName, pushDomain, err := resolveRoute(context.Background(), cp, "myapp:v1.2.3", "", "Dockerfile")

	assert.NoError(t, err)
	assert.Equal(t, "registry.example.com", registry)
	assert.Equal(t, "myapp", imageName)
	assert.Equal(t, "app.example.com", pushDomain)
}

func TestResolveRoute_RegistryQualifiedTaggedImageUsesImageLookup(t *testing.T) {
	cp := &resolveFromImageTestControlPlane{
		findRoutesByImage: func(_ context.Context, imageName string) ([]domain.Route, error) {
			assert.Equal(t, "registry.example.com/myapp", imageName)
			return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/myapp:latest"}}, nil
		},
	}

	_, _, _, err := resolveRoute(context.Background(), cp, "registry.example.com/myapp:v1.2.3", "", "Dockerfile")

	assert.NoError(t, err)
}

func TestIsInsufficientScope(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"HTTPError 403", &remote.HTTPError{StatusCode: 403, Status: "403 Forbidden", Body: "insufficient scope"}, true},
		{"wrapped HTTPError 403", fmt.Errorf("deploy intent: %w", &remote.HTTPError{StatusCode: 403, Status: "403 Forbidden", Body: "scope"}), true},
		{"HTTPError 500", &remote.HTTPError{StatusCode: 500, Status: "500 Internal Server Error", Body: "broke"}, false},
		{"plain error", fmt.Errorf("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isInsufficientScope(tt.err))
		})
	}
}
