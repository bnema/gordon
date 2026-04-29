package acme

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// challengeSvc is a simple map-backed implementation of ChallengeService for testing.
type challengeSvc map[string]string

func (s challengeSvc) GetHTTP01Challenge(_ context.Context, token string) (string, bool) {
	keyAuth, ok := s[token]
	return keyAuth, ok
}

func TestHandlerServesGETChallenge(t *testing.T) {
	svc := challengeSvc{"abc": "key-auth"}
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("expected Content-Type text/plain; charset=utf-8, got %q", ct)
	}

	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("expected Cache-Control no-store, got %q", cc)
	}

	body := rec.Body.String()
	if body != "key-auth" {
		t.Errorf("expected body %q, got %q", "key-auth", body)
	}
}

func TestHandlerServesHEADChallenge(t *testing.T) {
	svc := challengeSvc{"abc": "key-auth"}
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodHead, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("expected Content-Type text/plain; charset=utf-8, got %q", ct)
	}

	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("expected Cache-Control no-store, got %q", cc)
	}

	body := rec.Body.String()
	if body != "" {
		t.Errorf("expected empty body for HEAD, got %q", body)
	}
}

func TestHandlerRejectsUnsafeToken(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "path traversal", path: "/.well-known/acme-challenge/../secret"},
		{name: "slash in token", path: "/.well-known/acme-challenge/foo/bar"},
		{name: "empty token", path: "/.well-known/acme-challenge/"},
		{name: "backslash in token", path: "/.well-known/acme-challenge/foo\\bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(challengeSvc{"abc": "key-auth"})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			resp := rec.Result()
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("expected status 404, got %d for path %q", resp.StatusCode, tt.path)
			}
		})
	}
}

func TestHandlerRejectsPOST(t *testing.T) {
	svc := challengeSvc{"abc": "key-auth"}
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", resp.StatusCode)
	}
}

func TestHandlerRejectsMissingPrefix(t *testing.T) {
	h := NewHandler(challengeSvc{"abc": "key-auth"})

	req := httptest.NewRequest(http.MethodGet, "/some-other-path", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for non-matching prefix, got %d", resp.StatusCode)
	}
}

func TestHandlerRejectsUnknownToken(t *testing.T) {
	h := NewHandler(challengeSvc{"abc": "key-auth"})

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/unknown", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for unknown token, got %d", resp.StatusCode)
	}
}

func TestHandlerRejectsNilService(t *testing.T) {
	h := NewHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for nil service, got %d", resp.StatusCode)
	}
}
