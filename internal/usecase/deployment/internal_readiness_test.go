package deployment

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// internalSpec is an HTTP service whose only interface is internal.
func internalSpec() domain.AppService {
	return domain.AppService{
		Name:      "api",
		Image:     "registry.example.com/blog/api:1.0.0",
		HTTP:      []domain.AppHTTPInterface{{Port: 8080, Visibility: domain.AppVisibilityInternal}},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: 2 * time.Second},
	}
}

// TestBackendPorts_InternalHTTPIsNeverPublished proves internal HTTP ports
// gain no loopback publication, including through readiness metadata, while
// public interfaces keep theirs.
func TestBackendPorts_InternalHTTPIsNeverPublished(t *testing.T) {
	internal := internalSpec()
	assert.Empty(t, backendPorts(internal), "internal http must not be published")

	public := domain.AppService{
		Name: "web",
		HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
	}
	assert.Equal(t, []int{8080}, containerPorts(backendPorts(public)))

	mixed := domain.AppService{
		HTTP: []domain.AppHTTPInterface{
			{Host: "blog.example.com", Port: 8080, TLS: "auto"},
			{Port: 9090, Visibility: domain.AppVisibilityInternal},
		},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Port: 9090},
	}
	assert.Equal(t, []int{8080}, containerPorts(backendPorts(mixed)),
		"the internal readiness port must not add a second publication")

	tcp := domain.AppService{
		TCP:       []domain.AppTCPInterface{{Port: 5432}},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessTCP},
	}
	assert.Equal(t, []int{5432}, containerPorts(backendPorts(tcp)))
}

func containerPorts(ports []domain.ContainerBackendPort) []int {
	result := make([]int, 0, len(ports))
	for _, port := range ports {
		result = append(result, port.ContainerPort)
	}
	return result
}

// TestInternalProbeShape proves readiness type selects the probe transport.
func TestInternalProbeShape(t *testing.T) {
	httpProtocol, path := internalProbeShape(internalSpec())
	assert.Equal(t, domain.ProbeProtocolHTTP, httpProtocol)
	assert.Equal(t, "/healthz", path)

	tcpSpec := internalSpec()
	tcpSpec.Readiness = domain.AppReadiness{Type: domain.AppReadinessTCP}
	tcpProtocol, tcpPath := internalProbeShape(tcpSpec)
	assert.Equal(t, domain.ProbeProtocolTCP, tcpProtocol)
	assert.Empty(t, tcpPath)
}

// TestWaitInternalReady_RetriesUntilReady proves an unhealthy attempt is
// retried and the request carries the exact identity, network, and shape.
func TestWaitInternalReady_RetriesUntilReady(t *testing.T) {
	var requests []domain.ContainerNetworkProbeRequest
	attempts := 0
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			return time.Unix(1700000000, 0).UTC(), nil
		},
		networkProbe: func(_ context.Context, request domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			requests = append(requests, request)
			attempts++
			if attempts < 3 {
				return domain.ContainerNetworkProbeResult{Diagnostic: "no http response"}, nil
			}
			return domain.ContainerNetworkProbeResult{Ready: true, Status: 200}, nil
		},
	}
	require.NoError(t, waitInternalReadyWithDeps(context.Background(), deps, "c-api", internalSpec(), "gordon--blog--net", 8080))
	require.Len(t, requests, 3)
	assert.Equal(t, "c-api", requests[0].TargetContainerID)
	assert.Equal(t, "gordon--blog--net", requests[0].Network)
	assert.Equal(t, domain.ProbeProtocolHTTP, requests[0].Protocol)
	assert.Equal(t, "/healthz", requests[0].Path)
	assert.Equal(t, 8080, requests[0].Port)
	assert.Equal(t, time.Unix(1700000000, 0).UTC(), requests[0].ExpectedStartedAt)
}

// TestWaitInternalReady_InfrastructureErrorFailsImmediately proves a helper
// that cannot run is never retried as an unhealthy attempt.
func TestWaitInternalReady_InfrastructureErrorFailsImmediately(t *testing.T) {
	attempts := 0
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			return time.Unix(1700000000, 0).UTC(), nil
		},
		networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			attempts++
			return domain.ContainerNetworkProbeResult{}, errors.New("helper create failed")
		},
	}
	err := waitInternalReadyWithDeps(context.Background(), deps, "c-api", internalSpec(), "net", 8080)
	require.ErrorContains(t, err, "infrastructure error")
	assert.Equal(t, 1, attempts, "an infrastructure failure must not be retried")
}

