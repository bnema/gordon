package proxy

import (
	"net"
	"net/http"
	"strings"
	"sync"
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

// Cooldown period for logging direct IP access warnings
const logCooldown = 1 * time.Minute

// Map to store the last log time for each IP address attempting direct access
var ipLogTimes = make(map[string]time.Time)
var ipLogMutex sync.Mutex // Mutex to protect access to ipLogTimes

// isHostIPAddress checks if the host part of a string (like "1.2.3.4:80" or "1.2.3.4") is a valid IP address.
func isHostIPAddress(host string) bool {
	// Split host and port
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// If splitting fails, assume it might be just an IP without a port
		h = host
	}
	// Try parsing the host part as an IP address
	ip := net.ParseIP(h)
	return ip != nil
}

// blockDirectIPMiddleware rejects requests where the Host header is an IP address,
// unless it's an ACME challenge request. It includes a log cooldown mechanism.
func blockDirectIPMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			host := c.Request().Host
			path := c.Request().URL.Path
			realIP := c.RealIP() // Get the real IP once

			if isHostIPAddress(host) {
				// Allow ACME HTTP-01 challenges to proceed even via IP
				if strings.HasPrefix(path, "/.well-known/acme-challenge/") {
					logger.Debug("Allowing direct IP access for ACME challenge", "host", host, "path", path, "ip", realIP)
					return next(c)
				}

				// --- Log Cooldown Logic ---
				now := time.Now()
				logAllowed := false

				ipLogMutex.Lock()
				lastLogTime, exists := ipLogTimes[realIP]
				if !exists || now.Sub(lastLogTime) > logCooldown {
					// It's a new IP or the cooldown has passed
					ipLogTimes[realIP] = now // Update the last log time
					logAllowed = true
				}
				ipLogMutex.Unlock()

				// Log only if allowed by the cooldown
				if logAllowed {
					logger.Warn("Blocking direct IP access attempt (log cooldown active)",
						"host", host,
						"path", path,
						"ip", realIP,
						"user_agent", c.Request().UserAgent())
				} else {
					// Optionally log at DEBUG level that we suppressed a log, useful for verifying
					// logger.Debug("Suppressed duplicate direct IP access log due to cooldown", "ip", realIP, "host", host)
				}
				// --- End Log Cooldown Logic ---

				// Block other direct IP access attempts regardless of logging
				return echo.NewHTTPError(http.StatusForbidden, "Direct IP access is not permitted.")
			}

			// If host is not an IP, proceed normally
			return next(c)
		}
	}
}

