package imageref

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type stubManifests struct {
	raw map[string][]byte
	err error
}

func (f *stubManifests) GetManifest(name, reference string) ([]byte, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	raw, ok := f.raw[name+":"+reference]
	if !ok {
		return nil, "", errors.New("missing")
	}
	return raw, "application/vnd.oci.image.manifest.v1+json", nil
}

func TestResolver_PinnedPassesThrough(t *testing.T) {
	r := NewResolver("reg.example.com", &stubManifests{}, nil)
	valid := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err := r.ResolveDigest(context.Background(), "img@"+valid)
	require.NoError(t, err)
	assert.Equal(t, valid, got)

	_, err = r.ResolveDigest(context.Background(), "img@sha256:short")
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
}

func TestResolver_LocalManifestHashes(t *testing.T) {
	r := NewResolver("reg.example.com", &stubManifests{raw: map[string][]byte{"blog/web:1.0": []byte("{}")}}, nil)
	got, err := r.ResolveDigest(context.Background(), "reg.example.com/blog/web:1.0")
	require.NoError(t, err)
	assert.True(t, len(got) == 7+64)

	_, err = r.ResolveDigest(context.Background(), "reg.example.com/blog/missing:1.0")
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
}

func TestResolver_ExternalNeedsRemote(t *testing.T) {
	r := NewResolver("reg.example.com", &stubManifests{}, nil)
	_, err := r.ResolveDigest(context.Background(), "docker.io/library/nginx:1.0")
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
}

func TestResolver_RejectsMalformedRemoteDigest(t *testing.T) {
	r := NewResolver("reg.example.com", &stubManifests{}, &recordingRemote{digest: "sha256:not-a-digest"})
	_, err := r.ResolveDigest(context.Background(), "docker.io/library/nginx:1.0")
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
}

// recordingRemote records whether the connector was consulted.
type recordingRemote struct {
	called bool
	digest string
}

func (r *recordingRemote) ResolveDigest(context.Context, string) (string, error) {
	r.called = true
	if r.digest != "" {
		return r.digest, nil
	}
	return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

// TestResolver_RejectsDisallowedRefsWithoutConnecting proves policy is
// enforced before the remote connector is consulted, including for
// already-pinned digest references.
func TestResolver_RejectsDisallowedRefsWithoutConnecting(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	remote := &recordingRemote{}
	r := NewResolver("reg.example.com", &stubManifests{}, remote).WithPolicy(fixedPolicy(domain.ImageSourcePolicy{
		InstallationRegistry: "reg.example.com",
		AllowedRegistries:    []string{"registry.example.com"},
	}))

	_, err := r.ResolveDigest(context.Background(), "127.0.0.1:12345/private@"+digest)
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed)

	_, err = r.ResolveDigest(context.Background(), "other.example.com/team/app@"+digest)
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed)

	_, err = r.ResolveDigest(context.Background(), "other.example.com/team/app:1.0")
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed)

	assert.False(t, remote.called, "policy rejection must happen before connecting")
}

func TestResolver_AllowsInstallationAndAllowlistedRefs(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifests := &stubManifests{raw: map[string][]byte{"blog/web:1.0": []byte("{}")}}
	remote := &recordingRemote{}
	r := NewResolver("reg.example.com", manifests, remote).WithPolicy(fixedPolicy(domain.ImageSourcePolicy{
		InstallationRegistry: "reg.example.com",
		AllowedRegistries:    []string{"registry.example.com"},
	}))

	got, err := r.ResolveDigest(context.Background(), "reg.example.com/blog/web:1.0")
	require.NoError(t, err)
	assert.True(t, len(got) == 7+64)

	got, err = r.ResolveDigest(context.Background(), "registry.example.com/team/app@"+digest)
	require.NoError(t, err)
	assert.Equal(t, digest, got)
	assert.False(t, remote.called, "digest refs resolve without contacting the connector")
}

// TestResolver_RequireDigestRejectsTags proves the require-digest policy is
// enforced on the manifest reference before it is resolved.
func TestResolver_RequireDigestRejectsTags(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := NewResolver("reg.example.com", &stubManifests{}, &recordingRemote{}).WithPolicy(fixedPolicy(domain.ImageSourcePolicy{
		AllowedRegistries:    []string{"registry.example.com"},
		RequireDigest:        true,
		InstallationRegistry: "reg.example.com",
	}))

	_, err := r.ResolveDigest(context.Background(), "registry.example.com/team/app:1.0")
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed)

	got, err := r.ResolveDigest(context.Background(), "registry.example.com/team/app@"+digest)
	require.NoError(t, err)
	assert.Equal(t, digest, got)
}

func fixedPolicy(policy domain.ImageSourcePolicy) func() domain.ImageSourcePolicy {
	return func() domain.ImageSourcePolicy { return policy }
}

// TestResolver_InstallationAliasResolvesLocally proves a tagged or untagged
// reference on an installation alias (the CI host) resolves through local
// manifest storage, never the remote resolver.
func TestResolver_InstallationAliasResolvesLocally(t *testing.T) {
	manifests := &stubManifests{raw: map[string][]byte{"web:v1": []byte(`{"v":1}`), "web:latest": []byte(`{"v":"latest"}`)}}
	remote := &recordingRemote{}
	r := NewResolver("gordon.example.com", manifests, remote).WithPolicy(fixedPolicy(domain.ImageSourcePolicy{
		InstallationRegistry: "gordon.example.com",
		InstallationAliases:  []string{"ci.example.com"},
	}))

	tagged, err := r.ResolveDigest(context.Background(), "ci.example.com/web:v1")
	require.NoError(t, err)
	primary, err := r.ResolveDigest(context.Background(), "gordon.example.com/web:v1")
	require.NoError(t, err)
	assert.Equal(t, primary, tagged, "an alias names the same local manifest")

	latest, err := r.ResolveDigest(context.Background(), "ci.example.com/web")
	require.NoError(t, err)
	assert.NotEqual(t, tagged, latest, "an untagged reference resolves the latest tag")

	_, err = r.ResolveDigest(context.Background(), "CI.example.com:443/web:v1")
	require.NoError(t, err, "the alias host is compared canonically")
	assert.False(t, remote.called, "installation references never reach the remote resolver")
}

// TestResolver_PolicyProviderIsReadPerCall proves a policy change (a reload)
// reaches the resolver: the alias follows the current policy, and a host
// that is no longer an installation alias is external again.
func TestResolver_PolicyProviderIsReadPerCall(t *testing.T) {
	manifests := &stubManifests{raw: map[string][]byte{"web:v1": []byte("{}")}}
	policy := domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com"}
	r := NewResolver("gordon.example.com", manifests, nil).WithPolicy(func() domain.ImageSourcePolicy { return policy })

	_, err := r.ResolveDigest(context.Background(), "ci.example.com/web:v1")
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed, "no CI alias yet")

	policy = domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com", InstallationAliases: []string{"ci.example.com"}}
	_, err = r.ResolveDigest(context.Background(), "ci.example.com/web:v1")
	require.NoError(t, err)

	policy = domain.ImageSourcePolicy{InstallationRegistry: "gordon.example.com", InstallationAliases: []string{"ci2.example.com"}, AllowedRegistries: []string{"ci.example.com"}}
	_, err = r.ResolveDigest(context.Background(), "ci2.example.com/web:v1")
	require.NoError(t, err)
	_, err = r.ResolveDigest(context.Background(), "ci.example.com/web:v1")
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable, "a former alias is an external reference")
	assert.Contains(t, err.Error(), "needs remote resolution")
}
