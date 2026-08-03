package compatoldnew

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RunCompatibilityTrafficProtocolBinaries compares actual baseline and candidate
// Gordon binaries. Each side receives new listeners, local backends, filesystem
// state, and a generated certificate; persisted artifacts contain booleans only.
func RunCompatibilityTrafficProtocolBinaries(ctx context.Context, repoRoot, artifactDir string) (Report, error) {
	if repoRoot == "" || artifactDir == "" {
		return Report{}, errors.New("traffic binary compatibility requires repository root and artifact directory")
	}
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		return Report{}, err
	}
	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}
	parent, err := os.MkdirTemp("", "gordon-compat-traffic-binary-*")
	if err != nil {
		return Report{}, fmt.Errorf("traffic binary compatibility create fixture parent: %w", err)
	}
	defer os.RemoveAll(parent)

	old, oldErr := runTrafficBinarySide(ctx, SideOld, binaries.Old.BinaryPath, parent)
	if oldErr != nil {
		old = trafficBinaryCaptureFailure(SideOld)
	}
	current, currentErr := runTrafficBinarySide(ctx, SideNew, binaries.New.BinaryPath, parent)
	if currentErr != nil {
		current = trafficBinaryCaptureFailure(SideNew)
	}
	return CompareSideResultsWithMetadata(old, current, nil, artifactDir, ReportMetadata{
		BaselineCommit:  binaries.Old.Commit,
		CandidateCommit: binaries.New.Commit,
		RerunCommand:    trafficBinaryRerunCommand(),
	})
}

func trafficBinaryCaptureFailure(side string) SideResult {
	return SideResult{
		Side:     side,
		Artifact: NewProxyArtifact("traffic protocol binary matrix", trafficBinaryObservation{}, LevelExact),
		// Do not persist listener addresses or process logs in a report.
		ValidationError: errors.New("traffic binary capture failed"),
	}
}

type trafficBinaryObservation struct {
	SmartHTTP           bool `json:"smartHTTP"`
	SmartHTTPSFallback  bool `json:"smartHTTPSFallback"`
	MuxHTTPSTermination bool `json:"muxHTTPSTermination"`
	TLSPassthrough      bool `json:"tlsPassthrough"`
	RawTCP              bool `json:"rawTCP"`
	UDP                 bool `json:"udp"`
	ListenersRebound    bool `json:"listenersRebound"`
	NoGordonDockerDelta bool `json:"noGordonDockerDelta"`
}

type trafficBinarySide struct {
	fixture           SideFixture
	reservations      *trafficBinaryReservations
	backends          *trafficProtocolBackends
	certPath          string
	keyPath           string
	listenerAddresses trafficBinaryAddresses
}

type trafficBinaryReservations struct {
	server, admin, registry, smart, mux, raw net.Listener
	udp                                      net.PacketConn
}

func reserveTrafficBinaryAddresses() (*trafficBinaryReservations, error) {
	result := &trafficBinaryReservations{}
	listeners := []*net.Listener{&result.server, &result.admin, &result.registry, &result.smart, &result.mux, &result.raw}
	for _, destination := range listeners {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = result.close()
			return nil, fmt.Errorf("reserve traffic binary TCP address: %w", err)
		}
		*destination = listener
	}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		_ = result.close()
		return nil, fmt.Errorf("reserve traffic binary UDP address: %w", err)
	}
	result.udp = packet
	return result, nil
}

func (r *trafficBinaryReservations) close() error {
	if r == nil {
		return nil
	}
	var errs []error
	for _, listener := range []*net.Listener{&r.server, &r.admin, &r.registry, &r.smart, &r.mux, &r.raw} {
		if *listener != nil {
			errs = append(errs, (*listener).Close())
			*listener = nil
		}
	}
	if r.udp != nil {
		errs = append(errs, r.udp.Close())
		r.udp = nil
	}
	return errors.Join(errs...)
}

