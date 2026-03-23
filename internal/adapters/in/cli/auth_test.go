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

func TestRunAuthLoginWithToken_StoresToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", filepath.Join("/nonexistent", "pass-disabled"))

	// Mock server — verification may fail (token exchange not mocked), but token should still be stored.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
