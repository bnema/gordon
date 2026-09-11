package deployment

import (
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestValidateImageSource_RequireDigestAppliesToTags(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	svc := imagePolicyService(domain.ImageSourcePolicy{
		RequireDigest:        true,
		InstallationRegistry: "registry.example.com",
	})
	assert.ErrorIs(t, svc.validateImageSource("registry.example.com/team/app:1.0", ""), domain.ErrAppImageNotAllowed)
	require.NoError(t, svc.validateImageSource("registry.example.com/team/app", digest))
}
