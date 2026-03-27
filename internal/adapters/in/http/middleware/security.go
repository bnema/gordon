package middleware

import (
	"net/http"
)

// SecurityHeaders middleware adds standard security headers to HTTP responses.
func SecurityHeaders(next http.Handler) http.Handler {
	return SecurityHeadersWithOptions(false)(next)
}

// SecurityHeadersWithOptions returns a middleware that adds standard security headers.
// When forceHSTS is true, the HSTS header is always set regardless of TLS state.
// This is needed when Gordon runs behind a TLS-terminating proxy (e.g. Cloudflare)
// where r.TLS is nil but the client connection is always HTTPS.
func SecurityHeadersWithOptions(forceHSTS bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

			// Only set when TLS is active or force_hsts is configured,
			// since HSTS over plain HTTP would lock out clients.
			if r.TLS != nil || forceHSTS {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}

			// CSP is intentionally NOT set here — Gordon proxies arbitrary web apps,
			// so a blanket policy would break proxied sites. CSP is set only on
			// Gordon-generated error responses (see proxy error handlers).

			next.ServeHTTP(w, r)
		})
	}
}
