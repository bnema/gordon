package deployment

import (
	"context"
	"errors"
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
	// networkProbe runs one bounded session against an internal port that
	// has no host publication. Nil disables the internal path: it must
	// never fall back to a loopback bind that does not exist.
	networkProbe func(ctx context.Context, request domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error)
}

// ProbeRuntime is the runtime subset readiness probing needs. It mirrors
// out.ContainerRuntime signatures so both the live adapter and the
// mockery-generated MockContainerRuntime satisfy it.
type ProbeRuntime interface {
	GetContainerNetworkInfo(ctx context.Context, containerID string) (string, int, error)
	GetContainerLogsSince(ctx context.Context, containerID string, since time.Time, follow bool) (io.ReadCloser, error)
	InspectContainer(ctx context.Context, containerID string) (*domain.Container, error)
	ProbeContainerNetwork(ctx context.Context, request domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error)
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
		networkProbe: func(ctx context.Context, request domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
			return runtime.ProbeContainerNetwork(ctx, request)
		},
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

// waitServiceReady dispatches readiness for one service. It resolves the
// app private network lazily: only an internal interface needs it. A
// service whose readiness port belongs to an internal HTTP interface
// always probes over the private network; it never falls back to a
// loopback bind, and a public service never takes the network path.
func (s *Service) waitServiceReady(ctx context.Context, app, containerID string, spec domain.AppService, binds map[int]int) error {
	switch spec.Readiness.Type {
	case domain.AppReadinessHTTP, domain.AppReadinessTCP:
		port := readinessContainerPort(spec)
		if port > 0 && spec.InternallyOnlyPort(port) {
			network, err := s.privateNetworkForApp(ctx, app)
			if err != nil {
				return err
			}
			return waitInternalReadyWithDeps(ctx, s.probeDeps(), containerID, spec, network, port)
		}
	}
	return waitServiceReadyWithDeps(ctx, s.probeDeps(), containerID, spec, binds)
}

// privateNetworkForApp resolves the incarnation-owned private network, the
// only network an internal readiness helper may join. It fails closed when
// the app has no incarnation id.
func (s *Service) privateNetworkForApp(ctx context.Context, app string) (string, error) {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return "", fmt.Errorf("deployment: load ownership for private network: %w", err)
	}
	if ownership.ID == "" {
		return "", fmt.Errorf("deployment: app %q has no incarnation id for internal readiness: %w", app, domain.ErrAppStateConflict)
	}
	return domain.AppPrivateNetworkName(s.deps.Networks.Prefix, ownership.ID), nil
}

