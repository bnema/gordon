package deployment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func bindPolicyFor(source, root string, readOnly bool, apps, services []string) domain.AppBindPolicy {
	return domain.AppBindPolicy{
		Name:            "config",
		Source:          source,
		Root:            root,
		ReadOnly:        readOnly,
		AllowedApps:     apps,
		AllowedServices: services,
	}
}

func TestResolveServiceBinds_ExactTranslation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds", "config")
	require.NoError(t, os.MkdirAll(source, 0o755))
	realSource, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)

	svc := NewService(Deps{}, zerowrap.Default()).
		WithBindPolicies(map[string]domain.AppBindPolicy{
			"config": bindPolicyFor(source, root, false, []string{"blog"}, []string{"web"}),
		})

	got, err := svc.resolveServiceBinds("blog", domain.AppService{
		Name:  "web",
		Binds: []domain.AppBind{{Name: "config", Path: "/etc/app.conf", ReadOnly: true}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, domain.ContainerBind{
		Name:        "config",
		Source:      realSource,
		Destination: "/etc/app.conf",
		ReadOnly:    true,
	}, got[0])
}

func TestResolveServiceBinds_ReadOnlyNonWeakening(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))

	svc := NewService(Deps{}, zerowrap.Default()).
		WithBindPolicies(map[string]domain.AppBindPolicy{
			"config": bindPolicyFor(source, root, true, []string{"blog"}, []string{"web"}),
		})

	got, err := svc.resolveServiceBinds("blog", domain.AppService{
		Name:  "web",
		Binds: []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].ReadOnly, "a read-only policy must not be weakened by the manifest")
}

func TestResolveServiceBinds_UnknownAndRevokedPolicy(t *testing.T) {
	spec := domain.AppService{
		Name:  "web",
		Binds: []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}},
	}

	t.Run("unknown policy", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default())
		_, err := svc.resolveServiceBinds("blog", spec)
		require.ErrorIs(t, err, domain.ErrBindPolicy)
		assert.Contains(t, err.Error(), "blog")
		assert.Contains(t, err.Error(), "web")
		assert.Contains(t, err.Error(), "config")
	})

	t.Run("revoked policy", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "binds")
		require.NoError(t, os.MkdirAll(source, 0o755))
		svc := NewService(Deps{}, zerowrap.Default()).
			WithBindPolicies(map[string]domain.AppBindPolicy{
				"config": bindPolicyFor(source, root, false, []string{"blog"}, []string{"web"}),
			})
		_, err := svc.resolveServiceBinds("blog", spec)
		require.NoError(t, err)

		svc.SetBindPolicies(nil)
		_, err = svc.resolveServiceBinds("blog", spec)
		require.ErrorIs(t, err, domain.ErrBindPolicy)
	})

	t.Run("refused policy omits source", func(t *testing.T) {
		const secretSource = "/srv/gordon/secret-source"
		svc := NewService(Deps{}, zerowrap.Default()).
			WithBindPolicies(map[string]domain.AppBindPolicy{
				"config": bindPolicyFor(secretSource, "/srv/gordon", false, []string{"other"}, []string{"web"}),
			})
		_, err := svc.resolveServiceBinds("blog", spec)
		require.ErrorIs(t, err, domain.ErrBindPolicy)
		assert.NotContains(t, err.Error(), secretSource, "errors must never leak the host source")
	})
}

// TestCreateAndStart_RefusesRevokedBindBeforeRuntimeMutation proves the
// immediate-pre-create re-resolution fails before any runtime call when the
// policy is missing or revoked.
func TestCreateAndStart_RefusesRevokedBindBeforeRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	p := pinnedService{
		name: "web",
		spec: domain.AppService{
			Name:  "web",
			Binds: []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}},
		},
	}

	_, _, _, err := svc.createAndStart(context.Background(), "blog", "rev-1", p, "op-1", nil)

	require.ErrorIs(t, err, domain.ErrBindPolicy)
	assert.Contains(t, err.Error(), "config")
	runtime.AssertNotCalled(t, "CreateContainer")
	runtime.AssertNotCalled(t, "CreateVolume")
	runtime.AssertNotCalled(t, "StartContainer")
	runtime.AssertNotCalled(t, "ConnectContainerToNetwork")
	runtime.AssertExpectations(t)
}

