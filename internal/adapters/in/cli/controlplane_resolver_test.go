package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/localadmin"
)

func isolateControlPlaneResolver(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	originalRemoteFlag, originalTokenFlag, originalInsecureTLSFlag := remoteFlag, tokenFlag, insecureTLSFlag
	originalFactory := newLocalControlPlaneClient
	t.Cleanup(func() {
		remoteFlag = originalRemoteFlag
		tokenFlag = originalTokenFlag
		insecureTLSFlag = originalInsecureTLSFlag
		newLocalControlPlaneClient = originalFactory
	})
	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false
}

func localControlPlaneTestClient(t *testing.T, handler http.Handler) *remote.Client {
	t.Helper()
	dir := t.TempDir()
	listener, _, err := localadmin.Listen(dir)
	require.NoError(t, err)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return remote.NewLocalClientForSocket(localadmin.SocketPath(dir))
}

func TestResolveControlPlane_LocalUsesSocketBackedRemotePlane(t *testing.T) {
	isolateControlPlaneResolver(t)
	requests := make(chan string, 1)
	client := localControlPlaneTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remote.Status{RegistryDomain: "socket.local"})
	}))
	factoryCalls := 0
	newLocalControlPlaneClient = func() (*remote.Client, error) {
		factoryCalls++
		return client, nil
	}

	// A deliberately unusable config path proves local resolution does not open a kernel/store.
	handle, err := resolveControlPlane(filepath.Join(t.TempDir(), "missing", "gordon.toml"))
	require.NoError(t, err)
	require.NotNil(t, handle)
	assert.False(t, handle.isRemote)
	assert.IsType(t, &remoteControlPlane{}, handle.plane)
	assert.Equal(t, 1, factoryCalls)

	status, err := handle.plane.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "socket.local", status.RegistryDomain)
	select {
	case path := <-requests:
		assert.Equal(t, "/admin/status", path)
	case <-time.After(2 * time.Second):
		t.Fatal("socket server received no request")
	}
}

func TestResolveControlPlane_LocalFailsClosedWhenDaemonUnavailable(t *testing.T) {
	isolateControlPlaneResolver(t)
	newLocalControlPlaneClient = func() (*remote.Client, error) {
		return nil, remote.ErrDaemonUnavailable
	}

	handle, err := resolveControlPlane(filepath.Join(t.TempDir(), "would-create-store.toml"))
	require.Error(t, err)
	assert.ErrorIs(t, err, remote.ErrDaemonUnavailable)
	assert.Nil(t, handle)
}

func TestResolveControlPlane_ExplicitRemoteDoesNotUseLocalFactory(t *testing.T) {
	isolateControlPlaneResolver(t)
	remoteFlag = "https://example.invalid"
	tokenFlag = "token"
	newLocalControlPlaneClient = func() (*remote.Client, error) {
		t.Fatal("local factory invoked for explicit remote")
		return nil, errors.New("unreachable")
	}

	handle, err := resolveControlPlane("")
	require.NoError(t, err)
	assert.True(t, handle.isRemote)
	assert.IsType(t, &remoteControlPlane{}, handle.plane)
}

func TestResolveControlPlane_ExplicitUnknownRemoteReturnsError(t *testing.T) {
	isolateControlPlaneResolver(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	remoteFlag = "does-not-exist"

	handle, err := resolveControlPlane("")
	require.Error(t, err)
	assert.Nil(t, handle)
	assert.Contains(t, err.Error(), "does-not-exist")
}

func TestResolveControlPlane_LocalIgnoresConfigAndStoreState(t *testing.T) {
	isolateControlPlaneResolver(t)
	client := localControlPlaneTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remote.Status{})
	}))
	newLocalControlPlaneClient = func() (*remote.Client, error) { return client, nil }

	lockedStore := filepath.Join(t.TempDir(), "state.db")
	require.NoError(t, os.WriteFile(lockedStore, []byte("active daemon store"), 0o600))
	handle, err := resolveControlPlane(lockedStore)
	require.NoError(t, err)
	_, err = handle.plane.GetStatus(context.Background())
	require.NoError(t, err)
}
