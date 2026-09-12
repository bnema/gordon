package deployment

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// ProbeDeps abstracts the runtime hooks readiness needs. The deployment
// service wires these to the ContainerRuntime adapter; tests inject the
// mockery-generated ContainerRuntime mock through NewProbeDeps.
type ProbeDeps struct {
	networkInfo    func(ctx context.Context, containerID string) (string, int, error)
	containerStart func(ctx context.Context, containerID string) (time.Time, error)
	logStream      func(ctx context.Context, containerID string, since time.Time) (io.ReadCloser, error)
	httpGet        func(ctx context.Context, url string) (int, error)
	tcpDial        func(ctx context.Context, addr string) error
}

// ProbeRuntime is the runtime subset readiness probing needs. It mirrors
// out.ContainerRuntime signatures so both the live adapter and the
// mockery-generated MockContainerRuntime satisfy it.
type ProbeRuntime interface {
	GetContainerNetworkInfo(ctx context.Context, containerID string) (string, int, error)
	GetContainerLogsSince(ctx context.Context, containerID string, since time.Time, follow bool) (io.ReadCloser, error)
	InspectContainer(ctx context.Context, containerID string) (*domain.Container, error)
}

// NewProbeDeps wires readiness probes to a runtime implementation.
// Production passes the ContainerRuntime adapter; tests pass the mockery
// MockContainerRuntime which satisfies ProbeRuntime.
func NewProbeDeps(runtime ProbeRuntime) ProbeDeps {
	return ProbeDeps{
		networkInfo: func(ctx context.Context, containerID string) (string, int, error) {
			return runtime.GetContainerNetworkInfo(ctx, containerID)
		},
		containerStart: func(ctx context.Context, containerID string) (time.Time, error) {
			ctr, err := runtime.InspectContainer(ctx, containerID)
			if err != nil {
				return time.Time{}, err
			}
			return ctr.StartedAt, nil
		},
		logStream: func(ctx context.Context, containerID string, since time.Time) (io.ReadCloser, error) {
			return runtime.GetContainerLogsSince(ctx, containerID, since, false)
		},
		httpGet: httpGetOnce,
		tcpDial: tcpDialOnce,
	}
}

// NewTestProbeDeps builds ProbeDeps with injectable HTTP/TCP probers.
// Tests control L4 outcomes without dialing; logStream still reads the
// runtime mock.
func NewTestProbeDeps(runtime ProbeRuntime, httpGet func(ctx context.Context, url string) (int, error), tcpDial func(ctx context.Context, addr string) error) ProbeDeps {
	deps := NewProbeDeps(runtime)
	deps.httpGet = httpGet
	deps.tcpDial = tcpDial
	return deps
}

// defaultProbeDeps wires readiness to the live runtime adapter.
func (s *Service) defaultProbeDeps() ProbeDeps {
	return NewProbeDeps(s.deps.Runtime)
}

// probeDeps returns the injected test probes or the live adapter wiring.
func (s *Service) probeDeps() ProbeDeps {
	if s.probes != nil {
		return *s.probes
	}
	return s.defaultProbeDeps()
}

// waitServiceReadyWithDeps dispatches readiness by manifest type.
// binds carries the container's 127.0.0.1 backend publishes; L4 probes
// dial them rootless-first and never container IPs (plan D3: loopback-
// only generated backends). Restart/start reuse the recorded binds from
// the active record; deploy passes the freshly read binds.
func waitServiceReadyWithDeps(ctx context.Context, deps ProbeDeps, containerID string, spec domain.AppService, binds map[int]int) error {
	switch spec.Readiness.Type {
	case "", domain.AppReadinessNone:
		return nil
	case domain.AppReadinessLog:
		return waitLogReadyWithDeps(ctx, deps, containerID, spec)
	case domain.AppReadinessHTTP:
		return waitHTTPReadyWithDeps(ctx, deps, spec, binds)
	case domain.AppReadinessTCP:
		return waitTCPReadyWithDeps(ctx, deps, spec, binds)
	default:
		return fmt.Errorf("deployment: unknown readiness type %q", spec.Readiness.Type)
	}
}

