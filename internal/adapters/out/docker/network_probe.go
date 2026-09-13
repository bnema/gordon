package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/bnema/gordon/internal/domain"
)

// DefaultNetworkProbeImage is the digest-pinned, multi-arch helper image
// one bounded internal readiness session runs. It provides only a POSIX
// shell with busybox nc/printf. Pin by digest, never by tag: the helper
// must be byte-identical across hosts and engines. The image must be
// pre-provisioned and available offline on the target host, because a
// probe never pulls.
const DefaultNetworkProbeImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

// Bounded resources and windows for the probe helper. They exist only to
// bound a misbehaving helper, not to size a workload.
const (
	probeHelperMemory   = 32 << 20
	probeHelperNanoCPUs = 500_000_000
	probeHelperPIDs     = 32
	// probeHelperCleanup bounds a force-remove of one helper.
	probeHelperCleanup = 30 * time.Second
	probeMaxSeconds    = 30
	// probeHelperOrphanAge is how old a leftover probe helper must be
	// before a later probe reclaims it. Live helpers last seconds, so this
	// never touches an in-flight session, including a concurrent one.
	probeHelperOrphanAge = 10 * time.Minute
)

// networkProbeArgv is the helper argv: a shell script plus positional
// arguments, so the target address never appears in a shell string.
type networkProbeArgv struct {
	script string
	args   []string
}

// httpProbeScript sends one HTTP/1.0 request over a raw connection and
// accepts only a 2xx/3xx status line. It never follows redirects and never
// consults a proxy. The first line must be an HTTP status line: a
// non-HTTP greeting that merely starts with 2 or 3 is never readiness. It
// prints the observed status (000 when none) for diagnostics.
const httpProbeScript = `path="$1"; tmo="$2"; ip="$3"; port="$4"
line=$(printf 'GET %s HTTP/1.0\r\nHost: readiness\r\nConnection: close\r\n\r\n' "$path" | nc -w "$tmo" "$ip" "$port" | head -c 128 | head -n1)
code=$(printf '%s\n' "$line" | awk '$1 ~ /^HTTP\/[0-9]+\.[0-9]+$/ && $2 ~ /^[0-9][0-9][0-9]$/ { print $2 }')
case "$code" in
  2??|3??) printf '%s\n' "$code"; exit 0;;
  [0-9][0-9][0-9]) printf '%s\n' "$code"; exit 1;;
  *) printf '%s\n' 000; exit 1;;
esac`

// tcpProbeScript accepts only an accepted connection.
const tcpProbeScript = `nc -z -w "$1" "$2" "$3"`

// ProbeContainerNetwork runs one bounded readiness session against one
// declared internal container port. The helper joins only the exact
// network in the request, targets the exact inspected endpoint IP (never
// a service alias), and is always force-removed under an independent
// cleanup context. An infrastructure failure returns an error and must
// never be retried as an unhealthy attempt; an unhealthy target returns a
// result with Ready=false.
func (r *Runtime) ProbeContainerNetwork(ctx context.Context, request domain.ContainerNetworkProbeRequest) (result domain.ContainerNetworkProbeResult, retErr error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "ProbeContainerNetwork",
		"network":             request.Network,
	})
	log := zerowrap.FromCtx(ctx)

	if err := request.Validate(); err != nil {
		return domain.ContainerNetworkProbeResult{}, err
	}
	sessionCtx, sessionCancel := context.WithTimeout(ctx, request.Timeout)
	defer sessionCancel()
	targetIP, err := r.inspectProbeTarget(sessionCtx, request)
	if err != nil {
		return domain.ContainerNetworkProbeResult{}, err
	}

	helperID, cleanup, err := r.createProbeHelper(sessionCtx, request, targetIP)
	if err != nil {
		return domain.ContainerNetworkProbeResult{}, err
	}
	defer func() {
		result, retErr = probeCleanupResult(log, cleanup, result, retErr)
	}()

	if _, err := r.client.ContainerStart(sessionCtx, helperID, client.ContainerStartOptions{}); err != nil {
		return domain.ContainerNetworkProbeResult{}, log.WrapErr(err, "failed to start network probe helper")
	}

	timedOut, err := r.waitForProbeHelper(sessionCtx, helperID)
	if err != nil {
		return domain.ContainerNetworkProbeResult{}, err
	}
	// A bound expiry is a readiness failure, not an infrastructure
	// failure. Return it before any post-session step, and never classify
	// it through a context that just expired. Caller cancellation stays an
	// error so the deployment stops immediately.
	if timedOut {
		return probeWaitTimeoutResult(ctx)
	}

	exitCode, stdout, err := r.probeHelperOutcome(sessionCtx, helperID)
	if result, handled := classifyProbeStepError(sessionCtx, ctx, err); handled {
		return result, err
	}
	// Revalidate identity before trusting the outcome: a target that
	// restarted during the session must not report readiness for a
	// generation that no longer exists. The caller may retry this as a
	// candidate that is simply not ready yet.
	finalIP, err := r.inspectProbeTarget(sessionCtx, request)
	if result, handled := classifyProbeStepError(sessionCtx, ctx, err); handled {
		return result, err
	}
	if finalIP != targetIP {
		return domain.ContainerNetworkProbeResult{}, fmt.Errorf("deployment: probe target endpoint changed during readiness: %w", domain.ErrAppStateConflict)
	}
	if err := ctx.Err(); err != nil {
		return domain.ContainerNetworkProbeResult{}, err
	}

	result = domain.ContainerNetworkProbeResult{Ready: exitCode == 0}
	if request.Protocol == domain.ProbeProtocolHTTP {
		result.Status = parseProbeStatus(stdout)
		result.Ready = result.Ready && result.Status >= 200 && result.Status < 400
	}
	if !result.Ready {
		result.Diagnostic = probeDiagnostic(request, result.Status)
	}
	return result, nil
}

