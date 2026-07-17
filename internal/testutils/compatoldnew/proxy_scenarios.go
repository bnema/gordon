package compatoldnew

import (
	"context"
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
	managedHTTPRouteScenarioName = "proxy/managed-http-route"
	managedHTTPRouteMarker       = "gordon-compat-managed-http-route\n"
	managedHTTPRouteImagePort    = 8080
	managedHTTPRouteAttempts     = 2
)

// ProxyScenarios returns Phase 5 proxy compatibility scenario shells.
func ProxyScenarios() []Scenario {
	return []Scenario{
		implementedScenario(managedHTTPRouteScenarioName, SurfaceProxy, "6.5 Proxy and traffic compatibility", false),
		proxyScenario("proxy/unknown-host"),
		proxyScenario("proxy/external-route"),
		proxyScenario("proxy/h2c-backend"),
		proxyScenario("proxy/registry-domain-routing"),
		proxyScenario("proxy/body-size-limit"),
		proxyScenario("proxy/zero-downtime-drain"),
		proxyScenario("proxy/access-log-emitted"),
	}
}

func proxyScenario(name string) Scenario {
	return pendingScenario(name, SurfaceProxy, "6.5 Proxy and traffic compatibility", true, "old/new proxy compatibility scenario execution requires a real container runtime")
}

// DockerCompatibilityPreflight checks the Docker-compatible runtime without
// persisting daemon output. The harness intentionally uses Docker's stable CLI
// surface so it is not coupled to a locally installed Podman binary.
func DockerCompatibilityPreflight(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker compatibility runtime unavailable: binary not found in PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}") // #nosec G204 -- fixed Docker preflight command.
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("docker compatibility runtime unavailable: docker info failed")
	}
	if strings.TrimSpace(string(output)) == "" {
		return fmt.Errorf("docker compatibility runtime unavailable: docker info returned no server version")
	}
	return nil
}

// RunCompatibilityManagedHTTPRoute compares an actual HTTP request through
// old and new Gordon processes to independently named, Docker-managed route
// containers. The sides are deliberately sequential to avoid cross-routing.
func RunCompatibilityManagedHTTPRoute(ctx context.Context, repoRoot, artifactDir string) (report Report, err error) {
	if repoRoot == "" {
		return Report{}, fmt.Errorf("managed proxy compatibility: repository root is required")
	}
	if artifactDir == "" {
		return Report{}, fmt.Errorf("managed proxy compatibility: report artifact directory is required")
	}
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		return Report{}, err
	}

	runID := RunID(managedHTTPRouteScenarioName)
	resources := newManagedProxyResources(runID)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := resources.cleanup(cleanupCtx); cleanupErr != nil && err == nil {
			err = fmt.Errorf("managed proxy compatibility cleanup: %w", cleanupErr)
		}
	}()
	if err := resources.buildImage(ctx); err != nil {
		return Report{}, err
	}
	if err := resources.requireImageProxyPort(ctx); err != nil {
		return Report{}, err
	}

	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}
	parent, err := os.MkdirTemp("", "gordon-compat-managed-proxy-*")
	if err != nil {
		return Report{}, fmt.Errorf("managed proxy compatibility: create fixture parent: %w", err)
	}
	defer os.RemoveAll(parent)

	domain := managedProxyDomain(runID)
	old, oldErr := runManagedHTTPRouteSide(ctx, SideOld, binaries.Old.BinaryPath, parent, domain, resources)
	if oldErr != nil {
		old = managedProxyCaptureFailure(SideOld, oldErr)
	}
	// The candidate must not accidentally resolve the baseline's canonical
	// container. Remove only our known old-side name before starting new.
	if cleanupErr := resources.removeContainer(ctx, SideOld); cleanupErr != nil {
		old.ValidationError = errors.Join(old.ValidationError, cleanupErr)
	}
	new, newErr := runManagedHTTPRouteSide(ctx, SideNew, binaries.New.BinaryPath, parent, domain, resources)
	if newErr != nil {
		new = managedProxyCaptureFailure(SideNew, newErr)
	}
	return CompareSideResultsWithMetadata(old, new, nil, artifactDir, ReportMetadata{
		BaselineCommit:  binaries.Old.Commit,
		CandidateCommit: binaries.New.Commit,
		RerunCommand:    managedHTTPRouteRerunCommand(),
	})
}

type managedProxyResources struct {
	runID      string
	imageTag   string
	containers map[string]string
}

