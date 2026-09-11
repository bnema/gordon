package cli

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	adminhttp "github.com/bnema/gordon/internal/adapters/in/http/admin"
	"github.com/bnema/gordon/internal/adapters/localadmin"
	in "github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// validLocalAppManifest is a minimal manifest accepted by the admin apply
// surface (one service, one HTTP host).
const validLocalAppManifest = `
name = "blog"
[[service]]
name = "web"
image = "img:1"
[[service.http]]
host = "blog.example.com"
port = 8080
`

// TestSharedCommandLocalAdminComposition_Status executes a shared command
// through socket discovery, the Unix transport, and the local authority.
func TestSharedCommandLocalAdminComposition_Status(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	withRemoteTarget(t, "")

	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetRegistryDomain().Return("registry.example.test").Once()
	configSvc.EXPECT().GetRegistryPort().Return(5000).Once()
	configSvc.EXPECT().GetServerPort().Return(8080).Once()
	configSvc.EXPECT().IsNetworkIsolationEnabled().Return(true).Once()
	appSvc := inmocks.NewMockAppService(t)
	appSvc.EXPECT().List(mock.Anything).Return([]in.AppSummary{{App: "blog", Active: "rev-1", Converged: true}}, nil).Once()

	handler := adminhttp.NewHandler(adminhttp.HandlerDeps{ConfigSvc: configSvc, AppSvc: appSvc, Log: zerowrap.Default()})
	shutdown := startLocalAuthority(t, filepath.Join(xdg, "gordon"), handler.LocalAuthority())
	defer shutdown()
	stubLocalAppClient(t, remote.NewLocalClient)

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.Contains(t, out.String(), "registry.example.test")
	assert.Contains(t, out.String(), "blog")
}

// TestAppLocalAdminComposition_EndToEnd composes the real local authority,
// Unix listener, discovery, transport, and app control plane.
func TestAppLocalAdminComposition_EndToEnd(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	withRemoteTarget(t, "")

	appSvc := inmocks.NewMockAppService(t)
	appSvc.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, false).Return(
		&in.AppApplyResult{App: "blog", ResultingRevision: "rev-2", Pending: true, IntentID: "apply-1"},
		nil, nil,
	).Once()
	appSvc.EXPECT().List(mock.Anything).Return([]in.AppSummary{
		{App: "blog", Desired: "rev-2", Active: "rev-1"},
	}, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{
		App:             "blog",
		DesiredRevision: "rev-2",
		DesiredStatus:   "pending",
		Services:        map[string]in.AppServiceView{"web": {Container: "ctr-web"}},
	}, nil).Once()
	appSvc.EXPECT().Diff(mock.Anything, "blog").Return(domain.AppDiff{}, nil).Once()
	appSvc.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(&domain.AppOperation{
		Op:      "apply",
		App:     "blog",
		Outcome: "succeeded",
	}, nil).Once()

	handler := adminhttp.NewHandler(adminhttp.HandlerDeps{AppSvc: appSvc, Log: zerowrap.Default()})
	dir := filepath.Join(xdg, "gordon")
	shutdown := startLocalAuthority(t, dir, handler.LocalAuthority())

	// The CLI resolves the socket through real discovery and the Unix transport.
	stubLocalAppClient(t, remote.NewLocalClient)

	plane, err := resolveAppControlPlane()
	require.NoError(t, err)

	ctx := context.Background()

	apply, err := plane.ApplyApp(ctx, dto.AppApplyRequest{ManifestTOML: validLocalAppManifest})
	require.NoError(t, err)
	assert.Equal(t, "rev-2", apply.ResultingRevision)

	apps, err := plane.ListApps(ctx)
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].App)

	show, err := plane.ShowApp(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "ctr-web", show.Active.Services["web"].Container)

	diff, err := plane.DiffApp(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "blog", diff.App)

	// Idempotent-mutation recovery path: re-query the same key.
	op, err := plane.OperationByKey(ctx, "blog", "key-1")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", op.Outcome)

	// Shutdown removes only the daemon-owned socket; restart recreates it.
	shutdown()
	assert.NoFileExists(t, localadmin.SocketPath(dir))

	restartShutdown := startLocalAuthority(t, dir, handler.LocalAuthority())
	require.FileExists(t, localadmin.SocketPath(dir))
	restartShutdown()
	assert.NoFileExists(t, localadmin.SocketPath(dir))
}

// startLocalAuthority serves the given handler on an owner-only Unix socket
// and returns a shutdown function that stops the server and removes the
// socket, mirroring daemon lifecycle.
func startLocalAuthority(t *testing.T, dir string, handler http.Handler) func() {
	t.Helper()
	ln, info, err := localadmin.Listen(dir)
	require.NoError(t, err)

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		require.NoError(t, localadmin.RemoveOwnedSocket(localadmin.SocketPath(dir), info))
	}
}
