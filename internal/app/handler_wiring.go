// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/bnema/gordon/internal/adapters/dto"
	acmehttp "github.com/bnema/gordon/internal/adapters/in/http/acme"
	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/adapters/in/http/onboarding"
	proxyadapter "github.com/bnema/gordon/internal/adapters/in/http/proxy"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"
	"github.com/bnema/gordon/internal/boundaries/out"
)

func loopbackOnly(next http.Handler, log zerowrap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}

		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			log.Warn().
				Str("path", r.URL.Path).
				Str("remote_addr", r.RemoteAddr).
				Msg("blocked non-loopback access on internal admin route")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// createHTTPHandlers creates HTTP handlers with middleware.
// Returns three handlers: registry, HTTP proxy (with CIDR + onboarding), and HTTPS proxy.
func createHTTPHandlers(svc *services, cfg Config, log zerowrap.Logger, accessWriter out.AccessLogWriter) (http.Handler, http.Handler, http.Handler) {
	// Parse trusted proxies once for all middleware chains.
	// This ensures consistent IP extraction across logging, rate limiting, and auth.
	trustedNets := httphelper.ParseTrustedProxies(cfg.API.RateLimit.TrustedProxies)

	// Registry handler
	registryHandler := registry.NewHandler(svc.registrySvc, log, svc.maxBlobChunkSize, svc.maxBlobSize)
	svc.registryHandler = registryHandler
	if svc.reloadCoordinator != nil {
		svc.reloadCoordinator.SetRegistryLimits(registryHandler)
	}
	registryWithMiddleware, cidrAllowlistMiddleware, rateLimitMiddleware := buildRegistryHandlerWithMiddleware(
		svc,
		cfg,
		trustedNets,
		registryHandler,
		log,
	)

	registryMux := http.NewServeMux()
	registerAuthRoutes(registryMux, svc, trustedNets, cidrAllowlistMiddleware, rateLimitMiddleware, cfg, log)
	registryMux.Handle("/v2/", wrapRegistryForLocalMode(registryWithMiddleware, cfg, log))
	registerAdminRoutes(registryMux, svc, cfg, trustedNets, log)

	// Proxy handler
	proxyHandler := proxyadapter.NewHandler(svc.proxySvc, trustedNets, log)

	// HTTP proxy handler chain: HTTPS redirect for non-proxy clients, then CIDR allowlist
	proxyAllowedNets, proxyCIDRMiddleware := buildProxyCIDRAllowlistMiddleware(cfg, trustedNets, log)

	httpProxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
		middleware.HTTPSRedirect(proxyAllowedNets, effectiveProxyHTTPPort(cfg), effectiveProxyTLSPort(cfg), cfg.Server.ForceHTTPSRedirect, log, func(host string) bool {
			return svc.proxySvc.IsKnownHost(context.Background(), host)
		}),
	}
	if proxyCIDRMiddleware != nil {
		httpProxyMiddlewares = append(httpProxyMiddlewares, proxyCIDRMiddleware)
	}

	httpProxyWithMiddleware := otelhttp.NewHandler(
		middleware.Chain(httpProxyMiddlewares...)(proxyHandler),
		"gordon.proxy",
	)

	// Build the onboarding handler once if internal CA is available and TLS is enabled.
	var obHandler *onboarding.Handler
	if svc.caAdapter != nil && effectiveProxyTLSPort(cfg) != 0 {
		mobileconfigBytes := pkiadapter.GenerateMobileconfig(
			svc.caAdapter.RootCertificateDER(),
			svc.caAdapter.RootCommonName(),
		)
		obHandler = onboarding.NewHandler(
			svc.caAdapter.RootCertificate(),
			mobileconfigBytes,
			svc.caAdapter.RootFingerprint(),
			effectiveProxyHTTPPort(cfg),
			effectiveProxyTLSPort(cfg),
		)
	}

	// HTTP proxyMux: trusted proxy traffic flows through the normal proxy chain.
	// Direct clients get an onboarding gate (when CA is available) placed BEFORE
	// HTTPSRedirect so force_https_redirect cannot bypass onboarding.
	// ACME HTTP-01 challenge handler is registered before the catch-all "/" so
	// it gets first chance regardless of source IP.
	proxyMux := http.NewServeMux()

	// Register ACME HTTP-01 challenge handler before all other routes so
	// Let's Encrypt validation always succeeds, even for onboarding clients.
	if svc.publicTLSSvc != nil {
		proxyMux.Handle(acmehttp.Prefix, acmehttp.NewHandler(svc.publicTLSSvc))
	}

	if proxyCIDRMiddleware != nil && proxyAllowedNets == nil {
		// Invalid proxy_allowed_ips: deny all traffic (fail-closed).
		proxyMux.Handle("/", proxyCIDRMiddleware(httpProxyWithMiddleware))
	} else if obHandler != nil {
		proxyMux.Handle("/", directHTTPOnboardingGate(obHandler, proxyAllowedNets, httpProxyWithMiddleware, log))
	} else {
		proxyMux.Handle("/", httpProxyWithMiddleware)
	}

	// HTTPS proxy handler chain: security headers + proxy + CA onboarding
	// Onboarding routes live on the TLS port so Tailnet / direct clients
	// can click through the initial cert warning, install the CA, and
	// then trust all subsequent connections.
	// The middleware chain wraps the entire mux so onboarding routes also
	// get PanicRecovery, RequestLogger, and SecurityHeaders.
	httpsProxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}

	httpsMux := http.NewServeMux()
	if obHandler != nil && cfg.Server.GordonDomain != "" {
		gordonDomain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cfg.Server.GordonDomain)), ".")
		onboardingMux := http.NewServeMux()
		registerOnboardingRoutes(onboardingMux, obHandler)
		// Register onboarding paths host-gated so normal traffic hits
		// proxyHandler directly through the catch-all / pattern.
		httpsMux.Handle("GET /.well-known/gordon/", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /.well-known/gordon/ca", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /.well-known/gordon/ca.crt", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /.well-known/gordon/ca.mobileconfig", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("/", proxyHandler)
	} else {
		httpsMux.Handle("/", proxyHandler)
	}

	httpsHandler := otelhttp.NewHandler(middleware.Chain(httpsProxyMiddlewares...)(httpsMux), "gordon.proxy.tls")

	// Wrap top-level handlers with access logging outside all gates
	// (loopbackOnly, denyAllHandler, CIDR allowlist) so every request —
	// including rejected probes — produces exactly one access-log line.
	var registryOut, proxyOut, httpsOut http.Handler = registryMux, proxyMux, httpsHandler
	if accessWriter != nil {
		excludeHC := cfg.Logging.AccessLog.ExcludeHealthChecks
		registryOut = middleware.AccessLogger(accessWriter, excludeHC, log, trustedNets)(registryOut)
		proxyOut = middleware.AccessLogger(accessWriter, excludeHC, log, trustedNets)(proxyOut)
		httpsOut = middleware.AccessLogger(accessWriter, excludeHC, log, trustedNets)(httpsOut)
	}
	svc.httpsProxyHandler = httpsOut

	return registryOut, proxyOut, httpsOut
}

