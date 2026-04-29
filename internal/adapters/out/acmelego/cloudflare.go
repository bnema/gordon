package acmelego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// zoneAPIResponse is a minimal Cloudflare API response for zone listing.
type zoneAPIResponse struct {
	Success bool            `json:"success"`
	Errors  []zoneAPIError  `json:"errors"`
	Result  []zoneAPIResult `json:"result"`
}

type zoneAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type zoneAPIResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

const defaultCloudflareBaseURL = "https://api.cloudflare.com/client/v4"

// CloudflareZoneResolver resolves a hostname to its Cloudflare DNS zone by
// searching from the full hostname down to the TLD.
type CloudflareZoneResolver struct {
	token   string
	baseURL string
	client  *http.Client

	mu    sync.RWMutex
	cache map[string]zoneCacheEntry
}

type zoneCacheEntry struct {
	zone      out.CloudflareZone
	err       error
	createdAt time.Time
}

const (
	defaultCacheTTL        = 10 * time.Minute
	defaultCacheMaxSize    = 100
	defaultMaxResponseSize = 1 << 20 // 1 MB
)

// CloudflareZoneResolverOption configures a CloudflareZoneResolver.
type CloudflareZoneResolverOption func(*CloudflareZoneResolver)

// WithCloudflareBaseURL sets the base URL for the Cloudflare API.
func WithCloudflareBaseURL(baseURL string) CloudflareZoneResolverOption {
	return func(r *CloudflareZoneResolver) {
		r.baseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithCloudflareHTTPClient sets the HTTP client for the resolver.
func WithCloudflareHTTPClient(client *http.Client) CloudflareZoneResolverOption {
	return func(r *CloudflareZoneResolver) {
		r.client = client
	}
}

// NewCloudflareZoneResolver creates a new CloudflareZoneResolver.
func NewCloudflareZoneResolver(token string, opts ...CloudflareZoneResolverOption) *CloudflareZoneResolver {
	r := &CloudflareZoneResolver{
		token:   token,
		baseURL: defaultCloudflareBaseURL,
		client:  http.DefaultClient,
		cache:   make(map[string]zoneCacheEntry),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// compile-time interface check
var _ out.CloudflareZoneResolver = (*CloudflareZoneResolver)(nil)

// FindZone finds the most specific Cloudflare zone for the given domain.
// It searches from the full hostname down to the TLD (e.g. for
// api.prod.example.com it tries api.prod.example.com, prod.example.com,
// example.com, com) and returns the first active zone found.
func (r *CloudflareZoneResolver) FindZone(ctx context.Context, domainName string) (out.CloudflareZone, error) {
	domainName = strings.TrimSuffix(strings.TrimSpace(domainName), ".")
	if domainName == "" {
		return out.CloudflareZone{}, fmt.Errorf("cloudflare zone resolver: empty domain")
	}

	if cached, ok := r.cached(domainName); ok {
		return cached.zone, cached.err
	}

	parts := strings.Split(domainName, ".")

	// candidateCount is the total possible candidates (full domain down to TLD).
	// For "api.prod.example.com" -> 4 candidates; for "example.com" -> 2.
	candidateCount := len(parts)
	candidates := make([]string, 0, candidateCount)

	// Build candidates from full hostname down to the TLD.
	// For parts ["api","prod","example","com"]:
	//   "api.prod.example.com", "prod.example.com", "example.com", "com"
	for i := range parts {
		candidate := strings.Join(parts[i:], ".")
		candidates = append(candidates, candidate)
	}

	var candidateErrs []error
	for _, candidate := range candidates {
		if cached, ok := r.cached(candidate); ok {
			if cached.err == nil {
				r.storeCache(domainName, cached.zone, nil)
				return cached.zone, nil
			}
			candidateErrs = append(candidateErrs, fmt.Errorf("%s: %w", candidate, cached.err))
			continue
		}
		zone, err := r.findZoneByName(ctx, candidate)
		r.storeCache(candidate, zone, err)
		if err != nil {
			candidateErrs = append(candidateErrs, fmt.Errorf("%s: %w", candidate, err))
			continue
		}
		r.storeCache(domainName, zone, nil)
		return zone, nil
	}

	err := fmt.Errorf("cloudflare zone resolver: no active zone found for %q: %w", domainName, errors.Join(candidateErrs...))
	r.storeCache(domainName, out.CloudflareZone{}, err)
	return out.CloudflareZone{}, err
}

func (r *CloudflareZoneResolver) cached(name string) (zoneCacheEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.cache[name]
	if !ok {
		return entry, false
	}
	if time.Since(entry.createdAt) > defaultCacheTTL {
		return zoneCacheEntry{}, false
	}
	return entry, true
}

func (r *CloudflareZoneResolver) storeCache(name string, zone out.CloudflareZone, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= defaultCacheMaxSize {
		// Evict oldest entry
		var oldestKey string
		var oldestTime time.Time
		for k, v := range r.cache {
			if oldestKey == "" || v.createdAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.createdAt
			}
		}
		delete(r.cache, oldestKey)
	}
	r.cache[name] = zoneCacheEntry{zone: zone, err: err, createdAt: time.Now()}
}

// findZoneByName queries the Cloudflare API for an active zone with the exact given name.
func (r *CloudflareZoneResolver) findZoneByName(ctx context.Context, name string) (out.CloudflareZone, error) {
	u, err := url.Parse(r.baseURL + "/zones")
	if err != nil {
		return out.CloudflareZone{}, fmt.Errorf("parse url: %w", err)
	}

	q := u.Query()
	q.Set("name", name)
	q.Set("status", "active")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return out.CloudflareZone{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return out.CloudflareZone{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	lr := io.LimitReader(resp.Body, defaultMaxResponseSize+1)
	body, err := io.ReadAll(lr)
	if err != nil {
		return out.CloudflareZone{}, fmt.Errorf("read response: %w", err)
	}
	if len(body) > defaultMaxResponseSize {
		return out.CloudflareZone{}, fmt.Errorf("cloudflare api: response body too large (%d bytes)", len(body))
	}

	if resp.StatusCode != http.StatusOK {
		return out.CloudflareZone{}, fmt.Errorf("cloudflare api: status %d", resp.StatusCode)
	}

	var apiResp zoneAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return out.CloudflareZone{}, fmt.Errorf("parse response: %w", err)
	}

	if !apiResp.Success {
		errMsg := "cloudflare api returned unsuccessfully"
		if len(apiResp.Errors) > 0 {
			errMsg = apiResp.Errors[0].Message
		}
		return out.CloudflareZone{}, fmt.Errorf("cloudflare api: %s", errMsg)
	}

	for _, z := range apiResp.Result {
		if z.Name == name && z.Status == "active" {
			return out.CloudflareZone{
				ID:   z.ID,
				Name: z.Name,
			}, nil
		}
	}

	return out.CloudflareZone{}, fmt.Errorf("no active zone found for %q", name)
}
