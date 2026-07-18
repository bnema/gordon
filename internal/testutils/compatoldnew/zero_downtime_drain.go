package compatoldnew

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	zeroDowntimeDrainScenarioName        = "proxy/zero-downtime-drain"
	zeroDowntimeDrainPort                = 8080
	zeroDowntimeDrainTimeout             = 10 * time.Second
	zeroDowntimeDrainStart               = "SLOW-START\n"
	zeroDowntimeDrainDone                = "SLOW-DONE\n"
	zeroDowntimeDrainStateMarker         = "/state/request-started"
	zeroDowntimeDrainStateRelease        = "/state/request-release"
	zeroDowntimeDrainOldInstance         = "old"
	zeroDowntimeDrainReplacementInstance = "replacement"
	zeroDowntimeDrainBaseImage           = "busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
)

const zeroDowntimeDrainDockerfile = `FROM busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
LABEL gordon.proxy.port=8080
ENV INSTANCE_MARKER=replacement
EXPOSE 8080
RUN mkdir -p /www/cgi-bin /state && printf '%s\n' '#!/bin/sh' 'echo "Content-Type: text/plain"' 'echo' 'tmp=/state/request-started.tmp' 'umask 077' ': > "$tmp" && mv "$tmp" /state/request-started' 'printf "INSTANCE:%s\\n" "$INSTANCE_MARKER"' 'printf "SLOW-START\\n"' 'while [ ! -f /state/request-release ]; do sleep 0.1; done' 'printf "SLOW-DONE\\n"' > /www/cgi-bin/slow && printf '%s\n' '#!/bin/sh' 'echo "Content-Type: text/plain"' 'echo' 'printf "INSTANCE:%s\\n" "$INSTANCE_MARKER"' > /www/cgi-bin/fast && chmod 0555 /www/cgi-bin/slow /www/cgi-bin/fast
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`

type zeroDowntimeDrainResources struct {
	runID                 string
	sourceTag             string
	domain                string
	imageTags             []string
	volumeNames           []string
	cleanupCommand        func(context.Context, ...string) error
	cleanupContainersFunc func(context.Context) error
}

func newZeroDowntimeDrainResources(runID, domain string) *zeroDowntimeDrainResources {
	part := sanitizePart(runID)
	if len(part) > 60 {
		part = part[:60]
	}
	return &zeroDowntimeDrainResources{
		runID:          runID,
		domain:         domain,
		sourceTag:      "gordon-compat-zero-drain:" + part,
		cleanupCommand: dockerCompatibilityCommand,
	}
}

