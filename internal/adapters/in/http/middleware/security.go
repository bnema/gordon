package middleware

import (
	"net/http"
)

// SecurityHeaders middleware adds standard security headers to HTTP responses.
// This provides defense-in-depth against various web attacks.
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
			// Prevent MIME type sniffing
			w.Header().Set("X-Content-Type-Options", "nosniff")

			// Prevent clickjacking
			w.Header().Set("X-Frame-Options", "DENY")

			// XSS protection (legacy, but still useful for older browsers)
			w.Header().Set("X-XSS-Protection", "1; mode=block")

			// Control referrer information
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

			// Restrict browser features
			w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

			// HSTS: Enforce HTTPS-only for browsers.
			// Set when the request arrived over TLS, or when force_hsts is enabled
			// (for deployments behind a TLS-terminating proxy like Cloudflare).
			if r.TLS != nil || forceHSTS {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}

			// NOTE: CSP is intentionally NOT set here. Gordon proxies arbitrary web
			// apps, so a blanket "default-src 'none'" would break all proxied sites.
			// Instead, CSP is set directly on proxy-generated error responses in the
			// proxy error handlers (service.go).

			next.ServeHTTP(w, r)
		})
	}
}
