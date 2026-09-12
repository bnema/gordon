package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/localadmin"
)

// appPlaneFixtureHandler serves the same app DTO payloads a daemon would,
// independent of transport, so local and remote seams can be compared.
func appPlaneFixtureHandler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/apps", func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(t, w, map[string]any{
			"apps": []dto.AppSummaryDTO{{App: "blog", Desired: "rev-2", Active: "rev-1", Converged: false}},
		})
	})
	mux.HandleFunc("/admin/apps/blog", func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(t, w, dto.AppShowResponse{
			App:     "blog",
			Desired: dto.AppDesiredDTO{Revision: "rev-2", Status: "pending"},
			Active: dto.AppActiveDTO{
				Converged: true,
				Services: map[string]dto.AppActiveServiceDTO{
					"web": {EffectiveRevision: "rev-1", Container: "ctr-web"},
				},
			},
		})
	})
	mux.HandleFunc("/admin/apps/blog/diff", func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(t, w, dto.AppDiffResponse{App: "blog"})
	})
	mux.HandleFunc("/admin/apps/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeFixtureJSON(t, w, dto.AppApplyResponse{App: "blog", ResultingRevision: "rev-3"})
	})
	return mux
}

func writeFixtureJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(payload))
}

// startUnixAppPlane serves the fixture over an owner-only Unix socket.
func startUnixAppPlane(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ln, _, err := localadmin.Listen(dir)
	require.NoError(t, err)

	srv := &http.Server{Handler: appPlaneFixtureHandler(t), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return localadmin.SocketPath(dir)
}

// withRemoteTarget pins the CLI remote flags and clears the environment.
func withRemoteTarget(t *testing.T, target string) {
	t.Helper()
	origRemote, origToken, origInsecure := remoteFlag, tokenFlag, insecureTLSFlag
	t.Cleanup(func() { remoteFlag, tokenFlag, insecureTLSFlag = origRemote, origToken, origInsecure })
	remoteFlag, tokenFlag, insecureTLSFlag = target, "", false

	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func stubLocalAppClient(t *testing.T, fn func() (*remote.Client, error)) {
	t.Helper()
	restore := newLocalAppClient
	newLocalAppClient = fn
	t.Cleanup(func() { newLocalAppClient = restore })
}

func TestResolveAppPlane_UsesLocalAdminSocket(t *testing.T) {
	socketPath := startUnixAppPlane(t)
	withRemoteTarget(t, "")
	stubLocalAppClient(t, func() (*remote.Client, error) {
		return remote.NewLocalClientForSocket(socketPath), nil
	})

	handle, err := resolveAppPlane()
	require.NoError(t, err)
	defer handle.close()
	plane := handle.plane

	apps, err := plane.ListApps(context.Background())
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].App)

	show, err := plane.ShowApp(context.Background(), "blog")
	require.NoError(t, err)
	assert.Equal(t, "rev-2", show.Desired.Revision)

	diff, err := plane.DiffApp(context.Background(), "blog")
	require.NoError(t, err)
	assert.Equal(t, "blog", diff.App)

	apply, err := plane.ApplyApp(context.Background(), dto.AppApplyRequest{ManifestTOML: "name = \"blog\"\n"})
	require.NoError(t, err)
	assert.Equal(t, "rev-3", apply.ResultingRevision)
}

// TestResolveAppControlPlane_LocalAndRemoteDTOParity asserts the shared DTO
// seam yields identical values over both transports.
func TestControlPlane_LocalAndRemoteDTOParity(t *testing.T) {
	fixture := appPlaneFixtureHandler(t)

	tcp := httptest.NewServer(fixture)
	t.Cleanup(tcp.Close)

	dir := t.TempDir()
	ln, _, err := localadmin.Listen(dir)
	require.NoError(t, err)
	unixSrv := &http.Server{Handler: fixture, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = unixSrv.Serve(ln) }()
	t.Cleanup(func() { _ = unixSrv.Close() })

	localPlane := NewRemoteControlPlane(remote.NewLocalClientForSocket(localadmin.SocketPath(dir)))
	remotePlane := NewRemoteControlPlane(remote.NewClient(tcp.URL))

	ctx := context.Background()

	localApps, err := localPlane.ListApps(ctx)
	require.NoError(t, err)
	remoteApps, err := remotePlane.ListApps(ctx)
	require.NoError(t, err)
	assert.Equal(t, remoteApps, localApps)

	localShow, err := localPlane.ShowApp(ctx, "blog")
	require.NoError(t, err)
	remoteShow, err := remotePlane.ShowApp(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, remoteShow, localShow)

	localDiff, err := localPlane.DiffApp(ctx, "blog")
	require.NoError(t, err)
	remoteDiff, err := remotePlane.DiffApp(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, remoteDiff, localDiff)
}

// TestResolveAppControlPlane_ExplicitRemoteNeverProbesLocal pins that an
// explicit remote is authoritative: the local socket is never consulted, and
// a remote failure never falls back to it.
func TestResolveAppPlane_ExplicitRemoteNeverProbesLocal(t *testing.T) {
	t.Run("remote succeeds", func(t *testing.T) {
		tcp := httptest.NewServer(appPlaneFixtureHandler(t))
		t.Cleanup(tcp.Close)
		withRemoteTarget(t, tcp.URL)

		probed := false
		stubLocalAppClient(t, func() (*remote.Client, error) {
			probed = true
			return nil, remote.ErrDaemonUnavailable
		})

		handle, err := resolveAppPlane()
		require.NoError(t, err)
		defer handle.close()
		plane := handle.plane

		apps, err := plane.ListApps(context.Background())
		require.NoError(t, err)
		require.Len(t, apps, 1)
		assert.False(t, probed, "local socket must not be probed when a remote is selected")
	})

	t.Run("remote fails without local fallback", func(t *testing.T) {
		tcp := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := tcp.URL
		tcp.Close()

		withRemoteTarget(t, url)

		probed := false
		stubLocalAppClient(t, func() (*remote.Client, error) {
			probed = true
			return remote.NewLocalClientForSocket("/nonexistent/admin.sock"), nil
		})

		handle, err := resolveAppPlane()
		require.NoError(t, err, "client construction succeeds; the failure must surface on the request")
		defer handle.close()
		plane := handle.plane

		_, err = plane.ListApps(context.Background())
		require.Error(t, err)
		assert.False(t, probed, "a failed remote must never fall back to the local socket")
	})
}

func TestResolveAppPlane_ReportsDaemonUnavailable(t *testing.T) {
	withRemoteTarget(t, "")
	stubLocalAppClient(t, func() (*remote.Client, error) {
		return nil, remote.ErrDaemonUnavailable
	})

	_, err := resolveAppPlane()
	require.Error(t, err)
	assert.ErrorContains(t, err, "daemon-unavailable")
	assert.ErrorIs(t, err, remote.ErrDaemonUnavailable)
}
