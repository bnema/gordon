package proxy

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"github.com/bnema/gordon/pkg/kv" // Correct import path based on the module name
)

// rateLimiterCleanup holds the cleanup functions for rate limiters
var rateLimiterCleanup []func()

// Create blacklistedIPs set to track IPs for middleware skipping
type requestContext struct {
	blacklisted bool
	requestID   string
}

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

	// Add debug middleware to log all headers
	p.httpsServer.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			log.Debug("Request headers",
				"method", req.Method,
				"path", req.URL.Path,
				"remote_addr", req.RemoteAddr,
				"x-forwarded-for", req.Header.Get("X-Forwarded-For"),
				"x-real-ip", req.Header.Get("X-Real-IP"))
			return next(c)
		}
	})

	// Create blacklist middleware for HTTPS
	p.httpsServer.Use(p.createBlacklistMiddleware())

	// Create a new Starskey rate limiter store for HTTPS server
	httpsRateLimiter, err := kv.NewStarskeyRateLimiterStore(
		"./data/ratelimiter/https", // Path to store rate limit data
		2,                          // Rate: 2 requests per second (lowered from 10)
		10,                         // Burst: 10 requests (lowered from 30)
		3*time.Minute,              // Expires in 3 minutes
	)
	if err != nil {
		log.Error("Failed to create HTTPS rate limiter store, falling back to memory store", "error", err)

		// Fallback to memory store if Starskey store creation fails
		p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
			Store: middleware.NewRateLimiterMemoryStoreWithConfig(
				middleware.RateLimiterMemoryStoreConfig{
					Rate:      rate.Limit(2),   // 2 requests per second (lowered from 10)
					Burst:     10,              // Burst of 10 requests (lowered from 30)
					ExpiresIn: 3 * time.Minute, // Store expiration
				},
			),
			DenyHandler: func(context echo.Context, identifier string, err error) error {
				log.Warn("Rate limit exceeded",
					"ip", identifier,
					"path", context.Request().URL.Path,
					"method", context.Request().Method)
				return context.String(http.StatusTooManyRequests, "Too many requests")
			},
		}))
	} else {
		// Use the Starskey store for HTTPS rate limiting
		log.Debug("Using Starskey rate limiter for HTTPS server")

		// Add custom Starskey rate limiter middleware
		p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
			Store: httpsRateLimiter,
			IdentifierExtractor: func(ctx echo.Context) (string, error) {
				id := ctx.RealIP()
				log.Debug("Rate limiting HTTPS request",
					"ip", id,
					"remote_addr", ctx.Request().RemoteAddr)

				// Add a response header to show the detected IP (for debugging)
				ctx.Response().Header().Set("X-Rate-Limit-IP", id)

				return id, nil
			},
			DenyHandler: func(context echo.Context, identifier string, err error) error {
				log.Warn("HTTPS rate limit exceeded",
					"ip", identifier,
					"path", context.Request().URL.Path,
					"method", context.Request().Method)
				return context.String(http.StatusTooManyRequests, "Too many requests")
			},
		}))

		// Add cleanup function to global slice
		rateLimiterCleanup = append(rateLimiterCleanup, func() {
			if err := httpsRateLimiter.Close(); err != nil {
				log.Error("Failed to close HTTPS rate limiter store", "error", err)
			}
		})
	}

	// Add custom logger middleware with request ID if enabled in config
	if p.config.EnableLogs {
		p.httpsServer.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: `{"time":"${time_rfc3339_nano}","id":"${id}","remote_ip":"${remote_ip}","host":"${host}","method":"${method}","uri":"${uri}","user_agent":"${user_agent}","status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}","bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n",
			CustomTimeFormat: "2006-01-02T15:04:05.00000Z07:00",
			Skipper: func(c echo.Context) bool {
				return !p.config.EnableLogs
			},
		}))
		log.Debug("HTTP request logging enabled for HTTPS server")
	} else {
		log.Debug("HTTP request logging disabled for HTTPS server")
	}

	// Create blacklist middleware for HTTP
	p.httpServer.Use(p.createBlacklistMiddleware())

	// Create a new Starskey rate limiter store for HTTP server
	httpRateLimiter, err := kv.NewStarskeyRateLimiterStore(
		"./data/ratelimiter/http", // Path to store rate limit data
		2,                         // Rate: 2 requests per second (lowered from 10)
		10,                        // Burst: 10 requests (lowered from 30)
		3*time.Minute,             // Expires in 3 minutes
	)
	if err != nil {
		log.Error("Failed to create HTTP rate limiter store, falling back to memory store", "error", err)

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
				log.Warn("Rate limit exceeded",
					"ip", identifier,
					"path", context.Request().URL.Path,
					"method", context.Request().Method)
				return context.String(http.StatusTooManyRequests, "Too many requests")
			},
		}))
	} else {
		// Use the Starskey store for HTTP rate limiting
		log.Debug("Using Starskey rate limiter for HTTP server")

		// Add custom Starskey rate limiter middleware
		p.httpServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
			Store: httpRateLimiter,
			IdentifierExtractor: func(ctx echo.Context) (string, error) {
				id := ctx.RealIP()
				log.Debug("Rate limiting HTTP request",
					"ip", id,
					"remote_addr", ctx.Request().RemoteAddr)

				// Add a response header to show the detected IP (for debugging)
				ctx.Response().Header().Set("X-Rate-Limit-IP", id)

				return id, nil
			},
			DenyHandler: func(context echo.Context, identifier string, err error) error {
				log.Warn("HTTP rate limit exceeded",
					"ip", identifier,
					"path", context.Request().URL.Path,
					"method", context.Request().Method)
				return context.String(http.StatusTooManyRequests, "Too many requests")
			},
		}))

		// Add cleanup function to global slice
		rateLimiterCleanup = append(rateLimiterCleanup, func() {
			if err := httpRateLimiter.Close(); err != nil {
				log.Error("Failed to close HTTP rate limiter store", "error", err)
			}
		})
	}

	// Add custom logger middleware with request ID for HTTP server if enabled in config
	if p.config.EnableLogs {
		p.httpServer.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: `{"time":"${time_rfc3339_nano}","id":"${id}","remote_ip":"${remote_ip}","host":"${host}","method":"${method}","uri":"${uri}","user_agent":"${user_agent}","status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}","bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n",
			CustomTimeFormat: "2006-01-02T15:04:05.00000Z07:00",
			Skipper: func(c echo.Context) bool {
				return !p.config.EnableLogs
			},
		}))
		log.Debug("HTTP request logging enabled for HTTP server")
	} else {
		log.Debug("HTTP request logging disabled for HTTP server")
	}
}

