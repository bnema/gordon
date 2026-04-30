package acmelego

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
)

func TestCloudflareZoneResolverFindsParentZone(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		// Verify auth header
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		// Verify query parameters
		name := r.URL.Query().Get("name")
		status := r.URL.Query().Get("status")
		assert.Equal(t, "active", status)

		switch name {
		case "api.prod.example.com":
			// Return empty - no zone found
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(zoneAPIResponse{
				Success: true,
				Result:  []zoneAPIResult{},
			})
		case "prod.example.com":
			// Return empty - no zone found
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(zoneAPIResponse{
				Success: true,
				Result:  []zoneAPIResult{},
			})
		case "example.com":
			// Return active zone
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(zoneAPIResponse{
				Success: true,
				Result: []zoneAPIResult{
					{
						ID:     "zone-id-123",
						Name:   "example.com",
						Status: "active",
					},
				},
			})
		default:
			t.Errorf("unexpected zone name query: %s", name)
		}
	}))
	defer server.Close()

	resolver := NewCloudflareZoneResolver("test-token",
		WithCloudflareBaseURL(server.URL))

	zone, err := resolver.FindZone(context.Background(), "api.prod.example.com")
	require.NoError(t, err)
	assert.Equal(t, "zone-id-123", zone.ID)
	assert.Equal(t, "example.com", zone.Name)
	assert.Equal(t, 3, requestCount, "should have made 3 API calls")
}

func TestCloudflareZoneResolverDoesNotLeakTokenInError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a 500 error
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"success":false,"errors":[]}`))
	}))
	defer server.Close()

	resolver := NewCloudflareZoneResolver("super-secret-token-abc123",
		WithCloudflareBaseURL(server.URL))

	_, err := resolver.FindZone(context.Background(), "example.com")
	require.Error(t, err)
	errStr := err.Error()

	// The error must NOT contain the token
	assert.NotContains(t, errStr, "super-secret-token-abc123")
	assert.NotContains(t, errStr, "secret-token")
}

func TestCloudflareZoneResolverSimpleDomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		assert.Equal(t, "example.com", name)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(zoneAPIResponse{
			Success: true,
			Result: []zoneAPIResult{
				{
					ID:     "zone-1",
					Name:   "example.com",
					Status: "active",
				},
			},
		})
	}))
	defer server.Close()

	resolver := NewCloudflareZoneResolver("test-token",
		WithCloudflareBaseURL(server.URL))

	zone, err := resolver.FindZone(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Equal(t, "zone-1", zone.ID)
	assert.Equal(t, "example.com", zone.Name)
}

func TestCloudflareZoneResolverNoZoneFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(zoneAPIResponse{
			Success: true,
			Result:  []zoneAPIResult{},
		})
	}))
	defer server.Close()

	resolver := NewCloudflareZoneResolver("test-token",
		WithCloudflareBaseURL(server.URL))

	_, err := resolver.FindZone(context.Background(), "missing.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active zone found")
}

func TestCloudflareZoneResolverCachesErrorsWithShortTTL(t *testing.T) {
	resolver := NewCloudflareZoneResolver("token")
	resolver.storeCache("example.com", out.CloudflareZone{}, errors.New("temporary failure"))

	entry, ok := resolver.cached("example.com")
	require.True(t, ok)
	assert.Error(t, entry.err)
	assert.Equal(t, defaultErrorCacheTTL, entry.ttl)
}

func TestCloudflareZoneResolverUnsuccessfulAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(zoneAPIResponse{
			Success: false,
			Errors: []zoneAPIError{
				{Code: 1000, Message: "authentication error"},
			},
			Result: []zoneAPIResult{},
		})
	}))
	defer server.Close()

	resolver := NewCloudflareZoneResolver("bad-token",
		WithCloudflareBaseURL(server.URL))

	_, err := resolver.FindZone(context.Background(), "example.com")
	require.Error(t, err)
	// The resolver iterates all candidates; the final error is the generic one.
	assert.Contains(t, err.Error(), "no active zone found")
	// Token must never leak in error messages.
	assert.NotContains(t, err.Error(), "bad-token")
}

// Ensure the interface compliance.
func TestCloudflareZoneResolverImplementsInterface(t *testing.T) {
	var _ out.CloudflareZoneResolver = (*CloudflareZoneResolver)(nil)

	// Test NewCloudflareZoneResolver returns non-nil
	r := NewCloudflareZoneResolver("token")
	require.NotNil(t, r)

	// Check defaults
	assert.Equal(t, "https://api.cloudflare.com/client/v4", r.baseURL)
	assert.NotNil(t, r.client)
	assert.Equal(t, defaultCloudflareTimeout, r.client.Timeout)
}

func TestWithCloudflareHTTPClientNilLeavesDefault(t *testing.T) {
	// Passing nil must not replace the default HTTP client.
	r := NewCloudflareZoneResolver("token", WithCloudflareHTTPClient(nil))
	require.NotNil(t, r.client)
	assert.Equal(t, defaultCloudflareTimeout, r.client.Timeout,
		"default timeout should be preserved when nil is passed")
}

func TestCloudflareZoneResolverFindZoneLowercasesInput(t *testing.T) {
	// The server expects lowercase names; uppercase input must be normalized.
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		name := r.URL.Query().Get("name")
		assert.Equal(t, "example.com", name, "should receive lowercased domain, got %q", name)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(zoneAPIResponse{
			Success: true,
			Result: []zoneAPIResult{
				{ID: "zone-1", Name: "example.com", Status: "active"},
			},
		})
	}))
	defer server.Close()

	resolver := NewCloudflareZoneResolver("test-token",
		WithCloudflareBaseURL(server.URL))

	// Uppercase input — should lowercase to EXAMPLE.COM -> example.com
	zone, err := resolver.FindZone(context.Background(), "EXAMPLE.COM")
	require.NoError(t, err)
	assert.Equal(t, "zone-1", zone.ID)
	assert.Equal(t, 1, callCount)
}

func TestCloudflareZoneResolverFindZoneMixedCaseWithTrailingDot(t *testing.T) {
	// Mixed case with trailing dot should be lowercased and dot-stripped.
	// The search chain tries "mixed.example.com" (empty), then "example.com" (found).
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		name := r.URL.Query().Get("name")
		switch name {
		case "mixed.example.com":
			// No zone found for subdomain
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(zoneAPIResponse{
				Success: true,
				Result:  []zoneAPIResult{},
			})
		case "example.com":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(zoneAPIResponse{
				Success: true,
				Result: []zoneAPIResult{
					{ID: "zone-1", Name: "example.com", Status: "active"},
				},
			})
		default:
			t.Errorf("unexpected zone name query: %s", name)
		}
	}))
	defer server.Close()

	resolver := NewCloudflareZoneResolver("test-token",
		WithCloudflareBaseURL(server.URL))

	zone, err := resolver.FindZone(context.Background(), "Mixed.EXAMPLE.Com.")
	require.NoError(t, err)
	assert.Equal(t, "zone-1", zone.ID)
	// First call: mixed.example.com (empty), second: example.com (found)
	assert.Equal(t, 2, callCount, "should try mixed.example.com then find example.com")
}
