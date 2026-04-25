// Package proxy implements the HTTP adapter for the reverse proxy.
package proxy

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/boundaries/in"
)

// Handler implements http.Handler for the reverse proxy.
// It owns all HTTP-level concerns: concurrency limiting, body size enforcement,
// transport selection, and reverse proxy execution.
// Routing decisions are delegated to the ProxyService usecase.
type Handler struct {
	proxySvc          in.ProxyService
	log               zerowrap.Logger
	trustedNets       []*net.IPNet
	appTransport      http.RoundTripper
	h2cTransport      http.RoundTripper
	registryTransport http.RoundTripper
	activeConns       atomic.Int64
}

// NewHandler creates a new proxy HTTP handler.
func NewHandler(proxySvc in.ProxyService, trustedNets []*net.IPNet, log zerowrap.Logger) *Handler {
	return &Handler{
		proxySvc:          proxySvc,
		log:               log,
		trustedNets:       trustedNets,
		appTransport:      newAppTransport(),
		h2cTransport:      newH2CTransport(),
		registryTransport: newRegistryTransport(),
	}
}

// ServeHTTP handles incoming HTTP requests and proxies them to the appropriate backend.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := h.proxySvc.ProxyConfig()

	// SECURITY: Limit concurrent connections to prevent resource exhaustion.
	if cfg.MaxConcurrentConns > 0 {
		current := h.activeConns.Add(1)
		if current > int64(cfg.MaxConcurrentConns) {
			h.activeConns.Add(-1)
			proxyError(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		defer h.activeConns.Add(-1)
	}

	// SECURITY: Limit request body size to prevent resource exhaustion.
	if cfg.MaxBodySize > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBodySize)
	}

	// Enrich request context with fields for downstream logging
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "http",
		zerowrap.FieldMethod:   r.Method,
		zerowrap.FieldPath:     r.URL.Path,
		zerowrap.FieldHost:     r.Host,
		zerowrap.FieldClientIP: middleware.GetClientIP(r, h.trustedNets),
	})
	r = r.WithContext(ctx)
	log := zerowrap.FromCtx(ctx)

	host := normalizeRequestHost(r.Host)
	if host == "" {
		proxyError(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Check if this is the registry domain
	if h.proxySvc.IsRegistryDomain(host) {
		log.Debug().Msg("routing request to registry")
		h.forwardToRegistry(w, r, cfg.RegistryPort)
		return
	}

	// Get target for this domain
	log.Debug().Str("resolving_target_for", host).Msg("looking up proxy target")
	target, err := h.proxySvc.GetTarget(ctx, host)
	if err != nil {
		log.Warn().Err(err).Msg("no route found for domain")
		proxyError(w, "404 page not found", http.StatusNotFound)
		return
	}

	log.Debug().
		Str("host", target.Host).
		Int("port", target.Port).
		Str("container_id", target.ContainerID).
		Msg("resolved proxy target")

	h.forwardToTarget(w, r, target, cfg.MaxResponseSize)
}

func normalizeRequestHost(host string) string {
	host = strings.TrimSpace(host)
	if h, port, err := net.SplitHostPort(host); err == nil {
		portNum, err := strconv.Atoi(port)
		if err != nil || portNum < 1 || portNum > 65535 {
			return ""
		}
		host = h
	} else if strings.Contains(host, ":") {
		return ""
	}
	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")
	return host
}
