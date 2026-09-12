package deployment

import (
	"context"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func imagePolicyService(policy domain.ImageSourcePolicy) *Service {
	return NewService(Deps{ImagePolicy: policy}, zerowrap.Default())
}

// TestValidateImageSource_RejectsPrivateRegistryDigest proves the image
// policy runs on the shared preflight path used by deploy, boot, restart,
// and recovery, so a digest-pinned local/private reference is refused
// before any pull.
func TestValidateImageSource_RejectsPrivateRegistryDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	svc := imagePolicyService(domain.ImageSourcePolicy{
		AllowedRegistries:    []string{"registry.example.com"},
		InstallationRegistry: "gordon.example.com",
	})

	assert.ErrorIs(t, svc.validateImageSource("127.0.0.1:12345/private", digest), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, svc.validateImageSource("169.254.169.254/metadata", digest), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, svc.validateImageSource("other.example.com/team/app", digest), domain.ErrAppImageNotAllowed)
	require.NoError(t, svc.validateImageSource("registry.example.com/team/app", digest))
	require.NoError(t, svc.validateImageSource("gordon.example.com/blog/web", digest))
}

func TestPullImage_RechecksRegistryPolicyImmediatelyBeforePull(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{
		Runtime:     runtime,
		Registry:    RegistryConfig{Domain: "gordon.example.com"},
		ImagePolicy: domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com"},
	}, zerowrap.Default())

	_, err := svc.pullImage(context.Background(), "registry.example.com/team/app@sha256:"+strings.Repeat("a", 64))

	assert.ErrorIs(t, err, domain.ErrAppImageNotAllowed)
}

func TestValidateImageSource_RequireDigestAppliesToAllRegistries(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	svc := imagePolicyService(domain.ImageSourcePolicy{
		AllowedRegistries:    []string{"registry.example.com"},
		RequireDigest:        true,
		InstallationRegistry: "gordon.example.com",
	})
	assert.ErrorIs(t, svc.validateImageSource("registry.example.com/team/app:1.0", ""), domain.ErrAppImageNotAllowed)
	assert.ErrorIs(t, svc.validateImageSource("gordon.example.com/team/app:1.0", ""), domain.ErrAppImageNotAllowed)
	require.NoError(t, svc.validateImageSource("registry.example.com/team/app", digest))
	require.NoError(t, svc.validateImageSource("gordon.example.com/team/app", digest))
}

func TestPreflightImage_RejectsMalformedResolvedDigestBeforePull(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{
		Runtime:     runtime,
		Registry:    RegistryConfig{Domain: "gordon.example.com"},
		ImagePolicy: domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com"},
	}, zerowrap.Default())

	_, err := svc.preflightImage(context.Background(), "gordon.example.com/team/app:1.0", "sha256:short")

	assert.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
}
