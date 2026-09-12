package deployment_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

type switchingTraffic struct {
	mu      sync.RWMutex
	backend string
	ready   <-chan struct{}
}

func (s *switchingTraffic) RebuildTraffic(context.Context) error {
	<-s.ready
	s.mu.Lock()
	s.backend = "new"
	s.mu.Unlock()
	return nil
}

func (s *switchingTraffic) WithdrawService(context.Context, string, string) error {
	s.mu.Lock()
	s.backend = "withdrawn"
	s.mu.Unlock()
	return nil
}

func (s *switchingTraffic) WithdrawServiceState(context.Context, string, string) error {
	s.mu.Lock()
	s.backend = "withdrawn"
	s.mu.Unlock()
	return nil
}

func (s *switchingTraffic) current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.backend
}

// TestDeploy_HTTPZeroDowntimeBehavior exercises the observable cutover contract:
// requests keep reaching the old backend while candidate readiness is delayed,
// traffic changes only after readiness, and retirement follows the switch.
func TestDeploy_HTTPZeroDowntimeBehavior(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()
	rev.Spec.Services[0].StopGrace = time.Millisecond
	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
	}}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18080, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
	}}, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-old").Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()

	ready := make(chan struct{})
	traffic := &switchingTraffic{backend: "old", ready: ready}
	retired := make(chan struct{})
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).RunAndReturn(func(context.Context, string, time.Duration) error {
		assert.Equal(t, "new", traffic.current(), "old backend retired before traffic switched")
		close(retired)
		return nil
	}).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, traffic.current())
	}))
	defer front.Close()

	probeStarted := make(chan struct{})
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) {
			select {
			case <-probeStarted:
			default:
				close(probeStarted)
			}
			<-ready
			return http.StatusOK, nil
		},
		func(context.Context, string) error { return nil },
	))

	done := make(chan error, 1)
	go func() {
		_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
		done <- err
	}()
	select {
	case <-probeStarted:
	case err := <-done:
		require.NoError(t, err)
		t.Fatal("deploy completed before readiness probe started")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for readiness probe")
	}
	for range 20 {
		resp, err := http.Get(front.URL) //nolint:noctx // bounded local test server
		require.NoError(t, err)
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, readErr)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "old", string(body))
	}
	close(ready)
	require.NoError(t, <-done)
	<-retired
	resp, err := http.Get(front.URL) //nolint:noctx // bounded local test server
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "new", string(body))
}
