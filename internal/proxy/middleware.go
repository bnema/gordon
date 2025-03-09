package proxy

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// Create blacklistedIPs set to track IPs for middleware skipping
type requestContext struct {
	blacklisted bool
}

// setupMiddleware configures middleware for both HTTP and HTTPS servers
func (p *Proxy) setupMiddleware() {
	// Create blacklist middleware for HTTPS
	p.httpsServer.Use(p.createBlacklistMiddleware())

	// Add rate limiter middleware to prevent spam attacks
	p.httpsServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(10),  // 10 requests per second
				Burst:     30,              // Burst of 30 requests
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

	// Add default Echo logger middleware only if enabled in config
	if p.config.EnableLogs {
		p.httpsServer.Use(middleware.Logger())
		log.Debug("HTTP request logging enabled for HTTPS server")
	} else {
		log.Debug("HTTP request logging disabled for HTTPS server")
	}

	// Create blacklist middleware for HTTP
	p.httpServer.Use(p.createBlacklistMiddleware())

	// Add rate limiter middleware to prevent spam attacks for HTTP server
	p.httpServer.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(10),  // 10 requests per second
				Burst:     30,              // Burst of 30 requests
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

	// Add default Echo logger middleware for HTTP server only if enabled in config
	if p.config.EnableLogs {
		p.httpServer.Use(middleware.Logger())
		log.Debug("HTTP request logging enabled for HTTP server")
	} else {
		log.Debug("HTTP request logging disabled for HTTP server")
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