// waitInternalReadyWithDeps polls one bounded private-network session per
// attempt until the target is ready or the readiness timeout expires. An
// infrastructure error fails immediately and is never retried as an
// unhealthy attempt: a helper that cannot run says nothing about the
// target.
func waitInternalReadyWithDeps(ctx context.Context, deps ProbeDeps, containerID string, spec domain.AppService, network string, port int) error {
	timeout := readinessTimeout(spec)
	protocol, path := internalProbeShape(spec)
	if deps.networkProbe == nil || deps.containerStart == nil {
		return fmt.Errorf("deployment: internal readiness probe is not wired")
	}
	startedAt, err := deps.containerStart(ctx, containerID)
	if err != nil {
		return fmt.Errorf("deployment: internal readiness execution start for %s: %w", containerID, err)
	}
	if startedAt.IsZero() {
		// Without an execution boundary the probe could report readiness
		// for a generation that already restarted. Fail closed.
		return fmt.Errorf("deployment: internal readiness needs the execution start of %s, runtime reported none", containerID)
	}
	deadline := time.Now().Add(timeout)
	lastAttempt := "no attempt completed"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("deployment: internal readiness timeout after %s on container port %d (last attempt: %s)", timeout, port, lastAttempt)
		}
		attemptTimeout := min(5*time.Second, remaining)
		result, probeErr := runInternalProbeAttempt(ctx, deps, domain.ContainerNetworkProbeRequest{
			TargetContainerID: containerID,
			ExpectedStartedAt: startedAt,
			Network:           network,
			Protocol:          protocol,
			Port:              port,
			Path:              path,
			Timeout:           attemptTimeout,
		})
		ready, retryStale, diagnostic, classifyErr := classifyInternalProbe(result, probeErr)
		if classifyErr != nil {
			return classifyErr
		}
		if ready {
			return nil
		}
		if diagnostic != "" {
			lastAttempt = diagnostic
		}
		if retryStale {
			// The candidate restarted or briefly vanished: re-read its
			// execution boundary and keep polling until the deadline. This
			// is "not ready yet", not a broken helper, so it must never
			// abort the wait as an infrastructure error.
			next, startErr := rereadInternalProbeStart(ctx, deps, containerID, startedAt)
			if startErr != nil {
				return startErr
			}
			startedAt = next
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func runInternalProbeAttempt(ctx context.Context, deps ProbeDeps, request domain.ContainerNetworkProbeRequest) (domain.ContainerNetworkProbeResult, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	result, err := deps.networkProbe(attemptCtx, request)
	attemptErr := attemptCtx.Err()
	cancel()
	if err != nil && !errors.Is(err, domain.ErrNetworkProbeCleanup) && errors.Is(attemptErr, context.DeadlineExceeded) && ctx.Err() == nil {
		return domain.ContainerNetworkProbeResult{Diagnostic: "probe attempt timed out"}, nil
	}
	return result, err
}

// rereadInternalProbeStart refreshes the execution boundary after a stale
// or vanished candidate, keeping the previous value when the runtime
// reports none so the next attempt still fails closed.
func rereadInternalProbeStart(ctx context.Context, deps ProbeDeps, containerID string, current time.Time) (time.Time, error) {
	next, err := deps.containerStart(ctx, containerID)
	if err != nil {
		return current, fmt.Errorf("deployment: internal readiness execution start for %s: %w", containerID, err)
	}
	if next.IsZero() {
		return current, nil
	}
	return next, nil
}

// classifyInternalProbe maps one attempt's raw outcome to the wait loop's
// next step: ready, not-ready (diagnostic), a stale or vanished candidate
// that should be re-read and retried, or an infrastructure failure that
// must abort the wait immediately.
func classifyInternalProbe(result domain.ContainerNetworkProbeResult, probeErr error) (ready, retryStale bool, diagnostic string, err error) {
	switch {
	case probeErr == nil:
		if result.Ready {
			return true, false, "", nil
		}
		if result.Diagnostic != "" {
			return false, false, result.Diagnostic, nil
		}
		return false, false, "not ready", nil
	case errors.Is(probeErr, domain.ErrAppStateConflict), errors.Is(probeErr, domain.ErrContainerNotFound):
		return false, true, "target execution changed; retrying", nil
	default:
		return false, false, "", fmt.Errorf("deployment: internal readiness probe infrastructure error: %w", probeErr)
	}
}

// internalProbeShape selects the probe transport for an internal port:
// http readiness maps to a bounded HTTP probe, tcp readiness to a TCP
// connection probe.
func internalProbeShape(spec domain.AppService) (domain.ProbeProtocol, string) {
	if spec.Readiness.Type == domain.AppReadinessHTTP {
		path := spec.Readiness.Path
		if path == "" {
			path = "/"
		}
		return domain.ProbeProtocolHTTP, path
	}
	return domain.ProbeProtocolTCP, ""
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
	containerPort := readinessContainerPort(spec)
	hostPort, ok := binds[containerPort]
	if !ok || hostPort <= 0 {
		return 0, fmt.Errorf("deployment: no 127.0.0.1 backend bind for container port %d", containerPort)
	}
	return hostPort, nil
}

// readinessContainerPort resolves the container port a readiness probe
// targets: the manifest's readiness port when set, else the single
// effective-public HTTP port (the backend the proxy dials), else the
// single TCP-capable interface port. Validation guarantees unambiguous
// selection for the latter two; preferring the public HTTP port keeps a
// public+internal service probeable when it declares no explicit
// readiness port.
func readinessContainerPort(spec domain.AppService) int {
	if spec.Readiness.Port > 0 {
		return spec.Readiness.Port
	}
	if port := singlePublicHTTPPort(spec); port > 0 {
		return port
	}
	return singleTCPPort(spec)
}

// singlePublicHTTPPort returns the container port when the service
// declares exactly one effective-public HTTP interface and no TCP
// interface, else 0.
func singlePublicHTTPPort(spec domain.AppService) int {
	if len(spec.TCP) > 0 {
		return 0
	}
	port := 0
	for _, h := range spec.HTTP {
		if !h.IsPublic() {
			continue
		}
		if port != 0 && port != h.Port {
			return 0
		}
		port = h.Port
	}
	return port
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
