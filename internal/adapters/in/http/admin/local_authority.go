package admin

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// LocalAuthoritySubject is the principal carried on requests served over the
// owner-only admin Unix socket. Filesystem access to the 0600 socket is what
// establishes this identity; the scopes below still gate each route.
const LocalAuthoritySubject = "local-owner"

// localAuthorityScopes are the least-privilege scopes required by CLI
// commands which share the local and remote ControlPlane. The path-and-method
// allowlist below is the second boundary: config:write, for example, permits
// reload and prune operations but cannot reach unrelated config mutations.
var localAuthorityScopes = []string{
	domain.AdminScopeApps(domain.AdminActionRead, domain.AdminActionWrite),
	domain.AdminScopeStatus(domain.AdminActionRead),
	domain.AdminScopeConfig(domain.AdminActionRead, domain.AdminActionWrite),
	domain.AdminScopeSecrets(domain.AdminActionRead, domain.AdminActionWrite),
	domain.AdminScopeLogs(domain.AdminActionRead),
	domain.AdminScopeVolumes(domain.AdminActionRead, domain.AdminActionWrite),
}

// LocalAuthority returns the handler for the owner-only admin Unix socket.
// Filesystem ownership authenticates the caller; this wrapper then restricts
// that identity to the canonical endpoints used by local CLI commands.
func (h *Handler) LocalAuthority() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Classify and dispatch on one canonical path: the socket bypasses the
		// ServeMux that would otherwise clean "." and ".." segments, so a
		// non-canonical path must never be allowlisted here and interpreted
		// differently by the router below.
		if !strings.HasPrefix(r.URL.Path, "/admin/") && r.URL.Path != "/admin" {
			sendForbidden(w, "local administration is limited to app endpoints")
			return
		}
		trimmed := strings.TrimPrefix(r.URL.Path, "/admin")
		cleaned := path.Clean(trimmed)
		if cleaned != trimmed || !localPathAllowed(r.Method, cleaned) {
			sendForbidden(w, "endpoint is not available to the local owner")
			return
		}

		scopes := make([]string, len(localAuthorityScopes))
		copy(scopes, localAuthorityScopes)

		ctx := context.WithValue(r.Context(), domain.ContextKeyScopes, scopes)
		ctx = context.WithValue(ctx, domain.ContextKeySubject, LocalAuthoritySubject)
		h.handleAdminRoutes(w, r.WithContext(ctx))
	})
}

func localPathAllowed(method, cleanedPath string) bool {
	if localExactPathAllowed(method, cleanedPath) {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(cleanedPath, "/"), "/")
	prefixRules := map[string]func(string, []string) bool{
		"apps":    localAppPathAllowed,
		"backups": localBackupPathAllowed,
		"secrets": localSecretPathAllowed,
		"logs":    localLogPathAllowed,
		"tags":    localTagPathAllowed,
	}
	rule, ok := prefixRules[parts[0]]
	return ok && rule(method, parts)
}

func localSecretPathAllowed(method string, parts []string) bool {
	return len(parts) == 2 && parts[1] != "" && (method == http.MethodGet || method == http.MethodPost) ||
		len(parts) == 3 && parts[1] != "" && parts[2] != "" && method == http.MethodDelete
}

func localLogPathAllowed(method string, parts []string) bool {
	return (len(parts) == 1 || len(parts) == 2 && parts[1] != "") && method == http.MethodGet
}

func localTagPathAllowed(method string, parts []string) bool {
	return len(parts) == 2 && parts[1] != "" && method == http.MethodGet
}

func localExactPathAllowed(method, cleanedPath string) bool {
	allowed := map[string]string{
		"/status":                 http.MethodGet,
		"/tls/status":             http.MethodGet,
		"/traffic/status":         http.MethodGet,
		"/config":                 http.MethodGet,
		"/networks":               http.MethodGet,
		"/volumes":                http.MethodGet,
		"/volumes/prune":          http.MethodPost,
		"/images":                 http.MethodGet,
		"/images/prune":           http.MethodPost,
		"/reload":                 http.MethodPost,
		"/backups":                http.MethodGet,
		"/backups/status":         http.MethodGet,
		"/backups/volumes":        http.MethodGet,
		"/backups/volumes/status": http.MethodGet,
	}
	return allowed[cleanedPath] == method
}

func localAppPathAllowed(method string, parts []string) bool {
	appRoutes := map[string]string{
		"":                              http.MethodGet,
		"apply":                         http.MethodPost,
		"{app}":                         http.MethodGet,
		"{app}/diff":                    http.MethodGet,
		"{app}/deploy":                  http.MethodPost,
		"{app}/restart":                 http.MethodPost,
		"{app}/stop":                    http.MethodPost,
		"{app}/start":                   http.MethodPost,
		"{app}/remove":                  http.MethodPost,
		"{app}/secrets/set":             http.MethodPost,
		"{app}/secrets/delete":          http.MethodPost,
		"{app}/operations/by-key/{key}": http.MethodGet,
	}
	return appRoutes[localAppRouteShape(parts)] == method
}

func localAppRouteShape(parts []string) string {
	if len(parts) == 1 {
		return ""
	}
	if len(parts) == 2 && parts[1] == "apply" {
		return "apply"
	}
	if len(parts) < 2 || parts[1] == "" {
		return "invalid"
	}
	shaped := append([]string{"{app}"}, parts[2:]...)
	if len(shaped) == 4 && shaped[1] == "operations" && shaped[2] == "by-key" && shaped[3] != "" {
		shaped[3] = "{key}"
	}
	return strings.Join(shaped, "/")
}

func localBackupPathAllowed(method string, parts []string) bool {
	if len(parts) == 2 && parts[1] != "" && parts[1] != "status" && parts[1] != "volumes" {
		return method == http.MethodGet || method == http.MethodPost
	}
	if len(parts) == 3 && parts[1] == "volumes" && parts[2] != "" && parts[2] != "status" {
		return method == http.MethodGet || method == http.MethodPost
	}
	return len(parts) == 3 && parts[1] != "" && parts[1] != "volumes" && parts[2] == "detect" && method == http.MethodGet
}