func probeWaitTimeoutResult(parentCtx context.Context) (domain.ContainerNetworkProbeResult, error) {
	if err := parentCtx.Err(); err != nil {
		return domain.ContainerNetworkProbeResult{}, err
	}
	return probeTimeoutFailure(), nil
}

func classifyProbeStepError(sessionCtx, parentCtx context.Context, err error) (domain.ContainerNetworkProbeResult, bool) {
	if err == nil {
		return domain.ContainerNetworkProbeResult{}, false
	}
	if probeBoundExpired(sessionCtx, parentCtx) {
		return probeTimeoutFailure(), true
	}
	return domain.ContainerNetworkProbeResult{}, true
}

// probeTimeoutFailure is the bounded readiness failure of a session whose
// own bound expired. It is not an infrastructure error: the target may
// simply be slow, so the caller may retry it as an unhealthy attempt.
func probeTimeoutFailure() domain.ContainerNetworkProbeResult {
	return domain.ContainerNetworkProbeResult{Diagnostic: "probe did not complete within its bound"}
}

// probeBoundExpired reports whether a post-wait probe step failed because
// the session's own bound expired. A caller cancellation is never a bound
// expiry: it must stay an error so the deployment stops immediately.
func probeBoundExpired(sessionCtx, parentCtx context.Context) bool {
	return parentCtx.Err() == nil && errors.Is(sessionCtx.Err(), context.DeadlineExceeded)
}

func probeCleanupResult(log zerowrap.Logger, cleanup func() error, result domain.ContainerNetworkProbeResult, retErr error) (domain.ContainerNetworkProbeResult, error) {
	cleanErr := cleanup()
	if cleanErr == nil {
		return result, retErr
	}
	log.Warn().Err(cleanErr).Msg("failed to remove network probe helper")
	if retErr != nil {
		return domain.ContainerNetworkProbeResult{}, fmt.Errorf("%w: %v; prior probe error: %w", domain.ErrNetworkProbeCleanup, cleanErr, retErr)
	}
	return domain.ContainerNetworkProbeResult{}, fmt.Errorf("%w: %v", domain.ErrNetworkProbeCleanup, cleanErr)
}

