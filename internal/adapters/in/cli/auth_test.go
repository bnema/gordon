package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

func TestRunAuthLoginWithToken_VerifiesAndStores(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", filepath.Join("/nonexistent", "pass-disabled"))

	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"routes":0,"registry_domain":"","registry_port":0,"server_port":0,"auto_route":false,"network_isolation":false,"container_status":{}}`))
	}))
	defer server.Close()

	config := &remote.ClientConfig{
		Active: "prod",
		Remotes: map[string]remote.RemoteEntry{
			"prod": {URL: server.URL},
		},
	}
	assert.NoError(t, remote.SaveRemotes(remote.DefaultRemotesPath(), config))

	var out bytes.Buffer
	err := runAuthLoginWithToken(context.Background(), "", "token123", &out)
	assert.NoError(t, err)

	loaded, err := remote.LoadRemotes("")
	assert.NoError(t, err)
	assert.Equal(t, "token123", loaded.Remotes["prod"].Token)
	assert.Equal(t, "Bearer token123", receivedAuth)
}

func TestRunAuthLoginWithToken_StoresTokenOnVerificationFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", filepath.Join("/nonexistent", "pass-disabled"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	config := &remote.ClientConfig{
		Active: "prod",
		Remotes: map[string]remote.RemoteEntry{
			"prod": {URL: server.URL},
		},
	}
	assert.NoError(t, remote.SaveRemotes(remote.DefaultRemotesPath(), config))

	var out bytes.Buffer
	err := runAuthLoginWithToken(context.Background(), "", "token123", &out)
	assert.NoError(t, err)

	loaded, err := remote.LoadRemotes("")
	assert.NoError(t, err)
	assert.Equal(t, "token123", loaded.Remotes["prod"].Token)
}
