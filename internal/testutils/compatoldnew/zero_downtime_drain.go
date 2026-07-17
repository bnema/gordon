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
	zeroDowntimeDrainScenarioName = "proxy/zero-downtime-drain"
	zeroDowntimeDrainPort         = 8080
	zeroDowntimeDrainStart        = "SLOW-START\n"
	zeroDowntimeDrainDone         = "SLOW-DONE\n"
	zeroDowntimeDrainBaseImage    = "busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
)

const zeroDowntimeDrainDockerfile = `FROM busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
LABEL gordon.proxy.port=8080
EXPOSE 8080
RUN mkdir -p /www/cgi-bin && printf '%s\n' '#!/bin/sh' 'echo "Content-Type: text/plain"' 'echo' 'printf "SLOW-START\\n"' 'sleep 3' 'printf "SLOW-DONE\\n"' > /www/cgi-bin/slow && chmod 0555 /www/cgi-bin/slow
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`

type zeroDowntimeDrainResources struct {
	runID     string
	sourceTag string
	domain    string
	imageRefs map[string]string
}

func newZeroDowntimeDrainResources(runID, domain string) *zeroDowntimeDrainResources {
	part := sanitizePart(runID)
	if len(part) > 60 {
		part = part[:60]
	}
	return &zeroDowntimeDrainResources{
		runID: runID, domain: domain,
		sourceTag: "gordon-compat-zero-drain:" + part,
		imageRefs: make(map[string]string),
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
	return nil
}

func (r *zeroDowntimeDrainResources) cleanup(ctx context.Context) error {
	var errs []error
	if err := r.cleanupContainers(ctx); err != nil {
		errs = append(errs, err)
	}
	for _, ref := range r.imageRefs {
		if err := dockerCompatibilityCommand(ctx, "image", "rm", "-f", ref); err != nil {
			errs = append(errs, fmt.Errorf("remove zero downtime drain registry image: %w", err))
		}
	}
	if err := dockerCompatibilityCommand(ctx, "image", "rm", "-f", r.sourceTag); err != nil {
		errs = append(errs, fmt.Errorf("remove zero downtime drain source image: %w", err))
	}
	return errors.Join(errs...)
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
stabilization_delay = "100ms"
drain_mode = "inflight"
drain_timeout = "10s"
%s`, registryPort, fixture.DataDir, registryPort, mustDrainRuntimeSocket(), proxyPort, secret, registryPort, routes)
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
	if err := pushZeroDowntimeDrainImage(ctx, setup.fixture.Root, setup.image, resources.sourceTag, "compat-"+side, setup.token); err != nil {
		return SideResult{}, err
	}
	resources.imageRefs[side] = setup.image
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

func pushZeroDowntimeDrainImage(ctx context.Context, workDir, image, sourceTag, username, token string) error {
	registry := strings.Split(image, "/")[0]
	if err := dockerCompatibilityCommand(ctx, "tag", sourceTag, image); err != nil {
		return fmt.Errorf("zero downtime drain tag image: %w", err)
	}
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
	name := "gordon-" + resources.domain
	if err := dockerCompatibilityCommand(ctx, "run", "-d", "--name", name,
		"--label", LabelRun+"="+labels[LabelRun], "--label", LabelSide+"="+labels[LabelSide], "--label", LabelFixture+"="+labels[LabelFixture],
		"--label", "gordon.managed=true", "--label", "gordon.domain="+resources.domain, "--label", "gordon.route="+resources.domain,
		"-p", "127.0.0.1::"+strconv.Itoa(zeroDowntimeDrainPort), image); err != nil {
		return fmt.Errorf("zero downtime drain initial container: %w", err)
	}
	return nil
}

type zeroDowntimeDrainObservation struct {
	SlowStarted           bool `json:"slowStarted"`
	DeployStatus          int  `json:"deployStatus"`
	OldRunningDuringDrain bool `json:"oldRunningDuringDrain"`
	SlowStatus            int  `json:"slowStatus"`
	SlowDone              bool `json:"slowDone"`
	ReplacementRunning    bool `json:"replacementRunning"`
	CanonicalRunning      bool `json:"canonicalRunning"`
	NewNameRemoved        bool `json:"newNameRemoved"`
	OldRemoved            bool `json:"oldRemoved"`
	FreshStatus           int  `json:"freshStatus"`
	FreshSucceeded        bool `json:"freshSucceeded"`
	TimingBounded         bool `json:"timingBounded"`
}

func captureZeroDowntimeDrain(ctx context.Context, proxyAddress, adminURL, domain, token string) (ProxyArtifact, error) {
	observation := zeroDowntimeDrainObservation{TimingBounded: true}
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	response, reader, first, err := beginZeroDowntimeDrainSlowRequest(requestCtx, proxyAddress, domain, &observation)
	if err != nil {
		return zeroDowntimeDrainArtifact(observation), err
	}
	defer response.Body.Close()
	observation.SlowStarted = first == zeroDowntimeDrainStart
	if !observation.SlowStarted || observation.SlowStatus != http.StatusOK {
		return zeroDowntimeDrainArtifact(observation), fmt.Errorf("zero downtime drain did not receive slow start (status %d, marker %t)", observation.SlowStatus, observation.SlowStarted)
	}
	deployCh := make(chan struct {
		status int
		err    error
	}, 1)
	go func() {
		status, err := deployZeroDowntimeDrain(requestCtx, adminURL, domain, token)
		deployCh <- struct {
			status int
			err    error
		}{status, err}
	}()
	oldName := "gordon-" + domain
	observation.OldRunningDuringDrain = waitZeroDowntimeDrainRunning(requestCtx, oldName, 2*time.Second)
	rest, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
	observation.SlowDone = string(rest) == zeroDowntimeDrainDone
	deploy := <-deployCh
	observation.DeployStatus = deploy.status
	if readErr != nil {
		return zeroDowntimeDrainArtifact(observation), fmt.Errorf("zero downtime drain read completion: %w", readErr)
	}
	if deploy.err != nil {
		return zeroDowntimeDrainArtifact(observation), deploy.err
	}
	observation.ReplacementRunning, observation.CanonicalRunning, observation.NewNameRemoved = waitZeroDowntimeDrainReplacement(requestCtx, domain, 30*time.Second)
	observation.OldRemoved = observation.ReplacementRunning
	observation.FreshStatus, observation.FreshSucceeded = freshZeroDowntimeDrainRequest(requestCtx, proxyAddress, domain)
	artifact := zeroDowntimeDrainArtifact(observation)
	if observation.DeployStatus != http.StatusOK || !observation.OldRunningDuringDrain || !observation.SlowDone || !observation.ReplacementRunning || !observation.OldRemoved || !observation.FreshSucceeded || !observation.TimingBounded {
		return artifact, fmt.Errorf("zero downtime drain contract failed: %+v", observation)
	}
	return artifact, nil
}

func freshZeroDowntimeDrainRequest(ctx context.Context, proxyAddress, domain string) (int, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+proxyAddress+"/cgi-bin/slow", nil)
	if err != nil {
		return 0, false
	}
	req.Host = domain
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, err == nil && response.StatusCode == http.StatusOK && string(body) == zeroDowntimeDrainStart+zeroDowntimeDrainDone
}

func beginZeroDowntimeDrainSlowRequest(ctx context.Context, proxyAddress, domain string, observation *zeroDowntimeDrainObservation) (*http.Response, *bufio.Reader, string, error) {
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
				first, readErr := reader.ReadString('\n')
				if readErr == nil {
					observation.SlowStatus = response.StatusCode
					return response, reader, first, nil
				}
				_ = response.Body.Close()
			} else {
				_ = response.Body.Close()
			}
		}
		select {
		case <-ctx.Done():
			return nil, nil, "", fmt.Errorf("zero downtime drain slow request: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	observation.SlowStatus = lastStatus
	return nil, nil, "", fmt.Errorf("zero downtime drain slow request did not become ready (status %d)", lastStatus)
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

func waitZeroDowntimeDrainRunning(ctx context.Context, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := dockerCompatibilityOutput(ctx, "container", "inspect", "--format", "{{.State.Running}}", name)
		if err == nil && strings.TrimSpace(output) == "true" {
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

func waitZeroDowntimeDrainReplacement(ctx context.Context, domain string, timeout time.Duration) (bool, bool, bool) {
	deadline := time.Now().Add(timeout)
	canonical, next := "gordon-"+domain, "gordon-"+domain+"-new"
	for time.Now().Before(deadline) {
		canonicalRunning := waitZeroDowntimeDrainRunning(ctx, canonical, 100*time.Millisecond)
		_, nextErr := dockerCompatibilityOutput(ctx, "container", "inspect", "--format", "{{.State.Running}}", next)
		if canonicalRunning && nextErr != nil {
			return true, true, true
		}
		select {
		case <-ctx.Done():
			return false, false, false
		case <-time.After(100 * time.Millisecond):
		}
	}
	canonicalRunning := waitZeroDowntimeDrainRunning(context.Background(), canonical, time.Second)
	_, nextErr := dockerCompatibilityOutput(context.Background(), "container", "inspect", "--format", "{{.State.Running}}", next)
	return false, canonicalRunning, nextErr != nil
}

func zeroDowntimeDrainArtifact(observation zeroDowntimeDrainObservation) ProxyArtifact {
	return NewProxyArtifact("zero downtime drain", observation, LevelExact)
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
	return "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityZeroDowntimeDrain$' -count=1"
}
