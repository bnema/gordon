package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

const promotionTOML = `
[server]
gordon_domain = "gordon.example"
ci_domain = "ci.example"
[auth]
enabled = true
[[trusted_issuers]]
issuer = "https://issuer.example"
audience = "aud"
[[promotion_policies]]
issuer = "https://issuer.example"
subjects = ["a", "b"]
app = "web"
service = "web"
repository = "web"
`

func readPromotionConfig(t *testing.T, text string) (*viper.Viper, Config) {
	t.Helper()
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(text)))
	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	return v, cfg
}

func TestPromotionConfig(t *testing.T) {
	v, cfg := readPromotionConfig(t, promotionTOML)
	require.NoError(t, validatePromotionConfig(v, cfg))
	snapshot, err := buildPromotionSnapshot(cfg)
	require.NoError(t, err)
	require.Equal(t, time.Hour, snapshot.Issuers()[0].MaxTokenLifetime)
	require.Equal(t, time.Minute, snapshot.Issuers()[0].ClockSkew)
	require.True(t, snapshot.Policies().Allows(domain.WorkloadPrincipal{Issuer: "https://issuer.example", Subject: "a"}, "web", "web", "web"))
	cases := map[string]string{
		"issuer HTTP":        strings.ReplaceAll(promotionTOML, "https://issuer.example", "http://issuer.example"),
		"audience":           strings.Replace(promotionTOML, `audience = "aud"`, `audience = ""`, 1),
		"empty subjects":     strings.Replace(promotionTOML, `["a", "b"]`, `[]`, 1),
		"empty subject":      strings.Replace(promotionTOML, `["a", "b"]`, `[""]`, 1),
		"wildcard":           strings.Replace(promotionTOML, `["a", "b"]`, `["a*"]`, 1),
		"app":                strings.Replace(promotionTOML, `app = "web"`, `app = "Bad"`, 1),
		"service":            strings.Replace(promotionTOML, `service = "web"`, `service = "Bad"`, 1),
		"repository":         strings.Replace(promotionTOML, `repository = "web"`, `repository = "../bad"`, 1),
		"auth":               strings.Replace(promotionTOML, `enabled = true`, `enabled = false`, 1),
		"gordon collision":   strings.Replace(promotionTOML, `ci_domain = "ci.example"`, `ci_domain = "GORDON.EXAMPLE."`, 1),
		"legacy collision":   strings.Replace(promotionTOML, `ci_domain = "ci.example"`, "ci_domain = \"ci.example\"\nlegacy_registry_domains = [\"ci.example\"]", 1),
		"unknown issuer key": strings.Replace(promotionTOML, `audience = "aud"`, "audience = \"aud\"\ntypo = true", 1),
		"unknown policy key": promotionTOML + "typo = true\n",
		"duplicate issuer":   promotionTOML + "\n[[trusted_issuers]]\nissuer = \"https://issuer.example\"\naudience = \"other\"\n",
		"duplicate policy":   promotionTOML + "\n[[promotion_policies]]\nissuer = \"https://issuer.example\"\nsubjects = [\"a\"]\napp = \"web\"\nservice = \"web\"\nrepository = \"other\"\n",
		"bad duration":       strings.Replace(promotionTOML, `audience = "aud"`, "audience = \"aud\"\nclock_skew = \"bad\"", 1),
		"negative lifetime":  strings.Replace(promotionTOML, `audience = "aud"`, "audience = \"aud\"\nmax_token_lifetime = \"-1s\"", 1),
		"invalid jwks":       strings.Replace(promotionTOML, `audience = "aud"`, "audience = \"aud\"\njwks_uri = \"http://issuer.example/keys\"", 1),
	}
	// Change only the policy issuer, leaving the declaration intact.
	cases["undeclared"] = strings.Replace(promotionTOML, "[[promotion_policies]]\nissuer = \"https://issuer.example\"", "[[promotion_policies]]\nissuer = \"https://other.example\"", 1)
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			v, cfg := readPromotionConfig(t, text)
			require.Error(t, validatePromotionConfig(v, cfg))
		})
	}
	v, cfg = readPromotionConfig(t, strings.Replace(promotionTOML, "[[promotion_policies]]\nissuer = \"https://issuer.example\"", "[[promotion_policies]]\nissuer = \"gordon\"", 1))
	require.NoError(t, validatePromotionConfig(v, cfg))
}

func TestPromotionReloadSnapshot(t *testing.T) {
	v, cfg := readPromotionConfig(t, promotionTOML)
	svc := &services{}
	r := &reloadRuntime{v: v, svc: svc}
	require.NoError(t, r.Apply(context.Background(), cfg))
	before := svc.promotionSnapshot.Load()
	require.NotNil(t, before)
	cfg.PromotionPolicies[0].Subjects = nil
	require.Error(t, r.Apply(context.Background(), cfg))
	require.Same(t, before, svc.promotionSnapshot.Load())
	issuers := before.Issuers()
	issuers[0].Issuer = "changed"
	require.Equal(t, "https://issuer.example", before.Issuers()[0].Issuer)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				snapshot := svc.PromotionSnapshot()
				require.Equal(t, "https://issuer.example", snapshot.Issuers()[0].Issuer)
			}
		}()
	}
	cfg.PromotionPolicies[0].Subjects = []string{"a"}
	for range 50 {
		require.NoError(t, r.Apply(context.Background(), cfg))
	}
	wg.Wait()
}

