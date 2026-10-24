package app

import (
	"net"
	"net/http"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/in/http/ci"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"
	"github.com/bnema/gordon/internal/domain"
)

// managementHosts lists the Gordon-served hosts that need a TLS
// certificate besides app hosts: the management domain and, when set, the
// CI domain.
func managementHosts(cfg Config) []string {
	hosts := []string{cfg.Server.GordonDomain}
	if cfg.Server.CIDomain != "" {
		hosts = append(hosts, cfg.Server.CIDomain)
	}
	return hosts
}

// buildCIHandler wires the CI host router, or returns nil when the
// collaborators it needs are absent (authentication disabled). The router
// is wired whenever it can serve: the proxy dispatches to it only while the
// current config names a CI domain, so a reload that adds or removes
// server.ci_domain takes effect without rewiring. rawRegistry is the
// registry handler without the main host's authentication middleware.
func buildCIHandler(svc *services, cfg Config, trustedNets []*net.IPNet, rawRegistry http.Handler, log zerowrap.Logger) http.Handler {
	if svc.authSvc == nil || svc.workloadIdentitySvc == nil || svc.appSvc == nil || svc.proxySvc == nil {
		return nil
	}
	return ci.NewHandler(ci.Deps{
		Auth:           svc.authSvc,
		Identity:       svc.workloadIdentitySvc,
		Apps:           svc.appSvc,
		Policies:       func() domain.SetImagePolicySet { return svc.SetImageSnapshot().Policies() },
		Domain:         func() string { return svc.proxySvc.ProxyConfig().CIDomain },
		Registry:       rawRegistry,
		TrustedNets:    trustedNets,
		RateLimit:      ciRateLimit(cfg, log),
		TokenRateLimit: ciTokenRateLimit(cfg, log),
		Log:            log,
	})
}

// ciRateLimit uses the main registry's configured limits when enabled.
// Otherwise the public CI host still has a global 500 rps / 1000 burst and
// per-IP 50 rps / 100 burst: enough for many blob/manifest requests per push,
// unlike the deliberately stricter identity-provider token exchange limit.
func ciRateLimit(cfg Config, log zerowrap.Logger) func(http.Handler) http.Handler {
	globalRPS, perIPRPS, burst := 500.0, 50.0, 1000
	ipBurst := 100
	if cfg.API.RateLimit.Enabled {
		globalRPS, perIPRPS = cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.PerIPRPS
		burst, ipBurst = cfg.API.RateLimit.Burst, cfg.API.RateLimit.Burst
	}
	return registry.RateLimitMiddleware(
		ratelimit.NewMemoryStore(globalRPS, burst, log),
		ratelimit.NewMemoryStore(perIPRPS, ipBurst, log),
		cfg.API.RateLimit.TrustedProxies, log)
}

// ciTokenRateLimit limits the CI token endpoint like the main one: the
// configured limits when rate limiting is enabled, otherwise the fixed
// brute-force limits every token endpoint keeps.
func ciTokenRateLimit(cfg Config, log zerowrap.Logger) func(http.Handler) http.Handler {
	if cfg.API.RateLimit.Enabled {
		return registry.RateLimitMiddleware(
			ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log),
			ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log),
			cfg.API.RateLimit.TrustedProxies, log)
	}
	return registry.RateLimitMiddleware(
		ratelimit.NewMemoryStore(50, 100, log),
		ratelimit.NewMemoryStore(5, 10, log),
		cfg.API.RateLimit.TrustedProxies, log)
}