// setupMiddleware configures middleware for both HTTP and HTTPS servers
func (p *Proxy) setupMiddleware() {
	// Configure Echo IP extraction. Since the proxy is edge-facing (directly exposed),
	// ignore X-Forwarded-For and use the direct remote address.
	p.httpsServer.IPExtractor = echo.ExtractIPDirect()

	// Add UUID generator middleware
	p.httpsServer.Use(p.createRequestIDMiddleware())

	// --- Block Direct IP Access Middleware (Conditional) ---
	if p.config.BlockDirectIP {
		// This should run early, before logging or rate limiting potentially.
		p.httpsServer.Use(blockDirectIPMiddleware())
		logger.Info("Direct IP blocking middleware is ENABLED (allows ACME)")
	} else {
		logger.Info("Direct IP blocking middleware is DISABLED")
	}

	// Add middleware to log incoming headers (for debugging Cloudflare detection)
	// p.httpsServer.Use(logHeadersMiddleware())

	// --- Rate Limiting Setup (Domain Only Now) ---
	if p.config.EnableRateLimit {
		logger.Debug("Rate limiting enabled. Setting up limiter for domain requests only.")

		// --- REMOVED Rate Limiter for Direct IP Access ---

		// --- Rate Limiter for Domain Access (Existing Logic) ---
		// Use Starskey or fallback for requests coming through a domain name.
		domainRateLimiterStore, err := kv.NewStarskeyRateLimiterStore(
			"./data/ratelimiter/domain", // Path for domain limiter data
			10,                          // Rate: 10 requests per second (default)
			30,                          // Burst: 30 requests (default)
			3*time.Minute,               // Expires in 3 minutes
		)

		if err != nil {
			logger.Error("Failed to create Domain rate limiter store (Starskey), falling back to memory store", "error", err)
			// Fallback to memory store
			p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
				Store: middleware.NewRateLimiterMemoryStoreWithConfig(
					middleware.RateLimiterMemoryStoreConfig{
						Rate:      rate.Limit(10),
						Burst:     30,
						ExpiresIn: 3 * time.Minute,
					},
				),
				// Apply this limiter ONLY if the host is NOT an IP address (redundant now but safe)
				Skipper: func(c echo.Context) bool {
					host := c.Request().Host
					isIP := isHostIPAddress(host)
					if !isIP {
						// Log only if applying the limiter
						logger.Debug("Applying domain rate limiter (Memory Fallback)", "host", host, "ip", c.RealIP())
					}
					return isIP // Skip if it IS an IP
				},
				IdentifierExtractor: func(ctx echo.Context) (string, error) {
					return ctx.RealIP(), nil
				},
				DenyHandler: func(context echo.Context, identifier string, err error) error {
					logger.Warn("Domain rate limit exceeded (Memory Fallback)",
						"ip", identifier,
						"host", context.Request().Host,
						"path", context.Request().URL.Path,
						"method", context.Request().Method,
						"error", err.Error())
					return context.String(http.StatusTooManyRequests, "Too many requests")
				},
			}))
		} else {
			logger.Debug("Using Starskey rate limiter for domain requests")
			// Use the Starskey store
			p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
				Store: domainRateLimiterStore,
				// Apply this limiter ONLY if the host is NOT an IP address (redundant now but safe)
				Skipper: func(c echo.Context) bool {
					host := c.Request().Host
					isIP := isHostIPAddress(host)
					if !isIP {
						// Log only if applying the limiter
						logger.Debug("Applying domain rate limiter (Starskey)", "host", host, "ip", c.RealIP())
					}
					return isIP // Skip if it IS an IP
				},
				IdentifierExtractor: func(ctx echo.Context) (string, error) {
					id := ctx.RealIP()
					ctx.Response().Header().Set("X-Rate-Limit-Domain-IP", id) // Keep debug header
					return id, nil
				},
				DenyHandler: func(context echo.Context, identifier string, err error) error {
					logger.Warn("Domain rate limit exceeded (Starskey)",
						"ip", identifier,
						"host", context.Request().Host,
						"path", context.Request().URL.Path,
						"method", context.Request().Method,
						"error", err.Error())
					return context.String(http.StatusTooManyRequests, "Too many requests")
				},
			}))

			// Add cleanup function for the Starskey domain limiter
			rateLimiterCleanup = append(rateLimiterCleanup, func() {
				if err := domainRateLimiterStore.Close(); err != nil {
					logger.Error("Failed to close Domain rate limiter store (Starskey)", "error", err)
				}
			})
		}
		logger.Debug("Configured rate limiter for domain requests")

	} else {
		logger.Info("Rate limiting is disabled globally via configuration")
	}

	// --- Logging Middleware (Existing) ---
	if p.config.EnableHttpLogs {
		p.httpsServer.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			// Use a standard string with escaped quotes for the JSON format
			Format:           "{\"time\":\"${time_rfc3339_nano}\",\"id\":\"${id}\",\"remote_ip\":\"${remote_ip}\",\"host\":\"${host}\",\"method\":\"${method}\",\"uri\":\"${uri}\",\"user_agent\":\"${user_agent}\",\"status\":${status},\"error\":\"${error}\",\"latency\":${latency},\"latency_human\":\"${latency_human}\",\"bytes_in\":${bytes_in},\"bytes_out\":${bytes_out}}\n",
			CustomTimeFormat: "2006-01-02T15:04:05.00000Z07:00",
			Skipper: func(c echo.Context) bool {
				// Skip logging if disabled in config OR if it's a successful ACME challenge
				// (to avoid polluting logs during certificate issuance)
				isAcme := strings.HasPrefix(c.Request().URL.Path, "/.well-known/acme-challenge/")
				return !p.config.EnableHttpLogs || (isAcme && c.Response().Status == http.StatusOK)
			},
		}))
		logger.Debug("HTTP request logging enabled for proxy server")
	} else {
		logger.Debug("HTTP request logging disabled for proxy server")
	}

	// REMOVED the duplicated rate limiter and logger blocks that seemed intended for a separate HTTP server
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

			// Continue processing
			return next(c)
		}
	}
}

// logHeadersMiddleware creates a middleware function to log request headers
// func logHeadersMiddleware() echo.MiddlewareFunc {
// 	return func(next echo.HandlerFunc) echo.HandlerFunc {
// 		return func(c echo.Context) error {
// 			// Log all headers at DEBUG level
// 			headerStrings := []string{}
// 			for k, v := range c.Request().Header {
// 				headerStrings = append(headerStrings, fmt.Sprintf("%s: %s", k, strings.Join(v, ",")))
// 			}
// 			logger.Debug("Incoming Request Headers", "request_id", c.Get(RequestIDKey), "headers", strings.Join(headerStrings, " | "))

// 			return next(c)
// 		}
// 	}
// }
