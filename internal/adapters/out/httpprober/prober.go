// Package httpprober provides HTTP probing functionality for health checks.
package httpprober

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"
)

// DefaultTimeout is the default timeout for HTTP probes.
const DefaultTimeout = 5 * time.Second

// Prober implements the HTTPProber interface for health checking.
type Prober struct {
	client  *http.Client
	timeout time.Duration
}

// Option configures the Prober.
type Option func(*Prober)

// WithTimeout sets the probe timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(p *Prober) {
		p.timeout = timeout
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Prober) {
		p.client = client
	}
}

// New creates a new HTTP prober.
func New(opts ...Option) *Prober {
	p := &Prober{
		timeout: DefaultTimeout,
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.client == nil {
		p.client = &http.Client{
			Timeout: p.timeout,
			Transport: &http.Transport{
				// #nosec G402 - InsecureSkipVerify is intentional for health probing
				// internal services that may use self-signed certificates.
				// This prober only checks reachability, not data integrity.
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
				DisableKeepAlives: true,
			},
			// Don't follow redirects - we want to see the actual response
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	return p
}

// Probe sends an HTTP GET request and returns the status code and response time.
func (p *Prober) Probe(ctx context.Context, url string) (int, int64, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create request: %w", err)
	}

	// Set a reasonable user agent
	req.Header.Set("User-Agent", "Gordon-HealthCheck/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		elapsed := time.Since(start).Milliseconds()
		return 0, elapsed, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start).Milliseconds()
	return resp.StatusCode, elapsed, nil
}
