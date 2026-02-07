package middleware

import (
	"net/http"
)

// SecurityHeaders middleware adds standard security headers to HTTP responses.
// This provides defense-in-depth against various web attacks.
func SecurityHeaders(next http.Handler) http.Handler {
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

		// HSTS: Enforce HTTPS-only for browsers. Only set when the request
		// arrived over a real TLS connection. We do NOT trust X-Forwarded-Proto
		// here because it is client-spoofable unless the request came from a
		// trusted proxy, and we have no trusted-proxy list at this layer.
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		// NOTE: CSP is intentionally NOT set here. Gordon proxies arbitrary web
		// apps, so a blanket "default-src 'none'" would break all proxied sites.
		// Instead, CSP is set directly on proxy-generated error responses in the
		// proxy error handlers (service.go).

		next.ServeHTTP(w, r)
	})
}