// TestCreateContainer_BindBearingRuntimeErrorIsRedactedNotBindPolicy proves a
// CreateContainer failure on a bind-bearing service is reported as a generic
// redacted runtime error, never as a bind policy violation, and never leaks
// the resolved host source embedded in the runtime error.
func TestCreateContainer_BindBearingRuntimeErrorIsRedactedNotBindPolicy(t *testing.T) {
	const secretSource = "/srv/gordon/secret-source"
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("mount %s: permission denied", secretSource)).Once()
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())

	_, err := svc.createContainer(context.Background(), "web", &domain.ContainerConfig{
		Binds: []domain.ContainerBind{{Name: "config", Source: secretSource, Destination: "/etc/app.conf"}},
	})

	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrBindPolicy, "a runtime failure is not a policy refusal")
	assert.NotContains(t, err.Error(), secretSource, "errors must never leak the resolved host source")
	assert.NotContains(t, err.Error(), "permission denied", "no runtime text may reach the caller")
	runtime.AssertExpectations(t)
}

func TestCheckImageVolumes_IncludesBindDestinations(t *testing.T) {
	t.Run("bind destination maps image volume", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		runtime.EXPECT().InspectImageVolumes(context.Background(), "img:1").Return([]string{"/data"}, nil).Once()
		svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())

		err := svc.checkImageVolumes(context.Background(), domain.AppService{
			Name:  "web",
			Binds: []domain.AppBind{{Name: "config", Path: "/data"}},
		}, "img:1")

		require.NoError(t, err)
	})

	t.Run("unmapped image volume still refused", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		runtime.EXPECT().InspectImageVolumes(context.Background(), "img:1").Return([]string{"/data"}, nil).Once()
		svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())

		err := svc.checkImageVolumes(context.Background(), domain.AppService{Name: "web"}, "img:1")

		require.ErrorIs(t, err, domain.ErrAppUnmanagedImageVolume)
	})
}

