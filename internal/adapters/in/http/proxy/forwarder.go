// Package proxy implements the HTTP adapter for the reverse proxy.
package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	gordonhttp "github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/domain"
)

// newAppTransport creates the shared HTTP/1.1 transport for proxying to application containers.
func newAppTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
	}
}

// newH2CTransport creates a dedicated cleartext HTTP/2 transport for containers
// that opt in via the gordon.proxy.protocol=h2c label.
// Uses Go 1.24+ native UnencryptedHTTP2 protocol support.
// HTTP/1 is disabled so http:// URLs use HTTP/2 prior-knowledge.
func newH2CTransport() *http.Transport {
	var protos http.Protocols
	protos.SetUnencryptedHTTP2(true)
	// Do NOT set HTTP1 — this forces HTTP/2 for http:// URLs.
	return &http.Transport{
		Protocols: &protos,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
	}
}

// newRegistryTransport creates the transport for the registry reverse proxy (localhost loopback).
// It needs a longer ResponseHeaderTimeout because admin endpoints like /admin/deploy
// perform blocking operations (image pull, container start, readiness checks).
func newRegistryTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 3 * time.Minute,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
	}
}

// transportForTarget selects the appropriate HTTP transport based on the target's protocol.
func (h *Handler) transportForTarget(target *domain.ProxyTarget) http.RoundTripper {
	if target.Protocol == "h2c" {
		return h.h2cTransport
	}
	return h.appTransport
}

// forwardToTarget proxies a request to the resolved target.
func (h *Handler) forwardToTarget(w http.ResponseWriter, r *http.Request, target *domain.ProxyTarget, maxResponseSize int64) {
	log := zerowrap.FromCtx(r.Context())

	targetURL, err := url.Parse(fmt.Sprintf("%s://%s", target.Scheme, net.JoinHostPort(target.Host, strconv.Itoa(target.Port))))
	if err != nil {
		log.WrapErr(err, "failed to parse target URL")
		proxyError(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	originalHost := target.OriginalHost
	releaseInFlight := h.proxySvc.TrackInFlight(target.ContainerID)
	defer releaseInFlight()

	transport := h.transportForTarget(target)

	proxy := newReverseProxy(targetURL, originalHost, transport, r, h.trustedNets,
		func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
			var maxBytesErr *http.MaxBytesError
			if errors.As(proxyErr, &maxBytesErr) {
				log.Warn().Err(proxyErr).Str("target", targetURL.String()).Msg("proxy error: request body too large")
				proxyError(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			log.Error().Err(proxyErr).Str("target", targetURL.String()).Msg("proxy error: connection failed")
			proxyError(w, "Service Unavailable", http.StatusServiceUnavailable)
		},
		modifyResponse(maxResponseSize),
	)

	if target.Protocol == "h2c" {
		log.Debug().
			Str("target", targetURL.String()).
			Str("protocol", "h2c").
			Str(zerowrap.FieldEntityID, target.ContainerID).
			Msg("proxying request via h2c")
	} else {
		log.Debug().
			Str("target", targetURL.String()).
			Str(zerowrap.FieldEntityID, target.ContainerID).
			Msg("proxying request")
	}

	proxy.ServeHTTP(w, r)
}

// forwardToRegistry proxies a request to the local registry server.
func (h *Handler) forwardToRegistry(w http.ResponseWriter, r *http.Request, registryPort int) {
	h.proxySvc.TrackRegistryRequest()
	defer h.proxySvc.ReleaseRegistryRequest()

	log := zerowrap.FromCtx(r.Context())

	targetURL, err := url.Parse(fmt.Sprintf("http://localhost:%d", registryPort))
	if err != nil {
		log.WrapErr(err, "failed to parse registry target URL")
		proxyError(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()
			pr.Out.Host = targetURL.Host
		},
		Transport: h.registryTransport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
			var maxBytesErr *http.MaxBytesError
			if errors.As(proxyErr, &maxBytesErr) {
				log.Warn().Err(proxyErr).Int("registry_port", registryPort).Msg("registry proxy error: request body too large")
				proxyError(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			log.Error().Err(proxyErr).Int("registry_port", registryPort).Msg("registry proxy error")
			proxyError(w, "Registry Unavailable", http.StatusServiceUnavailable)
		},
		// Strip browser-oriented security headers from upstream registry responses
		// to avoid conflicts with Gordon's own headers on browser-facing routes.
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("X-Content-Type-Options")
			resp.Header.Del("X-Frame-Options")
			resp.Header.Del("X-XSS-Protection")
			resp.Header.Del("Referrer-Policy")
			resp.Header.Del("Permissions-Policy")
			resp.Header.Del("Content-Security-Policy")
			resp.Header.Del("Strict-Transport-Security")

			return nil
		},
	}

	log.Debug().Str("target", targetURL.String()).Msg("proxying request to registry")
	proxy.ServeHTTP(w, r)
}

// newReverseProxy creates a reverse proxy using Rewrite instead of Director to prevent
// hop-by-hop header attacks. A malicious client could send "Connection: Authorization"
// to strip the Authorization header when using the default Director. Rewrite processes
// headers after hop-by-hop removal, ensuring headers like Authorization are preserved.
//
// originalHost, when non-empty, overrides the Host header sent to the backend.
// This is used for DNS-pinned targets where the proxy dials an IP but needs to
// send the original hostname for virtual-hosted upstreams.
//
// incomingReq and trustedNets are used to preserve X-Forwarded-Proto when the
// request originates from a trusted upstream proxy.
func newReverseProxy(targetURL *url.URL, originalHost string, transport http.RoundTripper, incomingReq *http.Request, trustedNets []*net.IPNet, errorHandler func(http.ResponseWriter, *http.Request, error), modifyResp func(*http.Response) error) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			existingProto := pr.In.Header.Get("X-Forwarded-Proto")
			pr.SetURL(targetURL)
			pr.SetXForwarded()
			if originalHost != "" {
				pr.Out.Host = net.JoinHostPort(originalHost, targetURL.Port())
			} else {
				pr.Out.Host = targetURL.Host
			}
			// Preserve X-Forwarded-Proto from trusted upstream proxies.
			// SetXForwarded() unconditionally sets it based on the incoming scheme
			// (HTTP between proxies), but when a trusted proxy already sent the
			// real client proto, we must honor it.
			if existingProto != "" && isTrustedSource(incomingReq, trustedNets) {
				pr.Out.Header.Set("X-Forwarded-Proto", existingProto)
			}
		},
		Transport:      transport,
		ErrorHandler:   errorHandler,
		ModifyResponse: modifyResp,
	}
}