func (r *zeroDowntimeDrainResources) buildImage(ctx context.Context) error {
	if err := dockerCompatibilityCommand(ctx, "pull", zeroDowntimeDrainBaseImage); err != nil {
		return fmt.Errorf("zero downtime drain pull pinned base image: %w", err)
	}
	dir, err := os.MkdirTemp("", "gordon-compat-zero-drain-image-*")
	if err != nil {
		return fmt.Errorf("zero downtime drain image directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(zeroDowntimeDrainDockerfile), 0o600); err != nil {
		return fmt.Errorf("zero downtime drain Dockerfile: %w", err)
	}
	labels := ResourceLabels(r.runID, "shared", zeroDowntimeDrainScenarioName)
	if err := dockerCompatibilityCommand(ctx, "build", "--pull=false",
		"--label", LabelRun+"="+labels[LabelRun], "--label", LabelSide+"="+labels[LabelSide], "--label", LabelFixture+"="+labels[LabelFixture],
		"--tag", r.sourceTag, dir); err != nil {
		return fmt.Errorf("zero downtime drain build image: %w", err)
	}
	r.trackImageTag(r.sourceTag)
	return nil
}

func (r *zeroDowntimeDrainResources) trackImageTag(tag string) {
	for _, tracked := range r.imageTags {
		if tracked == tag {
			return
		}
	}
	r.imageTags = append(r.imageTags, tag)
}

func (r *zeroDowntimeDrainResources) stateVolumeName(side string) string {
	part := sanitizePart(r.runID)
	if len(part) > 45 {
		part = part[:45]
	}
	return "gordon-compat-zero-drain-state-" + part + "-" + sanitizePart(side)
}

func (r *zeroDowntimeDrainResources) createStateVolume(ctx context.Context, side string) (string, error) {
	volume := r.stateVolumeName(side)
	labels := ResourceLabels(r.runID, side, zeroDowntimeDrainScenarioName)
	if err := dockerCompatibilityCommand(ctx, "volume", "create", "--label", LabelRun+"="+labels[LabelRun],
		"--label", LabelSide+"="+labels[LabelSide], "--label", LabelFixture+"="+labels[LabelFixture], volume); err != nil {
		return "", fmt.Errorf("create zero downtime drain state volume: %w", err)
	}
	r.volumeNames = append(r.volumeNames, volume)
	return volume, nil
}

func (r *zeroDowntimeDrainResources) cleanup(ctx context.Context) error {
	var errs []error
	if err := r.runCleanupContainers(ctx); err != nil {
		errs = append(errs, err)
	}
	for _, tag := range r.imageTags {
		if err := r.runCleanupCommand(ctx, "image", "rm", "-f", tag); err != nil {
			errs = append(errs, fmt.Errorf("remove zero downtime drain image %q: %w", tag, err))
		}
	}
	for _, volume := range r.volumeNames {
		if err := r.runCleanupCommand(ctx, "volume", "rm", "-f", volume); err != nil {
			errs = append(errs, fmt.Errorf("remove zero downtime drain state volume %q: %w", volume, err))
		}
	}
	return errors.Join(errs...)
}

func (r *zeroDowntimeDrainResources) runCleanupContainers(ctx context.Context) error {
	if r.cleanupContainersFunc != nil {
		return r.cleanupContainersFunc(ctx)
	}
	return r.cleanupContainers(ctx)
}

func (r *zeroDowntimeDrainResources) runCleanupCommand(ctx context.Context, args ...string) error {
	if r.cleanupCommand != nil {
		return r.cleanupCommand(ctx, args...)
	}
	return dockerCompatibilityCommand(ctx, args...)
}

func (r *zeroDowntimeDrainResources) cleanupContainers(ctx context.Context) error {
	var errs []error
	for _, name := range []string{"gordon-" + r.domain, "gordon-" + r.domain + "-new", "gordon-" + r.domain + "-next"} {
		if err := removeZeroDowntimeDrainContainer(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	// A failed or changed deployment can use a generated name. Remove only the
	// containers bearing this unique test domain; never prune unrelated state.
	ids, err := dockerCompatibilityOutput(ctx, "ps", "-aq", "--filter", "label=gordon.domain="+r.domain)
	if err != nil {
		errs = append(errs, fmt.Errorf("list zero downtime drain domain containers: %w", err))
	} else if fields := strings.Fields(ids); len(fields) > 0 {
		args := append([]string{"rm", "-f"}, fields...)
		if err := dockerCompatibilityCommand(ctx, args...); err != nil {
			errs = append(errs, fmt.Errorf("remove zero downtime drain labeled containers: %w", err))
		}
	}
	return errors.Join(errs...)
}

func removeZeroDowntimeDrainContainer(ctx context.Context, name string) error {
	if _, err := dockerCompatibilityOutput(ctx, "container", "inspect", "--format", "{{.Name}}", name); err != nil {
		// A missing exact name is expected between sequential sides. Do not turn
		// it into a cleanup failure, but retain no daemon output.
		return nil
	}
	if err := dockerCompatibilityCommand(ctx, "rm", "-f", name); err != nil {
		return fmt.Errorf("remove zero downtime drain container: %w", err)
	}
	return nil
}

// RunCompatibilityZeroDowntimeDrain proves each binary drains a live proxied
// request while the active daemon (rather than a second local CLI kernel)
// deploys its replacement.
func RunCompatibilityZeroDowntimeDrain(ctx context.Context, repoRoot, artifactDir string) (report Report, err error) {
	if repoRoot == "" || artifactDir == "" {
		return Report{}, fmt.Errorf("zero downtime drain compatibility requires repository root and artifact directory")
	}
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		return Report{}, err
	}
	runID := RunID(zeroDowntimeDrainScenarioName)
	domain := zeroDowntimeDrainDomain(runID)
	resources := newZeroDowntimeDrainResources(runID, domain)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = errors.Join(err, resources.cleanup(cleanupCtx))
	}()
	if err := resources.buildImage(ctx); err != nil {
		return Report{}, err
	}
	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}
	parent, err := os.MkdirTemp("", "gordon-compat-zero-drain-*")
	if err != nil {
		return Report{}, fmt.Errorf("zero downtime drain fixture parent: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(parent)) }()

	old, oldErr := runZeroDowntimeDrainSide(ctx, SideOld, binaries.Old.BinaryPath, parent, domain, resources)
	if oldErr != nil {
		old = zeroDowntimeDrainFailure(SideOld, oldErr)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	cleanupErr := resources.cleanupContainers(cleanupCtx)
	cleanupCancel()
	if cleanupErr != nil {
		old.ValidationError = errors.Join(old.ValidationError, cleanupErr)
	}
	newSide, newErr := runZeroDowntimeDrainSide(ctx, SideNew, binaries.New.BinaryPath, parent, domain, resources)
	if newErr != nil {
		newSide = zeroDowntimeDrainFailure(SideNew, newErr)
	}
	return CompareSideResultsWithMetadata(old, newSide, nil, artifactDir, ReportMetadata{
		BaselineCommit: binaries.Old.Commit, CandidateCommit: binaries.New.Commit, RerunCommand: zeroDowntimeDrainRerunCommand(),
	})
}

type zeroDowntimeDrainSide struct {
	fixture      SideFixture
	registryPort int
	proxyPort    int
	reservations *adminAPIPortReservations
	token        string
	image        string
}

func (s *zeroDowntimeDrainSide) releaseReservations() error { return s.reservations.close() }

func stageZeroDowntimeDrainSide(parent, domain, runID string) (zeroDowntimeDrainSide, error) {
	fixture, err := StageSideFixture(parent, filepath.Join(FixtureRoot(), "configs", "minimal.toml"))
	if err != nil {
		return zeroDowntimeDrainSide{}, err
	}
	reservations, err := reserveAdminAPIPorts()
	if err != nil {
		_ = os.RemoveAll(fixture.Root)
		return zeroDowntimeDrainSide{}, err
	}
	cleanup := func(stageErr error) (zeroDowntimeDrainSide, error) {
		return zeroDowntimeDrainSide{}, errors.Join(stageErr, reservations.close(), os.RemoveAll(fixture.Root))
	}
	registryPort := reservations.registry.Addr().(*net.TCPAddr).Port
	proxyPort := reservations.proxy.Addr().(*net.TCPAddr).Port
	part := sanitizePart(runID)
	if len(part) > 45 {
		part = part[:45]
	}
	image := "localhost:" + strconv.Itoa(registryPort) + "/gordon-compat-drain-" + part + ":latest"
	// #nosec G101 -- isolated unsafe-backend fixture requires a deterministic local secret.
	secret := "compat-zero-drain-test-only-secret-0123456789"
	config := zeroDowntimeDrainConfig(fixture, registryPort, proxyPort, image, domain, false, secret)
	if err := os.WriteFile(fixture.ConfigPath, []byte(config), 0o600); err != nil {
		return cleanup(fmt.Errorf("write zero downtime drain config: %w", err))
	}
	secretPath := filepath.Join(fixture.DataDir, "secrets", secret)
	if err := os.MkdirAll(filepath.Dir(secretPath), 0o700); err != nil {
		return cleanup(fmt.Errorf("create zero downtime drain secret directory: %w", err))
	}
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		return cleanup(fmt.Errorf("write zero downtime drain secret: %w", err))
	}
	return zeroDowntimeDrainSide{fixture: fixture, registryPort: registryPort, proxyPort: proxyPort, reservations: reservations, image: image}, nil
}

func zeroDowntimeDrainConfig(fixture SideFixture, registryPort, proxyPort int, image, domain string, route bool, secret string) string {
	routes := ""
	if route {
		routes = fmt.Sprintf("\n[routes]\n%q = %q\n", domain, image)
	}
	return fmt.Sprintf(`[server]
registry_port = %d
registry_listen_address = "127.0.0.1"
data_dir = %q
gordon_domain = "localhost:%d"
runtime = %q

[entrypoints.edge]
address = "127.0.0.1:%d"
protocol = "smart_tcp"

[auth]
enabled = true
secrets_backend = "unsafe"
token_secret = %q
token_expiry = "30d"
access_token_ttl = "15m"

[api.rate_limit]
enabled = false

[network_isolation]
enabled = false

[images]
allowed_registries = ["localhost:%d"]

[deploy]
readiness_delay = "100ms"
stabilization_delay = "2s"
drain_mode = "inflight"
drain_timeout = %q
%s`, registryPort, fixture.DataDir, registryPort, mustDrainRuntimeSocket(), proxyPort, secret, registryPort, zeroDowntimeDrainTimeout.String(), routes)
}

func mustDrainRuntimeSocket() string {
	socket, err := adminDockerSocket()
	if err != nil {
		return "/var/run/docker.sock"
	}
	return socket
}

func runZeroDowntimeDrainSide(ctx context.Context, side, binaryPath, parent, domain string, resources *zeroDowntimeDrainResources) (_ SideResult, err error) {
	setup, err := stageZeroDowntimeDrainSide(parent, domain, resources.runID)
	if err != nil {
		return SideResult{}, err
	}
	defer func() { err = errors.Join(err, setup.releaseReservations(), os.RemoveAll(setup.fixture.Root)) }()
	setup.token, err = generateZeroDowntimeDrainToken(ctx, binaryPath, setup.fixture, side)
	if err != nil {
		return SideResult{}, fmt.Errorf("zero downtime drain generate token: %w", err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(setup.registryPort))
	instance := newZeroDowntimeDrainInstance(binaryPath, setup.fixture, address)
	if err := setup.releaseReservations(); err != nil {
		return SideResult{}, fmt.Errorf("release zero downtime drain ports: %w", err)
	}
	if err := instance.Start(ctx, zeroDowntimeDrainServeArgs(side, setup.fixture.ConfigPath)...); err != nil {
		return SideResult{}, fmt.Errorf("zero downtime drain initial server: %w", err)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 30*time.Second)
	readyErr := instance.WaitReady(readyCtx)
	readyCancel()
	if readyErr != nil {
		return SideResult{}, fmt.Errorf("zero downtime drain initial ready: %w", readyErr)
	}
	accessToken, err := exchangeAdminToken(ctx, "http://"+address, setup.token, side)
	if err != nil {
		return SideResult{}, fmt.Errorf("zero downtime drain exchange admin credential: %w", err)
	}
	if err := pushZeroDowntimeDrainImage(ctx, setup.fixture.Root, setup.image, resources, "compat-"+side, setup.token); err != nil {
		return SideResult{}, err
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	stopErr := instance.Stop(stopCtx)
	stopCancel()
	if stopErr != nil {
		return SideResult{}, fmt.Errorf("zero downtime drain initial stop: %w", stopErr)
	}
	config := zeroDowntimeDrainConfig(setup.fixture, setup.registryPort, setup.proxyPort, setup.image, domain, true, "compat-zero-drain-test-only-secret-0123456789")
	if err := os.WriteFile(setup.fixture.ConfigPath, []byte(config), 0o600); err != nil {
		return SideResult{}, fmt.Errorf("enable zero downtime drain route: %w", err)
	}
	if err := startZeroDowntimeDrainInitial(ctx, resources, side, setup.image); err != nil {
		return SideResult{}, err
	}
	if err := instance.Start(ctx, zeroDowntimeDrainServeArgs(side, setup.fixture.ConfigPath)...); err != nil {
		return SideResult{}, fmt.Errorf("zero downtime drain active server: %w", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer stopCancel()
		err = errors.Join(err, instance.Stop(stopCtx))
	}()
	readyCtx, readyCancel = context.WithTimeout(ctx, 30*time.Second)
	readyErr = instance.WaitReady(readyCtx)
	readyCancel()
	if readyErr != nil {
		return SideResult{}, fmt.Errorf("zero downtime drain active ready: %w", readyErr)
	}
	artifact, validationErr := captureZeroDowntimeDrain(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(setup.proxyPort)), "http://"+address, domain, accessToken)
	return SideResult{Side: side, Artifact: artifact, ValidationError: validationErr}, nil
}

func generateZeroDowntimeDrainToken(ctx context.Context, binaryPath string, fixture SideFixture, side string) (string, error) {
	capture, err := CaptureCommand(ctx, CommandCaptureRequest{
		BinaryPath: binaryPath,
		Args: []string{"auth", "token", "generate", "--config", fixture.ConfigPath, "--subject", "compat-" + side,
			"--scopes", "push,pull,admin:*:*", "--expiry", "0"},
		Dir: fixture.Root, Env: adminAPIEnvironment(fixture), Source: "gordon auth token generate", Level: LevelExact,
	})
	if err != nil {
		return "", err
	}
	raw := capture.RawValue().(map[string]any)
	if raw["exitCode"] != 0 {
		return "", fmt.Errorf("token generation exited %v", raw["exitCode"])
	}
	token := jwtLine.FindString(raw["stdout"].(string))
	if token == "" {
		return "", fmt.Errorf("token generation did not emit a JWT")
	}
	return token, nil
}

func newZeroDowntimeDrainInstance(binaryPath string, fixture SideFixture, address string) *GordonInstance {
	return &GordonInstance{BinaryPath: binaryPath, ConfigPath: fixture.ConfigPath, DataDir: fixture.DataDir, WorkingDir: fixture.Root, Env: adminAPIEnvironment(fixture), ReadinessProbe: ReadinessProbe{TCPAddress: address}}
}

func zeroDowntimeDrainServeArgs(side, configPath string) []string {
	if side == SideNew {
		return []string{"serve", "--role", "monolith", "--config", configPath}
	}
	return []string{"serve", "--config", configPath}
}

func pushZeroDowntimeDrainImage(ctx context.Context, workDir, image string, resources *zeroDowntimeDrainResources, username, token string) error {
	registry := strings.Split(image, "/")[0]
	if err := dockerCompatibilityCommand(ctx, "tag", resources.sourceTag, image); err != nil {
		return fmt.Errorf("zero downtime drain tag image: %w", err)
	}
	// Record the exact local registry tag before any later operation can fail.
	// Cleanup removes tags individually rather than broadly pruning Docker state.
	resources.trackImageTag(image)
	configDir := filepath.Join(workDir, "docker-config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("zero downtime drain Docker config: %w", err)
	}
	credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + token))
	config := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, registry, credentials)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(config), 0o600); err != nil {
		return fmt.Errorf("zero downtime drain Docker credentials: %w", err)
	}
	push := exec.CommandContext(ctx, "docker", "push", image) // #nosec G204 -- fixture image reference only.
	push.Env = append(os.Environ(), "DOCKER_CONFIG="+configDir)
	if output, err := push.CombinedOutput(); err != nil {
		return fmt.Errorf("zero downtime drain registry push failed: %s (%q)", classifyZeroDowntimeDrainDockerError(string(output)), string(output))
	}
	return nil
}