// inspectProbeTarget resolves the exact endpoint IP of the target on the
// requested network and enforces the expected execution start. It fails
// closed, wrapping domain.ErrContainerNotFound or domain.ErrAppStateConflict
// when the target is gone, not running, not attached to that network, or
// has restarted, so the caller can tell "not ready yet" from a broken
// helper.
func (r *Runtime) inspectProbeTarget(ctx context.Context, request domain.ContainerNetworkProbeRequest) (string, error) {
	inspectResult, err := r.client.ContainerInspect(ctx, request.TargetContainerID, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return "", fmt.Errorf("%w: %s", domain.ErrContainerNotFound, request.TargetContainerID)
		}
		return "", fmt.Errorf("deployment: inspect probe target: %w", err)
	}
	observed := inspectResult.Container
	if observed.State == nil || observed.State.Status != container.StateRunning {
		return "", fmt.Errorf("deployment: probe target %s is not running: %w", request.TargetContainerID, domain.ErrAppStateConflict)
	}
	startedAt, _ := time.Parse(time.RFC3339Nano, observed.State.StartedAt)
	if startedAt.Year() <= 1 {
		startedAt = time.Time{}
	}
	if !startedAt.Equal(request.ExpectedStartedAt) {
		return "", fmt.Errorf("deployment: probe target %s execution changed during readiness: %w", request.TargetContainerID, domain.ErrAppStateConflict)
	}
	if observed.NetworkSettings == nil {
		return "", fmt.Errorf("deployment: probe target %s has no network settings: %w", request.TargetContainerID, domain.ErrAppStateConflict)
	}
	endpoint, ok := observed.NetworkSettings.Networks[request.Network]
	if !ok || endpoint == nil || !endpoint.IPAddress.IsValid() {
		return "", fmt.Errorf("deployment: probe target %s is not attached to network %q: %w", request.TargetContainerID, request.Network, domain.ErrAppStateConflict)
	}
	return endpoint.IPAddress.String(), nil
}

// createProbeHelper creates the hardened helper container on only the
// requested network. It has no host ports, mounts, volumes, environment,
// or shared networks, runs non-root on a read-only rootfs with all
// capabilities dropped, and never gains new privileges.
func (r *Runtime) createProbeHelper(ctx context.Context, request domain.ContainerNetworkProbeRequest, targetIP string) (string, func() error, error) {
	log := zerowrap.FromCtx(ctx)
	// Reclaim helpers orphaned by a previous crash before adding another:
	// in-process cleanup cannot run after SIGKILL, so a later probe is the
	// only owner that can bound that accumulation.
	r.sweepOrphanedProbeHelpers(ctx)

	command := buildNetworkProbeCommand(request, targetIP)
	entrypoint := append([]string{"sh", "-c", command.script, "sh"}, command.args...)

	created, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        DefaultNetworkProbeImage,
			Entrypoint:   entrypoint,
			User:         "65534:65534",
			AttachStdout: true,
			AttachStderr: true,
			Labels: map[string]string{
				domain.LabelPurpose: domain.PurposeNetworkProbe,
			},
		},
		HostConfig: probeHelperHostConfig(request.Network),
		Name:       fmt.Sprintf("gordon-network-probe-%d", time.Now().UTC().UnixNano()),
	})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return "", nil, log.WrapErr(fmt.Errorf(
				"network probe helper image %s is not available on this host: pre-provision the pinned image, the probe never pulls: %w",
				DefaultNetworkProbeImage, err), "failed to create network probe helper")
		}
		return "", nil, log.WrapErr(err, "failed to create network probe helper")
	}

	var cleanupErr error
	var cleaned bool
	cleanup := func() error {
		if cleaned {
			return cleanupErr
		}
		cleaned = true
		// Independent bounded context: the session context may already be
		// expired, but the helper must still be force-removed.
		removeCtx, cancel := context.WithTimeout(context.Background(), probeHelperCleanup)
		defer cancel()
		cleanupErr = r.RemoveContainer(removeCtx, created.ID, true)
		return cleanupErr
	}
	return created.ID, cleanup, nil
}

// sweepOrphanedProbeHelpers best-effort removes probe helpers older than
// probeHelperOrphanAge. It is bounded to the probe purpose label, so it
// can never touch a workload, a volume archive helper, or an in-flight
// session. A sweep failure never fails the probe.
func (r *Runtime) sweepOrphanedProbeHelpers(ctx context.Context) {
	log := zerowrap.FromCtx(ctx)
	list, err := r.client.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", domain.LabelPurpose+"="+domain.PurposeNetworkProbe),
	})
	if err != nil {
		log.Debug().Err(err).Msg("network probe orphan sweep: list failed")
		return
	}
	cutoff := time.Now().Add(-probeHelperOrphanAge)
	for _, orphan := range list.Items {
		if time.Unix(orphan.Created, 0).After(cutoff) {
			continue // live session, leave it alone
		}
		if err := ctx.Err(); err != nil {
			return
		}
		removeCtx, cancel := context.WithTimeout(ctx, probeHelperCleanup)
		removeErr := r.RemoveContainer(removeCtx, orphan.ID, true)
		cancel()
		if removeErr != nil {
			log.Debug().Err(removeErr).Msg("network probe orphan sweep: remove failed")
			continue
		}
		log.Info().Str("container", orphan.ID).Msg("reclaimed orphaned network probe helper")
	}
}