func TestPromotionConfigExplicitDurations(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "bad"} {
		_, cfg := readPromotionConfig(t, strings.Replace(promotionTOML, `audience = "aud"`, "audience = \"aud\"\nmax_token_lifetime = \""+value+"\"", 1))
		_, err := buildPromotionSnapshot(cfg)
		require.Error(t, err)
	}
	_, cfg := readPromotionConfig(t, strings.Replace(promotionTOML, `audience = "aud"`, "audience = \"aud\"\nclock_skew = \"0s\"\nmax_token_lifetime = \"30m\"\nallow_private_network = true", 1))
	s, err := buildPromotionSnapshot(cfg)
	require.NoError(t, err)
	require.Zero(t, s.Issuers()[0].ClockSkew)
	require.Equal(t, 30*time.Minute, s.Issuers()[0].MaxTokenLifetime)
	require.True(t, s.Issuers()[0].AllowPrivateNetwork)
}

func TestPromotionConfigLoadAndReloadFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(path, []byte(promotionTOML), 0600))
	v, cfg, err := initConfig(path)
	require.NoError(t, err)
	svc := &services{}
	runtime := &reloadRuntime{v: v, svc: svc}
	require.NoError(t, runtime.Apply(context.Background(), cfg))
	before := svc.promotionSnapshot.Load()
	invalid := promotionTOML + "typo = true\n"
	require.NoError(t, os.WriteFile(path, []byte(invalid), 0600))
	_, _, err = initConfig(path)
	require.ErrorContains(t, err, "unknown promotion_policies key")
	require.NoError(t, v.ReadInConfig())
	coordinator := &reloadCoordinator{v: v, applyRuntime: runtime.Apply, log: zerowrap.Default()}
	require.ErrorContains(t, coordinator.applyLoadedConfig(context.Background(), time.Now()), "unknown promotion_policies key")
	require.Same(t, before, svc.promotionSnapshot.Load())
}

func TestPromotionStartupRejectsAppHostCollision(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	svc, _ := newPublisherServices(state)
	var cfg Config
	cfg.Auth.Enabled = true
	cfg.Server.CIDomain = "blog.example.com"
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(servedActive("c-1", 32770), true, nil).Once()
	svc.appTrafficPublisher = newAppTrafficPublisher(svc, cfg)
	si := &serviceInit{ctx: context.Background(), cfg: cfg, svc: svc}
	require.ErrorContains(t, si.initPromotionSnapshot(), "collides with an app host")
	require.Nil(t, svc.promotionSnapshot.Load())
}

func TestCIDomainHostnameValidation(t *testing.T) {
	for _, host := range []string{"*", "https://ci.example", "ci.example:443", "ci.example/path", ".", " ", "bad..example", "-bad.example", "127.0.0.1", "[::1]", "ci.example."} {
		t.Run(host, func(t *testing.T) {
			var cfg Config
			cfg.Auth.Enabled = true
			cfg.Server.CIDomain = host
			require.Error(t, validateCIDomain(cfg))
			cfg.Auth.Enabled = false
			require.Error(t, validateCIDomain(cfg))
		})
	}
	var cfg Config
	require.NoError(t, validateCIDomain(cfg))
	cfg.Auth.Enabled = true
	cfg.Server.CIDomain = " CI.EXAMPLE "
	require.NoError(t, validateCIDomain(cfg))
	for _, host := range []string{" gordon.example ", "GORDON.EXAMPLE", "gordon.example."} {
		cfg.Server.CIDomain = host
		cfg.Server.GordonDomain = "gordon.example"
		require.Error(t, validateCIDomain(cfg))
	}
	cfg.Server.CIDomain = "ci.example"
	cfg.Server.GordonDomain = "other.example"
	cfg.Server.LegacyRegistryDomains = []string{" CI.EXAMPLE. "}
	require.Error(t, validateCIDomain(cfg))
}

func TestPromotionQuotedUnknownKey(t *testing.T) {
	for _, text := range []string{promotionTOML + "\"issuer subjects\" = \"ignored\"\n", strings.Replace(promotionTOML, `audience = "aud"`, "audience = \"aud\"\n\"issuer audience\" = \"ignored\"", 1)} {
		v, cfg := readPromotionConfig(t, text)
		require.Error(t, validatePromotionConfig(v, cfg))
	}
}

func TestPromotionPolicyErrorRedactsIdentifiers(t *testing.T) {
	for _, field := range []string{"app", "service"} {
		_, cfg := readPromotionConfig(t, promotionTOML)
		if field == "app" {
			cfg.PromotionPolicies[0].App = "PrivateAppName"
		} else {
			cfg.PromotionPolicies[0].Service = "PrivateServiceName"
		}
		_, err := buildPromotionSnapshot(cfg)
		require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
		require.NotContains(t, err.Error(), "PrivateAppName")
		require.NotContains(t, err.Error(), "PrivateServiceName")
		require.Contains(t, err.Error(), field+" name")
	}
}

func TestPromotionCIAppHostCanonicalCollisions(t *testing.T) {
	var cfg Config
	cfg.Server.CIDomain = " CI.EXAMPLE "
	for _, host := range []string{"ci.example", "CI.EXAMPLE", " ci.example ", "ci.example."} {
		index := apptraffic.NewHostIndex()
		index.Replace([]apptraffic.RouteEntry{{Kind: "http", Host: host, Backend: domain.AppBackend{Host: "127.0.0.1", Port: 8080, ContainerID: "c-1"}}})
		require.ErrorContains(t, validateCIAppHosts(cfg, index), "collides with an app host")
	}
}