func classifyZeroDowntimeDrainDockerError(output string) string {
	lower := strings.ToLower(output)
	for _, category := range []string{"unauthorized", "forbidden", "connection refused", "server gave http response to https client", "tls", "not found"} {
		if strings.Contains(lower, category) {
			return category
		}
	}
	return "command error"
}

func startZeroDowntimeDrainInitial(ctx context.Context, resources *zeroDowntimeDrainResources, side, image string) error {
	labels := ResourceLabels(resources.runID, side, zeroDowntimeDrainScenarioName)
	volume, err := resources.createStateVolume(ctx, side)
	if err != nil {
		return err
	}
	name := "gordon-" + resources.domain
	if err := dockerCompatibilityCommand(ctx, "run", "-d", "--name", name,
		"--label", LabelRun+"="+labels[LabelRun], "--label", LabelSide+"="+labels[LabelSide], "--label", LabelFixture+"="+labels[LabelFixture],
		"--label", "gordon.managed=true", "--label", "gordon.domain="+resources.domain, "--label", "gordon.route="+resources.domain,
		"--env", "INSTANCE_MARKER="+zeroDowntimeDrainOldInstance,
		"--mount", "type=volume,src="+volume+",dst=/state",
		"-p", "127.0.0.1::"+strconv.Itoa(zeroDowntimeDrainPort), image); err != nil {
		return fmt.Errorf("zero downtime drain initial container: %w", err)
	}
	return nil
}

