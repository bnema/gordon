package proxy

import (
	"net/http"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/logger"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"github.com/bnema/gordon/pkg/kv" // Correct import path based on the module name
)

// rateLimiterCleanup holds the cleanup functions for rate limiters
var rateLimiterCleanup []func()

// RequestIDKey is the key used to store the request ID in the context
const RequestIDKey = "request_id"

// setupMiddleware configures middleware for both HTTP and HTTPS servers
func (p *Proxy) setupMiddleware() {
	// Configure Echo to trust X-Forwarded-* headers
	// Since we are the reverse proxy, we need to make sure we identify client IPs correctly
	p.httpsServer.IPExtractor = echo.ExtractIPFromXFFHeader()
	p.httpServer.IPExtractor = echo.ExtractIPFromXFFHeader()

	// Add UUID generator middleware for both HTTP and HTTPS servers
	p.httpsServer.Use(p.createRequestIDMiddleware())
	p.httpServer.Use(p.createRequestIDMiddleware())

	// Check if rate limiting is enabled via configuration
	if p.config.EnableRateLimit {
		// Create a new Starskey rate limiter store for HTTPS server
		httpsRateLimiter, err := kv.NewStarskeyRateLimiterStore(
			"./data/ratelimiter/https", // Path to store rate limit data
			10,                         // Rate: 10 requests per second (lowered from 10)
			30,                         // Burst: 30 requests (lowered from 30)
			3*time.Minute,              // Expires in 3 minutes
		)
		if err != nil {
			logger.Error("Failed to create HTTPS rate limiter store, falling back to memory store", "error", err)

			// Fallback to memory store if Starskey store creation fails
			p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
				Store: middleware.NewRateLimiterMemoryStoreWithConfig(
					middleware.RateLimiterMemoryStoreConfig{
						Rate:      rate.Limit(10),  // 10 requests per second (lowered from 10)
						Burst:     30,              // Burst of 30 requests (lowered from 30)
						ExpiresIn: 3 * time.Minute, // Store expiration
					},
				),
				DenyHandler: func(context echo.Context, identifier string, err error) error {
					logger.Warn("Rate limit exceeded",
						"ip", identifier,
						"path", context.Request().URL.Path,
						"method", context.Request().Method)
					return context.String(http.StatusTooManyRequests, "Too many requests")
				},
			}))
		} else {
			// Use the Starskey store for HTTPS rate limiting
			logger.Debug("Using Starskey rate limiter for HTTPS server")

			// Add custom Starskey rate limiter middleware
			p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
				Store: httpsRateLimiter,
				IdentifierExtractor: func(ctx echo.Context) (string, error) {
					id := ctx.RealIP()
					logger.Debug("Rate limiting HTTPS request",
						"ip", id,
						"remote_addr", ctx.Request().RemoteAddr)

					// Add a response header to show the detected IP (for debugging)
					ctx.Response().Header().Set("X-Rate-Limit-IP", id)

					return id, nil
				},
				DenyHandler: func(context echo.Context, identifier string, err error) error {
					logger.Warn("HTTPS rate limit exceeded",
						"ip", identifier,
						"path", context.Request().URL.Path,
						"method", context.Request().Method)
					return context.String(http.StatusTooManyRequests, "Too many requests")
				},
			}))

			// Add cleanup function to global slice
			rateLimiterCleanup = append(rateLimiterCleanup, func() {
				if err := httpsRateLimiter.Close(); err != nil {
					logger.Error("Failed to close HTTPS rate limiter store", "error", err)
				}
			})
		}
	} else {
		logger.Info("Rate limiting is disabled for HTTPS server via configuration")
	}

	// Add custom logger middleware with request ID if enabled in config
	if p.config.EnableHttpLogs {
		p.httpsServer.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format:           `{"time":"${time_rfc3339_nano}","id":"${id}","remote_ip":"${remote_ip}","host":"${host}","method":"${method}","uri":"${uri}","user_agent":"${user_agent}","status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}","bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n",
			CustomTimeFormat: "2006-01-02T15:04:05.00000Z07:00",
			Skipper: func(c echo.Context) bool {
				return !p.config.EnableHttpLogs
			},
		}))
		logger.Debug("HTTP request logging enabled for HTTPS server")
	} else {
		logger.Debug("HTTP request logging disabled for HTTPS server")
	}

	// Check if rate limiting is enabled via configuration
	if p.config.EnableRateLimit {
		// Create a new Starskey rate limiter store for HTTP server
		httpRateLimiter, err := kv.NewStarskeyRateLimiterStore(
			"./data/ratelimiter/http", // Path to store rate limit data
			2,                         // Rate: 2 requests per second (lowered from 10)
			10,                        // Burst: 10 requests (lowered from 30)
			3*time.Minute,             // Expires in 3 minutes
		)
		if err != nil {
			logger.Error("Failed to create HTTP rate limiter store, falling back to memory store", "error", err)

			// Fallback to memory store if Starskey store creation fails
			p.httpServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
				Store: middleware.NewRateLimiterMemoryStoreWithConfig(
					middleware.RateLimiterMemoryStoreConfig{
						Rate:      rate.Limit(2),   // 2 requests per second (lowered from 10)
						Burst:     10,              // Burst of 10 requests (lowered from 30)
						ExpiresIn: 3 * time.Minute, // Store expiration
					},
				),
				DenyHandler: func(context echo.Context, identifier string, err error) error {
					logger.Warn("Rate limit exceeded",
						"ip", identifier,
						"path", context.Request().URL.Path,
						"method", context.Request().Method)
					return context.String(http.StatusTooManyRequests, "Too many requests")
				},
			}))
		} else {
			// Use the Starskey store for HTTP rate limiting
			logger.Debug("Using Starskey rate limiter for HTTP server")

			// Add custom Starskey rate limiter middleware
			p.httpServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
				Store: httpRateLimiter,
				IdentifierExtractor: func(ctx echo.Context) (string, error) {
					id := ctx.RealIP()
					logger.Debug("Rate limiting HTTP request",
						"ip", id,
						"remote_addr", ctx.Request().RemoteAddr)

					// Add a response header to show the detected IP (for debugging)
					ctx.Response().Header().Set("X-Rate-Limit-IP", id)

					return id, nil
				},
				DenyHandler: func(context echo.Context, identifier string, err error) error {
					logger.Warn("HTTP rate limit exceeded",
						"ip", identifier,
						"path", context.Request().URL.Path,
						"method", context.Request().Method)
					return context.String(http.StatusTooManyRequests, "Too many requests")
				},
			}))

			// Add cleanup function to global slice
			rateLimiterCleanup = append(rateLimiterCleanup, func() {
				if err := httpRateLimiter.Close(); err != nil {
					logger.Error("Failed to close HTTP rate limiter store", "error", err)
				}
			})
		}
	} else {
		logger.Info("Rate limiting is disabled for HTTP server via configuration")
	}

	// Add custom logger middleware with request ID for HTTP server if enabled in config
	if p.config.EnableHttpLogs {
		p.httpServer.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format:           `{"time":"${time_rfc3339_nano}","id":"${id}","remote_ip":"${remote_ip}","host":"${host}","method":"${method}","uri":"${uri}","user_agent":"${user_agent}","status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}","bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n",
			CustomTimeFormat: "2006-01-02T15:04:05.00000Z07:00",
			Skipper: func(c echo.Context) bool {
				return !p.config.EnableHttpLogs
			},
		}))
		logger.Debug("HTTP request logging enabled for HTTP server")
	} else {
		logger.Debug("HTTP request logging disabled for HTTP server")
	}
}

