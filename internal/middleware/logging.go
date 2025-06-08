package middleware

import (
	"net/http"
	"time"

	"gordon/internal/logging"
	"github.com/rs/zerolog/log"
)

// ResponseWriter wraps http.ResponseWriter to capture status code and bytes written
type ResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int
}

func NewResponseWriter(w http.ResponseWriter) *ResponseWriter {
	return &ResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // Default status code
	}
}

func (rw *ResponseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *ResponseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

func (rw *ResponseWriter) StatusCode() int {
	return rw.statusCode
}

func (rw *ResponseWriter) BytesWritten() int {
	return rw.bytes
}

// RequestLogger is a middleware that logs HTTP requests
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		// Wrap the response writer to capture status and bytes
		rw := NewResponseWriter(w)
		
		// Get client IP (handle X-Forwarded-For and X-Real-IP headers)
		clientIP := getClientIP(r)
		
		// Call the next handler
		next.ServeHTTP(rw, r)
		
		// Calculate duration
		duration := time.Since(start)
		
		// Log the request using proxy logger
		logging.ProxyLogger.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("query", r.URL.RawQuery).
			Str("host", r.Host).
			Str("user_agent", r.UserAgent()).
			Str("referer", r.Referer()).
			Str("client_ip", clientIP).
			Int("status", rw.StatusCode()).
			Int("bytes", rw.BytesWritten()).
			Dur("duration", duration).
			Str("proto", r.Proto).
			Msg("HTTP Request")
	})
}

// getClientIP extracts the client IP from various headers
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (comma-separated list, first is original client)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (original client)
		for idx := 0; idx < len(xff); idx++ {
			if xff[idx] == ',' {
				return xff[:idx]
			}
		}
		return xff
	}
	
	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	
	// Check X-Forwarded header (less common)
	if xf := r.Header.Get("X-Forwarded"); xf != "" {
		return xf
	}
	
	// Fallback to RemoteAddr
	return r.RemoteAddr
}

// ContainerRequestLogger logs requests with container-specific information
func ContainerRequestLogger(containerID string, domain string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			
			// Wrap the response writer to capture status and bytes
			rw := NewResponseWriter(w)
			
			// Get client IP
			clientIP := getClientIP(r)
			
			// Call the next handler
			next.ServeHTTP(rw, r)
			
			// Calculate duration
			duration := time.Since(start)
			
			// Log the request with container information using proxy logger
			logging.ProxyLogger.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("query", r.URL.RawQuery).
				Str("host", r.Host).
				Str("domain", domain).
				Str("container_id", containerID).
				Str("user_agent", r.UserAgent()).
				Str("referer", r.Referer()).
				Str("client_ip", clientIP).
				Int("status", rw.StatusCode()).
				Int("bytes", rw.BytesWritten()).
				Dur("duration", duration).
				Str("proto", r.Proto).
				Msg("Container Request")
		})
	}
}

// PanicRecovery middleware recovers from panics and logs them
func PanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Error().
					Interface("panic", err).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("host", r.Host).
					Msg("Panic recovered")
				
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		
		next.ServeHTTP(w, r)
	})
}

// CORS middleware adds CORS headers
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

// Chain combines multiple middleware functions
func Chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}