func TestRunInternalProbeAttempt_CleanupFailureIsNeverSuppressed(t *testing.T) {
	request := domain.ContainerNetworkProbeRequest{Timeout: 10 * time.Millisecond}
	deps := ProbeDeps{networkProbe: func(ctx context.Context, _ domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
		<-ctx.Done()
		return domain.ContainerNetworkProbeResult{}, fmt.Errorf("%w: remove failed", domain.ErrNetworkProbeCleanup)
	}}

	_, err := runInternalProbeAttempt(context.Background(), deps, request)
	require.ErrorIs(t, err, domain.ErrNetworkProbeCleanup)
}

// TestWaitInternalReady_CombinedCleanupAndStaleErrorAbortsAfterOneAttempt
// proves a helper cleanup failure wins over the stale-generation retry: a
// probe error wrapping both a stale sentinel and ErrNetworkProbeCleanup
// aborts the wait after the first attempt instead of polling until the
// readiness deadline.
func TestWaitInternalReady_CombinedCleanupAndStaleErrorAbortsAfterOneAttempt(t *testing.T) {
	attempts := 0
	startReads := 0
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			startReads++
			return time.Unix(1700000000, 0).UTC(), nil
		},
		networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			attempts++
			return domain.ContainerNetworkProbeResult{}, fmt.Errorf("stale candidate: %w (helper not removed: %w)", domain.ErrAppStateConflict, domain.ErrNetworkProbeCleanup)
		},
	}
	err := waitInternalReadyWithDeps(context.Background(), deps, "c-api", internalSpec(), "net", 8080)
	require.ErrorIs(t, err, domain.ErrNetworkProbeCleanup)
	assert.NotContains(t, err.Error(), "timeout")
	assert.Equal(t, 1, attempts, "a cleanup failure must abort after one attempt")
	assert.Equal(t, 1, startReads, "a cleanup failure must not re-read the execution boundary")
}

// TestWaitInternalReady_Timeout proves a never-ready target fails at the
// readiness deadline.
func TestWaitInternalReady_Timeout(t *testing.T) {
	spec := internalSpec()
	spec.Readiness.Timeout = 50 * time.Millisecond
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			return time.Unix(1700000000, 0).UTC(), nil
		},
		networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			return domain.ContainerNetworkProbeResult{Diagnostic: "http status 503"}, nil
		},
	}
	err := waitInternalReadyWithDeps(context.Background(), deps, "c-api", spec, "net", 8080)
	require.ErrorContains(t, err, "timeout")
	assert.ErrorContains(t, err, "http status 503")
}

// TestWaitInternalReady_Cancellation proves a canceled deployment context
// stops polling immediately.
func TestWaitInternalReady_Cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			return time.Unix(1700000000, 0).UTC(), nil
		},
		networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			cancel()
			return domain.ContainerNetworkProbeResult{}, nil
		},
	}
	err := waitInternalReadyWithDeps(ctx, deps, "c-api", internalSpec(), "net", 8080)
	require.ErrorIs(t, err, context.Canceled)
}

// TestWaitInternalReady_MissingExecutionStart proves a runtime that reports
// no execution boundary fails closed rather than probing an unidentified
// generation.
func TestWaitInternalReady_MissingExecutionStart(t *testing.T) {
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			return time.Time{}, nil
		},
		networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			t.Fatal("must not probe without an execution start")
			return domain.ContainerNetworkProbeResult{}, nil
		},
	}
	err := waitInternalReadyWithDeps(context.Background(), deps, "c-api", internalSpec(), "net", 8080)
	require.ErrorContains(t, err, "execution start")
}

// TestWaitInternalReady_StaleGenerationIsRetried proves a candidate that
// restarts during the readiness window is re-probed against its new
// execution boundary instead of aborting the wait as an infrastructure
// error.
func TestWaitInternalReady_StaleGenerationIsRetried(t *testing.T) {
	first := time.Unix(1700000000, 0).UTC()
	second := time.Unix(1700000100, 0).UTC()
	starts := []time.Time{first, second, second}
	startReads := 0
	attempts := 0
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			start := starts[min(startReads, len(starts)-1)]
			startReads++
			return start, nil
		},
		networkProbe: func(_ context.Context, request domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			attempts++
			if attempts == 1 {
				return domain.ContainerNetworkProbeResult{}, fmt.Errorf("execution changed: %w", domain.ErrAppStateConflict)
			}
			assert.Equal(t, second, request.ExpectedStartedAt, "the retry must use the new execution boundary")
			return domain.ContainerNetworkProbeResult{Ready: true, Status: 200}, nil
		},
	}
	require.NoError(t, waitInternalReadyWithDeps(context.Background(), deps, "c-api", internalSpec(), "net", 8080))
	assert.Equal(t, 2, attempts)
}

