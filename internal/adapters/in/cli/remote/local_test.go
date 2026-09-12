package remote

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

	"github.com/bnema/gordon/internal/adapters/localadmin"
)

func mustListenUnix(t *testing.T, dir string, handler http.Handler) string {
	t.Helper()
	ln, _, err := localadmin.Listen(dir)
	require.NoError(t, err)

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return localadmin.SocketPath(dir)
}

func TestLocalAdminDiscoveryRejectsUnsafeSelectedCandidate(t *testing.T) {
	unsafeDir := t.TempDir()
	goodDir := t.TempDir()

	unsafeLn, _, err := localadmin.Listen(unsafeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unsafeLn.Close() })
	require.NoError(t, os.Chmod(localadmin.SocketPath(unsafeDir), 0o666))

	goodLn, _, err := localadmin.Listen(goodDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = goodLn.Close() })

	missingDir := t.TempDir()

	_, err = DiscoverLocalSocketIn([]string{missingDir, unsafeDir, goodDir})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDaemonUnavailable)
	assert.ErrorIs(t, err, localadmin.ErrUnsafePath)
}

func TestLocalAdminDiscoveryReportsUnavailable(t *testing.T) {
	dir := t.TempDir()
	_, err := DiscoverLocalSocketIn([]string{dir})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDaemonUnavailable)
}

func TestLocalAdminDiscoveryUsesXDGCandidates(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	ln, _, err := localadmin.Listen(filepath.Join(xdg, "gordon"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	got, err := DiscoverLocalSocket()
	require.NoError(t, err)
	assert.Equal(t, localadmin.SocketPath(filepath.Join(xdg, "gordon")), got)
}

func TestLocalAdminClientFailsWithoutSocket(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir()}
	restore := localSocketDirs
	localSocketDirs = func() []string { return dirs }
	t.Cleanup(func() { localSocketDirs = restore })

	_, err := NewLocalClient()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDaemonUnavailable)
}

// TestLocalAdminClientSendsNoTokenAndNoExchange proves tokenless local
// requests: no Authorization header and no /auth/token call.
func TestLocalAdminClientSendsNoTokenAndNoExchange(t *testing.T) {
	dir := t.TempDir()
	type captured struct {
		path   string
		auth   string
		method string
	}
	requests := make(chan captured, 4)

	socketPath := mustListenUnix(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captured{path: r.URL.Path, auth: r.Header.Get("Authorization"), method: r.Method}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apps": []any{}})
	}))

	client := NewLocalClientForSocket(socketPath)
	apps, err := client.ListApps(context.Background())
	require.NoError(t, err)
	assert.Empty(t, apps)

	select {
	case got := <-requests:
		assert.Equal(t, "/admin/apps", got.path)
		assert.Empty(t, got.auth)
	case <-time.After(2 * time.Second):
		t.Fatal("no request captured")
	}

	select {
	case extra := <-requests:
		t.Fatalf("unexpected extra request: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestLocalAdminClientIgnoresEnvironmentProxy(t *testing.T) {
	dir := t.TempDir()
	requests := make(chan string, 1)

	socketPath := mustListenUnix(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apps": []any{}})
	}))

	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	client := NewLocalClientForSocket(socketPath)
	_, err := client.ListApps(context.Background())
	require.NoError(t, err)

	select {
	case got := <-requests:
		assert.Equal(t, "/admin/apps", got)
	case <-time.After(2 * time.Second):
		t.Fatal("no request captured")
	}
}

func TestLocalAdminClientRejectsRedirects(t *testing.T) {
	dir := t.TempDir()
	socketPath := mustListenUnix(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/apps" {
			http.Redirect(w, r, "http://evil.example/admin/apps", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))

	client := NewLocalClientForSocket(socketPath)
	_, err := client.ListApps(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirect")
}

func TestLocalAdminClientRespectsCancellation(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	socketPath := mustListenUnix(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		client := NewLocalClientForSocket(socketPath)
		_, err := client.ListApps(ctx)
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded), "got %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("request did not honor cancellation")
	}
	close(release)
}

func TestLocalAdminClientFailsOnMissingSocket(t *testing.T) {
	client := NewLocalClientForSocket(filepath.Join(t.TempDir(), "absent.sock"))
	_, err := client.ListApps(context.Background())
	require.Error(t, err)

	var transportErr *requestTransportError
	assert.True(t, errors.As(err, &transportErr), "expected transport error, got %T: %v", err, err)
}

func TestLocalAdminTransportDialsUnixOnly(t *testing.T) {
	dir := t.TempDir()
	socketPath := mustListenUnix(t, dir, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	// The synthetic base URL host must never be resolved: only the socket is dialed.
	client := NewLocalClientForSocket(socketPath)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, LocalBaseURL+"/admin/apps", nil)
	require.NoError(t, err)
	resp, err := client.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}
