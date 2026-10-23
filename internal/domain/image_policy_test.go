package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

const testDigest202 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestImageSourcePolicy_DefaultRegistries(t *testing.T) {
	policy := domain.ImageSourcePolicy{}
	allowed := []string{
		"nginx:1.25",
		"docker.io/library/nginx:1.25",
		"registry-1.docker.io/library/nginx@" + testDigest202,
		"ghcr.io/example/app:1",
		"quay.io/example/app:1",
	}
	for _, ref := range allowed {
		require.NoError(t, policy.ValidateImageSource(ref), ref)
	}
	assert.ErrorIs(t, policy.ValidateImageSource("registry.example.com/team/app:1"), domain.ErrAppImageNotAllowed)
}

func TestImageSourcePolicy_AllowsExplicitPrivateRegistry(t *testing.T) {
	policy := domain.ImageSourcePolicy{AllowedRegistries: []string{"REGISTRY.INTERNAL.:5000"}}
	require.NoError(t, policy.ValidateImageSource("registry.internal:5000/team/app:1"))
	assert.ErrorIs(t, policy.ValidateImageSource("registry.internal/team/app:1"), domain.ErrAppImageNotAllowed)
}

func TestImageSourcePolicy_EnforcesAllowlist(t *testing.T) {
	policy := domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}}
	require.NoError(t, policy.ValidateImageSource("registry.example.com/team/app:1.0"))
	require.NoError(t, policy.ValidateImageSource("registry.example.com/team/app@"+testDigest202))
	require.NoError(t, policy.ValidateImageSource("docker.io/library/nginx:1.0"))
	assert.ErrorIs(t, policy.ValidateImageSource("other.example.com/team/app:1.0"), domain.ErrAppImageNotAllowed)
}

func TestImageSourcePolicy_AlwaysAllowsInstallationRegistry(t *testing.T) {
	policy := domain.ImageSourcePolicy{
		AllowedRegistries:    []string{"registry.example.com"},
		InstallationRegistry: "gordon.example.com",
	}
	require.NoError(t, policy.ValidateImageSource("gordon.example.com/blog/web:1.4.2"))
	require.NoError(t, policy.ValidateImageSource("gordon.example.com/blog/web@"+testDigest202))
}

func TestImageSourcePolicy_RequireDigest(t *testing.T) {
	policy := domain.ImageSourcePolicy{
		AllowedRegistries:    []string{"registry.example.com"},
		RequireDigest:        true,
		InstallationRegistry: "gordon.example.com",
	}
	require.NoError(t, policy.ValidateImageSource("registry.example.com/team/app@"+testDigest202))
	require.NoError(t, policy.ValidateImageSource("gordon.example.com/team/app@"+testDigest202))
	assert.ErrorIs(t, policy.ValidateImageSource("registry.example.com/team/app:1.0"), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, policy.ValidateImageSource("gordon.example.com/team/app:1.0"), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, policy.ValidateImageSource("nginx"), domain.ErrAppImageNotAllowed)
}

func TestImageSourcePolicy_RejectsMalformedDigests(t *testing.T) {
	policy := domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com"}
	for _, digest := range []string{
		"sha256:short",
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaag",
		"sha512:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		assert.ErrorIs(t, policy.ValidateImageSource("gordon.example.com/team/app@"+digest), domain.ErrAppImageNotAllowed, digest)
	}
}

func TestImageSourcePolicy_CanonicalizesHostAndDefaultPort(t *testing.T) {
	policy := domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}}
	require.NoError(t, policy.ValidateImageSource("REGISTRY.EXAMPLE.COM./team/app:1"))
	require.NoError(t, policy.ValidateImageSource("registry.example.com:443/team/app:1"))
}

func TestImageSourcePolicy_RejectsMalformedAllowlistEntries(t *testing.T) {
	for _, entry := range []string{"https://registry.example.com", "user@registry.example.com", "registry.example.com:", "registry.example.com:0443", "registry.example.com:443:80"} {
		policy := domain.ImageSourcePolicy{AllowedRegistries: []string{entry}}
		assert.ErrorIs(t, policy.Validate(), domain.ErrAppImageNotAllowed, entry)
	}
}

func TestImageSourcePolicy_RejectsMalformedReferences(t *testing.T) {
	policy := domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}}
	refs := []string{"", "   ", "registry.example.com/BadRepo:1.0", "https://registry.example.com/app:1", "user@registry.example.com/app:1", "registry.example.com:/app:1", "registry.example.com:0443/app:1", "registry.example.com:443:80/app:1", "[::1/app:1"}
	for _, ref := range refs {
		assert.ErrorIs(t, policy.ValidateImageSource(ref), domain.ErrAppImageNotAllowed, ref)
	}
}