// TestSingleWriterRequiredAndImplicitHTTPReadiness proves the two
// independent service rules: singleWriterRequired marks the stateful
// services whose superseded generation must stay recovery-inhibited, and
// implicitHTTPProbe decides which undeclared readiness checks keep the
// implicit HTTP probe.
func TestSingleWriterRequiredAndImplicitHTTPReadiness(t *testing.T) {
	httpSpec := func() domain.AppService {
		return domain.AppService{
			Name: "web",
			HTTP: []domain.AppHTTPInterface{{Host: "app.example.com", Port: 8080, TLS: domain.AppTLSAuto}},
		}
	}

	t.Run("stateless public HTTP service keeps the implicit HTTP probe", func(t *testing.T) {
		assert.True(t, implicitHTTPProbe(httpSpec()))
		assert.Equal(t, domain.AppReadinessHTTP, deploymentReadiness(httpSpec()).Readiness.Type)
		assert.False(t, singleWriterRequired(httpSpec()))
	})

	t.Run("undeclared probe is not guessed for internal, mixed, or L4 services", func(t *testing.T) {
		internal := domain.AppService{
			Name: "api",
			HTTP: []domain.AppHTTPInterface{{Port: 8080, Visibility: domain.AppVisibilityInternal}},
		}
		assert.False(t, implicitHTTPProbe(internal),
			"an internal-only interface is not reachable through a loopback publish, so no HTTP probe is guessed")
		assert.Empty(t, deploymentReadiness(internal).Readiness.Type)
		assert.False(t, singleWriterRequired(internal))

		mixed := httpSpec()
		mixed.HTTP = append(mixed.HTTP, domain.AppHTTPInterface{Port: 9090, Visibility: domain.AppVisibilityInternal})
		assert.False(t, implicitHTTPProbe(mixed))
		assert.Empty(t, deploymentReadiness(mixed).Readiness.Type)
		assert.False(t, singleWriterRequired(mixed))

		l4Only := domain.AppService{
			Name: "db",
			TCP:  []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 5432, Publish: "5432"}},
		}
		assert.False(t, implicitHTTPProbe(l4Only), "an HTTP GET proves nothing about an L4 workload")
		assert.Empty(t, deploymentReadiness(l4Only).Readiness.Type)
	})

	t.Run("declared probes are never replaced by the implicit HTTP probe", func(t *testing.T) {
		for _, declared := range []string{domain.AppReadinessHTTP, domain.AppReadinessTCP, domain.AppReadinessLog} {
			spec := httpSpec()
			spec.Readiness = domain.AppReadiness{Type: declared}
			assert.False(t, implicitHTTPProbe(spec), "%s is an explicit declaration", declared)
			assert.Equal(t, declared, deploymentReadiness(spec).Readiness.Type)
		}
	})

	t.Run("an explicit none on a probeable service keeps the implicit HTTP probe", func(t *testing.T) {
		spec := httpSpec()
		spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessNone}
		assert.Equal(t, domain.AppReadinessHTTP, deploymentReadiness(spec).Readiness.Type)
	})

	t.Run("an explicit none on an L4 service stays unchecked", func(t *testing.T) {
		spec := domain.AppService{
			Name:      "db",
			TCP:       []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 5432, Publish: "5432"}},
			Readiness: domain.AppReadiness{Type: domain.AppReadinessNone},
		}
		assert.Equal(t, domain.AppReadinessNone, deploymentReadiness(spec).Readiness.Type)
	})

	t.Run("writable bind is single-writer", func(t *testing.T) {
		spec := httpSpec()
		spec.Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}}
		assert.False(t, implicitHTTPProbe(spec), "a stateful service must declare its own readiness")
		assert.True(t, singleWriterRequired(spec))
	})

	t.Run("read-only bind is also single-writer", func(t *testing.T) {
		spec := httpSpec()
		spec.Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf", ReadOnly: true}}
		assert.False(t, implicitHTTPProbe(spec))
		assert.True(t, singleWriterRequired(spec))
	})

	t.Run("volume is single-writer", func(t *testing.T) {
		spec := httpSpec()
		spec.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}}
		assert.False(t, implicitHTTPProbe(spec))
		assert.True(t, singleWriterRequired(spec))
	})
}

// TestRestartOneService_RevokedBindFailsWithoutRuntimeMutation proves an
// in-place restart fails closed when the bind policy is missing/revoked,
// before touching the runtime.
func TestRestartOneService_RevokedBindFailsWithoutRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	eff := domain.AppEffectiveService{
		Container: "c1",
		Spec: domain.AppService{
			Name:  "web",
			Binds: []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}},
		},
	}

	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.restart", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := svc.restartOneService(context.Background(), "blog", "op-1", "web", eff, op, 0)

	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "config")
	assert.Equal(t, "failed", result.Result)
	runtime.AssertNotCalled(t, "RestartContainer")
	runtime.AssertExpectations(t)
}

// TestEnsureServiceRunning_RevokedBindFailsWithoutRuntimeMutation proves the
// boot/start recovery path fails closed before starting a container when the
// bind policy is missing/revoked.
func TestEnsureServiceRunning_RevokedBindFailsWithoutRuntimeMutation(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())
	eff := domain.AppEffectiveService{
		Container: "c1",
		Spec: domain.AppService{
			Name:  "web",
			Binds: []domain.AppBind{{Name: "config", Path: "/etc/app.conf"}},
		},
	}
	op := &domain.AppOperation{Op: "op-1", Steps: []domain.AppOperationStep{{
		ID: "service.web.start", State: domain.AppStepPending, Before: eff.Container,
	}}}
	result := &LifecycleResult{Services: map[string]ServiceResult{}}

	svc.ensureServiceRunning(context.Background(), "blog", "op-1", "web", eff, op, 0, result)

	assert.Equal(t, domain.AppStepFailed, op.Steps[0].State)
	assert.Contains(t, op.Steps[0].Error, "config")
	runtime.AssertNotCalled(t, "StartContainer")
	runtime.AssertExpectations(t)
}
