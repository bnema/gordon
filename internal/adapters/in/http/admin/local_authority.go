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

// localAuthorityScopes are the least-privilege scopes granted to the local
// owner: app read/write plus logs read (app operation lookups include bounded
// diagnostics only when the caller holds logs:read). No config, auth, prune,
// route, backup, or volume scope is ever granted locally.
var localAuthorityScopes = []string{
	domain.AdminScopeApps(domain.AdminActionRead, domain.AdminActionWrite),
	domain.AdminScopeLogs(domain.AdminActionRead),
}

// localAuthorityPrefixes are the admin route prefixes reachable locally.
// Process logs at /logs are intentionally excluded; app container logs use
// /logs/<app> and remain available to `gordon apps logs`.
var localAuthorityPrefixes = []string{"/apps"}

// LocalAuthority returns the handler for the owner-only admin Unix socket.
// It allows app administration and app log reads, injects the local-owner
// principal with fixed scopes, and denies every other admin route with the
// normal JSON denial.
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
		if cleaned != trimmed {
			r.URL.Path = "/admin" + cleaned
		}
		if !localPathAllowed(cleaned) {
			sendForbidden(w, "local administration is limited to app endpoints")
			return
		}

		scopes := make([]string, len(localAuthorityScopes))
		copy(scopes, localAuthorityScopes)

		ctx := context.WithValue(r.Context(), domain.ContextKeyScopes, scopes)
		ctx = context.WithValue(ctx, domain.ContextKeySubject, LocalAuthoritySubject)
		h.handleAdminRoutes(w, r.WithContext(ctx))
	})
}

func localPathAllowed(cleanedPath string) bool {
	for _, prefix := range localAuthorityPrefixes {
		if cleanedPath == prefix || strings.HasPrefix(cleanedPath, prefix+"/") {
			return true
		}
	}
	// App container logs are served by the generic logs handler, but the
	// process-log root itself is not part of local app administration.
	return strings.HasPrefix(cleanedPath, "/logs/")
}