type zeroDowntimeDrainObservation struct {
	MarkerObserved                       bool   `json:"marker_observed"`
	OldResponseFromOld                   bool   `json:"old_response_from_old"`
	FreshResponseFromReplacement         bool   `json:"fresh_response_from_replacement"`
	ReplacementRoutedDuringStabilization bool   `json:"replacement_routed_during_stabilization"`
	OldSurvivedRefreshUntilRelease       bool   `json:"old_survived_refresh_until_release"`
	TargetChanged                        bool   `json:"target_changed"`
	DeploySucceeded                      bool   `json:"deploy_succeeded"`
	DeployBlockedUntilResponseRelease    bool   `json:"deploy_blocked_until_response_release"`
	DeployReturnedBeforeResponseRelease  bool   `json:"deploy_returned_before_response_release"`
	OldTargetContinuouslyRunning         bool   `json:"old_target_continuously_running"`
	DrainDuration                        string `json:"drain_duration"`
	DrainCompletedWithinTimeout          bool   `json:"drain_completed_within_timeout"`
}

// zeroDowntimeDrainSafetyObservation contains only the semantic outcomes that
// must remain compatible. Wall-clock duration is retained in the raw artifact
// for diagnostics, but cannot cause old/new parity failures.
type zeroDowntimeDrainSafetyObservation struct {
	MarkerObserved                       bool `json:"marker_observed"`
	OldResponseFromOld                   bool `json:"old_response_from_old"`
	FreshResponseFromReplacement         bool `json:"fresh_response_from_replacement"`
	ReplacementRoutedDuringStabilization bool `json:"replacement_routed_during_stabilization"`
	OldSurvivedRefreshUntilRelease       bool `json:"old_survived_refresh_until_release"`
	TargetChanged                        bool `json:"target_changed"`
	DeploySucceeded                      bool `json:"deploy_succeeded"`
	DeployBlockedUntilResponseRelease    bool `json:"deploy_blocked_until_response_release"`
	DeployReturnedBeforeResponseRelease  bool `json:"deploy_returned_before_response_release"`
	OldTargetContinuouslyRunning         bool `json:"old_target_continuously_running"`
	DrainCompletedWithinTimeout          bool `json:"drain_completed_within_timeout"`
}