func newManagedProxyResources(runID string) *managedProxyResources {
	tagPart := sanitizePart(runID)
	if len(tagPart) > 70 {
		tagPart = tagPart[:70]
	}
	return &managedProxyResources{
		runID:      runID,
		imageTag:   "gordon-compat-managed-http-route:" + tagPart,
		containers: make(map[string]string),
	}
}

func (r *managedProxyResources) buildImage(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "gordon-compat-http-image-*")
	if err != nil {
		return fmt.Errorf("managed proxy build image: create Dockerfile directory: %w", err)
	}
	defer os.RemoveAll(dir)
	const dockerfile = `FROM busybox:1.36.1
LABEL gordon.proxy.port=8080
EXPOSE 8080
RUN mkdir -p /www && printf 'gordon-compat-managed-http-route\n' > /www/index.html
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		return fmt.Errorf("managed proxy build image: write Dockerfile: %w", err)
	}
	labels := ResourceLabels(r.runID, "shared", managedHTTPRouteScenarioName)
	args := []string{"build", "--label", LabelRun + "=" + labels[LabelRun], "--label", LabelSide + "=" + labels[LabelSide], "--label", LabelFixture + "=" + labels[LabelFixture], "--tag", r.imageTag, dir}
	if err := dockerCompatibilityCommand(ctx, args...); err != nil {
		return fmt.Errorf("managed proxy build image: %w", err)
	}
	return nil
}

// requireImageProxyPort reads only the one expected image label. It proves the
// route is resolving gordon.proxy.port from image metadata without retaining a
// raw Docker inspect document in reports.
func (r *managedProxyResources) requireImageProxyPort(ctx context.Context) error {
	value, err := dockerCompatibilityOutput(ctx, "image", "inspect", "--format", "{{ index .Config.Labels \"gordon.proxy.port\" }}", r.imageTag)
	if err != nil {
		return fmt.Errorf("managed proxy image proxy-port label: %w", err)
	}
	if strings.TrimSpace(value) != strconv.Itoa(managedHTTPRouteImagePort) {
		return fmt.Errorf("managed proxy image proxy-port label: expected %d", managedHTTPRouteImagePort)
	}
	return nil
}

func (r *managedProxyResources) startContainer(ctx context.Context, side, domain string) (string, error) {
	name := "gordon-" + domain
	if side == SideNew {
		name += "-new"
	}
	r.containers[side] = name
	labels := ResourceLabels(r.runID, side, managedHTTPRouteScenarioName)
	args := []string{
		"run", "-d", "--name", name,
		"--label", LabelRun + "=" + labels[LabelRun],
		"--label", LabelSide + "=" + labels[LabelSide],
		"--label", LabelFixture + "=" + labels[LabelFixture],
		"--label", "gordon.managed=true",
		"--label", "gordon.domain=" + domain,
		"--label", "gordon.route=" + domain,
		"-p", "127.0.0.1::8080", r.imageTag,
	}
	if err := dockerCompatibilityCommand(ctx, args...); err != nil {
		return "", fmt.Errorf("start managed proxy container: %w", err)
	}
	portOutput, err := dockerCompatibilityOutput(ctx, "port", name, "8080/tcp")
	if err != nil {
		return "", fmt.Errorf("read managed proxy container port: %w", err)
	}
	address, err := managedProxyPublishedAddress(portOutput)
	if err != nil {
		return "", err
	}
	if err := waitForTCP(ctx, address); err != nil {
		return "", fmt.Errorf("managed proxy container ready: %w", err)
	}
	return address, nil
}

func (r *managedProxyResources) cleanup(ctx context.Context) error {
	var errs []error
	for _, side := range []string{SideOld, SideNew} {
		if err := r.removeContainer(ctx, side); err != nil {
			errs = append(errs, err)
		}
	}
	if err := dockerCompatibilityCommand(ctx, "image", "rm", "-f", r.imageTag); err != nil {
		errs = append(errs, fmt.Errorf("remove managed proxy image: %w", err))
	}
	return errors.Join(errs...)
}

func (r *managedProxyResources) removeContainer(ctx context.Context, side string) error {
	name := r.containers[side]
	if name == "" {
		return nil
	}
	if err := dockerCompatibilityCommand(ctx, "rm", "-f", name); err != nil {
		return fmt.Errorf("remove managed proxy %s container: %w", side, err)
	}
	delete(r.containers, side)
	return nil
}

func dockerCompatibilityCommand(ctx context.Context, args ...string) error {
	_, err := dockerCompatibilityOutput(ctx, args...)
	return err
}

func dockerCompatibilityOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- arguments are generated by this compatibility harness.
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s failed", strings.Join(args[:min(2, len(args))], " "))
	}
	return string(output), nil
}

func managedProxyPublishedAddress(output string) (string, error) {
	line := strings.TrimSpace(strings.Split(strings.TrimSpace(output), "\n")[0])
	if line == "" {
		return "", fmt.Errorf("managed proxy container did not publish port %d", managedHTTPRouteImagePort)
	}
	host, port, err := net.SplitHostPort(line)
	if err != nil || host != "127.0.0.1" || port == "" {
		return "", fmt.Errorf("managed proxy container returned invalid loopback port binding")
	}
	return net.JoinHostPort(host, port), nil
}

func runManagedHTTPRouteSide(ctx context.Context, side, binaryPath, parent, domain string, resources *managedProxyResources) (SideResult, error) {
	var lastErr error
	for attempt := 1; attempt <= managedHTTPRouteAttempts; attempt++ {
		result, err := runManagedHTTPRouteSideAttempt(ctx, side, binaryPath, parent, domain, resources)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt == managedHTTPRouteAttempts || !isAdminAPIBindRace(err) {
			return SideResult{}, err
		}
	}
	return SideResult{}, lastErr
}

func runManagedHTTPRouteSideAttempt(ctx context.Context, side, binaryPath, parent, domain string, resources *managedProxyResources) (_ SideResult, err error) {
	setup, err := stageManagedHTTPRouteSide(parent, domain, resources.imageTag)
	if err != nil {
		return SideResult{}, err
	}
	defer os.RemoveAll(setup.fixture.Root)
	defer func() {
		if releaseErr := setup.releaseReservations(); releaseErr != nil && err == nil {
			err = fmt.Errorf("release managed proxy port reservations: %w", releaseErr)
		}
	}()

	if _, err := resources.startContainer(ctx, side, domain); err != nil {
		return SideResult{}, err
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(setup.proxyPort))
	instance := &GordonInstance{
		BinaryPath:     binaryPath,
		ConfigPath:     setup.fixture.ConfigPath,
		DataDir:        setup.fixture.DataDir,
		WorkingDir:     setup.fixture.Root,
		Env:            managedProxyEnvironment(setup.fixture),
		ReadinessProbe: ReadinessProbe{TCPAddress: address},
	}
	args := []string{"serve", "--config", setup.fixture.ConfigPath}
	if side == SideNew {
		args = []string{"serve", "--role", "monolith", "--config", setup.fixture.ConfigPath}
	}
	if err := setup.releaseReservations(); err != nil {
		return SideResult{}, fmt.Errorf("release managed proxy port reservations before start: %w", err)
	}
	if err := instance.Start(ctx, args...); err != nil {
		return SideResult{}, fmt.Errorf("managed proxy %s start: %w", side, err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if stopErr := instance.Stop(stopCtx); stopErr != nil && err == nil {
			err = fmt.Errorf("managed proxy %s stop: %w", side, stopErr)
		}
	}()
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := instance.WaitReady(readyCtx); err != nil {
		return SideResult{}, fmt.Errorf("managed proxy %s ready: %w", side, err)
	}
	artifact, validationErr := captureManagedHTTPRoute(ctx, address, domain)
	return SideResult{Side: side, Artifact: artifact, ValidationError: validationErr}, nil
}

type managedProxySide struct {
	fixture      SideFixture
	registryPort int
	proxyPort    int
	reservations *adminAPIPortReservations
}

func (s *managedProxySide) releaseReservations() error {
	if s == nil {
		return nil
	}
	return s.reservations.close()
}

func stageManagedHTTPRouteSide(parent, domain, image string) (managedProxySide, error) {
	fixture, err := StageSideFixture(parent, filepath.Join(FixtureRoot(), "configs", "minimal.toml"))
	if err != nil {
		return managedProxySide{}, err
	}
	reservations, err := reserveAdminAPIPorts()
	if err != nil {
		_ = os.RemoveAll(fixture.Root)
		return managedProxySide{}, err
	}
	cleanup := func(stageErr error) (managedProxySide, error) {
		_ = reservations.close()
		_ = os.RemoveAll(fixture.Root)
		return managedProxySide{}, stageErr
	}
	registryPort := reservations.registry.Addr().(*net.TCPAddr).Port
	proxyPort := reservations.proxy.Addr().(*net.TCPAddr).Port
	runtimeSocket, err := adminDockerSocket()
	if err != nil {
		return cleanup(fmt.Errorf("resolve managed proxy Docker socket: %w", err))
	}
	config := fmt.Sprintf(`[server]