func TestImageSourcePolicy_IsInstallationImageDockerHubAlias(t *testing.T) {
	cases := []struct {
		name         string
		installation string
		ref          string
		want         bool
	}{
		{"implicit docker.io ref matches docker.io installation", "docker.io", "library/nginx:1", true},
		{"registry-1 ref matches docker.io installation", "docker.io", "registry-1.docker.io/library/nginx:1", true},
		{"docker.io ref matches registry-1 installation", "registry-1.docker.io", "docker.io/library/nginx:1", true},
		{"installation registry is not docker.io", "gordon.example.com", "docker.io/library/nginx:1", false},
		{"foreign ref does not match docker.io installation", "docker.io", "ghcr.io/example/app:1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := domain.ImageSourcePolicy{InstallationRegistry: tc.installation}
			assert.Equal(t, tc.want, policy.IsInstallationImage(tc.ref))
		})
	}
}

func TestImageSourcePolicy_InstallationAliases(t *testing.T) {
	policy := domain.ImageSourcePolicy{
		InstallationRegistry: "gordon.example.com",
		InstallationAliases:  []string{"ci.example.com", ""},
	}
	require.NoError(t, policy.Validate())
	assert.True(t, policy.IsInstallationImage("gordon.example.com/web@"+testDigest202))
	assert.True(t, policy.IsInstallationImage("CI.example.com./web@"+testDigest202), "aliases are canonicalized")
	assert.False(t, policy.IsInstallationImage("other.example.com/web@"+testDigest202))
	require.NoError(t, policy.ValidateImageSource("ci.example.com/web@"+testDigest202), "an alias is an allowed registry")

	bad := domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com", InstallationAliases: []string{"bad host/"}}
	assert.ErrorIs(t, bad.Validate(), domain.ErrAppImageNotAllowed)
}

func TestIsLocalOrPrivateHost(t *testing.T) {
	local := []string{"localhost", "sub.localhost", "127.0.0.1", "127.0.0.1:5000", "[::1]:5000", "10.0.0.1", "192.168.0.1", "172.31.0.1", "fc00::1", "0.0.0.0", "169.254.169.254"}
	for _, host := range local {
		assert.True(t, domain.IsLocalOrPrivateHost(host), "host %q should be local/private", host)
	}
	remote := []string{"registry.example.com", "registry.example.com:5000", "8.8.8.8", "docker.io", "2001:4860:4860::8888"}
	for _, host := range remote {
		assert.False(t, domain.IsLocalOrPrivateHost(host), "host %q should be public", host)
	}
}

func TestImageSourcePolicy_CloneIsIndependent(t *testing.T) {
	policy := domain.ImageSourcePolicy{
		AllowedRegistries:    []string{"registry.example.com"},
		InstallationRegistry: "gordon.example.com",
		InstallationAliases:  []string{"ci.example.com"},
		RequireDigest:        true,
	}
	clone := policy.Clone()
	assert.Equal(t, policy, clone)
	policy.AllowedRegistries[0] = "changed.example.com"
	policy.InstallationAliases[0] = "changed.example.com"
	assert.Equal(t, []string{"registry.example.com"}, clone.AllowedRegistries)
	assert.Equal(t, []string{"ci.example.com"}, clone.InstallationAliases)
	assert.Equal(t, domain.ImageSourcePolicy{}, domain.ImageSourcePolicy{}.Clone())
}

func TestParseImageReference_IndependentOfHost(t *testing.T) {
	cases := []struct {
		ref  string
		want domain.ImageReference
	}{
		{"ci.example.com/web:v1", domain.ImageReference{Host: "ci.example.com", Repository: "web", Tag: "v1"}},
		{"ci.example.com/web", domain.ImageReference{Host: "ci.example.com", Repository: "web"}},
		{"CI.example.com:443/team/web:v1", domain.ImageReference{Host: "ci.example.com", Repository: "team/web", Tag: "v1"}},
		{"gordon.example.com:5000/team/web@" + testDigest202, domain.ImageReference{Host: "gordon.example.com:5000", Repository: "team/web", Digest: testDigest202}},
		{"nginx:1.0", domain.ImageReference{Host: "docker.io", Repository: "nginx", Tag: "1.0"}},
	}
	for _, tc := range cases {
		got, err := domain.ParseImageReference(tc.ref)
		require.NoError(t, err, tc.ref)
		assert.Equal(t, tc.want, got, tc.ref)
	}
	for _, bad := range []string{"", "https://ci.example.com/web", "ci.example.com/", "ci.example.com/Web:v1"} {
		_, err := domain.ParseImageReference(bad)
		assert.Error(t, err, bad)
	}
}
