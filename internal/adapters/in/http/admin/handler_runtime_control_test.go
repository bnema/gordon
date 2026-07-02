package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRuntimeControlAPIParity(t *testing.T) {
	t.Run("deploy uses runtime control response shape", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		route := &domain.Route{Domain: "app.example.com", Image: "app:latest"}
		configSvc.EXPECT().GetRoute(mock.Anything, "app.example.com").Return(route, nil)
		runtimeControl := &fakeRuntimeControl{deploy: runtimeSuccess("cmd-deploy")}
		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.RuntimeControl = runtimeControl
		})
		server := newScopedTestServer(t, handler, "admin:config:write")

		resp, err := http.Post(server.URL+"/admin/deploy/app.example.com", "application/json", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "deployed", body["status"])
		assert.Equal(t, "app.example.com", body["domain"])
		assert.Equal(t, *route, runtimeControl.deployRoute)
	})

	t.Run("restart maps runtime policy denial", func(t *testing.T) {
		runtimeControl := &fakeRuntimeControl{restart: domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusDenied, Error: &domain.RuntimeCommandError{Code: "runtime_policy_denied:image_digest_required", Message: "image digest is required"}}}
		handler := newTestHandler(t, func(d *HandlerDeps) { d.RuntimeControl = runtimeControl })
		server := newScopedTestServer(t, handler, "admin:config:write")

		resp, err := http.Post(server.URL+"/admin/restart/app.example.com", "application/json", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, "app.example.com", runtimeControl.restartDomain)
	})

	t.Run("remove uses runtime control cleanup path", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		configSvc.EXPECT().RemoveRoute(mock.Anything, "app.example.com").Return(nil)
		runtimeControl := &fakeRuntimeControl{remove: runtimeSuccess("cmd-remove")}
		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.RuntimeControl = runtimeControl
		})
		server := newScopedTestServer(t, handler, "admin:routes:write")

		req, err := http.NewRequest(http.MethodDelete, server.URL+"/admin/routes/app.example.com", nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "app.example.com", runtimeControl.removeDomain)
		assert.True(t, runtimeControl.removeForce)
	})

	t.Run("deploy runtime unavailable maps service unavailable", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		configSvc.EXPECT().GetRoute(mock.Anything, "app.example.com").Return(&domain.Route{Domain: "app.example.com", Image: "app:latest"}, nil)
		runtimeControl := &fakeRuntimeControl{err: context.DeadlineExceeded}
		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.RuntimeControl = runtimeControl
		})
		server := newScopedTestServer(t, handler, "admin:config:write")

		resp, err := http.Post(server.URL+"/admin/deploy/app.example.com", "application/json", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})

	t.Run("status uses runtime actual state without dto drift", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		routes := []domain.Route{{Domain: "app.example.com", Image: "app:latest"}}
		configSvc.EXPECT().GetRoutes(mock.Anything).Return(routes)
		configSvc.EXPECT().GetRegistryDomain().Return("registry.example.com")
		configSvc.EXPECT().GetRegistryPort().Return(5000)
		configSvc.EXPECT().GetServerPort().Return(8080)
		configSvc.EXPECT().IsAutoRouteEnabled().Return(true)
		configSvc.EXPECT().IsNetworkIsolationEnabled().Return(false)
		runtimeControl := &fakeRuntimeControl{statuses: map[string]string{"app.example.com": "running"}}
		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.RuntimeControl = runtimeControl
		})
		server := newScopedTestServer(t, handler, "admin:status:read")

		resp, err := http.Get(server.URL + "/admin/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var body dto.StatusResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, dto.StatusResponse{Routes: 1, RegistryDomain: "registry.example.com", RegistryPort: 5000, ServerPort: 8080, AutoRoute: true, NetworkIsolation: false, ContainerStatuses: map[string]string{"app.example.com": "running"}}, body)
		assert.Equal(t, routes, runtimeControl.statusRoutes)
	})

	t.Run("status runtime unavailable maps service unavailable", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "app:latest"}})
		runtimeControl := &fakeRuntimeControl{statusErr: context.DeadlineExceeded}
		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.RuntimeControl = runtimeControl
		})
		server := newScopedTestServer(t, handler, "admin:status:read")

		resp, err := http.Get(server.URL + "/admin/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})
}

type fakeRuntimeControl struct {
	deploy  domain.RuntimeCommandResult
	restart domain.RuntimeCommandResult
	remove  domain.RuntimeCommandResult
	err     error

	deployRoute   domain.Route
	restartDomain string
	restartAttach bool
	removeDomain  string
	removeForce   bool
	statuses      map[string]string
	statusRoutes  []domain.Route
	statusErr     error
}

func (f *fakeRuntimeControl) DeployRoute(_ context.Context, route domain.Route) (domain.RuntimeCommandResult, error) {
	f.deployRoute = route
	return f.deploy, f.err
}

func (f *fakeRuntimeControl) RestartRoute(_ context.Context, domainName string, withAttachments bool) (domain.RuntimeCommandResult, error) {
	f.restartDomain = domainName
	f.restartAttach = withAttachments
	return f.restart, f.err
}

func (f *fakeRuntimeControl) RemoveRoute(_ context.Context, domainName string, force bool) (domain.RuntimeCommandResult, error) {
	f.removeDomain = domainName
	f.removeForce = force
	return f.remove, f.err
}

func (f *fakeRuntimeControl) RouteStatuses(_ context.Context, routes []domain.Route) (map[string]string, error) {
	f.statusRoutes = append([]domain.Route(nil), routes...)
	return f.statuses, f.statusErr
}

func runtimeSuccess(commandID string) domain.RuntimeCommandResult {
	return domain.RuntimeCommandResult{CommandID: domain.RuntimeCommandID(commandID), Status: domain.RuntimeCommandStatusSucceeded}
}
