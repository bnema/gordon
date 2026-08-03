package compatoldnew

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
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
	externalRouteScenarioName = "proxy/external-route"
	externalRouteMarker       = "gordon-compat-external-route\n"
	externalRoutePort         = 8080
	externalRouteBaseImage    = "busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
	externalRouteNetworkTries = 8
)

var cgnatCIDR = mustParseCIDR("100.64.0.0/10")

const externalRouteDockerfile = `FROM busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
RUN mkdir -p /www/cgi-bin && printf '%s\n' '#!/bin/sh' 'echo "Content-Type: text/plain"' 'echo "X-Gordon-Upstream-Host: $HTTP_HOST"' 'echo' 'printf "gordon-compat-external-route\\n"' > /www/cgi-bin/echo.cgi && chmod 0555 /www/cgi-bin/echo.cgi
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`

func mustParseCIDR(value string) *net.IPNet {
	_, cidr, err := net.ParseCIDR(value)
	if err != nil {
		panic(err)
	}
	return cidr
}

// externalRouteSubnet selects one of the /24s in CGNAT space. CGNAT is not
// RFC1918, loopback, or link-local, so it exercises the enabled SSRF policy.
func externalRouteSubnet(index uint16) string {
	index %= 1 << 14
	return fmt.Sprintf("100.%d.%d.0/24", 64+index/256, index%256)
}

func randomExternalRouteSubnet() (string, error) {
	var value [2]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("select CGNAT subnet: %w", err)
	}
	return externalRouteSubnet(binary.BigEndian.Uint16(value[:])), nil
}

type externalRouteResources struct {
	runID           string
	imageTag        string
	networkName     string
	upstreamName    string
	targetAddress   string
	imageBuilt      bool
	networkCreated  bool
	upstreamStarted bool
	sideContainers  map[string]string
}

func newExternalRouteResources(runID string) *externalRouteResources {
	part := sanitizePart(runID)
	if len(part) > 70 {
		part = part[:70]
	}
	return &externalRouteResources{
		runID:          runID,
		imageTag:       "gordon-compat-external-route:" + part,
		networkName:    NetworkPrefix(runID, "external"),
		upstreamName:   ContainerPrefix(runID, "upstream"),
		sideContainers: make(map[string]string),
	}
}

func dockerRuntimeSocket() (string, error) {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		if !strings.HasPrefix(host, "unix://") {
			return "", fmt.Errorf("external route Docker host must be a unix socket")
		}
		return strings.TrimPrefix(host, "unix://"), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := dockerCompatibilityOutput(ctx, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return "", fmt.Errorf("resolve external route Docker socket: %w", err)
	}
	host := strings.TrimSpace(output)
	if !strings.HasPrefix(host, "unix://") {
		return "", fmt.Errorf("external route Docker context must use a unix socket")
	}
	socket := strings.TrimPrefix(host, "unix://")
	if _, err := os.Stat(socket); err != nil {
		return "", fmt.Errorf("external route Docker socket unavailable: %w", err)
	}
	return socket, nil
}

