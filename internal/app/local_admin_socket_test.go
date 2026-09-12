package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	adminhttp "github.com/bnema/gordon/internal/adapters/in/http/admin"
	"github.com/bnema/gordon/internal/adapters/localadmin"
	in "github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
)

// unixHTTPClient returns a client that dials the given Unix socket and never
// consults environment proxies.
func unixHTTPClient(socketPath string) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func localAdminTestServices(t *testing.T, appSvc in.AppService) *services {
	t.Helper()
	return &services{adminHandler: adminhttp.NewHandler(adminhttp.HandlerDeps{
		AppSvc: appSvc,
		Log:    zerowrap.Default(),
	})}
}

func TestLocalAdminSocketServesAppSurfaceOverUnix(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	appSvc := inmocks.NewMockAppService(t)
	appSvc.EXPECT().List(mock.Anything).Return([]in.AppSummary(nil), nil).Once()

	server, err := startLocalAdminServer(localAdminTestServices(t, appSvc), make(chan error, 4), zerowrap.Default())
	require.NoError(t, err)
	require.NotNil(t, server)

	path := localadmin.SocketPath(filepath.Join(xdg, "gordon"))
	require.NoError(t, localadmin.ValidateSocket(path))

	client := unixHTTPClient(path)

	resp, err := client.Get("http://gordon.local/admin/apps")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	resp, err = client.Get("http://gordon.local/admin/auth/tokens")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	server.Close()
	assert.NoFileExists(t, path)
}

func TestLocalAdminSocketTCPAdminStaysDisabled(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	cfg := Config{}
	cfg.Auth.Enabled = false

	svc := localAdminTestServices(t, nil)
	registryHandler, _, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	// The TCP registry mux must not expose /admin/* in local-only mode.
	for _, path := range []string{"/admin/status", "/admin/apps", "/admin/apps/apply"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.10:12345"
		rec := httptest.NewRecorder()
		registryHandler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, "path %s", path)
	}

	// The owner-only socket does serve the app surface.
	server, err := startLocalAdminServer(svc, make(chan error, 4), zerowrap.Default())
	require.NoError(t, err)
	t.Cleanup(server.Close)

	path := localadmin.SocketPath(filepath.Join(xdg, "gordon"))
	client := unixHTTPClient(path)
	resp, err := client.Get("http://gordon.local/admin/apps")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestLocalAdminSocketCloseLeavesReplacementFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	server, err := startLocalAdminServer(localAdminTestServices(t, inmocks.NewMockAppService(t)), make(chan error, 4), zerowrap.Default())
	require.NoError(t, err)

	path := localadmin.SocketPath(filepath.Join(xdg, "gordon"))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("replacement"), 0o600))

	server.Close()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "replacement", string(content))

	// Close is idempotent and must not remove the replacement on a second call.
	server.Close()
	assert.FileExists(t, path)
}

func TestStartLocalAdminServerSkipsWithoutAdminHandler(t *testing.T) {
	server, err := startLocalAdminServer(&services{}, make(chan error, 4), zerowrap.Default())
	require.NoError(t, err)
	assert.Nil(t, server)
	server.Close()
}