func (o zeroDowntimeDrainObservation) safetyObservation() zeroDowntimeDrainSafetyObservation {
	return zeroDowntimeDrainSafetyObservation{
		MarkerObserved:                       o.MarkerObserved,
		OldResponseFromOld:                   o.OldResponseFromOld,
		FreshResponseFromReplacement:         o.FreshResponseFromReplacement,
		ReplacementRoutedDuringStabilization: o.ReplacementRoutedDuringStabilization,
		OldSurvivedRefreshUntilRelease:       o.OldSurvivedRefreshUntilRelease,
		TargetChanged:                        o.TargetChanged,
		DeploySucceeded:                      o.DeploySucceeded,
		DeployBlockedUntilResponseRelease:    o.DeployBlockedUntilResponseRelease,
		DeployReturnedBeforeResponseRelease:  o.DeployReturnedBeforeResponseRelease,
		OldTargetContinuouslyRunning:         o.OldTargetContinuouslyRunning,
		DrainCompletedWithinTimeout:          o.DrainCompletedWithinTimeout,
	}
}

func (o zeroDowntimeDrainObservation) satisfiesOrderingContract() bool {
	deploymentOrderingProved := o.DeployBlockedUntilResponseRelease ||
		(o.DeployReturnedBeforeResponseRelease && o.OldTargetContinuouslyRunning)
	return o.MarkerObserved && o.OldResponseFromOld && o.FreshResponseFromReplacement &&
		o.ReplacementRoutedDuringStabilization && o.OldSurvivedRefreshUntilRelease &&
		o.TargetChanged && o.DeploySucceeded && o.DrainCompletedWithinTimeout && deploymentOrderingProved
}

type zeroDowntimeDrainDeployResult struct {
	err         error
	completedAt time.Time
}

type zeroDowntimeDrainSlowCompletion struct {
	rest        []byte
	err         error
	completedAt time.Time
}

type zeroDowntimeDrainRaceObservation struct {
	oldResponseFromOld                   bool
	replacementRoutedDuringStabilization bool
	oldSurvivedRefreshUntilRelease       bool
	freshResponseFromReplacement         bool
	targetChanged                        bool
	deploySucceeded                      bool
	deployBlockedUntilResponseRelease    bool
	deployReturnedBeforeResponseRelease  bool
	oldTargetContinuouslyRunning         bool
	drainDuration                        time.Duration
}