// isTrustedSource reports whether the request's remote address is in trustedNets.
func isTrustedSource(r *http.Request, trustedNets []*net.IPNet) bool {
	return gordonhttp.IsTrustedSource(r, trustedNets)
}

// modifyResponse returns a function that adds proxy headers and enforces response size limits.
func modifyResponse(maxResponseSize int64) func(*http.Response) error {
	return func(resp *http.Response) error {
		if maxResponseSize > 0 {
			if resp.ContentLength > maxResponseSize {
				resp.Body.Close()
				const msg = "Response Too Large"
				resp.StatusCode = http.StatusBadGateway
				resp.Body = io.NopCloser(strings.NewReader(msg))
				resp.ContentLength = int64(len(msg))
				resp.Header.Set("Content-Type", "text/plain")
				resp.Header.Set("Cache-Control", "no-store")
				resp.Header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
				resp.Header.Del("Transfer-Encoding")
				resp.Header.Del("Content-Encoding")
				resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
				return nil
			}
			if resp.ContentLength < 0 {
				resp.Body = &limitedReadCloser{
					ReadCloser: resp.Body,
					remaining:  maxResponseSize,
				}
			}
		}

		return nil
	}
}

// limitedReadCloser wraps an io.ReadCloser with a byte limit.
type limitedReadCloser struct {
	io.ReadCloser
	remaining int64
	closed    bool
}

func (l *limitedReadCloser) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		if !l.closed {
			l.closed = true
			l.ReadCloser.Close()
		}
		return 0, fmt.Errorf("response body exceeded maximum size limit (remaining: %d bytes)", l.remaining)
	}
	toRead := len(p)
	if int64(toRead) > l.remaining {
		toRead = int(l.remaining)
	}
	n, err := l.ReadCloser.Read(p[:toRead])
	// Cap n to the requested slice to guard against misbehaving readers
	// that return more bytes than the buffer length.
	if n > toRead {
		n = toRead
	}
	l.remaining -= int64(n)
	if l.remaining < 0 {
		l.remaining = 0
	}
	return n, err
}

func (l *limitedReadCloser) Close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	return l.ReadCloser.Close()
}

// proxyError writes an error response with security headers appropriate for
// proxy-generated error pages.
func proxyError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, code)
}
