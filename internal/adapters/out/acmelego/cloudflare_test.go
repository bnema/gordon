package acmelego

import (
	"context"
	"encoding/json"
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
}