// waitHTTPReadyWithDeps polls GET path until 2xx/3xx or timeout.
func waitHTTPReadyWithDeps(ctx context.Context, deps ProbeDeps, spec domain.AppService, binds map[int]int) error {
	timeout := readinessTimeout(spec)
	port, err := dialPort(spec, binds)
	if err != nil {
		return err
	}
	path := spec.Readiness.Path
	if path == "" {
		path = "/"
	}
	pathPart, rawQuery, _ := strings.Cut(path, "?")
	probeURL := (&url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort("127.0.0.1", itoa(port)),
		Path:     pathPart,
		RawQuery: rawQuery,
	}).String()
	deadline := time.Now().Add(timeout)
	var lastAttempt string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, min(2*time.Second, time.Until(deadline)))
		status, err := deps.httpGet(attemptCtx, probeURL)
		cancel()
		if err == nil && status >= 200 && status < 400 {
			return nil
		}
		if err != nil {
			lastAttempt = err.Error()
		} else {
			lastAttempt = fmt.Sprintf("HTTP status %d", status)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("deployment: HTTP readiness timeout after %s: no 2xx/3xx from %s (last attempt: %s)", timeout, probeURL, lastAttempt)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// waitTCPReadyWithDeps dials the container port until accepted.
func waitTCPReadyWithDeps(ctx context.Context, deps ProbeDeps, spec domain.AppService, binds map[int]int) error {
	timeout := readinessTimeout(spec)
	port, err := dialPort(spec, binds)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", itoa(port))
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := deps.tcpDial(ctx, addr); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("deployment: TCP readiness timeout after %s: %s not reachable", timeout, addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// waitLogReadyWithDeps tails the logs of one exact container ID until the
// marker appears, bounded by the manifest readiness timeout and by the
// caller's deadline context. Logs are read from the observed start of the
// current execution onward, so a marker emitted by a previous execution
// of the same container ID can never satisfy the probe.
func waitLogReadyWithDeps(ctx context.Context, deps ProbeDeps, containerID string, spec domain.AppService) error {
	timeout := readinessTimeout(spec)
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	marker := spec.Readiness.Contains
	if containerID == "" {
		return fmt.Errorf("deployment: log readiness requires a container ID")
	}
	since, err := deps.containerStart(deadlineCtx, containerID)
	if err != nil {
		return fmt.Errorf("deployment: log readiness execution start for %s: %w", containerID, err)
	}
	if since.IsZero() {
		// Without an execution boundary the probe would scan the
		// container's whole history and could match a marker from a
		// previous execution. Fail closed instead.
		return fmt.Errorf("deployment: log readiness needs the execution start of %s, runtime reported none", containerID)
	}
	for {
		if err := deadlineCtx.Err(); err != nil {
			return fmt.Errorf("deployment: log readiness timeout waiting for %q: %w", marker, err)
		}
		stream, err := deps.logStream(deadlineCtx, containerID, since)
		if err != nil {
			return fmt.Errorf("deployment: log readiness stream for %s: %w", containerID, err)
		}
		found, readErr := scanForMarker(stream, marker)
		_ = stream.Close()
		if readErr != nil {
			return readErr
		}
		if found {
			return nil
		}
		select {
		case <-deadlineCtx.Done():
			return fmt.Errorf("deployment: log readiness timeout waiting for %q: %w", marker, deadlineCtx.Err())
		case <-time.After(time.Second):
		}
	}
}

// dialPort resolves the 127.0.0.1 host port for the readiness probe:
// the manifest's readiness port when set, else the single TCP-capable
// interface port (validation guarantees unambiguous selection).
func dialPort(spec domain.AppService, binds map[int]int) (int, error) {
	containerPort := spec.Readiness.Port
	if containerPort == 0 {
		containerPort = singleTCPPort(spec)
	}
	hostPort, ok := binds[containerPort]
	if !ok || hostPort <= 0 {
		return 0, fmt.Errorf("deployment: no 127.0.0.1 backend bind for container port %d", containerPort)
	}
	return hostPort, nil
}

// singleTCPPort returns the container port when exactly one TCP-capable
// interface exists; validation rejects ambiguity (readiness.port
// required) before the engine runs.
func singleTCPPort(spec domain.AppService) int {
	port := 0
	count := 0
	for _, h := range spec.HTTP {
		port, count = h.Port, count+1
	}
	for _, t := range spec.TCP {
		port, count = t.Port, count+1
	}
	if count == 1 {
		return port
	}
	return 0
}

// readinessTimeout applies the manifest default.
func readinessTimeout(spec domain.AppService) time.Duration {
	if spec.Readiness.Timeout > 0 {
		return spec.Readiness.Timeout
	}
	return domain.AppDefaultReadinessTimeout
}
