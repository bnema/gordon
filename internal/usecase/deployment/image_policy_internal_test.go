package deployment

import (
	"context"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func imagePolicyService(policy domain.ImageSourcePolicy) *Service {
	return NewService(Deps{ImagePolicy: FixedImagePolicy(policy)}, zerowrap.Default())
}

func (s *Service) validateImageSource(image, digest string) error {
	return validateImageSource(s.imagePolicy(), image, digest)
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
		ImagePolicy: FixedImagePolicy(domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com"}),
	}, zerowrap.Default())

	_, err := svc.pullImage(context.Background(), svc.imagePolicy(), "registry.example.com/team/app@sha256:"+strings.Repeat("a", 64))

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
		ImagePolicy: FixedImagePolicy(domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com"}),
	}, zerowrap.Default())

	_, err := svc.preflightImage(context.Background(), "gordon.example.com/team/app:1.0", "sha256:short")

	assert.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
}

// TestPreflightImage_FollowsPublishedImagePolicy proves a reload that adds
// or changes the CI domain reaches the deploy pull path: a CI-alias
// reference is an installation image pulled through the internal registry,
// and a former alias is refused.
func TestPreflightImage_FollowsPublishedImagePolicy(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	runtime := outmocks.NewMockContainerRuntime(t)
	policy := domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com"}
	svc := NewService(Deps{
		Runtime:     runtime,
		Registry:    RegistryConfig{Domain: "gordon.example.com", PullAddress: "127.0.0.1:5000", Username: "gordon", Password: "secret"},
		ImagePolicy: func() domain.ImageSourcePolicy { return policy },
	}, zerowrap.Default())

	_, err := svc.preflightImage(context.Background(), "ci.example.com/web:v1", digest)
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed, "no CI domain yet")

	policy = domain.ImageSourcePolicy{InstallationAliases: []string{"ci.example.com"}}
	request := domain.ImagePullRequest{Reference: "127.0.0.1:5000/web@" + digest, Username: "gordon", Password: "secret", Transport: domain.ImagePullTransportHTTP}
	runtime.EXPECT().PullImageWithOptions(mock.Anything, request).Return(nil).Once()
	runtime.EXPECT().VerifyImageDigest(mock.Anything, "127.0.0.1:5000/web@"+digest, digest).Return(nil).Once()
	got, err := svc.preflightImage(context.Background(), "ci.example.com/web:v1", digest)
	require.NoError(t, err, "the default installation registry still applies to the read policy")
	assert.Equal(t, "127.0.0.1:5000/web@"+digest, got)

	policy = domain.ImageSourcePolicy{InstallationAliases: []string{"ci2.example.com"}}
	_, err = svc.preflightImage(context.Background(), "ci.example.com/web:v1", digest)
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed, "the former CI domain is refused")

	// A non-installation allowlisted host stays external: anonymous pull.
	policy = domain.ImageSourcePolicy{InstallationAliases: []string{"ci2.example.com"}, AllowedRegistries: []string{"registry.example.com"}}
	runtime.EXPECT().PullImage(mock.Anything, "registry.example.com/team/app@"+digest).Return(nil).Once()
	got, err = svc.preflightImage(context.Background(), "registry.example.com/team/app:1", digest)
	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/team/app@"+digest, got)
}
