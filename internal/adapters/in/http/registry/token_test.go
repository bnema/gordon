package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
)

func tokenTestLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestParseRequestedScopes_MultipleScopeParams(t *testing.T) {
	log := tokenTestLogger()
	h := &TokenHandler{log: log}

	req := httptest.NewRequest(http.MethodGet, "/v2/token?scope=repository:repo1:pull&scope=repository:repo2:push", nil)

	scopes := h.parseRequestedScopes(req, log)

	assert.Len(t, scopes, 2)
	assert.Contains(t, scopes, "repository:repo1:pull")
	assert.Contains(t, scopes, "repository:repo2:push")
}

func TestParseRequestedScopes_InvalidScopeFiltered(t *testing.T) {
	log := tokenTestLogger()
	h := &TokenHandler{log: log}

	tests := []struct {
		name        string
		query       string
		wantScopes  []string
		wantDefault bool
	}{
		{
			name:        "invalid format is skipped",
			query:       "scope=invalid-no-colons&scope=repository:valid:pull",
			wantScopes:  []string{"repository:valid:pull"},
			wantDefault: false,
		},
		{
			name:        "missing actions is skipped",
			query:       "scope=repository:myrepo&scope=repository:valid:pull",
			wantScopes:  []string{"repository:valid:pull"},
			wantDefault: false,
		},
		{
			name:        "all invalid returns default",
			query:       "scope=invalid&scope=also-invalid",
			wantScopes:  []string{"repository:*:pull"},
			wantDefault: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v2/token?"+tt.query, nil)
			scopes := h.parseRequestedScopes(req, log)

			assert.Equal(t, tt.wantScopes, scopes)
		})
	}
}

func TestParseRequestedScopes_NonRepositoryScopeRejected(t *testing.T) {
	log := tokenTestLogger()
	h := &TokenHandler{log: log}

	tests := []struct {
		name       string
		query      string
		wantScopes []string
	}{
		{
			name:       "registry scope is rejected",
			query:      "scope=registry:catalog:*&scope=repository:myrepo:pull",
			wantScopes: []string{"repository:myrepo:pull"},
		},
		{
			name:       "only non-repository scopes returns default",
			query:      "scope=registry:catalog:*&scope=custom:something:action",
			wantScopes: []string{"repository:*:pull"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v2/token?"+tt.query, nil)
			scopes := h.parseRequestedScopes(req, log)

			assert.Equal(t, tt.wantScopes, scopes)
		})
	}
}

func TestParseRequestedScopes_NoScopeReturnsDefault(t *testing.T) {
	log := tokenTestLogger()
	h := &TokenHandler{log: log}

	req := httptest.NewRequest(http.MethodGet, "/v2/token", nil)
	scopes := h.parseRequestedScopes(req, log)

	assert.Equal(t, []string{"repository:*:pull"}, scopes)
}

func TestParseRequestedScopes_EmptyScopeParamReturnsDefault(t *testing.T) {
	log := tokenTestLogger()
	h := &TokenHandler{log: log}

	req := httptest.NewRequest(http.MethodGet, "/v2/token?scope=", nil)
	scopes := h.parseRequestedScopes(req, log)

	// Empty scope param is invalid and should return default
	assert.Equal(t, []string{"repository:*:pull"}, scopes)
}

func TestParseRequestedScopes_ValidScopesPreserved(t *testing.T) {
	log := tokenTestLogger()
	h := &TokenHandler{log: log}

	tests := []struct {
		name       string
		query      string
		wantScopes []string
	}{
		{
			name:       "single scope",
			query:      "scope=repository:myrepo:pull",
			wantScopes: []string{"repository:myrepo:pull"},
		},
		{
			name:       "multiple actions",
			query:      "scope=repository:myrepo:push,pull",
			wantScopes: []string{"repository:myrepo:push,pull"},
		},
		{
			name:       "wildcard repo",
			query:      "scope=repository:*:pull",
			wantScopes: []string{"repository:*:pull"},
		},
		{
			name:       "org wildcard",
			query:      "scope=repository:myorg/*:push,pull",
			wantScopes: []string{"repository:myorg/*:push,pull"},
		},
		{
			name:       "nested repo",
			query:      "scope=repository:myorg/team/app:pull",
			wantScopes: []string{"repository:myorg/team/app:pull"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v2/token?"+tt.query, nil)
			scopes := h.parseRequestedScopes(req, log)

			assert.Equal(t, tt.wantScopes, scopes)
		})
	}
}

func TestIsLocalhostRequest(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"127.0.0.1 is localhost", "127.0.0.1:12345", true},
		{"127.0.0.2 is localhost", "127.0.0.2:12345", true},
		{"::1 is localhost", "[::1]:12345", true},
		{"::1 without brackets", "::1:12345", true},
		{"192.168.1.1 is not localhost", "192.168.1.1:12345", false},
		{"10.0.0.1 is not localhost", "10.0.0.1:12345", false},
		{"public IP is not localhost", "8.8.8.8:12345", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v2/token", nil)
			req.RemoteAddr = tt.remoteAddr

			result := isLocalhostRequest(req)

			assert.Equal(t, tt.want, result)
		})
	}
}
