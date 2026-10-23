// Package ci implements the CI host router: the only surface a CI job of an
// OIDC-capable forge reaches. It exchanges a workload identity token for a
// short-lived ci-access token, accepts image pushes into the repositories
// the promotion policies grant, and serves promotion and operation lookup.
// Every other route answers 404: the CI host never reaches the admin API
// or the full registry.
package ci

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

const (
	// maxJWTBytes caps the raw OIDC JWT accepted by the token endpoint
	// before any parsing or verification.
	maxJWTBytes = 16 << 10
	// maxVerifyWait bounds how long one token request may wait for the
	// identity provider (a JWKS refresh), below the request deadline.
	maxVerifyWait = 20 * time.Second
	// defaultMaxTokenWaiters caps concurrent token requests in Verify.
	defaultMaxTokenWaiters = 32
	// maxCIAccessTTL mirrors the ci-access lifetime cap of the auth service.
	maxCIAccessTTL = time.Hour

	tokenPath = "/auth/token" //nolint:gosec // G101: a URL path, not a credential
	ciPrefix  = "/-/ci/"
)

// Deps are the CI router collaborators. Policies and Domain are read per
// request, so a config reload applies to the next request.
type Deps struct {
	Auth     in.AuthService
	Identity in.WorkloadIdentityService
	Apps     in.AppService
	// Policies returns the current promotion policy generation.
	Policies func() domain.PromotionPolicySet
	// Domain returns the current CI domain (server.ci_domain).
	Domain func() string
	// Registry is the raw registry handler, without the main host's
	// authentication middleware: the CI router authorizes every request.
	Registry http.Handler
	// TrustedNets are the reverse proxies whose X-Forwarded-* headers are
	// trusted (client IP and X-Forwarded-Proto).
	TrustedNets []*net.IPNet
	// TokenRateLimit wraps the token endpoint. Nil disables it.
	TokenRateLimit func(http.Handler) http.Handler
	// MaxTokenWaiters caps concurrent token requests waiting on the
	// identity provider. Zero uses the default.
	MaxTokenWaiters int
	Log             zerowrap.Logger
}

// Handler is the CI host router.
type Handler struct {
	deps    Deps
	waiters chan struct{}
	token   http.Handler
	chain   http.Handler
}

// NewHandler builds the CI router with its full middleware chain.
func NewHandler(deps Deps) *Handler {
	waiters := deps.MaxTokenWaiters
	if waiters <= 0 {
		waiters = defaultMaxTokenWaiters
	}
	h := &Handler{deps: deps, waiters: make(chan struct{}, waiters)}
	h.token = http.HandlerFunc(h.handleToken)
	if deps.TokenRateLimit != nil {
		h.token = deps.TokenRateLimit(h.token)
	}
	h.chain = middleware.Chain(
		middleware.PanicRecovery(deps.Log),
		middleware.RequestLogger(deps.Log, deps.TrustedNets),
		middleware.SecurityHeaders,
		h.httpsOnly,
	)(http.HandlerFunc(h.route))
	return h
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.chain.ServeHTTP(w, r)
}

// route dispatches on the explicit allowlist. Allowlist checks run before
// authentication, so a route outside it answers 404 to every caller and
// never reveals whether credentials would have mattered.
func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == tokenPath && r.Method == http.MethodGet:
		h.token.ServeHTTP(w, r)
	case strings.HasPrefix(path, "/v2"):
		h.serveRegistry(w, r)
	case strings.HasPrefix(path, ciPrefix):
		h.serveCI(w, r)
	default:
		writeNotFound(w)
	}
}

// httpsOnly answers 404 unless the request arrived over TLS or through a
// trusted reverse proxy that reports https. Credentials never travel over
// plaintext to the CI host.
func (h *Handler) httpsOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.isHTTPS(r) {
			writeNotFound(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !httphelper.IsTrustedSource(r, h.deps.TrustedNets) {
		return false
	}
	values := r.Header.Values("X-Forwarded-Proto")
	return len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "https")
}

// ciDomain is the canonical configured CI domain.
func (h *Handler) ciDomain() string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h.deps.Domain())), ".")
}

// realm is the token endpoint URL announced in Bearer challenges. It is
// built from the configured domain, never from request headers, and is
// always https because the router serves nothing else.
func (h *Handler) realm() string {
	return "https://" + h.ciDomain() + tokenPath
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, dto.ErrorResponse{Error: "not found"})
}

// writeRegistryError answers in the Docker Registry V2 error format.
func writeRegistryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	writeJSON(w, status, dto.RegistryErrorResponse{Errors: []dto.RegistryErrorItem{{Code: code, Message: message}}})
}

// redactUsername keeps the first and last character of a Basic username.
func redactUsername(username string) string {
	if len(username) <= 2 {
		return "***"
	}
	return username[:1] + "***" + username[len(username)-1:]
}