// registerOnboardingRoutes registers CA onboarding well-known HTTP routes on
// the given mux. Both direct-HTTP and Gordon-domain HTTPS onboarding use this.
func registerOnboardingRoutes(mux *http.ServeMux, ob *onboarding.Handler) {
	mux.HandleFunc("GET /.well-known/gordon/", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca.crt", ob.ServeCACert)
	mux.HandleFunc("GET /.well-known/gordon/ca.mobileconfig", ob.ServeMobileconfig)
}

// directHTTPOnboardingGate returns an http.Handler that splits HTTP traffic
// by source IP. Trusted proxy IPs flow through to the normal proxy chain.
// Direct clients are served the CA onboarding flow on allowed paths and
// receive 403 on everything else. This gate runs BEFORE HTTPSRedirect so
// force_https_redirect cannot bypass onboarding for direct clients.
func directHTTPOnboardingGate(ob *onboarding.Handler, proxyNets []*net.IPNet, proxyChain http.Handler, log zerowrap.Logger) http.Handler {
	// Build a small mux for direct-client onboarding paths.
	onboardingMux := http.NewServeMux()
	registerOnboardingRoutes(onboardingMux, ob)

	// Reserve ACME challenge path for future use.
	onboardingMux.HandleFunc("/.well-known/acme-challenge/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	// Catch-all: reject any other direct HTTP request.
	// Uses a method-aware split: GET writes a body, HEAD gets an empty 403.
	onboardingMux.HandleFunc("/", directHTTPForbidden)

	onboardingWithMiddleware := middleware.Chain(
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log), // Intentionally omit trusted proxy nets so direct onboarding logs use RemoteAddr only.
		middleware.SecurityHeaders,
	)(onboardingMux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteIP := httphelper.ExtractRemoteIP(r.RemoteAddr)
		if httphelper.IsTrustedOrLocal(remoteIP, proxyNets) {
			proxyChain.ServeHTTP(w, r)
			return
		}
		onboardingWithMiddleware.ServeHTTP(w, r)
	})
}