func (r *externalRouteResources) start(ctx context.Context) error {
	if err := dockerCompatibilityCommand(ctx, "pull", externalRouteBaseImage); err != nil {
		return fmt.Errorf("external route pull pinned base image: %w", err)
	}
	if err := r.buildImage(ctx); err != nil {
		return err
	}
	if err := r.createNetwork(ctx); err != nil {
		return err
	}
	labels := ResourceLabels(r.runID, "shared", externalRouteScenarioName)
	if err := dockerCompatibilityCommand(ctx,
		"run", "-d", "--name", r.upstreamName, "--network", r.networkName,
		"--label", LabelRun+"="+labels[LabelRun],
		"--label", LabelSide+"="+labels[LabelSide],
		"--label", LabelFixture+"="+labels[LabelFixture],
		r.imageTag,
	); err != nil {
		return fmt.Errorf("start external route upstream: %w", err)
	}
	r.upstreamStarted = true
	ip, err := dockerCompatibilityOutput(ctx, "container", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", r.upstreamName)
	if err != nil {
		return fmt.Errorf("read external route upstream address: %w", err)
	}
	ip = strings.TrimSpace(ip)
	if parsed := net.ParseIP(ip); parsed == nil || !cgnatCIDR.Contains(parsed) {
		return fmt.Errorf("external route upstream did not receive a CGNAT address")
	}
	r.targetAddress = net.JoinHostPort(ip, strconv.Itoa(externalRoutePort))
	return nil
}

func (r *externalRouteResources) buildImage(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "gordon-compat-external-image-*")
	if err != nil {
		return fmt.Errorf("external route image directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(externalRouteDockerfile), 0o600); err != nil {
		return fmt.Errorf("external route Dockerfile: %w", err)
	}
	labels := ResourceLabels(r.runID, "shared", externalRouteScenarioName)
	if err := dockerCompatibilityCommand(ctx,
		"build", "--pull=false",
		"--label", LabelRun+"="+labels[LabelRun],
		"--label", LabelSide+"="+labels[LabelSide],
		"--label", LabelFixture+"="+labels[LabelFixture],
		"--tag", r.imageTag, dir,
	); err != nil {
		return fmt.Errorf("build external route upstream image: %w", err)
	}
	r.imageBuilt = true
	return nil
}

func (r *externalRouteResources) createNetwork(ctx context.Context) error {
	labels := ResourceLabels(r.runID, "shared", externalRouteScenarioName)
	for attempt := 0; attempt < externalRouteNetworkTries; attempt++ {
		subnet, err := randomExternalRouteSubnet()
		if err != nil {
			return err
		}
		collision, err := createExternalRouteNetwork(ctx, r.networkName, subnet, labels)
		if err == nil {
			r.networkCreated = true
			return nil
		}
		if !collision {
			return err
		}
		// A generated subnet collided with an existing Docker network. Try
		// another CGNAT /24; no broad prune is ever used.
	}
	return fmt.Errorf("create external route CGNAT network after %d subnet collisions", externalRouteNetworkTries)
}

func createExternalRouteNetwork(ctx context.Context, name, subnet string, labels map[string]string) (collision bool, err error) {
	args := []string{
		"network", "create", "--driver", "bridge", "--subnet", subnet,
		"--label", LabelRun + "=" + labels[LabelRun],
		"--label", LabelSide + "=" + labels[LabelSide],
		"--label", LabelFixture + "=" + labels[LabelFixture],
		name,
	}
	cmd, err := newIsolatedCommand(ctx, "docker", args, nil, nil, true)
	if err != nil {
		return false, fmt.Errorf("prepare external route Docker network")
	}
	if _, err := cmd.Output(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.ToLower(string(exitErr.Stderr))
			if strings.Contains(stderr, "pool overlaps") || strings.Contains(stderr, "overlaps with other one") {
				return true, nil
			}
		}
		// Deliberately do not retain Docker stderr: reports must not include
		// arbitrary daemon data or raw inspect output.
		return false, fmt.Errorf("create external route CGNAT network")
	}
	return false, nil
}

