package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

const testDigest202 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestImageSourcePolicy_RejectsLocalAndPrivateRegistries(t *testing.T) {
	policy := domain.ImageSourcePolicy{}
	refs := []string{
		"localhost:5000/private/app:latest",
		"localhost/private/app:latest",
		"127.0.0.1:12345/private@" + testDigest202,
		"127.0.0.1/private@" + testDigest202,
		"[::1]:5000/private:latest",
		"10.1.2.3/private:latest",
		"192.168.1.10/private:latest",
		"172.16.5.5/private:latest",
		"169.254.1.1/private:latest",
		"0.0.0.0/private:latest",
		"169.254.169.254/latest/meta-data:latest",
	}
	for _, ref := range refs {
		t.Run(ref, func(t *testing.T) {
			assert.ErrorIs(t, policy.ValidateImageSource(ref), domain.ErrAppImageNotAllowed)
		})
	}
}

func TestImageSourcePolicy_EnforcesAllowlist(t *testing.T) {
	policy := domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}}
	require.NoError(t, policy.ValidateImageSource("registry.example.com/team/app:1.0"))
	require.NoError(t, policy.ValidateImageSource("registry.example.com/team/app@"+testDigest202))
	assert.ErrorIs(t, policy.ValidateImageSource("docker.io/library/nginx:1.0"), domain.ErrAppImageNotAllowed)
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
	policy := domain.ImageSourcePolicy{RequireDigest: true}
	require.NoError(t, policy.ValidateImageSource("registry.example.com/team/app@"+testDigest202))
	assert.ErrorIs(t, policy.ValidateImageSource("registry.example.com/team/app:1.0"), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, policy.ValidateImageSource("nginx"), domain.ErrAppImageNotAllowed)
}

func TestImageSourcePolicy_AllowsDockerHubByDefault(t *testing.T) {
	policy := domain.ImageSourcePolicy{}
	require.NoError(t, policy.ValidateImageSource("nginx:1.25"))
	require.NoError(t, policy.ValidateImageSource("library/nginx:1.25"))
	require.NoError(t, policy.ValidateImageSource("docker.io/library/nginx@"+testDigest202))
}

func TestImageSourcePolicy_RejectsMalformedReferences(t *testing.T) {
	policy := domain.ImageSourcePolicy{}
	assert.ErrorIs(t, policy.ValidateImageSource(""), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, policy.ValidateImageSource("   "), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, policy.ValidateImageSource("registry.example.com/BadRepo:1.0"), domain.ErrAppImageNotAllowed)
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
