// Package middleware provides HTTP middleware for the adapters layer.
package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// ResponseWriter wraps http.ResponseWriter to capture status code and bytes written.
type ResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int
}

// NewResponseWriter creates a new wrapped response writer.
func NewResponseWriter(w http.ResponseWriter) *ResponseWriter {
	return &ResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // Default status code
	}
}

// WriteHeader captures the status code.
func (rw *ResponseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Write captures bytes written.
func (rw *ResponseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// StatusCode returns the captured status code.
func (rw *ResponseWriter) StatusCode() int {
	return rw.statusCode
}

// BytesWritten returns the number of bytes written.
func (rw *ResponseWriter) BytesWritten() int {
	return rw.bytes
}

// RequestLogger is a middleware that logs HTTP requests using zerowrap.
// It also attaches the logger to the request context for downstream handlers.
// The trustedNets parameter controls which proxy headers are trusted for IP extraction.
// Pass nil to only use RemoteAddr (safest default when not behind a trusted proxy).
func RequestLogger(log zerowrap.Logger, trustedNets ...[]*net.IPNet) func(http.Handler) http.Handler {
	var nets []*net.IPNet
	if len(trustedNets) > 0 {
		nets = trustedNets[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap the response writer to capture status and bytes
			rw := NewResponseWriter(w)

			// SECURITY: Use trusted-proxy-aware IP extraction to prevent
			// IP spoofing via X-Forwarded-For from untrusted sources.
			clientIP := GetClientIP(r, nets)

			// Attach the logger to the request context for downstream handlers
			ctx := zerowrap.WithCtx(r.Context(), log)
			r = r.WithContext(ctx)

			// Call the next handler
			next.ServeHTTP(rw, r)

			// Calculate duration
			duration := time.Since(start)

			// Log the request
			log.Info().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "http").
				Str(zerowrap.FieldMethod, r.Method).
				Str(zerowrap.FieldPath, r.URL.Path).
				Str("query", r.URL.RawQuery).
				Str(zerowrap.FieldHost, r.Host).
				Str("user_agent", r.UserAgent()).
				Str("referer", r.Referer()).
				Str(zerowrap.FieldClientIP, clientIP).
				Int(zerowrap.FieldStatus, rw.StatusCode()).
				Int("bytes", rw.BytesWritten()).
				Dur(zerowrap.FieldDuration, duration).
				Str("proto", r.Proto).
				Msg("HTTP request")
		})
	}
}


// PanicRecovery middleware recovers from panics and logs them.
func PanicRecovery(log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Error().
						Str(zerowrap.FieldLayer, "adapter").
						Str(zerowrap.FieldAdapter, "http").
						Interface("panic", err).
						Str(zerowrap.FieldMethod, r.Method).
						Str(zerowrap.FieldPath, r.URL.Path).
						Str(zerowrap.FieldHost, r.Host).
						Msg("panic recovered")

					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Internal Server Error"})
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// CORS middleware adds permissive CORS headers.
// NOTE: This is intentionally NOT used on the proxy chain (backends control their own CORS).
// Available for use on specific endpoints (e.g., registry API) where CORS is needed.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// Handle preflight OPTIONS request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Chain combines multiple middleware functions.
func Chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
