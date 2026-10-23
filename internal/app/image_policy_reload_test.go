package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/appstate"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/domain"
	cfgusecase "github.com/bnema/gordon/internal/usecase/config"
)

// These tests open a real bbolt app store and filesystem manifest store
// (real I/O), so they run outside synctest.

const imagePolicyBaseTOML = `
[server]
gordon_domain = "gordon.example.com"
[auth]
enabled = true
`

// imagePolicyServices runs the real initApps and initPromotionSnapshot
// wiring for cfg, with one local manifest web:v1 and web:latest.
func imagePolicyServices(t *testing.T, v *viper.Viper, cfg Config) *serviceInit {
	t.Helper()
	cfg.Server.DataDir = t.TempDir()
	configSvc := cfgusecase.NewService(v, nil)
	require.NoError(t, configSvc.Load(t.Context()))
	manifests, err := filesystem.NewManifestStorage(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	require.NoError(t, manifests.PutManifest("web", "v1", "application/vnd.oci.image.manifest.v1+json", []byte(`{"tag":"v1"}`)))
	require.NoError(t, manifests.PutManifest("web", "latest", "application/vnd.oci.image.manifest.v1+json", []byte(`{"tag":"latest"}`)))
	si := &serviceInit{ctx: t.Context(), v: v, cfg: cfg, log: zerowrap.Default(), svc: &services{
		configSvc:       configSvc,
		manifestStorage: manifests,
		internalRegUser: "gordon",
		internalRegPass: "secret",
	}}
	require.NoError(t, si.initApps())
	t.Cleanup(func() {
		require.NoError(t, si.svc.appSvcImpl.Shutdown(context.Background()))
		require.NoError(t, si.svc.appState.(*appstate.Store).Close())
	})
	require.NoError(t, si.initPromotionSnapshot())
	return si
}

func dryRunImage(t *testing.T, si *serviceInit, image string) error {
	t.Helper()
	spec := domain.AppSpec{Name: "blog", Services: []domain.AppService{{
		Name: "web", Image: image, StopGrace: 10 * time.Second,
		Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
	}}}
	_, dry, err := si.svc.appSvcImpl.Apply(t.Context(), spec, []byte("manifest"), true)
	if err == nil {
		require.NotNil(t, dry)
		assert.True(t, dry.Valid)
	}
	return err
}

func withCIDomain(t *testing.T, ciDomain string) (*viper.Viper, Config) {
	t.Helper()
	text := imagePolicyBaseTOML
	if ciDomain != "" {
		text = strings.Replace(text, "[auth]", "ci_domain = \""+ciDomain+"\"\n[auth]", 1)
	}
	return readPromotionConfig(t, text)
}

// TestReload_ImagePolicyFollowsCIDomain proves a reload adding, then
// changing, server.ci_domain reaches apply validation and local resolution
// of tagged and untagged CI-alias references, while other hosts stay
// external.
func TestReload_ImagePolicyFollowsCIDomain(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	v, cfg := withCIDomain(t, "")
	si := imagePolicyServices(t, v, cfg)
	runtime := &reloadRuntime{v: v, svc: si.svc, log: zerowrap.Default()}
	resolver := si.svc.newImageResolver("gordon.example.com")

	require.ErrorIs(t, dryRunImage(t, si, "ci.example.com/web"+digest), domain.ErrAppImageNotAllowed, "no CI domain at startup")
	_, err := resolver.ResolveDigest(t.Context(), "ci.example.com/web:v1")
	require.ErrorIs(t, err, domain.ErrAppImageNotAllowed)

	_, reloaded := withCIDomain(t, "ci.example.com")
	reloaded.Server.DataDir = si.cfg.Server.DataDir
	require.NoError(t, runtime.Apply(t.Context(), reloaded))

	require.NoError(t, dryRunImage(t, si, "ci.example.com/web"+digest))
	assert.Equal(t, []string{"ci.example.com"}, si.svc.ImagePolicy().InstallationAliases)
	tagged, err := resolver.ResolveDigest(t.Context(), "ci.example.com/web:v1")
	require.NoError(t, err)
	primary, err := resolver.ResolveDigest(t.Context(), "gordon.example.com/web:v1")
	require.NoError(t, err)
	assert.Equal(t, primary, tagged)
	latest, err := resolver.ResolveDigest(t.Context(), "ci.example.com/web")
	require.NoError(t, err)
	assert.NotEqual(t, tagged, latest, "the default tag is latest")

	_, changed := withCIDomain(t, "ci2.example.com")
	changed.Server.DataDir = si.cfg.Server.DataDir
	require.NoError(t, runtime.Apply(t.Context(), changed))
	require.ErrorIs(t, dryRunImage(t, si, "ci.example.com/web"+digest), domain.ErrAppImageNotAllowed, "the former CI domain is refused")
	require.NoError(t, dryRunImage(t, si, "ci2.example.com/web"+digest))
	_, err = resolver.ResolveDigest(t.Context(), "ci2.example.com/web:v1")
	require.NoError(t, err)

	// A non-installation, allowlisted host is still an external reference.
	changed.Images.AllowedRegistries = []string{"registry.example.com"}
	require.NoError(t, runtime.Apply(t.Context(), changed))
	_, err = resolver.ResolveDigest(t.Context(), "registry.example.com/web:v1")
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
	assert.Contains(t, err.Error(), "needs remote resolution")
}

// TestReload_InvalidImagePolicyKeepsPrevious proves a reload whose image
// policy does not validate fails and leaves the published policy, and the
// promotion generation published with it, unchanged.
func TestReload_InvalidImagePolicyKeepsPrevious(t *testing.T) {
	v, cfg := withCIDomain(t, "ci.example.com")
	svc := &services{}
	runtime := &reloadRuntime{v: v, svc: svc, log: zerowrap.Default()}
	require.NoError(t, runtime.Apply(t.Context(), cfg))
	beforePolicy := svc.imagePolicy.Load()
	beforePromotion := svc.promotionSnapshot.Load()
	require.NotNil(t, beforePolicy)

	invalid := cfg
	invalid.Server.CIDomain = "ci2.example.com"
	invalid.Images.AllowedRegistries = []string{"bad host/with path"}
	require.ErrorContains(t, runtime.Apply(t.Context(), invalid), "invalid image registry policy")
	assert.Same(t, beforePolicy, svc.imagePolicy.Load())
	assert.Same(t, beforePromotion, svc.promotionSnapshot.Load())
	assert.Equal(t, []string{"ci.example.com"}, svc.ImagePolicy().InstallationAliases)
}

// TestImagePolicy_SnapshotIsImmutable proves the published policy shares no
// slice with the config or with readers.
func TestImagePolicy_SnapshotIsImmutable(t *testing.T) {
	v, cfg := withCIDomain(t, "ci.example.com")
	cfg.Images.AllowedRegistries = []string{"registry.example.com"}
	svc := &services{}
	require.NoError(t, (&reloadRuntime{v: v, svc: svc, log: zerowrap.Default()}).Apply(t.Context(), cfg))

	cfg.Images.AllowedRegistries[0] = "changed.example.com"
	read := svc.ImagePolicy()
	read.AllowedRegistries[0] = "mutated.example.com"
	read.InstallationAliases[0] = "mutated.example.com"
	assert.Equal(t, []string{"registry.example.com"}, svc.ImagePolicy().AllowedRegistries)
	assert.Equal(t, []string{"ci.example.com"}, svc.ImagePolicy().InstallationAliases)
}

// TestImagePolicy_ConcurrentReadsDuringReload runs readers against reloads
// that alternate the CI domain. Under -race it proves the provider is safe
// to read while a reload publishes; every read is one whole generation.
func TestImagePolicy_ConcurrentReadsDuringReload(t *testing.T) {
	v, first := withCIDomain(t, "ci.example.com")
	_, second := withCIDomain(t, "ci2.example.com")
	svc := &services{}
	runtime := &reloadRuntime{v: v, svc: svc, log: zerowrap.Default()}
	require.NoError(t, runtime.Apply(t.Context(), first))

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				policy := svc.ImagePolicy()
				require.Len(t, policy.InstallationAliases, 1)
				alias := policy.InstallationAliases[0]
				require.Contains(t, []string{"ci.example.com", "ci2.example.com"}, alias)
				require.True(t, policy.IsInstallationImage(alias+"/web:v1"))
			}
		})
	}
	for i := range 100 {
		cfg := first
		if i%2 == 0 {
			cfg = second
		}
		require.NoError(t, runtime.Apply(t.Context(), cfg))
	}
	close(stop)
	readers.Wait()
}