// createRequestIDMiddleware returns a middleware function that generates a UUID for each request
func (p *Proxy) createRequestIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Generate a UUID for this request
			requestID := uuid.New().String()

			// Store it in the context
			c.Set(RequestIDKey, requestID)

			// Add it as a response header
			c.Response().Header().Set(echo.HeaderXRequestID, requestID)

			// If proxy detection is enabled, check for upstream proxy headers
			if p.config.DetectUpstreamProxy {
				p.detectUpstreamProxyFromRequest(c)
			}

			// Continue processing
			return next(c)
		}
	}
}

// detectUpstreamProxyFromRequest checks for headers that indicate TLS termination upstream
// This allows real-time detection as requests come in
func (p *Proxy) detectUpstreamProxyFromRequest(c echo.Context) {
	// Only check if detection is enabled
	if !p.config.DetectUpstreamProxy {
		return
	}

	// First check our cached detection status
	p.upstreamProxyMu.RLock()
	alreadyDetected := p.upstreamProxyDetected
	p.upstreamProxyMu.RUnlock()

	// Skip if we already detected a proxy previously
	if alreadyDetected {
		return
	}

	req := c.Request()
	headers := req.Header
	proxyDetected := false
	detectionMethod := ""
	headerName := ""
	headerValue := ""

	// Check for common TLS termination proxy headers
	// X-Forwarded-Proto: https indicates an upstream proxy terminated TLS
	if proto := headers.Get("X-Forwarded-Proto"); proto == "https" {
		proxyDetected = true
		detectionMethod = "request_header"
		headerName = "X-Forwarded-Proto"
		headerValue = proto
	}

	// X-Forwarded-SSL: on indicates an upstream proxy terminated TLS
	if !proxyDetected {
		if ssl := headers.Get("X-Forwarded-SSL"); ssl == "on" {
			proxyDetected = true
			detectionMethod = "request_header"
			headerName = "X-Forwarded-SSL"
			headerValue = ssl
		}
	}

	// Cloudflare specific headers
	if !proxyDetected {
		if cf := headers.Get("CF-Visitor"); cf != "" && strings.Contains(cf, "\"scheme\":\"https\"") {
			proxyDetected = true
			detectionMethod = "cloudflare_header"
			headerName = "CF-Visitor"
			headerValue = cf
		}
	}

	// If we detected a proxy, store the detection and log it
	if proxyDetected {
		// Store the detection in our state with lock protection
		p.upstreamProxyMu.Lock()
		p.upstreamProxyDetected = true
		p.upstreamProxyMu.Unlock()

		logger.Info("Upstream TLS-terminating proxy detected from request headers",
			"detection_method", detectionMethod,
			"header", headerName,
			"value", headerValue,
			"host", req.Host)

		// If certificate skipping is enabled, log it
		if p.config.SkipCertificates {
			logger.Info("Upstream TLS-terminating proxy detected during request, future certificate requests will be skipped",
				"host", req.Host,
				"certificate_acquisition", "will be skipped for new domains")
		}
	}
}