// TestWaitInternalReady_StaleGenerationUntilDeadlineTimesOut proves a
// target that keeps restarting never gets reported ready: the wait ends at
// the readiness deadline, not with a false infrastructure error.
func TestWaitInternalReady_StaleGenerationUntilDeadlineTimesOut(t *testing.T) {
	spec := internalSpec()
	spec.Readiness.Timeout = 50 * time.Millisecond
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			return time.Unix(1700000000, 0).UTC(), nil
		},
		networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			return domain.ContainerNetworkProbeResult{}, fmt.Errorf("gone: %w", domain.ErrContainerNotFound)
		},
	}
	err := waitInternalReadyWithDeps(context.Background(), deps, "c-api", spec, "net", 8080)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "infrastructure error")
}

// TestWaitInternalReady_VanishedCandidateKeepsPolling proves a candidate
// that is gone on the boundary re-read is still "not ready yet": the wait
// keeps its previous execution boundary and polls to the readiness
// deadline instead of aborting as an infrastructure error.
func TestWaitInternalReady_VanishedCandidateKeepsPolling(t *testing.T) {
	spec := internalSpec()
	spec.Readiness.Timeout = 600 * time.Millisecond
	startReads := 0
	attempts := 0
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) {
			startReads++
			if startReads == 1 {
				return time.Unix(1700000000, 0).UTC(), nil
			}
			return time.Time{}, fmt.Errorf("gone: %w", domain.ErrContainerNotFound)
		},
		networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			attempts++
			return domain.ContainerNetworkProbeResult{}, fmt.Errorf("gone: %w", domain.ErrContainerNotFound)
		},
	}
	err := waitInternalReadyWithDeps(context.Background(), deps, "c-api", spec, "net", 8080)
	require.ErrorContains(t, err, "timeout")
	assert.NotContains(t, err.Error(), "infrastructure error")
	assert.GreaterOrEqual(t, attempts, 2, "a vanished candidate must be retried, not abort the wait")
	assert.GreaterOrEqual(t, startReads, 2, "a vanished candidate must be re-read, not abort the wait")
}

// TestReadinessContainerPort_PrefersSinglePublicHTTP proves a public+
// internal service with no explicit readiness port probes the public HTTP
// backend instead of failing on an unresolved port.
func TestReadinessContainerPort_PrefersSinglePublicHTTP(t *testing.T) {
	mixed := domain.AppService{
		HTTP: []domain.AppHTTPInterface{
			{Host: "blog.example.com", Port: 3000, TLS: "auto"},
			{Port: 8080, Visibility: domain.AppVisibilityInternal},
		},
	}
	assert.Equal(t, 3000, readinessContainerPort(mixed))

	single := domain.AppService{HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 3000, TLS: "auto"}}}
	assert.Equal(t, 3000, readinessContainerPort(single))

	tcpOnly := domain.AppService{TCP: []domain.AppTCPInterface{{Port: 5432}}}
	assert.Equal(t, 5432, readinessContainerPort(tcpOnly))
}

// TestWaitServiceReady_InternalUsesNetworkPath proves the dispatcher routes
// an internal readiness port to the bounded network probe and never to the
// loopback path.
func TestWaitServiceReady_InternalUsesNetworkPath(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "inc-1"}, nil)

	networkCalls := 0
	loopbackCalls := 0
	service := NewService(Deps{State: state, Networks: NetworkConfig{Prefix: "gordon"}}, zerowrap.Default()).
		WithProbeDeps(ProbeDeps{
			containerStart: func(context.Context, string) (time.Time, error) {
				return time.Unix(1700000000, 0).UTC(), nil
			},
			networkProbe: func(_ context.Context, request domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
				networkCalls++
				assert.Equal(t, domain.AppPrivateNetworkName("gordon", "inc-1"), request.Network)
				return domain.ContainerNetworkProbeResult{Ready: true, Status: 200}, nil
			},
			httpGet: func(context.Context, string) (int, error) {
				loopbackCalls++
				return 200, nil
			},
		})

	require.NoError(t, service.waitServiceReady(ctx, "blog", "c-api", internalSpec(), nil))
	assert.Equal(t, 1, networkCalls)
	assert.Zero(t, loopbackCalls, "an internal port must never take the loopback path")
}

// TestWaitServiceReady_PublicKeepsLoopback proves a public readiness port
// still probes the loopback bind and never the network path.
func TestWaitServiceReady_PublicKeepsLoopback(t *testing.T) {
	ctx := context.Background()
	networkCalls := 0
	loopbackCalls := 0
	service := NewService(Deps{}, zerowrap.Default()).
		WithProbeDeps(ProbeDeps{
			httpGet: func(context.Context, string) (int, error) {
				loopbackCalls++
				return 200, nil
			},
			networkProbe: func(context.Context, domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
				networkCalls++
				return domain.ContainerNetworkProbeResult{}, nil
			},
		})

	spec := readySpec()
	require.NoError(t, service.waitServiceReady(ctx, "blog", "c-web", spec, map[int]int{8080: 18080}))
	assert.Equal(t, 1, loopbackCalls)
	assert.Zero(t, networkCalls, "a public port must never take the network path")
}