// probeHelperHostConfig is the smallest host configuration able to reach
// the target's private network and run a shell probe. Every field that
// would widen the helper's reach is left at its zero value.
func probeHelperHostConfig(network string) *container.HostConfig {
	pids := int64(probeHelperPIDs)
	return &container.HostConfig{
		AutoRemove:     false,
		NetworkMode:    container.NetworkMode(network),
		ReadonlyRootfs: true,
		SecurityOpt:    []string{"no-new-privileges:true"},
		CapDrop:        []string{"ALL"},
		CapAdd:         []string{},
		Resources: container.Resources{
			Memory:    probeHelperMemory,
			NanoCPUs:  probeHelperNanoCPUs,
			PidsLimit: &pids,
		},
	}
}

// waitForProbeHelper blocks until the helper exits or the bound expires.
// A bound expiry is reported as a non-fatal timeout: the target may be
// hanging, which is a readiness failure, not an infrastructure failure.
func (r *Runtime) waitForProbeHelper(ctx context.Context, helperID string) (bool, error) {
	waitResult := r.client.ContainerWait(ctx, helperID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case err := <-waitResult.Error:
		if err != nil {
			if ctx.Err() != nil {
				return true, nil
			}
			return false, fmt.Errorf("deployment: wait for network probe helper: %w", err)
		}
		return false, nil
	case <-waitResult.Result:
		return false, nil
	case <-ctx.Done():
		return true, nil
	}
}

// probeHelperOutcome reads the helper's exit code and bounded stdout.
func (r *Runtime) probeHelperOutcome(ctx context.Context, helperID string) (int, string, error) {
	log := zerowrap.FromCtx(ctx)
	logs, err := r.client.ContainerLogs(ctx, helperID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return 0, "", log.WrapErr(err, "failed to read network probe helper logs")
	}
	defer func() { _ = logs.Close() }()

	var stdout cappedProbeBuffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, logs); err != nil {
		return 0, "", log.WrapErr(err, "failed to demux network probe helper logs")
	}

	inspectResult, err := r.client.ContainerInspect(ctx, helperID, client.ContainerInspectOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("deployment: inspect network probe helper: %w", err)
	}
	if inspectResult.Container.State == nil {
		return 0, "", fmt.Errorf("deployment: network probe helper has no state: %w", domain.ErrAppStateConflict)
	}
	return inspectResult.Container.State.ExitCode, stdout.String(), nil
}

type cappedProbeBuffer struct {
	bytes.Buffer
}

func (b *cappedProbeBuffer) Write(p []byte) (int, error) {
	const limit = 64
	remaining := limit - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

// buildNetworkProbeCommand builds the helper argv for the requested
// protocol. The nc bound is kept strictly shorter than the attempt bound,
// so the helper exits while the caller context is still alive; it is
// rounded to whole seconds because nc -w takes seconds, floored at one,
// and clamped so one attempt can never outlive its bound.
func buildNetworkProbeCommand(request domain.ContainerNetworkProbeRequest, targetIP string) networkProbeArgv {
	seconds := int(math.Ceil(request.Timeout.Seconds())) - 1
	if seconds < 1 {
		seconds = 1
	}
	if seconds > probeMaxSeconds {
		seconds = probeMaxSeconds
	}
	port := strconv.Itoa(request.Port)
	if request.Protocol == domain.ProbeProtocolHTTP {
		return networkProbeArgv{
			script: httpProbeScript,
			args:   []string{request.Path, strconv.Itoa(seconds), targetIP, port},
		}
	}
	return networkProbeArgv{
		script: tcpProbeScript,
		args:   []string{strconv.Itoa(seconds), targetIP, port},
	}
}

// parseProbeStatus extracts the HTTP status the helper observed; 0 when
// none was read.
func parseProbeStatus(stdout string) int {
	status, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil || status < 0 || status > 999 {
		return 0
	}
	return status
}

// probeDiagnostic describes an unhealthy attempt without leaking addresses
// or command output.
func probeDiagnostic(request domain.ContainerNetworkProbeRequest, status int) string {
	if request.Protocol == domain.ProbeProtocolHTTP {
		if status > 0 {
			return fmt.Sprintf("http status %d", status)
		}
		return "no http response"
	}
	return "tcp connection refused or timed out"
}
