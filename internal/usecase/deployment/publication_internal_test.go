package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// readySpec is an HTTP service whose readiness fails fast.
func readySpec() domain.AppService {
	return domain.AppService{
		Name:      "web",
		Image:     "registry.example.com/blog/web:1.4.2",
		HTTP:      []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: 50 * time.Millisecond},
	}
}

// TestVerifyRunningService_NotReadyWithdrawsAndClearsBinds proves a running
// generation that fails readiness is withdrawn, never published, and its
// recorded binds are cleared so the proxy cannot dial an unready port.
func TestVerifyRunningService_NotReadyWithdrawsAndClearsBinds(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	traffic := &stubTraffic{}
	eff := domain.AppEffectiveService{
		Container:         "c-1",
		EffectiveRevision: "rev-1",
		Spec:              readySpec(),
		BackendBinds:      map[int]int{8080: 32771},
	}
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-1", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()

	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: traffic}, zerowrap.Default()).
		WithProbeDeps(NewTestProbeDeps(runtime,
			func(context.Context, string) (int, error) { return 500, nil },
			func(context.Context, string) error { return nil },
		))

	step := domain.AppOperationStep{ID: "service.web.start"}
	result := &LifecycleResult{Services: map[string]ServiceResult{}}
	require.True(t, svc.verifyRunningService(ctx, "blog", "web", eff, &step, result))

	assert.Equal(t, domain.AppStepFailed, step.State)
	assert.Equal(t, "failed", result.Services["web"].Result)
	// One serialized withdrawal plus one state-only bind clear through
	// the canonical boundary; the fail-closed ACTIVE write itself is
	// covered by the publisher tests in internal/app.
	assert.Equal(t, []string{"blog/web", "blog/web"}, traffic.withdrawn)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
}

// TestVerifyRunningService_ReadyPublishesBinds proves a ready generation is
// published with its freshly inspected binds.
func TestVerifyRunningService_ReadyPublishesBinds(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	traffic := &stubTraffic{}
	eff := domain.AppEffectiveService{Container: "c-1", EffectiveRevision: "rev-1", Spec: readySpec()}
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-1", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{"web": eff},
	}, true, nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()

	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: traffic}, zerowrap.Default()).
		WithProbeDeps(NewTestProbeDeps(runtime,
			func(context.Context, string) (int, error) { return 200, nil },
			func(context.Context, string) error { return nil },
		))

	step := domain.AppOperationStep{ID: "service.web.start"}
	result := &LifecycleResult{Services: map[string]ServiceResult{}}
	require.True(t, svc.verifyRunningService(ctx, "blog", "web", eff, &step, result))

	assert.Equal(t, domain.AppStepSucceeded, step.State)
	assert.Equal(t, map[int]int{8080: 32771}, result.Services["web"].BackendBinds)
	assert.Equal(t, []string{"blog/web"}, traffic.withdrawn, "the service is withdrawn before re-verification")
}

// TestRedactDiagnostics_RemovesSecretValues proves a known secret value is
// never persisted in failure diagnostics.
func TestRedactDiagnostics_RemovesSecretValues(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	secrets := outmocks.NewMockSecretProvider(t)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-1/web/database-url").Return("supersecret", nil).Once()

	svc := NewService(Deps{State: state, Secrets: secrets}, zerowrap.Default())
	p := pinnedService{name: "web", spec: domain.AppService{
		Name: "web", Secrets: map[string]string{"DATABASE_URL": "database-url"},
	}}

	got := svc.redactDiagnostics(context.Background(), "blog", p, []string{"connect failed for supersecret", "still running"})

	assert.Equal(t, []string{"connect failed for [redacted]", "still running"}, got)
}

// TestRedactDiagnostics_DropsWhenSecretUnreadable proves diagnostics are
// dropped rather than persisted unredacted when a secret cannot be read.
func TestRedactDiagnostics_DropsWhenSecretUnreadable(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	secrets := outmocks.NewMockSecretProvider(t)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-1/web/database-url").Return("", assert.AnError).Once()

	svc := NewService(Deps{State: state, Secrets: secrets}, zerowrap.Default())
	p := pinnedService{name: "web", spec: domain.AppService{
		Name: "web", Secrets: map[string]string{"DATABASE_URL": "database-url"},
	}}

	assert.Nil(t, svc.redactDiagnostics(context.Background(), "blog", p, []string{"supersecret leaked"}))
}

// TestStopService_StopFailureSurfaces proves a runtime stop failure is
// reported rather than recorded as a successful stop.
func TestStopService_StopFailureSurfaces(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().StopContainer(mock.Anything, "c-1").Return(errors.New("cannot stop")).Once()

	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: &stubTraffic{}}, zerowrap.Default())
	step, err := svc.stopService(context.Background(), "blog", "web", "c-1")

	require.Error(t, err)
	assert.Equal(t, domain.AppStepFailed, step.State)
	assert.Contains(t, step.Error, "cannot stop")
}

// TestStopService_WithdrawFailureBlocksStop proves withdrawal runs first and
// its failure stops the operation before the container is touched.
func TestStopService_WithdrawFailureBlocksStop(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: &stubTraffic{fail: true}}, zerowrap.Default())
	step, err := svc.stopService(context.Background(), "blog", "web", "c-1")

	require.Error(t, err)
	assert.Equal(t, domain.AppStepFailed, step.State)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything)
}