// createRequestIDMiddleware returns a middleware function that generates a UUID for each request
func (p *Proxy) createRequestIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Generate a new UUID for this request
			requestID := uuid.New().String()
			
			// Store the UUID in the context
			c.Set(RequestIDKey, requestID)
			
			// Add the UUID as a response header
			c.Response().Header().Set(echo.HeaderXRequestID, requestID)
			
			// Continue with the next middleware
			return next(c)
		}
	}
}

// createBlacklistMiddleware returns a middleware function that checks IPs against the blacklist
func (p *Proxy) createBlacklistMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Get client IP
			clientIP := c.RealIP()

			// Get host and strip port if present
			host := c.Request().Host
			if strings.Contains(host, ":") {
				host = strings.Split(host, ":")[0]
			}

			// Check if the host is an IP address
			if net.ParseIP(host) != nil {
				// This is an IP address being used as a hostname - silently reject
				// This prevents log spam without adding IPs to the blacklist
				// Set context for other middleware
				c.Set("reqContext", &requestContext{blacklisted: true})
				return c.String(http.StatusForbidden, "Forbidden")
			}

			// Debug log for all requests to check if the IP is being properly recognized
			log.Debug("Received request", "ip", clientIP, "path", c.Request().URL.Path)

			// Check if IP is blacklisted (if blacklist exists)
			if p.blacklist != nil && p.blacklist.IsBlocked(clientIP) {
				// Set context for other middleware
				c.Set("reqContext", &requestContext{blacklisted: true})

				// Rate-limited logging of blocked IPs
				p.logBlockedIP(clientIP, c.Request().URL.Path, c.Request().UserAgent())

				// Return forbidden without calling next handlers at all
				return c.String(http.StatusForbidden, "Forbidden")
			}

			// Store the context in the request context for non-blacklisted IPs
			c.Set("reqContext", &requestContext{blacklisted: false})
			return next(c)
		}
	}
}