// canonicalHostsEqual compares two hosts after normalising both: stripping
// port, trimming spaces, lowercasing, and removing trailing dot.
func canonicalHostsEqual(host, expected string) bool {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	expected = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(expected)), ".")
	return host == expected
}

// gordonDomainOnboardingGate returns a handler that serves onboarding routes
// only when the request host matches gordonDomain. For mismatched hosts it
// delegates to proxyHandler. gordonDomain must already be canonicalised
// (trimmed, lowered, trailing dot removed).
func gordonDomainOnboardingGate(gordonDomain string, onboardingMux, proxyHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gordonDomain != "" && canonicalHostsEqual(r.Host, gordonDomain) {
			onboardingMux.ServeHTTP(w, r)
			return
		}
		proxyHandler.ServeHTTP(w, r)
	})
}

// directHTTPForbidden responds with 403 for non-onboarding HTTP paths.
// HEAD requests get an empty body per HTTP semantics.
func directHTTPForbidden(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("Only certificate onboarding is available over HTTP.\n"))
	}
}

func buildRegistryHandlerWithMiddleware(
	svc *services,
	cfg Config,
	trustedNets []*net.IPNet,
	registryHandler http.Handler,
	log zerowrap.Logger,
) (http.Handler, func(http.Handler) http.Handler, func(http.Handler) http.Handler) {
	registryMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}

	cidrAllowlistMiddleware := buildRegistryCIDRAllowlistMiddleware(cfg, trustedNets, log)
	if cidrAllowlistMiddleware != nil {
		registryMiddlewares = append(registryMiddlewares, cidrAllowlistMiddleware)
	}

	rateLimitMiddleware := buildRegistryRateLimitMiddleware(cfg, log)
	registryMiddlewares = append(registryMiddlewares, rateLimitMiddleware)

	appendRegistryAuthMiddleware(&registryMiddlewares, svc, cfg, trustedNets, log)

	registryWithOtel := otelhttp.NewHandler(
		middleware.Chain(registryMiddlewares...)(registryHandler),
		"gordon.registry",
	)
	return registryWithOtel, cidrAllowlistMiddleware, rateLimitMiddleware
}

// parseCIDRAllowlist parses a list of IPs/CIDRs, logs warnings for invalid entries,
// and returns the parsed nets. label is used in log messages (e.g. "registry_allowed_ips").
func parseCIDRAllowlist(ips []string, label string, log zerowrap.Logger) ([]*net.IPNet, bool) {
	if len(ips) == 0 {
		return nil, false
	}

	allowedNets := httphelper.ParseTrustedProxies(ips)
	if len(allowedNets) != len(ips) {
		for _, entry := range ips {
			if nets := httphelper.ParseTrustedProxies([]string{entry}); len(nets) == 0 {
				log.Warn().Str("entry", entry).Msgf("ignoring invalid %s entry", label)
			}
		}
	}

	if len(allowedNets) == 0 {
		log.Error().
			Strs(label, ips).
			Msgf("%s is set but no valid entries were parsed; will deny all traffic (fail-closed)", label)
		return nil, true // allInvalid
	}

	return allowedNets, false
}

// denyAllHandler returns a middleware that rejects every request with 403 Forbidden.
func denyAllHandler(label string, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Warn().
				Str(zerowrap.FieldClientIP, middleware.GetClientIP(r, trustedNets)).
				Msgf("access denied due to invalid %s configuration", label)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden"})
		})
	}
}

func buildRegistryCIDRAllowlistMiddleware(cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	allowedNets, allInvalid := parseCIDRAllowlist(cfg.Server.RegistryAllowedIPs, "registry_allowed_ips", log)
	if allInvalid {
		return denyAllHandler("registry_allowed_ips", trustedNets, log)
	}
	if allowedNets == nil {
		return nil
	}
	return middleware.RegistryCIDRAllowlist(allowedNets, trustedNets, log)
}