func captureZeroDowntimeDrain(ctx context.Context, proxyAddress, adminURL, domain, token string) (ProxyArtifact, error) {
	observation := zeroDowntimeDrainObservation{}
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	oldName := "gordon-" + domain
	oldTargetID, err := zeroDowntimeDrainContainerID(requestCtx, oldName)
	if err != nil {
		return zeroDowntimeDrainArtifact(observation), fmt.Errorf("identify zero downtime drain old target: %w", err)
	}
	response, reader, instance, err := beginZeroDowntimeDrainSlowRequest(requestCtx, proxyAddress, domain)
	if err != nil {
		return zeroDowntimeDrainArtifact(observation), err
	}
	defer response.Body.Close()
	observation.OldResponseFromOld = instance == zeroDowntimeDrainOldInstance
	if !observation.OldResponseFromOld {
		return zeroDowntimeDrainArtifact(observation), fmt.Errorf("zero downtime drain slow response did not identify the old instance")
	}
	observation.MarkerObserved = waitZeroDowntimeDrainMarker(requestCtx, oldName, 10*time.Second)
	if !observation.MarkerObserved {
		return zeroDowntimeDrainArtifact(observation), fmt.Errorf("zero downtime drain old request marker was not observed before deploy")
	}
	if !zeroDowntimeDrainContainerRunning(requestCtx, oldTargetID) {
		return zeroDowntimeDrainArtifact(observation), fmt.Errorf("zero downtime drain old target was not running before deploy")
	}

	monitorDone := make(chan struct{})
	monitorCh, monitorStarted := monitorZeroDowntimeDrainTarget(requestCtx, oldTargetID, monitorDone)
	if !<-monitorStarted {
		close(monitorDone)
		<-monitorCh
		return zeroDowntimeDrainArtifact(observation), fmt.Errorf("zero downtime drain old target monitor could not start")
	}
	deployCh := make(chan zeroDowntimeDrainDeployResult, 1)
	go func() {
		_, deployErr := deployZeroDowntimeDrain(requestCtx, adminURL, domain, token)
		deployCh <- zeroDowntimeDrainDeployResult{err: deployErr, completedAt: time.Now()}
	}()

	race, raceErr := observeZeroDowntimeDrainRace(requestCtx, proxyAddress, domain, oldTargetID, reader, instance, deployCh, monitorDone, monitorCh)
	observation.OldResponseFromOld = observation.OldResponseFromOld && race.oldResponseFromOld
	observation.ReplacementRoutedDuringStabilization = race.replacementRoutedDuringStabilization
	observation.OldSurvivedRefreshUntilRelease = race.oldSurvivedRefreshUntilRelease
	observation.FreshResponseFromReplacement = race.freshResponseFromReplacement
	observation.TargetChanged = race.targetChanged
	observation.DeploySucceeded = race.deploySucceeded
	observation.DeployBlockedUntilResponseRelease = race.deployBlockedUntilResponseRelease
	observation.DeployReturnedBeforeResponseRelease = race.deployReturnedBeforeResponseRelease
	observation.OldTargetContinuouslyRunning = race.oldTargetContinuouslyRunning
	observation.DrainDuration = race.drainDuration.String()
	observation.DrainCompletedWithinTimeout = zeroDowntimeDrainCompletedWithinTimeout(race.drainDuration)
	artifact := zeroDowntimeDrainArtifact(observation)
	if raceErr != nil {
		return artifact, raceErr
	}
	if !observation.satisfiesOrderingContract() {
		return artifact, fmt.Errorf("zero downtime drain contract failed: %+v", observation)
	}
	return artifact, nil
}

func observeZeroDowntimeDrainRace(ctx context.Context, proxyAddress, domain, oldTargetID string, reader io.Reader, oldInstance string, deployCh <-chan zeroDowntimeDrainDeployResult, monitorDone chan<- struct{}, monitorCh <-chan bool) (zeroDowntimeDrainRaceObservation, error) {
	observation := zeroDowntimeDrainRaceObservation{}
	startedAt := time.Now()
	replacementRouted, oldSurvived, orderingErr := refreshZeroDowntimeDrainDuringStabilization(ctx, proxyAddress, domain, oldTargetID)
	observation.replacementRoutedDuringStabilization = replacementRouted
	observation.oldSurvivedRefreshUntilRelease = oldSurvived

	var earlyDeploy *zeroDowntimeDrainDeployResult
	select {
	case deploy := <-deployCh:
		earlyDeploy = &deploy
		orderingErr = errors.Join(orderingErr, errors.New("zero downtime drain deploy returned before slow response release"))
	default:
	}
	if releaseErr := releaseZeroDowntimeDrainSlowResponse(ctx, oldTargetID); releaseErr != nil {
		orderingErr = errors.Join(orderingErr, releaseErr)
	}
	slowRest, slowErr := io.ReadAll(io.LimitReader(reader, 1<<20))
	slowCompletion := zeroDowntimeDrainSlowCompletion{rest: slowRest, err: slowErr, completedAt: time.Now()}
	close(monitorDone)
	observation.oldTargetContinuouslyRunning = <-monitorCh
	observation.drainDuration = slowCompletion.completedAt.Sub(startedAt)

	deploy := awaitZeroDowntimeDrainDeploy(deployCh, earlyDeploy)
	observation.deploySucceeded = deploy.err == nil
	observation.deployReturnedBeforeResponseRelease = deploy.completedAt.Before(slowCompletion.completedAt)
	observation.deployBlockedUntilResponseRelease = !observation.deployReturnedBeforeResponseRelease
	if slowCompletion.err != nil {
		orderingErr = errors.Join(orderingErr, fmt.Errorf("zero downtime drain read completion: %w", slowCompletion.err))
	}
	instance, completed := parseZeroDowntimeDrainResponse("INSTANCE:" + oldInstance + "\n" + zeroDowntimeDrainStart + string(slowCompletion.rest))
	observation.oldResponseFromOld = completed && instance == zeroDowntimeDrainOldInstance
	if deploy.err != nil {
		orderingErr = errors.Join(orderingErr, deploy.err)
	}
	observation.targetChanged = waitZeroDowntimeDrainReplacementCanonical(ctx, domain, oldTargetID, 30*time.Second)
	_, freshInstance, freshErr := freshZeroDowntimeDrainRequest(ctx, proxyAddress, domain)
	observation.freshResponseFromReplacement = freshErr == nil && freshInstance == zeroDowntimeDrainReplacementInstance
	return observation, orderingErr
}