func (r *trafficBinaryReservations) addresses() trafficBinaryAddresses {
	return trafficBinaryAddresses{
		server: r.server.Addr().String(), admin: r.admin.Addr().String(), registry: r.registry.Addr().String(),
		smart: r.smart.Addr().String(), mux: r.mux.Addr().String(), raw: r.raw.Addr().String(), udp: r.udp.LocalAddr().String(),
	}
}

func runTrafficBinarySide(ctx context.Context, side, binaryPath, parent string) (_ SideResult, err error) {
	setup, err := stageTrafficBinarySide(parent)
	if err != nil {
		return SideResult{}, err
	}
	defer func() {
		if closeErr := setup.close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	before, err := trafficBinaryDockerResources(ctx)
	if err != nil {
		return SideResult{}, err
	}
	if err := setup.reservations.close(); err != nil {
		return SideResult{}, fmt.Errorf("release traffic binary reservations before start: %w", err)
	}
	instance := &GordonInstance{
		BinaryPath:      binaryPath,
		ConfigPath:      setup.fixture.ConfigPath,
		DataDir:         setup.fixture.DataDir,
		WorkingDir:      setup.fixture.Root,
		Env:             trafficBinaryEnvironment(setup.fixture),
		RuntimeRequired: true, // Legacy monolith owns the runtime in this compatibility slice.
		ReadinessProbe: ReadinessProbe{
			TCPAddress: setup.smartAddress(),
		},
	}
	args := []string{"serve", "--config", setup.fixture.ConfigPath}
	if side == SideNew {
		args = []string{"serve", "--role", "monolith", "--config", setup.fixture.ConfigPath}
	}
	if err := instance.Start(ctx, args...); err != nil {
		return SideResult{}, fmt.Errorf("start traffic binary %s: %w", side, err)
	}
	started := true
	defer func() {
		if !started {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if stopErr := instance.Stop(stopCtx); stopErr != nil && err == nil {
			err = fmt.Errorf("stop traffic binary %s: %w", side, stopErr)
		}
	}()
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	readyErr := instance.WaitReady(readyCtx)
	cancel()
	if readyErr != nil {
		return SideResult{}, fmt.Errorf("traffic binary %s ready: %w", side, readyErr)
	}

	observation := trafficBinaryObservation{}
	smartHTTPErr := probeTrafficHTTPFallback(setup.smartAddress(), "smart.test", false)
	smartHTTPSErr := probeTrafficHTTPFallback(setup.smartAddress(), "smart.test", true)
	muxHTTPSErr := probeTrafficHTTPFallback(setup.muxAddress(), "mux.test", true)
	observation.SmartHTTP = smartHTTPErr == nil
	observation.SmartHTTPSFallback = smartHTTPSErr == nil
	observation.MuxHTTPSTermination = muxHTTPSErr == nil
	observation.TLSPassthrough = probeTLSPassthrough(setup.muxAddress()) == nil
	observation.RawTCP = probeTrafficTCPEcho(setup.rawAddress()) == nil
	observation.UDP = probeTrafficUDPEcho(setup.udpAddress()) == nil

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	stopErr := instance.Stop(stopCtx)
	stopCancel()
	started = false
	if stopErr != nil {
		return SideResult{}, fmt.Errorf("stop traffic binary %s: %w", side, stopErr)
	}
	observation.ListenersRebound = verifyTrafficBinaryListenerRelease(setup.addresses()) == nil
	after, err := trafficBinaryDockerResources(ctx)
	if err != nil {
		return SideResult{}, err
	}
	observation.NoGordonDockerDelta = before == after
	artifact := NewProxyArtifact("traffic protocol binary matrix", observation, LevelExact)
	if !trafficBinaryObservationPassed(observation) {
		return SideResult{Side: side, Artifact: artifact, ValidationError: errors.New("traffic binary protocol contract failed")}, nil
	}
	return SideResult{Side: side, Artifact: artifact}, nil
}

func trafficBinaryObservationPassed(observation trafficBinaryObservation) bool {
	return observation.SmartHTTP && observation.SmartHTTPSFallback && observation.MuxHTTPSTermination && observation.TLSPassthrough && observation.RawTCP && observation.UDP && observation.ListenersRebound && observation.NoGordonDockerDelta
}

// stageTrafficBinarySide creates all per-side state before a binary starts.
func stageTrafficBinarySide(parent string) (trafficBinarySide, error) {
	fixture, err := StageSideFixture(parent, filepath.Join(FixtureRoot(), "configs", "minimal.toml"))
	if err != nil {
		return trafficBinarySide{}, err
	}
	cleanup := func(stageErr error, setup trafficBinarySide) (trafficBinarySide, error) {
		_ = setup.close()
		return trafficBinarySide{}, stageErr
	}
	reservations, err := reserveTrafficBinaryAddresses()
	if err != nil {
		return cleanup(err, trafficBinarySide{fixture: fixture})
	}
	backendHost, err := distributedDrainNonLoopbackIPv4()
	if err != nil {
		return cleanup(err, trafficBinarySide{fixture: fixture, reservations: reservations})
	}
	backends, err := startTrafficProtocolBackendsOn(backendHost)
	if err != nil {
		return cleanup(err, trafficBinarySide{fixture: fixture, reservations: reservations})
	}
	certPath, keyPath, err := writeTrafficBinaryCertificate(fixture.Root)
	if err != nil {
		return cleanup(err, trafficBinarySide{fixture: fixture, reservations: reservations, backends: backends})
	}
	setup := trafficBinarySide{fixture: fixture, reservations: reservations, backends: backends, certPath: certPath, keyPath: keyPath, listenerAddresses: reservations.addresses()}
	if err := os.WriteFile(fixture.ConfigPath, []byte(trafficBinaryConfig(setup)), 0o600); err != nil {
		return cleanup(fmt.Errorf("write traffic binary config: %w", err), setup)
	}
	return setup, nil
}

func (s trafficBinarySide) close() error {
	var errs []error
	if s.reservations != nil {
		errs = append(errs, s.reservations.close())
	}
	if s.backends != nil {
		s.backends.close()
	}
	if s.fixture.Root != "" {
		errs = append(errs, os.RemoveAll(s.fixture.Root))
	}
	return errors.Join(errs...)
}

func (s trafficBinarySide) smartAddress() string { return s.listenerAddresses.smart }
func (s trafficBinarySide) muxAddress() string   { return s.listenerAddresses.mux }
func (s trafficBinarySide) rawAddress() string   { return s.listenerAddresses.raw }
func (s trafficBinarySide) udpAddress() string   { return s.listenerAddresses.udp }

func (s trafficBinarySide) addresses() trafficBinaryAddresses { return s.listenerAddresses }

type trafficBinaryAddresses struct{ server, admin, registry, smart, mux, raw, udp string }

func writeTrafficBinaryCertificate(dir string) (string, string, error) {
	certificate, err := generatedTrafficTestCertificate()
	if err != nil {
		return "", "", err
	}
	key, ok := certificate.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return "", "", errors.New("generated traffic certificate has an unexpected private key")
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPath, keyPath := filepath.Join(dir, "traffic-cert.pem"), filepath.Join(dir, "traffic-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func trafficBinaryConfig(setup trafficBinarySide) string {
	tcpHost, tcpPort := trafficBackendAddress(setup.backends.tcp)
	udpHost, udpPort := trafficBackendAddress(setup.backends.udp)
	tlsHost, tlsPort := trafficBackendAddress(setup.backends.tls)
	addresses := setup.addresses()
	return fmt.Sprintf(`[server]
port = %d
tls_port = %d
registry_port = %d
registry_listen_address = "127.0.0.1"
data_dir = %q
gordon_domain = "smart.test"
tls_cert_file = %q
tls_key_file = %q

[entrypoints.smart]
address = %q
protocol = "smart_tcp"

[entrypoints.mux]
address = %q
protocol = "tls_mux"

[entrypoints.raw]
address = %q
protocol = "tcp"

[entrypoints.udp]
address = %q
protocol = "udp"

[auth]
enabled = false
secrets_backend = "unsafe"

[api.rate_limit]
enabled = false

[network_isolation]
enabled = false

[tls.acme]
enabled = false

[logging]
level = "warn"
format = "console"

[traffic.tcp]
drain_timeout = "1s"

[traffic.udp]
drain_timeout = "1s"

[[network_services]]
name = %q

[[network_services.ports]]
name = "raw"
container = %d
protocol = "tcp"

[[network_services.ports]]
name = "tls"
container = %d
protocol = "tcp"

[[network_services.ports]]
name = "udp"
container = %d
protocol = "udp"

[[traffic.tcp.routers]]
name = "raw"
entrypoint = "raw"
service = "network_service:%s:raw"

[[traffic.udp.routers]]
name = "udp"
entrypoint = "udp"
service = "network_service:%s:udp"

[[traffic.tls.routers]]
name = "passthrough"
entrypoint = "mux"
sni = "passthrough.test"
service = "network_service:%s:tls"
`, portOf(addresses.server), portOf(addresses.admin), portOf(addresses.registry), setup.fixture.DataDir, setup.certPath, setup.keyPath, addresses.smart, addresses.mux, addresses.raw, addresses.udp, tcpHost, tcpPort, tlsPort, udpPort, tcpHost, udpHost, tlsHost)
}

func portOf(address string) int {
	_, port, _ := net.SplitHostPort(address)
	value, _ := strconv.Atoi(port)
	return value
}

func trafficBinaryEnvironment(fixture SideFixture) []string {
	return append(append([]string{}, fixture.Env...), "XDG_CONFIG_HOME="+filepath.Join(fixture.Root, "xdg-config"), "XDG_RUNTIME_DIR="+filepath.Join(fixture.Root, "runtime"), "GORDON_ROLE=monolith", "GORDON_REMOTE=", "GORDON_TOKEN=")
}

func probeTrafficHTTPFallback(address, host string, secure bool) error {
	client := &http.Client{Timeout: 3 * time.Second}
	scheme := "http"
	if secure {
		scheme = "https"
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{ServerName: host, InsecureSkipVerify: true}} // #nosec G402 -- generated test certificate.
	}
	request, err := http.NewRequest(http.MethodGet, scheme+"://"+address, nil)
	if err != nil {
		return err
	}
	request.Host = host
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("unexpected HTTP fallback status %d", response.StatusCode)
	}
	return nil
}

func verifyTrafficBinaryListenerRelease(addresses trafficBinaryAddresses) error {
	for _, address := range []string{addresses.server, addresses.admin, addresses.registry, addresses.smart, addresses.mux, addresses.raw} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("traffic binary TCP listener leaked: %w", err)
		}
		if err := listener.Close(); err != nil {
			return err
		}
	}
	packet, err := net.ListenPacket("udp", addresses.udp)
	if err != nil {
		return fmt.Errorf("traffic binary UDP listener leaked: %w", err)
	}
	return packet.Close()
}

func trafficBinaryDockerResources(ctx context.Context) (string, error) {
	var resources []string
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", "label=gordon.managed"},
		{"network", "ls", "-q", "--filter", "label=gordon.managed"},
		{"volume", "ls", "-q", "--filter", "label=gordon.managed"},
	} {
		output, err := dockerCompatibilityOutput(ctx, args...)
		if err != nil {
			return "", fmt.Errorf("list Gordon Docker resources: %w", err)
		}
		resources = append(resources, strings.Fields(output)...)
	}
	return strings.Join(resources, "\n"), nil
}

func trafficBinaryRerunCommand() string {
	return "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityTrafficProtocolBinaries$' -count=1"
}