func buildProxyCIDRAllowlistMiddleware(cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) ([]*net.IPNet, func(http.Handler) http.Handler) {
	allowedNets, allInvalid := parseCIDRAllowlist(cfg.Server.ProxyAllowedIPs, "proxy_allowed_ips", log)
	if allInvalid {
		return nil, denyAllHandler("proxy_allowed_ips", trustedNets, log)
	}
	if allowedNets == nil {
		return nil, nil
	}

	log.Info().
		Strs("proxy_allowed_ips", cfg.Server.ProxyAllowedIPs).
		Msg("proxy origin IP allowlist enabled")

	return allowedNets, middleware.ProxyCIDRAllowlist(allowedNets, log)
}

func buildRegistryRateLimitMiddleware(cfg Config, log zerowrap.Logger) func(http.Handler) http.Handler {
	if cfg.API.RateLimit.Enabled {
		globalLimiter := ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log)
		ipLimiter := ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log)
		return registry.RateLimitMiddleware(
			globalLimiter,
			ipLimiter,
			cfg.API.RateLimit.TrustedProxies,
			log,
		)
	}

	return registry.RateLimitMiddleware(nil, nil, nil, log)
}

func appendRegistryAuthMiddleware(registryMiddlewares *[]func(http.Handler) http.Handler, svc *services, cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) {
	if svc.authSvc != nil {
		internalAuth := middleware.InternalRegistryAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		*registryMiddlewares = append(*registryMiddlewares, middleware.RegistryAuthV2(svc.authSvc, internalAuth, trustedNets, log))
		return
	}

	if cfg.Auth.Enabled {
		log.Error().Msg("authentication service unavailable; registry requests will be denied")
		*registryMiddlewares = append(*registryMiddlewares, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "authentication service unavailable"})
			})
		})
	}
}

func registerAuthRoutes(
	registryMux *http.ServeMux,
	svc *services,
	trustedNets []*net.IPNet,
	cidrAllowlistMiddleware func(http.Handler) http.Handler,
	rateLimitMiddleware func(http.Handler) http.Handler,
	cfg Config,
	log zerowrap.Logger,
) {
	if svc.authHandler == nil {
		return
	}

	// Auth endpoints always get rate limiting, even if global rate limiting is disabled.
	// This prevents brute-force attacks against password/token endpoints.
	authRateLimitMiddleware := rateLimitMiddleware
	if !cfg.API.RateLimit.Enabled {
		authGlobalLimiter := ratelimit.NewMemoryStore(50, 100, log)
		authIPLimiter := ratelimit.NewMemoryStore(5, 10, log)
		authRateLimitMiddleware = registry.RateLimitMiddleware(authGlobalLimiter, authIPLimiter, cfg.API.RateLimit.TrustedProxies, log)
	}

	// Auth endpoints are NOT protected by auth - they're where clients authenticate
	// but still need rate limiting to prevent brute force attacks.
	authMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}
	if cidrAllowlistMiddleware != nil {
		authMiddlewares = append(authMiddlewares, cidrAllowlistMiddleware)
	}
	authMiddlewares = append(authMiddlewares, authRateLimitMiddleware)
	authWithMiddleware := otelhttp.NewHandler(
		middleware.Chain(authMiddlewares...)(svc.authHandler),
		"gordon.auth",
	)
	registryMux.Handle("/auth/", authWithMiddleware)
}

func wrapRegistryForLocalMode(registryWithMiddleware http.Handler, cfg Config, log zerowrap.Logger) http.Handler {
	if !cfg.Auth.Enabled {
		return loopbackOnly(registryWithMiddleware, log)
	}
	return registryWithMiddleware
}

func registerAdminRoutes(registryMux *http.ServeMux, svc *services, cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) {
	if svc.adminHandler == nil {
		return
	}

	if !cfg.Auth.Enabled {
		log.Warn().Msg("auth disabled: admin API endpoints are not registered")
		return
	}

	adminMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}

	if svc.authSvc != nil {
		// Create rate limiters for admin API - uses same config as registry.
		var globalLimiter, ipLimiter out.RateLimiter
		if cfg.API.RateLimit.Enabled {
			globalLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log)
			ipLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log)
		}
		adminMiddlewares = append(adminMiddlewares, admin.AuthMiddleware(svc.authSvc, globalLimiter, ipLimiter, trustedNets, log))
	} else {
		adminMiddlewares = append(adminMiddlewares, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "authentication service unavailable"})
			})
		})
	}

	adminWithMiddleware := otelhttp.NewHandler(
		middleware.Chain(adminMiddlewares...)(svc.adminHandler),
		"gordon.admin",
	)
	registryMux.Handle("/admin/", loopbackOnly(adminWithMiddleware, log))
}