registry_port = %d
registry_listen_address = "127.0.0.1"
data_dir = %q
gordon_domain = "gordon.test"
runtime = %q

[entrypoints.edge]
address = "127.0.0.1:%d"
protocol = "smart_tcp"

[auth]
enabled = false
secrets_backend = "unsafe"

[network_isolation]
enabled = false

[routes]
%q = %q
`, registryPort, fixture.DataDir, runtimeSocket, proxyPort, domain, image)
	if err := os.WriteFile(fixture.ConfigPath, []byte(config), 0o600); err != nil {
		return cleanup(fmt.Errorf("write managed proxy config: %w", err))
	}
	return managedProxySide{fixture: fixture, registryPort: registryPort, proxyPort: proxyPort, reservations: reservations}, nil
}

func managedProxyEnvironment(fixture SideFixture) []string {
	return append(append([]string{}, fixture.Env...),
		"XDG_CONFIG_HOME="+filepath.Join(fixture.Root, "xdg-config"),
		"XDG_RUNTIME_DIR="+filepath.Join(fixture.Root, "runtime"),
		"GORDON_ROLE=monolith",
		"GORDON_REMOTE=",
		"GORDON_TOKEN=",
	)
}

type managedHTTPRouteObservation struct {
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
	Route   managedRouteID    `json:"route"`
}

type managedRouteID struct {
	Domain         string `json:"domain"`
	ImageProxyPort int    `json:"imageProxyPort"`
	Kind           string `json:"kind"`
}

func captureManagedHTTPRoute(ctx context.Context, address, domain string) (ProxyArtifact, error) {
	deadline := time.Now().Add(30 * time.Second)
	var observation managedHTTPRouteObservation
	var lastErr error
	for time.Now().Before(deadline) {
		observation, lastErr = captureManagedHTTPRouteOnce(ctx, address, domain)
		if lastErr == nil && observation.Status == http.StatusOK && observation.Body == managedHTTPRouteMarker {
			return managedHTTPRouteArtifact(observation), nil
		}
		select {
		case <-ctx.Done():
			return managedHTTPRouteArtifact(observation), fmt.Errorf("managed proxy request: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	artifact := managedHTTPRouteArtifact(observation)
	if lastErr != nil {
		return artifact, fmt.Errorf("managed proxy request failed")
	}
	return artifact, fmt.Errorf("managed proxy expected HTTP %d and deterministic response marker", http.StatusOK)
}

func captureManagedHTTPRouteOnce(ctx context.Context, address, domain string) (managedHTTPRouteObservation, error) {
	observation := managedHTTPRouteObservation{Headers: map[string]string{}, Route: managedRouteID{Domain: domain, ImageProxyPort: managedHTTPRouteImagePort, Kind: "managed"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/", nil)
	if err != nil {
		return observation, err
	}
	req.Host = domain
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return observation, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return observation, err
	}
	observation.Status = response.StatusCode
	observation.Body = string(body)
	// These are stable response headers that establish proxy/backend behavior
	// without writing arbitrary daemon or request metadata into artifacts.
	for _, key := range []string{"Content-Type", "Server", "Via", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		observation.Headers[key] = response.Header.Get(key)
	}
	return observation, nil
}

func managedHTTPRouteArtifact(observation managedHTTPRouteObservation) ProxyArtifact {
	return NewProxyArtifact("managed HTTP route", observation, LevelExact)
}

func managedProxyCaptureFailure(side string, validationErr error) SideResult {
	observation := managedHTTPRouteObservation{Headers: map[string]string{}, Route: managedRouteID{ImageProxyPort: managedHTTPRouteImagePort, Kind: "managed"}}
	return SideResult{Side: side, Artifact: managedHTTPRouteArtifact(observation), ValidationError: validationErr}
}

func waitForTCP(ctx context.Context, address string) error {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			return conn.Close()
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("wait TCP %s: %w", address, lastErr)
}

func managedProxyDomain(runID string) string {
	part := sanitizePart(runID)
	if len(part) > 34 {
		part = part[:34]
	}
	return "managed-" + part + ".test"
}

func managedHTTPRouteRerunCommand() string {
	return "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityManagedHTTPRoute$' -count=1"
}