func (r *externalRouteResources) cleanup(ctx context.Context) error {
	var errs []error
	for _, side := range []string{SideOld, SideNew} {
		if err := r.removeSideContainer(ctx, side); err != nil {
			errs = append(errs, err)
		}
	}
	if r.upstreamStarted {
		if err := dockerCompatibilityCommand(ctx, "rm", "-f", r.upstreamName); err != nil {
			errs = append(errs, fmt.Errorf("remove external route upstream: %w", err))
		}
	}
	if r.networkCreated {
		if err := dockerCompatibilityCommand(ctx, "network", "rm", r.networkName); err != nil {
			errs = append(errs, fmt.Errorf("remove external route network: %w", err))
		}
	}
	if r.imageBuilt {
		if err := dockerCompatibilityCommand(ctx, "image", "rm", "-f", r.imageTag); err != nil {
			errs = append(errs, fmt.Errorf("remove external route image: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (r *externalRouteResources) removeSideContainer(ctx context.Context, side string) error {
	name := r.sideContainers[side]
	if name == "" {
		return nil
	}
	if err := dockerCompatibilityCommand(ctx, "rm", "-f", name); err != nil {
		return fmt.Errorf("remove external route %s Gordon container: %w", side, err)
	}
	delete(r.sideContainers, side)
	return nil
}

// RunCompatibilityExternalRoute compares old and new binaries through an
// external route. Each Gordon process is started only after its predecessor
// exits; both resolve the same isolated, CGNAT upstream.
func RunCompatibilityExternalRoute(ctx context.Context, repoRoot, artifactDir string) (report Report, err error) {
	if repoRoot == "" || artifactDir == "" {
		return Report{}, fmt.Errorf("external route compatibility requires repository root and artifact directory")
	}
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		return Report{}, err
	}
	runID := RunID(externalRouteScenarioName)
	resources := newExternalRouteResources(runID)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = errors.Join(err, resources.cleanup(cleanupCtx))
	}()
	if err := resources.start(ctx); err != nil {
		return Report{}, err
	}
	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}
	parent, err := os.MkdirTemp("", "gordon-compat-external-route-*")
	if err != nil {
		return Report{}, fmt.Errorf("external route fixture parent: %w", err)
	}
	defer os.RemoveAll(parent)
	domain := externalRouteDomain(runID)
	old, oldErr := runExternalRouteSide(ctx, SideOld, binaries.Old.BinaryPath, parent, domain, resources)
	if oldErr != nil {
		old = externalRouteFailure(SideOld, oldErr)
	}
	newSide, newErr := runExternalRouteSide(ctx, SideNew, binaries.New.BinaryPath, parent, domain, resources)
	if newErr != nil {
		newSide = externalRouteFailure(SideNew, newErr)
	}
	return CompareSideResultsWithMetadata(old, newSide, nil, artifactDir, ReportMetadata{
		BaselineCommit:  binaries.Old.Commit,
		CandidateCommit: binaries.New.Commit,
		RerunCommand:    externalRouteRerunCommand(),
	})
}

func cleanupExternalRouteFixture(root, image string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	chmodErr := dockerCompatibilityCommand(ctx, "run", "--rm", "--volume", root+":/fixture", image, "sh", "-c", "chmod -R a+rwX /fixture")
	return errors.Join(chmodErr, os.RemoveAll(root))
}

func runExternalRouteSide(ctx context.Context, side, binaryPath, parent, domain string, resources *externalRouteResources) (_ SideResult, err error) {
	setup, err := stageExternalRouteSide(parent, domain, resources.targetAddress)
	if err != nil {
		return SideResult{}, err
	}
	defer func() {
		err = errors.Join(err, cleanupExternalRouteFixture(setup.fixture.Root, resources.imageTag))
	}()
	defer func() {
		if releaseErr := setup.releaseReservations(); releaseErr != nil && err == nil {
			err = fmt.Errorf("release external route port reservations: %w", releaseErr)
		}
	}()
	if err := setup.releaseReservations(); err != nil {
		return SideResult{}, fmt.Errorf("release external route port reservations before start: %w", err)
	}
	runtimeSocket, err := dockerRuntimeSocket()
	if err != nil {
		return SideResult{}, err
	}
	containerName := ContainerPrefix(resources.runID, side)
	resources.sideContainers[side] = containerName
	labels := ResourceLabels(resources.runID, side, externalRouteScenarioName)
	args := []string{
		"run", "-d", "--name", containerName, "--network", resources.networkName,
		"--label", LabelRun + "=" + labels[LabelRun],
		"--label", LabelSide + "=" + labels[LabelSide],
		"--label", LabelFixture + "=" + labels[LabelFixture],
		"--mount", "type=bind,src=" + binaryPath + ",dst=/gordon,readonly",
		"--mount", "type=bind,src=" + setup.fixture.Root + ",dst=/fixture",
		"--mount", "type=bind,src=" + runtimeSocket + ",dst=/var/run/docker.sock",
		"-e", "GORDON_ROLE=monolith", "-e", "GORDON_REMOTE=", "-e", "GORDON_TOKEN=",
		"--entrypoint", "/gordon", resources.imageTag,
		"serve",
	}
	if side == SideNew {
		args = append(args, "--role", "monolith")
	}
	args = append(args, "--config", "/fixture/"+filepath.Base(setup.fixture.ConfigPath))
	if err := dockerCompatibilityCommand(ctx, args...); err != nil {
		return SideResult{}, fmt.Errorf("external route %s start: %w", side, err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if stopErr := resources.removeSideContainer(stopCtx, side); stopErr != nil && err == nil {
			err = stopErr
		}
	}()
	artifact, validationErr := captureExternalRoute(ctx, containerName, setup.proxyPort, domain, resources.targetAddress)
	return SideResult{Side: side, Artifact: artifact, ValidationError: validationErr}, nil
}

func stageExternalRouteSide(parent, domain, target string) (managedProxySide, error) {
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
	config := fmt.Sprintf(`[server]
registry_port = %d
registry_listen_address = "127.0.0.1"
data_dir = "/fixture/data"
gordon_domain = "gordon.test"
runtime = "/var/run/docker.sock"

[entrypoints.edge]
address = "0.0.0.0:%d"
protocol = "smart_tcp"

[auth]
enabled = false
secrets_backend = "unsafe"

[network_isolation]
enabled = false

[api.rate_limit]
trusted_proxies = ["100.64.0.0/10", "10.0.2.0/24"]

[tls.acme]
enabled = false

[external_routes]
%q = %q
`, registryPort, proxyPort, domain, target)
	if err := os.WriteFile(fixture.ConfigPath, []byte(config), 0o600); err != nil {
		return cleanup(fmt.Errorf("write external route config: %w", err))
	}
	return managedProxySide{fixture: fixture, registryPort: registryPort, proxyPort: proxyPort, reservations: reservations}, nil
}

type externalRouteObservation struct {
	Status       int    `json:"status"`
	Body         string `json:"body"`
	UpstreamHost string `json:"upstreamHost"`
	Route        struct {
		Domain string `json:"domain"`
		Target string `json:"target"`
	} `json:"route"`
}

func captureExternalRoute(ctx context.Context, container string, proxyPort int, domain, target string) (ProxyArtifact, error) {
	deadline := time.Now().Add(30 * time.Second)
	var observation externalRouteObservation
	var lastErr error
	for time.Now().Before(deadline) {
		observation, lastErr = captureExternalRouteOnce(ctx, container, proxyPort, domain, target)
		if lastErr == nil && observation.Status == http.StatusOK && observation.Body == externalRouteMarker && observation.UpstreamHost == target {
			return externalRouteArtifact(observation), nil
		}
		select {
		case <-ctx.Done():
			return externalRouteArtifact(observation), fmt.Errorf("external route request: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return externalRouteArtifact(observation), fmt.Errorf("external route request failed")
	}
	return externalRouteArtifact(observation), fmt.Errorf("external route expected HTTP %d, fixed body, and upstream Host %q", http.StatusOK, target)
}

func captureExternalRouteOnce(ctx context.Context, container string, proxyPort int, domain, target string) (externalRouteObservation, error) {
	observation := externalRouteObservation{}
	observation.Route.Domain = domain
	observation.Route.Target = target
	// Run the GET in Gordon's network namespace. This is necessary with
	// rootless Docker, whose published-port relay is intentionally an untrusted
	// client, while still exercising the real Gordon HTTP proxy and public Host.
	command := fmt.Sprintf("wget -q -S -O - --header='Host: %s' http://127.0.0.1:%d/cgi-bin/echo.cgi 2>&1", domain, proxyPort)
	output, err := dockerCompatibilityOutput(ctx, "exec", container, "sh", "-c", command)
	if err != nil {
		return observation, err
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if fields := strings.Fields(line); len(fields) >= 2 && strings.HasPrefix(fields[0], "HTTP/") {
			observation.Status, _ = strconv.Atoi(fields[1])
		}
		if key, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(key, "X-Gordon-Upstream-Host") {
			observation.UpstreamHost = strings.TrimSpace(value)
		}
	}
	if strings.Contains(output, externalRouteMarker) {
		observation.Body = externalRouteMarker
	}
	return observation, nil
}

func externalRouteArtifact(observation externalRouteObservation) ProxyArtifact {
	return NewProxyArtifact("external route", observation, LevelExact)
}

func externalRouteFailure(side string, validationErr error) SideResult {
	return SideResult{Side: side, Artifact: externalRouteArtifact(externalRouteObservation{}), ValidationError: validationErr}
}

func externalRouteDomain(runID string) string {
	part := sanitizePart(runID)
	if len(part) > 34 {
		part = part[:34]
	}
	return "external-" + part + ".test"
}

func externalRouteRerunCommand() string {
	return "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityExternalRoute$' -count=1"
}