func refreshZeroDowntimeDrainDuringStabilization(ctx context.Context, proxyAddress, domain, oldTargetID string) (bool, bool, error) {
	if !waitZeroDowntimeDrainReplacementRunning(ctx, domain, 30*time.Second) {
		return false, false, errors.New("zero downtime drain replacement was not running during stabilization")
	}
	replacementRouted := freshZeroDowntimeDrainReplacementDuringStabilization(ctx, proxyAddress, domain)
	if !replacementRouted {
		return false, false, errors.New("zero downtime drain refresh did not route to replacement during stabilization")
	}
	if !zeroDowntimeDrainContainerRunning(ctx, oldTargetID) {
		return true, false, errors.New("zero downtime drain old target did not survive refresh until release")
	}
	return true, true, nil
}

func awaitZeroDowntimeDrainDeploy(deployCh <-chan zeroDowntimeDrainDeployResult, earlyDeploy *zeroDowntimeDrainDeployResult) zeroDowntimeDrainDeployResult {
	if earlyDeploy != nil {
		return *earlyDeploy
	}
	return <-deployCh
}

func freshZeroDowntimeDrainReplacementDuringStabilization(ctx context.Context, proxyAddress, domain string) bool {
	deadlineCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	replacementName := "gordon-" + domain + "-new"
	for {
		if !zeroDowntimeDrainContainerRunning(deadlineCtx, replacementName) {
			return false
		}
		_, instance, err := freshZeroDowntimeDrainRequest(deadlineCtx, proxyAddress, domain)
		if err == nil && instance == zeroDowntimeDrainReplacementInstance && zeroDowntimeDrainContainerRunning(deadlineCtx, replacementName) {
			return true
		}
		select {
		case <-deadlineCtx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func freshZeroDowntimeDrainRequest(ctx context.Context, proxyAddress, domain string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+proxyAddress+"/cgi-bin/fast", nil)
	if err != nil {
		return 0, "", err
	}
	req.Host = domain
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	instance, routed := parseZeroDowntimeDrainFastResponse(string(body))
	if err != nil || response.StatusCode != http.StatusOK || !routed {
		return response.StatusCode, instance, fmt.Errorf("zero downtime drain fresh response was incomplete")
	}
	return response.StatusCode, instance, nil
}

func beginZeroDowntimeDrainSlowRequest(ctx context.Context, proxyAddress, domain string) (*http.Response, *bufio.Reader, string, error) {
	deadline := time.Now().Add(10 * time.Second)
	var lastStatus int
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+proxyAddress+"/cgi-bin/slow", nil)
		if err != nil {
			return nil, nil, "", fmt.Errorf("zero downtime drain create slow request: %w", err)
		}
		req.Host = domain
		response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err == nil {
			lastStatus = response.StatusCode
			if response.StatusCode == http.StatusOK {
				reader := bufio.NewReader(response.Body)
				instanceLine, firstErr := reader.ReadString('\n')
				startLine, secondErr := reader.ReadString('\n')
				instance, started := parseZeroDowntimeDrainSlowStart(instanceLine, startLine)
				if firstErr == nil && secondErr == nil && started {
					return response, reader, instance, nil
				}
			}
			_ = response.Body.Close()
		}
		select {
		case <-ctx.Done():
			return nil, nil, "", fmt.Errorf("zero downtime drain slow request: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, nil, "", fmt.Errorf("zero downtime drain slow request did not become ready (status %d)", lastStatus)
}

func parseZeroDowntimeDrainSlowStart(instanceLine, startLine string) (string, bool) {
	instance := strings.TrimSuffix(instanceLine, "\n")
	if startLine != zeroDowntimeDrainStart || !strings.HasPrefix(instance, "INSTANCE:") {
		return "", false
	}
	instance = strings.TrimPrefix(instance, "INSTANCE:")
	if instance != zeroDowntimeDrainOldInstance && instance != zeroDowntimeDrainReplacementInstance {
		return "", false
	}
	return instance, true
}

func parseZeroDowntimeDrainResponse(body string) (string, bool) {
	lines := strings.SplitAfter(body, "\n")
	if len(lines) != 4 {
		return "", false
	}
	instance, started := parseZeroDowntimeDrainSlowStart(lines[0], lines[1])
	return instance, started && lines[2] == zeroDowntimeDrainDone && lines[3] == ""
}

func parseZeroDowntimeDrainFastResponse(body string) (string, bool) {
	instance := strings.TrimSuffix(body, "\n")
	if strings.Count(body, "\n") != 1 || !strings.HasPrefix(instance, "INSTANCE:") {
		return "", false
	}
	instance = strings.TrimPrefix(instance, "INSTANCE:")
	return instance, instance == zeroDowntimeDrainReplacementInstance
}

func zeroDowntimeDrainCompletedWithinTimeout(duration time.Duration) bool {
	return duration >= 0 && duration <= zeroDowntimeDrainTimeout
}

func deployZeroDowntimeDrain(ctx context.Context, adminURL, domain, token string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL+"/admin/deploy/"+domain, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, fmt.Errorf("zero downtime drain active-daemon deploy: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, fmt.Errorf("zero downtime drain deploy returned HTTP %d: %s", response.StatusCode, classifyZeroDowntimeDrainDeployError(string(body)))
	}
	return response.StatusCode, nil
}

func classifyZeroDowntimeDrainDeployError(body string) string {
	lower := strings.ToLower(body)
	for _, category := range []string{"image", "pull", "container", "readiness", "port", "not found", "unauthorized"} {
		if strings.Contains(lower, category) {
			return category
		}
	}
	return "deployment error"
}

func waitZeroDowntimeDrainMarker(ctx context.Context, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := dockerCompatibilityCommand(ctx, "exec", name, "test", "-f", zeroDowntimeDrainStateMarker); err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func zeroDowntimeDrainContainerID(ctx context.Context, target string) (string, error) {
	output, err := dockerCompatibilityOutput(ctx, "container", "inspect", "--format", "{{.Id}}", target)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(output)
	if id == "" {
		return "", errors.New("empty container ID")
	}
	return id, nil
}

func zeroDowntimeDrainContainerRunning(ctx context.Context, target string) bool {
	output, err := dockerCompatibilityOutput(ctx, "container", "inspect", "--format", "{{.State.Running}}", target)
	return err == nil && strings.TrimSpace(output) == "true"
}

func waitZeroDowntimeDrainReplacementRunning(ctx context.Context, domain string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	replacementName := "gordon-" + domain + "-new"
	for time.Now().Before(deadline) {
		if zeroDowntimeDrainContainerRunning(ctx, replacementName) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func releaseZeroDowntimeDrainSlowResponse(ctx context.Context, target string) error {
	if err := dockerCompatibilityCommand(ctx, "exec", target, "touch", zeroDowntimeDrainStateRelease); err != nil {
		return errors.New("zero downtime drain release old slow response")
	}
	return nil
}

func monitorZeroDowntimeDrainTarget(ctx context.Context, target string, done <-chan struct{}) (<-chan bool, <-chan bool) {
	result := make(chan bool, 1)
	started := make(chan bool, 1)
	go func() {
		defer close(result)
		defer close(started)
		running := zeroDowntimeDrainContainerRunning(ctx, target)
		started <- running
		if !running {
			result <- false
			return
		}
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				result <- running
				return
			case <-ctx.Done():
				result <- false
				return
			case <-ticker.C:
				if !zeroDowntimeDrainContainerRunning(ctx, target) {
					running = false
				}
			}
		}
	}()
	return result, started
}

func waitZeroDowntimeDrainReplacementCanonical(ctx context.Context, domain, oldTargetID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	canonical, next := "gordon-"+domain, "gordon-"+domain+"-new"
	for time.Now().Before(deadline) {
		canonicalID, idErr := zeroDowntimeDrainContainerID(ctx, canonical)
		_, nextErr := dockerCompatibilityOutput(ctx, "container", "inspect", "--format", "{{.State.Running}}", next)
		if idErr == nil && canonicalID != oldTargetID && zeroDowntimeDrainContainerRunning(ctx, canonical) && nextErr != nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

func zeroDowntimeDrainArtifact(observation zeroDowntimeDrainObservation) ProxyArtifact {
	artifact := NewProxyArtifact("zero downtime drain", observation, LevelExact)
	artifact.Normalized = observation.safetyObservation()
	return artifact
}
func zeroDowntimeDrainFailure(side string, validationErr error) SideResult {
	return SideResult{Side: side, Artifact: zeroDowntimeDrainArtifact(zeroDowntimeDrainObservation{}), ValidationError: validationErr}
}
func zeroDowntimeDrainDomain(runID string) string {
	part := sanitizePart(runID)
	if len(part) > 30 {
		part = part[:30]
	}
	return "drain-" + part + ".test"
}
func zeroDowntimeDrainRerunCommand() string {
	return "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityZeroDowntimeDrain$' -count=2"
}
